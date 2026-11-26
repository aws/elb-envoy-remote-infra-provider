// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	ec2model "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const (
	frontendSGDescription = "[k8s] Frontend SecurityGroup for ELB Proxy"

	defaultIPv4CIDR = "0.0.0.0/0"
	defaultIPv6CIDR = "::/0"
)

type securityGroupOutput struct {
	securityGroupTokens           []core.StringToken
	backendSecurityGroupToken     core.StringToken
	backendSecurityGroupAllocated bool
}

type securityGroupBuilder struct {
	backendSGProvider   networking.BackendSGProvider
	sgReconciler        networking.SecurityGroupReconciler
	nodeSGResolver      networking.NodeSGResolver
	controllerNamespace string
	clusterName         string
	logger              logging.Logger
}

func newSecurityGroupBuilder(
	backendSGProvider networking.BackendSGProvider,
	sgReconciler networking.SecurityGroupReconciler,
	nodeSGResolver networking.NodeSGResolver,
	controllerNamespace string,
	clusterName string,
	logger logging.Logger,
) *securityGroupBuilder {
	return &securityGroupBuilder{
		backendSGProvider:   backendSGProvider,
		sgReconciler:        sgReconciler,
		nodeSGResolver:      nodeSGResolver,
		controllerNamespace: controllerNamespace,
		clusterName:         clusterName,
		logger:              logger,
	}
}

// buildSecurityGroups builds all security group tokens for a proxy.
// If the user provided SGs in the ELBProxyConfiguration CRD, those are returned as literal tokens.
// Otherwise, a managed frontend SG is created on the stack and a shared backend SG is allocated.
func (b *securityGroupBuilder) buildSecurityGroups(
	ctx context.Context,
	stack core.Stack,
	irInfra *ir.Infra,
	elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration,
	backendTargetPorts []ir.PortProtocol,
	allBackendTargetPorts []ir.PortProtocol,
	resolvedIPv6 string,
) (securityGroupOutput, error) {
	if hasUserProvidedSecurityGroups(elbProxyConfig) {
		return b.handleExplicitSecurityGroups(elbProxyConfig)
	}
	return b.handleManagedSecurityGroup(ctx, stack, irInfra, elbProxyConfig, backendTargetPorts, allBackendTargetPorts, resolvedIPv6)
}

// handleExplicitSecurityGroups handles the case where the user specified SGs in the ELBProxyConfiguration CRD.
func (b *securityGroupBuilder) handleExplicitSecurityGroups(
	elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration,
) (securityGroupOutput, error) {
	if len(elbProxyConfig.Spec.IngressCidrs) > 0 || len(elbProxyConfig.Spec.IngressPrefixLists) > 0 {
		b.logger.Info("ingressCidrs and ingressPrefixLists are ignored when securityGroups is set — manage inbound rules on your custom SG directly",
			"proxy", elbProxyConfig.Name)
	}
	var sgTokens []core.StringToken
	for _, sg := range elbProxyConfig.Spec.NetworkConfiguration.VpcConfiguration.SecurityGroups {
		sgTokens = append(sgTokens, core.LiteralStringToken(sg.SecurityGroupId))
	}
	b.logger.Info("SG configured via ELBProxyConfiguration CRD", "proxy", elbProxyConfig.Name, "SGs", sgTokens)
	return securityGroupOutput{
		securityGroupTokens: sgTokens,
	}, nil
}

