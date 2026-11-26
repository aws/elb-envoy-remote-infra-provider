// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	awsec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/endpoints"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/provider"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// AWSVerifier performs read-only assertions against the real ELBProxy API, used
// to verify that controller reconciliation actually reached AWS (not just that
// Kubernetes conditions flipped). It reuses production code paths — the same
// ELBProxy client, tagging manager, and tracking tags the controller stamps —
// so discovery here matches how the controller finds its own resources: with
// GetResources from the Resource Groups Tagging API.
//
// The verifier is optional: it requires the controller's --cluster-name (so it
// can build the stack tag filter) and AWS credentials/region with read access
// to the ELBProxy API and tag:GetResources. Tests obtain one via
// Suite.NewAWSVerifier, which returns (nil, false) when verification is not
// configured so callers can skip cleanly.
type AWSVerifier struct {
	clusterName      string
	vpcID            string
	elbProxyClient   elbproxyaws.ELBProxyClient
	ec2Client        *awsec2.Client
	taggingManager   elbproxy.TaggingManager
	trackingProvider tracking.Provider
}

// NewAWSVerifier builds a read-only AWS verifier, or returns ok=false when
// AWS-side verification is not configured (no -cluster-name). On a genuine
// setup error (e.g. AWS config load failure) it fails the test.
//
// Usage:
//
//	v, ok := s.NewAWSVerifier(t)
//	if !ok {
//	    t.Skip("AWS verification not configured; set -cluster-name")
//	}
func (s *Suite) NewAWSVerifier(t *testing.T) (*AWSVerifier, bool) {
	t.Helper()
	if s.ClusterName == "" {
		Logf(t, "AWS verification disabled: -cluster-name not set")
		return nil, false
	}

	// Resolve region without mutating env: t.Setenv panics in parallel tests, so
	// pass it to the SDK via config.WithRegion instead.
	region := s.AWSRegion
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		t.Fatalf("AWS verification enabled (-cluster-name=%q) but no region set; pass -aws-region or set AWS_REGION", s.ClusterName)
	}

	logger := logging.DefaultLogger(os.Stderr, egv1a1.LogLevelError)

	// Load AWS config once and share it for all clients (ELBProxy, EC2 and tagging).
	awsCfg, err := awsconfig.LoadDefaultConfig(t.Context(), awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("AWS verify: load AWS config: %v", err)
	}
	// Endpoint override is optional: when -aws-api-endpoint is empty the SDK
	// default (prod) ELB Proxy API endpoint is used.
	var endpointOverride *string
	if s.AWSAPIEndpoint != "" {
		endpointOverride = aws.String(s.AWSAPIEndpoint)
	}
	elbProxyClient := elbproxyaws.NewELBProxyClientFromConfig(awsCfg, endpointOverride)
	ec2Client := awsec2.NewFromConfig(awsCfg)

	taggingEndpoints := map[string]string{}
	if s.AWSTaggingEndpoint != "" {
		taggingEndpoints[resourcegroupstaggingapi.ServiceID] = s.AWSTaggingEndpoint
	}
	rgt := services.NewRGT(provider.NewDefaultAWSClientsProvider(awsCfg, endpoints.NewResolver(taggingEndpoints)))

	return &AWSVerifier{
		clusterName:      s.ClusterName,
		vpcID:            s.AWSVpcID,
		elbProxyClient:   elbProxyClient,
		ec2Client:        ec2Client,
		taggingManager:   elbproxy.NewDefaultTaggingManager(elbProxyClient, rgt, logger),
		trackingProvider: tracking.NewProvider(s.ClusterName),
	}, true
}

// FindProxy discovers the single ELBProxy proxy owned by the given gateway stack
// (namespace/name), using the same stack tags the controller stamps. It fails
// the test if zero or more than one proxy matches.
func (v *AWSVerifier) FindProxy(t *testing.T, gatewayNamespace, gatewayName string) elbproxy.ProxyWithTags {
	t.Helper()
	ctx := t.Context()

	stackID := gatewayNamespace + "/" + gatewayName
	filter := tracking.TagsAsTagFilter(v.trackingProvider.StackTags(stackID))

	proxies, err := v.taggingManager.ListProxies(ctx, filter)
	if err != nil {
		t.Fatalf("AWS verify: list proxies for stack %q: %v", stackID, err)
	}
	switch len(proxies) {
	case 0:
		t.Fatalf("AWS verify: no ELBProxy proxy found for stack %q (filter %v)", stackID, filter)
	case 1:
		Logf(t, "AWS verify: found proxy %s for stack %q (status %s)",
			proxies[0].ProxyDetails.Arn, stackID, proxies[0].ProxyDetails.Status.Code)
		return proxies[0]
	default:
		t.Fatalf("AWS verify: expected exactly 1 ELBProxy proxy for stack %q, found %d", stackID, len(proxies))
	}
	return elbproxy.ProxyWithTags{} // unreachable
}

