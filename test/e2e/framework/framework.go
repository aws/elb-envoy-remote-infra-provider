// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package framework provides the end-to-end test harness used by the
// ELBProxy gateway extension test suite. The harness assumes the cluster,
// Envoy Gateway, and the extension are already installed; it only exercises
// and cleans up per-test resources.
package framework

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CLI flags. Parsed lazily by the test entry point.
var (
	// IP family constants. EKS clusters are single-stack at the pod/service
	// level (IPv4 or IPv6, chosen at cluster creation and immutable)
	IPFamilyIPv4 = "IPv4"
	IPFamilyIPv6 = "IPv6"
)
var (
	FlagControllerName   = flag.String("controller-name", "gateway.envoyproxy.io/gatewayclass-controller", "controllerName of the GatewayClass each test creates. Must match the Envoy Gateway controller the ELBProxy extension backs.")
	FlagEnvoyProxyNS     = flag.String("envoyproxy-namespace", "envoy-gateway-system", "Envoy Gateway install namespace. Each test's EnvoyProxy and GatewayClass-scoped ELBProxyConfigurations are created here.")
	FlagRunTest          = flag.String("run-test", "", "Run only the test with this ShortName.")
	FlagCleanup          = flag.Bool("cleanup", true, "Clean up resources created by each test on completion.")
	FlagRoleArn          = flag.String("role-arn", os.Getenv("E2E_ROLE_ARN"), "IAM role ARN for ELBProxyConfiguration.spec.roleArn.")
	FlagSubnetIDsCSV     = flag.String("subnet-ids", os.Getenv("E2E_SUBNET_IDS"), "Comma-separated subnet IDs for ELBProxyConfiguration networking.")
	FlagClusterName      = flag.String("cluster-name", os.Getenv("E2E_CLUSTER_NAME"), "Cluster name the controller was started with (--cluster-name). Required only for AWS-side verification (NewAWSVerifier); tests skip those checks when unset.")
	FlagAWSRegion        = flag.String("aws-region", firstNonEmpty(os.Getenv("E2E_AWS_REGION"), os.Getenv("AWS_REGION")), "AWS region for the read-only verification client. Defaults to E2E_AWS_REGION or AWS_REGION.")
	FlagAWSVpcID         = flag.String("aws-vpc-id", os.Getenv("E2E_AWS_VPC_ID"), "VPC ID for AWS security-group verification. Optional; scopes SG lookups to this VPC when set.")
	FlagAWSAPIEndpoint   = flag.String("aws-api-endpoint", os.Getenv("E2E_AWS_API_ENDPOINT"), "Override URL for the ELB Proxy API endpoint used by the verifier. Optional; when empty the SDK default (prod) endpoint is used. Set to the gamma URL for gamma testing, together with -aws-tagging-api-endpoint.")
	FlagTaggingEndpoint  = flag.String("aws-tagging-api-endpoint", os.Getenv("E2E_AWS_TAGGING_API_ENDPOINT"), "Override URL for the Resource Groups Tagging API endpoint the verifier finds proxies with. Optional; when empty the SDK default (prod) endpoint is used. Set to the gamma URL for gamma testing; the account must be allowlisted for it.")
	FlagIPFamily         = flag.String("ip-family", firstNonEmpty(os.Getenv("E2E_IP_FAMILY"), IPFamilyIPv4), "IP family of the cluster (IPv4 or IPv6). Determines addressability defaults for tests.")
	FlagApplyTimeout     = flag.Duration("apply-timeout", 30*time.Second, "Timeout for creating a single object.")
	FlagAcceptedTimeout  = flag.Duration("accepted-timeout", 5*time.Minute, "Timeout for waiting on Accepted conditions.")
	FlagAddressTimeout   = flag.Duration("address-timeout", 10*time.Minute, "Timeout for waiting on Gateway addresses to be assigned.")
	FlagHTTPProbeTimeout = flag.Duration("http-probe-timeout", 10*time.Minute, "Total timeout for HTTP probe (until first expected status). A cold load balancer can take several minutes to serve traffic.")
	FlagDeleteTimeout    = flag.Duration("delete-timeout", 10*time.Minute, "Timeout for resource cleanup.")
)

// TestCase is a single end-to-end scenario. Test builds and deploys its own
// resources (see test/e2e/resources); everything it creates is torn down
// after it returns.
type TestCase struct {
	ShortName   string // unique id, used by -run-test
	Description string
	Test        func(t *testing.T, s *Suite)
}