// handleManagedSecurityGroup handles the case where the controller manages SGs.
// Creates a frontend SG on the stack, allocates a shared backend SG, and reconciles node SG rules.
func (b *securityGroupBuilder) handleManagedSecurityGroup(
	ctx context.Context,
	stack core.Stack,
	irInfra *ir.Infra,
	elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration,
	backendTargetPorts []ir.PortProtocol,
	allBackendTargetPorts []ir.PortProtocol,
	resolvedIPv6 string,
) (securityGroupOutput, error) {
	proxyInfra := irInfra.GetProxyInfra()

	// proxyInfra.Name is in format of "namespace/gateway-name".
	namespacedGateway := proxyInfra.Name

	// Build the managed frontend SG as a stack resource.
	managedSG, err := b.buildManagedSecurityGroup(stack, irInfra, elbProxyConfig, resolvedIPv6)
	if err != nil {
		return securityGroupOutput{}, err
	}

	var sgTokens []core.StringToken
	sgTokens = append(sgTokens, managedSG.GroupID())

	// Allocate the shared backend SG.
	backendSGToken, err := b.getBackendSecurityGroup(ctx, namespacedGateway)
	if err != nil {
		return securityGroupOutput{}, err
	}
	sgTokens = append(sgTokens, backendSGToken)

	// Reconcile node SG ingress rules using the union of all active gateways' ports.
	// This ensures a shared port is only revoked when no gateway needs it anymore.
	backendSGID, err := backendSGToken.Resolve(ctx)
	if err != nil {
		return securityGroupOutput{}, fmt.Errorf("failed to resolve backend SG token: %w", err)
	}
	if err := b.reconcileNodeSGRules(ctx, backendSGID, allBackendTargetPorts); err != nil {
		return securityGroupOutput{}, err
	}

	b.logger.Info("Auto Create SG", "proxy SGs", sgTokens, "backend SG", backendSGToken)
	return securityGroupOutput{
		securityGroupTokens:           sgTokens,
		backendSecurityGroupToken:     backendSGToken,
		backendSecurityGroupAllocated: true,
	}, nil
}

func (b *securityGroupBuilder) getBackendSecurityGroup(ctx context.Context, namespacedGateway string) (core.StringToken, error) {
	ns, name := elbproxyutil.ParseProxyInfraName(namespacedGateway)
	backendSGID, err := b.backendSGProvider.Get(ctx, networking.ResourceTypeGateway, []types.NamespacedName{{Namespace: ns, Name: name}})
	if err != nil {
		return nil, fmt.Errorf("failed to get backend security group: %w", err)
	}
	return core.LiteralStringToken(backendSGID), nil
}

// reconcileNodeSGRules reconciles node SG ingress rules using the union of all active gateways' ports.
// Using the full union ensures a shared port is only revoked when no gateway needs it.
func (b *securityGroupBuilder) reconcileNodeSGRules(ctx context.Context, backendSGID string, allBackendTargetPorts []ir.PortProtocol) error {
	netPorts := make([]networking.PortProtocol, 0, len(allBackendTargetPorts))
	for _, p := range allBackendTargetPorts {
		netPorts = append(netPorts, networking.PortProtocol{Protocol: p.Protocol, Port: int32(p.Port)})
	}
	// This runs while building an active gateway's proxy, so the control-plane ports (xDS and the
	// Wasm module server) are always needed, even if no route has given any gateway a backend port yet.
	nodeSGPorts := networking.CollectNodeSGPorts(netPorts, true, networking.DefaultXDSServerPort, networking.DefaultWasmServerPort)

	// Scope the PermissionSelector by controller namespace so that multi-tenant
	// controllers sharing the same node SGs won't revoke each other's rules.
	// Desired state is the union of all active gateways' ports for this controller,
	// so a shared port is only revoked when no gateway managed by this instance needs it.
	clusterLabels := networking.BuildClusterRuleLabels(b.controllerNamespace)
	nodeSGPerms := networking.BuildNodeSGPermissions(nodeSGPorts, backendSGID, clusterLabels)

	nodeSGIDs, err := b.nodeSGResolver.ResolveNodeSGIDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to resolve node security group IDs: %w", err)
	}

	var errs []error
	for _, sgID := range nodeSGIDs {
		if err := b.sgReconciler.ReconcileIngress(ctx, sgID, nodeSGPerms,
			networking.WithPermissionSelector(labels.SelectorFromSet(clusterLabels))); err != nil {
			errs = append(errs, fmt.Errorf("failed to reconcile node security group rules for %s: %w", sgID, err))
		}
	}
	return errors.Join(errs...)
}