// ProxyARN returns the ARN of the single ELBProxy proxy owned by the given
// gateway stack. Useful for asserting proxy identity is stable across an
// update (i.e. the proxy was reconfigured in place, not recreated).
func (v *AWSVerifier) ProxyARN(t *testing.T, gatewayNamespace, gatewayName string) string {
	t.Helper()
	return v.FindProxy(t, gatewayNamespace, gatewayName).ProxyDetails.Arn
}

// AssertProxySubnets fails the test unless the proxy for the given gateway
// stack is deployed on exactly the wantSubnetIDs (order-independent), as
// reported by the ELBProxy API's network configuration.
func (v *AWSVerifier) AssertProxySubnets(t *testing.T, gatewayNamespace, gatewayName string, wantSubnetIDs []string) {
	t.Helper()
	proxy := v.FindProxy(t, gatewayNamespace, gatewayName)
	got := map[string]struct{}{}
	for _, az := range proxy.ProxyDetails.NetworkConfiguration.VpcConfiguration.AvailabilityZones {
		got[az.SubnetId] = struct{}{}
	}
	if len(got) != len(wantSubnetIDs) {
		t.Fatalf("AWS verify: proxy for %s/%s has %d subnets, want %d (%v)",
			gatewayNamespace, gatewayName, len(got), len(wantSubnetIDs), wantSubnetIDs)
	}
	for _, want := range wantSubnetIDs {
		if _, ok := got[want]; !ok {
			t.Fatalf("AWS verify: proxy for %s/%s missing expected subnet %q (have %v)",
				gatewayNamespace, gatewayName, want, keysOf(got))
		}
	}
	Logf(t, "AWS verify: proxy for %s/%s is on subnets %v", gatewayNamespace, gatewayName, wantSubnetIDs)
}

// AssertProxyIPv4Addressability fails the test unless the proxy for the given
// gateway stack reports the expected IPv4 addressability (e.g. "internal" or
// "internet-facing") from the ELBProxy API.
func (v *AWSVerifier) AssertProxyIPv4Addressability(t *testing.T, gatewayNamespace, gatewayName, want string) {
	t.Helper()
	proxy := v.FindProxy(t, gatewayNamespace, gatewayName)
	got := proxy.ProxyDetails.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv4Addressability
	if got != want {
		t.Fatalf("AWS verify: proxy for %s/%s ipv4Addressability = %q, want %q",
			gatewayNamespace, gatewayName, got, want)
	}
	Logf(t, "AWS verify: proxy for %s/%s ipv4Addressability = %s", gatewayNamespace, gatewayName, want)
}

// AssertProxyIPv6Addressability fails the test unless the proxy for the given
// gateway stack reports the expected IPv6 addressability (e.g. "internal",
// "internet-facing", or "off") from the ELBProxy API.
func (v *AWSVerifier) AssertProxyIPv6Addressability(t *testing.T, gatewayNamespace, gatewayName, want string) {
	t.Helper()
	proxy := v.FindProxy(t, gatewayNamespace, gatewayName)
	got := proxy.ProxyDetails.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv6Addressability
	if got != want {
		t.Fatalf("AWS verify: proxy for %s/%s ipv6Addressability = %q, want %q",
			gatewayNamespace, gatewayName, got, want)
	}
	Logf(t, "AWS verify: proxy for %s/%s ipv6Addressability = %s", gatewayNamespace, gatewayName, want)
}