// Suite holds the shared state passed to each TestCase.Test.
type Suite struct {
	Client     client.Client
	RestConfig *rest.Config

	ControllerName string // controllerName of the GatewayClass each test creates

	// EnvoyProxyNamespace is the Envoy Gateway install namespace. Each test's
	// EnvoyProxy lives here, and so do GatewayClass-scoped
	// ELBProxyConfigurations (the controller only honors them from its own
	// namespace).
	EnvoyProxyNamespace string

	RoleArn   string   // ELBProxyConfiguration.spec.roleArn
	SubnetIDs []string // optional ELBProxyConfiguration subnets

	// Ipv4Addressability and Ipv6Addressability are the default ELBProxyConfiguration
	// addressability values for tests, derived from the cluster's IP family.
	Ipv4Addressability string
	Ipv6Addressability string

	// IPFamily indicates the IP family of the cluster (IPv4 or IPv6).
	// Tests use this to determine addressability defaults and skip conditions.
	IPFamily string

	// ClusterName / AWSRegion configure the optional read-only AWS verifier.
	// Empty ClusterName disables AWS-side verification (NewAWSVerifier skips).
	ClusterName string
	AWSRegion   string
	AWSVpcID    string

	// AWSAPIEndpoint optionally overrides the ELB Proxy API endpoint the
	// verifier talks to. Empty means use the SDK default (prod) endpoint; set it
	// to the gamma URL when running against gamma.
	AWSAPIEndpoint string
	// AWSTaggingEndpoint optionally overrides the Resource Groups Tagging API
	// endpoint the verifier finds proxies with. Empty means use the SDK default
	// (prod) endpoint. ELB Proxy writes its tags to the tagging service in the
	// same environment, so set it to the matching URL along with AWSAPIEndpoint
	// when running against gamma.
	AWSTaggingEndpoint string

	Timeouts Timeouts

	// scheme backs Client; used to resolve GVKs for cleanup since Create strips
	// TypeMeta.
	scheme  *runtime.Scheme
	cleanup *cleanupTracker
}

// Timeouts groups the harness's poll/wait deadlines.
type Timeouts struct {
	Apply        time.Duration
	Accepted     time.Duration
	Address      time.Duration
	HTTPProbe    time.Duration
	Delete       time.Duration
	PollInterval time.Duration
}

// EnvoyIPFamily returns the EnvoyProxy spec.ipFamily value appropriate for the
// cluster's IP family. This controls the address family Envoy's listener
// socket actually binds to — a separate concern from ELBProxy's
// ipv4Addressability/ipv6Addressability, which only governs what addresses
// AWS exposes for the proxy's network interface and DNS records. On an IPv6
// cluster, Envoy binds DualStack: pods are IPv6-only, but the ELBProxy EC2
// proxy fleet runs in dual-stack VPC subnets, so it can and should accept
// both address families.
func (s *Suite) EnvoyIPFamily() string {
	if s.IPFamily == IPFamilyIPv6 {
		return "DualStack"
	}
	return "IPv4"
}

// SubnetIDsFromFlag parses the comma-separated -subnet-ids flag.
func SubnetIDsFromFlag() []string {
	if FlagSubnetIDsCSV == nil || *FlagSubnetIDsCSV == "" {
		return nil
	}
	parts := strings.Split(*FlagSubnetIDsCSV, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// firstNonEmpty returns the first non-empty string in vals, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewSuite loads the active kubeconfig and a typed client.
func NewSuite(t *testing.T) *Suite {
	t.Helper()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		t.Fatalf("failed to load kubeconfig: %v", err)
	}

	scheme := buildScheme()
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	// Compute default addressability from IP family. EKS pods are single-stack
	// (IPv4 or IPv6 only), but the proxy fleet runs on EC2 in VPC subnets,
	// which are dual-stack. On an IPv6 cluster the proxy still gets both an
	// IPv4 and an IPv6 address, since the underlying subnets support both.
	ipv4Addr := "internet-facing"
	ipv6Addr := "off"
	if *FlagIPFamily == IPFamilyIPv6 {
		ipv6Addr = "internet-facing"
	}

	return &Suite{
		Client:              cl,
		RestConfig:          cfg,
		ControllerName:      *FlagControllerName,
		EnvoyProxyNamespace: *FlagEnvoyProxyNS,
		RoleArn:             *FlagRoleArn,
		SubnetIDs:           SubnetIDsFromFlag(),
		Ipv4Addressability:  ipv4Addr,
		Ipv6Addressability:  ipv6Addr,
		ClusterName:         *FlagClusterName,
		AWSRegion:           *FlagAWSRegion,
		AWSVpcID:            *FlagAWSVpcID,
		AWSAPIEndpoint:      *FlagAWSAPIEndpoint,
		AWSTaggingEndpoint:  *FlagTaggingEndpoint,
		IPFamily:            *FlagIPFamily,
		Timeouts: Timeouts{
			Apply:        *FlagApplyTimeout,
			Accepted:     *FlagAcceptedTimeout,
			Address:      *FlagAddressTimeout,
			HTTPProbe:    *FlagHTTPProbeTimeout,
			Delete:       *FlagDeleteTimeout,
			PollInterval: 2 * time.Second,
		},
		scheme:  scheme,
		cleanup: &cleanupTracker{},
	}
}

// RunTest runs the case and cleans up everything it created afterward. Each
// case gets its own cleanup tracker so cases don't leak into each other.
func RunTest(t *testing.T, parent *Suite, tc TestCase) {
	t.Helper()

	s := *parent
	s.cleanup = &cleanupTracker{}

	if *FlagCleanup {
		t.Cleanup(func() {
			s.cleanup.cleanupAll(s.Timeouts.Delete, t)
		})
	}

	tc.Test(t, &s)
}