func (b *securityGroupBuilder) buildManagedSecurityGroup(
	stack core.Stack,
	irInfra *ir.Infra,
	elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration,
	resolvedIPv6 string,
) (*ec2model.SecurityGroup, error) {
	proxyInfra := irInfra.GetProxyInfra()
	namespacedGateway := proxyInfra.Name

	ns, gwName := elbproxyutil.ParseProxyInfraName(namespacedGateway)
	sgName := buildManagedSecurityGroupName(b.clusterName, ns, gwName)
	ingressPermissions, err := buildManagedSecurityGroupIngressPermissions(irInfra, elbProxyConfig, resolvedIPv6)
	if err != nil {
		return nil, fmt.Errorf("failed to build frontend SG ingress permissions: %w", err)
	}

	// Warn if the user specified IPv6 CIDRs but IPv6 is disabled — they will be dropped.
	if !isIPv6EnabledFromResolved(resolvedIPv6) {
		for _, cidr := range elbProxyConfig.Spec.IngressCidrs {
			if strings.Contains(cidr, ":") {
				b.logger.Info("IPv6 CIDR in spec.ingressCidrs ignored because IPv6 is disabled on this proxy",
					"proxy", elbProxyConfig.Name,
					"cidr", cidr)
			}
		}
	}

	sgTags := make(map[string]string)
	if elbproxyutil.IsMergedGatewayKey(namespacedGateway) {
		sgTags[tracking.TagKeyDeployMode] = tracking.TagValueDeployModeMerged
	}

	sg, err := ec2model.NewSecurityGroup(stack, tracking.ResourceIDFrontendSecurityGroup, ec2model.SecurityGroupSpec{
		GroupName:   sgName,
		Description: frontendSGDescription,
		Tags:        sgTags,
		Ingress:     ingressPermissions,
	})
	if err != nil {
		return nil, err
	}
	return sg, nil
}

// buildManagedSecurityGroupName generates a deterministic, collision-safe security group name.
func buildManagedSecurityGroupName(clusterName, namespace, name string) string {
	uuidHash := sha256.New()
	_, _ = uuidHash.Write([]byte(clusterName))
	_, _ = uuidHash.Write([]byte("/"))
	_, _ = uuidHash.Write([]byte(namespace))
	_, _ = uuidHash.Write([]byte("/"))
	_, _ = uuidHash.Write([]byte(name))
	uuid := hex.EncodeToString(uuidHash.Sum(nil))

	sanitizedCluster := invalidProxyNamePattern.ReplaceAllString(clusterName, "")
	sanitizedNamespace := invalidProxyNamePattern.ReplaceAllString(namespace, "")
	sanitizedName := invalidProxyNamePattern.ReplaceAllString(name, "")
	return fmt.Sprintf("elbproxy-%.10s-%.10s-%.10s-%.10s", sanitizedCluster, sanitizedNamespace, sanitizedName, uuid)
}

// buildManagedSecurityGroupIngressPermissions derives ingress rules from Gateway listeners.
// Each listener port gets one rule per CIDR (IPv4/IPv6) and one rule per prefix list. The HTTPS
// ports of HTTP/3 listeners get the same rules for UDP as well, for QUIC.
// Returns an error if any CIDR fails net.ParseCIDR() validation.
// resolvedIPv6 is the effective ipv6Addressability after defaults have been applied.
func buildManagedSecurityGroupIngressPermissions(irInfra *ir.Infra, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, resolvedIPv6 string) ([]ec2model.IPPermission, error) {
	proxyInfra := irInfra.GetProxyInfra()
	if proxyInfra == nil {
		return nil, nil
	}

	ipv4CIDRs, ipv6CIDRs, err := resolveIngressCIDRs(elbProxyConfig, resolvedIPv6)
	if err != nil {
		return nil, err
	}
	prefixListIDs := elbProxyConfig.Spec.IngressPrefixLists

	var permissions []ec2model.IPPermission
	type portProtocolKey struct {
		port     int32
		protocol string
	}
	seen := make(map[portProtocolKey]bool)

	// addPort adds one rule per CIDR and per prefix list for port over sgProtocol, once.
	addPort := func(port int32, sgProtocol string) {
		key := portProtocolKey{port: port, protocol: sgProtocol}
		if seen[key] {
			return
		}
		seen[key] = true

		for _, cidr := range ipv4CIDRs {
			permissions = append(permissions, ec2model.IPPermission{
				IPProtocol: sgProtocol,
				FromPort:   aws.Int32(port),
				ToPort:     aws.Int32(port),
				IPRanges: []ec2model.IPRange{{
					CIDRIP:      cidr,
					Description: fmt.Sprintf("Allow IPv4 %s traffic on port %d", sgProtocol, port),
				}},
			})
		}

		for _, cidr := range ipv6CIDRs {
			permissions = append(permissions, ec2model.IPPermission{
				IPProtocol: sgProtocol,
				FromPort:   aws.Int32(port),
				ToPort:     aws.Int32(port),
				IPv6Range: []ec2model.IPv6Range{{
					CIDRIPv6:    cidr,
					Description: fmt.Sprintf("Allow IPv6 %s traffic on port %d", sgProtocol, port),
				}},
			})
		}

		for _, plID := range prefixListIDs {
			permissions = append(permissions, ec2model.IPPermission{
				IPProtocol: sgProtocol,
				FromPort:   aws.Int32(port),
				ToPort:     aws.Int32(port),
				PrefixLists: []ec2model.PrefixList{{
					ListID:      plID,
					Description: fmt.Sprintf("Allow prefix list %s %s traffic on port %d", plID, sgProtocol, port),
				}},
			})
		}
	}

	for _, listener := range proxyInfra.Listeners {
		for _, port := range listener.Ports {
			addPort(port.ServicePort, listenerProtocolToSGProtocol(port.Protocol))
			// HTTP/3 runs over QUIC, which uses UDP on the HTTPS port. Like Envoy Gateway's own
			// Kubernetes Service, open UDP next to TCP on the HTTPS ports of HTTP/3 listeners.
			if listener.GetHttp3() != nil && port.Protocol == ir.HTTPSProtocolType {
				addPort(port.ServicePort, listenerProtocolToSGProtocol(ir.UDPProtocolType))
			}
		}
	}

	return permissions, nil
}