// keysOf returns the keys of a set, for error messages.
func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// findFrontendSG returns the managed frontend security group for a gateway
// stack, discovered by the same stack + resource-type tags the controller
// stamps. Fails the test if zero or more than one SG matches.
func (v *AWSVerifier) findFrontendSG(t *testing.T, gatewayNamespace, gatewayName string) awsec2types.SecurityGroup {
	t.Helper()
	ctx := t.Context()

	stackID := gatewayNamespace + "/" + gatewayName
	filters := []awsec2types.Filter{
		{Name: strptr("tag:" + tracking.TagKeyCluster), Values: []string{v.clusterName}},
		{Name: strptr("tag:" + tracking.TagKeyStack), Values: []string{stackID}},
		{Name: strptr("tag:" + tracking.TagKeyResourceType), Values: []string{tracking.ResourceIDFrontendSecurityGroup}},
	}
	if v.vpcID != "" {
		filters = append(filters, awsec2types.Filter{Name: strptr("vpc-id"), Values: []string{v.vpcID}})
	}

	out, err := v.ec2Client.DescribeSecurityGroups(ctx, &awsec2.DescribeSecurityGroupsInput{Filters: filters})
	if err != nil {
		t.Fatalf("AWS verify: describe frontend SG for stack %q: %v", stackID, err)
	}
	switch len(out.SecurityGroups) {
	case 0:
		t.Fatalf("AWS verify: no frontend SG found for stack %q", stackID)
	case 1:
		return out.SecurityGroups[0]
	default:
		t.Fatalf("AWS verify: expected exactly 1 frontend SG for stack %q, found %d", stackID, len(out.SecurityGroups))
	}
	return awsec2types.SecurityGroup{} // unreachable
}

// AssertFrontendSGIngressCidrs fails the test unless the managed frontend SG
// for the given gateway stack allows exactly wantCidrs across its ingress rules
// (IPv4 + IPv6, order-independent, deduplicated). This is the AWS-side
// equivalent of spec.ingressCidrs — it confirms the controller programmed the
// SG to match.
func (v *AWSVerifier) AssertFrontendSGIngressCidrs(t *testing.T, gatewayNamespace, gatewayName string, wantCidrs []string) {
	t.Helper()
	sg := v.findFrontendSG(t, gatewayNamespace, gatewayName)

	got := map[string]struct{}{}
	for _, perm := range sg.IpPermissions {
		for _, r := range perm.IpRanges {
			if r.CidrIp != nil {
				got[*r.CidrIp] = struct{}{}
			}
		}
		for _, r := range perm.Ipv6Ranges {
			if r.CidrIpv6 != nil {
				got[*r.CidrIpv6] = struct{}{}
			}
		}
	}

	want := map[string]struct{}{}
	for _, c := range wantCidrs {
		want[c] = struct{}{}
	}

	if !setsEqual(got, want) {
		t.Fatalf("AWS verify: frontend SG for %s/%s allows CIDRs %v, want %v",
			gatewayNamespace, gatewayName, sortedKeys(got), sortedKeys(want))
	}
	Logf(t, "AWS verify: frontend SG for %s/%s allows exactly %v", gatewayNamespace, gatewayName, sortedKeys(want))
}

// WaitForFrontendSGIngressCidrs polls until the frontend SG for the given
// gateway stack allows exactly wantCidrs, or the timeout elapses. Use after
// mutating spec.ingressCidrs, since the SG reconcile is asynchronous.
func (v *AWSVerifier) WaitForFrontendSGIngressCidrs(t *testing.T, gatewayNamespace, gatewayName string, wantCidrs []string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()

	want := map[string]struct{}{}
	for _, c := range wantCidrs {
		want[c] = struct{}{}
	}
	beat := waitHeartbeat(t, fmt.Sprintf("frontend SG for %s/%s to allow %v", gatewayNamespace, gatewayName, wantCidrs), timeout)

	stackID := gatewayNamespace + "/" + gatewayName
	filters := []awsec2types.Filter{
		{Name: strptr("tag:" + tracking.TagKeyCluster), Values: []string{v.clusterName}},
		{Name: strptr("tag:" + tracking.TagKeyStack), Values: []string{stackID}},
		{Name: strptr("tag:" + tracking.TagKeyResourceType), Values: []string{tracking.ResourceIDFrontendSecurityGroup}},
	}
	if v.vpcID != "" {
		filters = append(filters, awsec2types.Filter{Name: strptr("vpc-id"), Values: []string{v.vpcID}})
	}

	var lastGot []string
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		out, err := v.ec2Client.DescribeSecurityGroups(ctx, &awsec2.DescribeSecurityGroupsInput{Filters: filters})
		if err != nil {
			beat("describe error: " + err.Error())
			return false, nil
		}
		if len(out.SecurityGroups) != 1 {
			beat(fmt.Sprintf("expected 1 frontend SG, found %d", len(out.SecurityGroups)))
			return false, nil
		}
		got := map[string]struct{}{}
		for _, perm := range out.SecurityGroups[0].IpPermissions {
			for _, r := range perm.IpRanges {
				if r.CidrIp != nil {
					got[*r.CidrIp] = struct{}{}
				}
			}
			for _, r := range perm.Ipv6Ranges {
				if r.CidrIpv6 != nil {
					got[*r.CidrIpv6] = struct{}{}
				}
			}
		}
		lastGot = sortedKeys(got)
		if setsEqual(got, want) {
			return true, nil
		}
		beat(fmt.Sprintf("SG allows %v, want %v", lastGot, sortedKeys(want)))
		return false, nil
	})
	if err != nil {
		t.Fatalf("AWS verify: frontend SG for %s/%s never reached CIDRs %v (last %v): %v",
			gatewayNamespace, gatewayName, sortedKeys(want), lastGot, err)
	}
	Logf(t, "AWS verify: frontend SG for %s/%s allows exactly %v", gatewayNamespace, gatewayName, sortedKeys(want))
}

// WaitForFrontendSGPort polls until the gateway stack's frontend SG allows protocol/port.
func (v *AWSVerifier) WaitForFrontendSGPort(t *testing.T, gatewayNamespace, gatewayName, protocol string, port int32, timeout time.Duration) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()
	beat := waitHeartbeat(t, fmt.Sprintf("frontend SG for %s/%s to allow %s/%d", gatewayNamespace, gatewayName, protocol, port), timeout)

	stackID := gatewayNamespace + "/" + gatewayName
	filters := []awsec2types.Filter{
		{Name: strptr("tag:" + tracking.TagKeyCluster), Values: []string{v.clusterName}},
		{Name: strptr("tag:" + tracking.TagKeyStack), Values: []string{stackID}},
		{Name: strptr("tag:" + tracking.TagKeyResourceType), Values: []string{tracking.ResourceIDFrontendSecurityGroup}},
	}
	if v.vpcID != "" {
		filters = append(filters, awsec2types.Filter{Name: strptr("vpc-id"), Values: []string{v.vpcID}})
	}
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		out, err := v.ec2Client.DescribeSecurityGroups(ctx, &awsec2.DescribeSecurityGroupsInput{Filters: filters})
		if err != nil {
			beat("describe error: " + err.Error())
			return false, nil
		}
		if len(out.SecurityGroups) != 1 {
			beat(fmt.Sprintf("expected 1 frontend SG, found %d", len(out.SecurityGroups)))
			return false, nil
		}
		for _, perm := range out.SecurityGroups[0].IpPermissions {
			if aws.ToString(perm.IpProtocol) == protocol && perm.FromPort != nil && perm.ToPort != nil &&
				*perm.FromPort <= port && port <= *perm.ToPort {
				return true, nil
			}
		}
		beat("no matching rule yet")
		return false, nil
	})
	if err != nil {
		t.Fatalf("AWS verify: frontend SG for %s/%s never allowed %s/%d: %v", gatewayNamespace, gatewayName, protocol, port, err)
	}
	Logf(t, "AWS verify: frontend SG for %s/%s allows %s/%d", gatewayNamespace, gatewayName, protocol, port)
}