// resolveIngressCIDRs returns validated IPv4 and IPv6 CIDR lists for the frontend SG.
// When spec.ingressCidrs is unset, defaults to ["0.0.0.0/0"] for IPv4 and ["::/0"] for
// IPv6 (only when IPv6 is enabled on the proxy).
func resolveIngressCIDRs(elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, resolvedIPv6 string) (ipv4CIDRs, ipv6CIDRs []string, err error) {
	ipv6Enabled := isIPv6EnabledFromResolved(resolvedIPv6)
	if len(elbProxyConfig.Spec.IngressCidrs) > 0 {
		for _, cidr := range elbProxyConfig.Spec.IngressCidrs {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return nil, nil, fmt.Errorf("invalid CIDR %q in spec.ingressCidrs: %w", cidr, err)
			}
			if strings.Contains(cidr, ":") {
				// Drop IPv6 CIDRs when IPv6 is disabled — the proxy won't accept IPv6 traffic.
				if ipv6Enabled {
					ipv6CIDRs = append(ipv6CIDRs, cidr)
				}
			} else {
				ipv4CIDRs = append(ipv4CIDRs, cidr)
			}
		}
		return ipv4CIDRs, ipv6CIDRs, nil
	}
	// Default: open to all.
	ipv4CIDRs = []string{defaultIPv4CIDR}
	if ipv6Enabled {
		ipv6CIDRs = []string{defaultIPv6CIDR}
	}
	return ipv4CIDRs, ipv6CIDRs, nil
}

// isIPv6EnabledFromResolved checks whether IPv6 is enabled from the resolved
// ipv6Addressability value (after EnvoyProxy IPFamily defaults have been applied).
// Use this instead of isIPv6Enabled when the resolved value is available.
func isIPv6EnabledFromResolved(resolvedIPv6 string) bool {
	return resolvedIPv6 != "" && resolvedIPv6 != "off"
}

// listenerProtocolToSGProtocol maps an IR listener protocol to an EC2 security group IP protocol.
func listenerProtocolToSGProtocol(p string) string {
	if p == ir.UDPProtocolType {
		return strings.ToLower(string(corev1.ProtocolUDP))
	}
	return strings.ToLower(string(corev1.ProtocolTCP))
}

// hasUserProvidedSecurityGroups checks if the user specified security groups in the ELBProxyConfiguration CRD.
func hasUserProvidedSecurityGroups(elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration) bool {
	return len(elbProxyConfig.Spec.NetworkConfiguration.VpcConfiguration.SecurityGroups) > 0
}