// WaitForNodeSGPort polls until the node SGs allow (want=true) or no longer allow
// protocol/port from the backend SG. The rule is cluster-wide, so use a unique port.
func (v *AWSVerifier) WaitForNodeSGPort(t *testing.T, protocol string, port int32, want bool, timeout time.Duration) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()

	state := "allow"
	if !want {
		state = "no longer allow"
	}
	beat := waitHeartbeat(t, fmt.Sprintf("node SGs to %s %s/%d from the backend SG", state, protocol, port), timeout)

	vpcFilter := func(filters []awsec2types.Filter) []awsec2types.Filter {
		if v.vpcID != "" {
			filters = append(filters, awsec2types.Filter{Name: strptr("vpc-id"), Values: []string{v.vpcID}})
		}
		return filters
	}
	backendFilters := vpcFilter([]awsec2types.Filter{
		{Name: strptr("tag:" + tracking.TagKeyCluster), Values: []string{v.clusterName}},
		{Name: strptr("tag:" + tracking.TagKeyResourceType), Values: []string{tracking.ResourceIDBackendSecurityGroup}},
	})

	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		backend, err := v.ec2Client.DescribeSecurityGroups(ctx, &awsec2.DescribeSecurityGroupsInput{Filters: backendFilters})
		if err != nil {
			beat("describe backend SG: " + err.Error())
			return false, nil
		}
		if len(backend.SecurityGroups) != 1 {
			beat(fmt.Sprintf("expected 1 backend SG, found %d", len(backend.SecurityGroups)))
			return false, nil
		}
		backendID := aws.ToString(backend.SecurityGroups[0].GroupId)
		// The node SGs are the groups with ingress rules from the backend SG.
		nodes, err := v.ec2Client.DescribeSecurityGroups(ctx, &awsec2.DescribeSecurityGroupsInput{
			Filters: vpcFilter([]awsec2types.Filter{{Name: strptr("ip-permission.group-id"), Values: []string{backendID}}}),
		})
		if err != nil {
			beat("describe node SGs: " + err.Error())
			return false, nil
		}
		allowed := false
		for _, sg := range nodes.SecurityGroups {
			for _, perm := range sg.IpPermissions {
				if aws.ToString(perm.IpProtocol) != protocol || perm.FromPort == nil || perm.ToPort == nil ||
					*perm.FromPort > port || *perm.ToPort < port {
					continue
				}
				for _, pair := range perm.UserIdGroupPairs {
					if aws.ToString(pair.GroupId) == backendID {
						allowed = true
					}
				}
			}
		}
		if allowed == want {
			return true, nil
		}
		beat(fmt.Sprintf("allowed=%t", allowed))
		return false, nil
	})
	if err != nil {
		t.Fatalf("AWS verify: node SGs did not %s %s/%d from the backend SG: %v", state, protocol, port, err)
	}
	Logf(t, "AWS verify: node SGs %s %s/%d from the backend SG", map[bool]string{true: "allow", false: "don't allow"}[want], protocol, port)
}

func strptr(s string) *string { return &s }

func setsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WaitForProxyGone polls until no ELBProxy proxy owned by the given gateway
// stack remains, or the timeout elapses. Use after deleting a Gateway /
// ELBProxyConfiguration to assert the remote data plane was actually torn down in AWS
// (the controller's delete path deploys an empty stack so unmatched proxies are
// deleted).
func (v *AWSVerifier) WaitForProxyGone(t *testing.T, gatewayNamespace, gatewayName string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()

	stackID := gatewayNamespace + "/" + gatewayName
	filter := tracking.TagsAsTagFilter(v.trackingProvider.StackTags(stackID))
	beat := waitHeartbeat(t, fmt.Sprintf("proxy for %s to be deleted from AWS", stackID), timeout)

	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		proxies, err := v.taggingManager.ListProxies(ctx, filter)
		if err != nil {
			beat("list proxies error: " + err.Error())
			return false, nil
		}
		if len(proxies) == 0 {
			return true, nil
		}
		beat(fmt.Sprintf("%d proxy still present (status %s)", len(proxies), proxies[0].ProxyDetails.Status.Code))
		return false, nil
	})
	if err != nil {
		t.Fatalf("AWS verify: proxy for %s was not deleted within %s: %v", stackID, timeout, err)
	}
	Logf(t, "AWS verify: proxy for %s is gone from AWS", stackID)
}

// AssertProxyTag fails the test unless the proxy for the given gateway stack
// carries tag key=value. Discovery itself is by the controller's ownership
// tags, so this verifies user-supplied spec.tags reached the ELBProxy API.
func (v *AWSVerifier) AssertProxyTag(t *testing.T, gatewayNamespace, gatewayName, key, value string) {
	t.Helper()
	proxy := v.FindProxy(t, gatewayNamespace, gatewayName)
	got, ok := proxy.Tags[key]
	if !ok {
		t.Fatalf("AWS verify: proxy for %s/%s missing tag %q (have %v)", gatewayNamespace, gatewayName, key, proxy.Tags)
	}
	if got != value {
		t.Fatalf("AWS verify: proxy for %s/%s tag %q = %q, want %q", gatewayNamespace, gatewayName, key, got, value)
	}
	Logf(t, "AWS verify: proxy for %s/%s has tag %s=%s", gatewayNamespace, gatewayName, key, value)
}

// WaitForProxyTag polls until the proxy for the given gateway stack carries
// tag key=value, or the timeout elapses. Use after mutating spec.tags, since
// the controller reconciles to AWS asynchronously.
func (v *AWSVerifier) WaitForProxyTag(t *testing.T, gatewayNamespace, gatewayName, key, value string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()

	stackID := gatewayNamespace + "/" + gatewayName
	filter := tracking.TagsAsTagFilter(v.trackingProvider.StackTags(stackID))
	beat := waitHeartbeat(t, fmt.Sprintf("proxy %s/%s tag %s=%s", gatewayNamespace, gatewayName, key, value), timeout)

	var lastVal string
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		proxies, err := v.taggingManager.ListProxies(ctx, filter)
		if err != nil {
			beat("list proxies error: " + err.Error())
			return false, nil
		}
		if len(proxies) != 1 {
			beat(fmt.Sprintf("expected 1 proxy, found %d", len(proxies)))
			return false, nil
		}
		lastVal = proxies[0].Tags[key]
		if lastVal == value {
			return true, nil
		}
		beat(fmt.Sprintf("tag %s=%q, want %q", key, lastVal, value))
		return false, nil
	})
	if err != nil {
		t.Fatalf("AWS verify: proxy for %s/%s never reached tag %s=%s (last %q): %v",
			gatewayNamespace, gatewayName, key, value, lastVal, err)
	}
	Logf(t, "AWS verify: proxy for %s/%s reached tag %s=%s", gatewayNamespace, gatewayName, key, value)
}

// WaitForProxyAttribute polls until the proxy for the given gateway stack has
// attribute key with the given configured value. Use after mutating
// spec.attributes.
func (v *AWSVerifier) WaitForProxyAttribute(t *testing.T, gatewayNamespace, gatewayName, key, value string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()

	beat := waitHeartbeat(t, fmt.Sprintf("proxy %s/%s attribute %s=%s", gatewayNamespace, gatewayName, key, value), timeout)
	var lastVal string
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		proxy := v.findProxyNoFatal(ctx, gatewayNamespace, gatewayName)
		if proxy == nil {
			beat("proxy not found yet")
			return false, nil
		}
		got, ok := v.attributeValue(ctx, proxy.ProxyDetails.Arn, key)
		if !ok {
			beat("attribute not present yet")
			return false, nil
		}
		lastVal = got
		if got == value {
			return true, nil
		}
		beat(fmt.Sprintf("attribute %s=%q, want %q", key, got, value))
		return false, nil
	})
	if err != nil {
		t.Fatalf("AWS verify: proxy for %s/%s never reached attribute %s=%s (last %q): %v",
			gatewayNamespace, gatewayName, key, value, lastVal, err)
	}
	Logf(t, "AWS verify: proxy for %s/%s reached attribute %s=%s", gatewayNamespace, gatewayName, key, value)
}

// findProxyNoFatal is the non-fatal variant of FindProxy for use inside poll
// loops; it returns nil when zero or multiple proxies match or on error.
func (v *AWSVerifier) findProxyNoFatal(ctx context.Context, gatewayNamespace, gatewayName string) *elbproxy.ProxyWithTags {
	stackID := gatewayNamespace + "/" + gatewayName
	filter := tracking.TagsAsTagFilter(v.trackingProvider.StackTags(stackID))
	proxies, err := v.taggingManager.ListProxies(ctx, filter)
	if err != nil || len(proxies) != 1 {
		return nil
	}
	return &proxies[0]
}

// attributeValue returns the configured value of a single proxy attribute.
func (v *AWSVerifier) attributeValue(ctx context.Context, proxyArn, key string) (string, bool) {
	out, err := v.elbProxyClient.GetProxyAttributes(ctx, elbproxyaws.GetProxyAttributesInput{
		ProxyIdentifier: proxyArn,
	})
	if err != nil {
		return "", false
	}
	for _, a := range out.Attributes {
		if a.Key == key {
			return a.Value, true
		}
	}
	return "", false
}
