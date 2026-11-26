// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/bootstrap"
	upstreamversion "github.com/aws/elb-envoy-remote-infra-provider/pkg/version"
	"gopkg.in/yaml.v3"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
)

const (
	proxyTypeEnvoy         = "envoy"
	proxyBootstrapFileName = "bootstrap.yaml"
)

var invalidProxyNamePattern = regexp.MustCompile("[[:^alnum:]]")

type ELBProxyBuilder struct {
	logger         logging.Logger
	client         client.Client
	sgBuilder      *securityGroupBuilder
	subnetsBuilder *elbProxySubnetsBuilder
	serviceBuilder ServiceBuilder
	tagHelper      TagHelper
	clusterName    string
}

func NewProxyBuilder(
	logger logging.Logger,
	cli client.Client,
	backendSGProvider networking.BackendSGProvider,
	sgReconciler networking.SecurityGroupReconciler,
	subnetResolver networking.SubnetsResolver,
	trackingProvider tracking.Provider,
	nodeSGResolver networking.NodeSGResolver,
	elbProxyTaggingManager elbproxy.TaggingManager,
	tagHelper TagHelper,
	controllerNamespace string,
	clusterName string,
) *ELBProxyBuilder {

	sgBuilder := newSecurityGroupBuilder(backendSGProvider, sgReconciler, nodeSGResolver, controllerNamespace, clusterName, logger)
	subnetsBuilder := newELBProxySubnetsBuilder(subnetResolver, trackingProvider, elbProxyTaggingManager)
	svcBuilder := newServiceBuilder()

	return &ELBProxyBuilder{
		logger:         logger,
		client:         cli,
		sgBuilder:      sgBuilder,
		subnetsBuilder: subnetsBuilder,
		serviceBuilder: svcBuilder,
		tagHelper:      tagHelper,
		clusterName:    clusterName,
	}
}

func (b *ELBProxyBuilder) Build(ctx context.Context, irInfra *ir.Infra, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, stack core.Stack, backendTargetPorts []ir.PortProtocol, allBackendTargetPorts []ir.PortProtocol, xdsHosts []string) error {
	pr, err := b.buildProxy(ctx, irInfra, elbProxyConfig, stack, backendTargetPorts, allBackendTargetPorts, xdsHosts)
	if err != nil {
		return err
	}

	// The Kubernetes Service is IR-derived (ports come from Gateway listeners).
	// In IR-absent mode the service synthesizer preserves the existing Service, so
	// we skip building it here to avoid adding an empty/incomplete desired Service
	// to the stack.
	if !stack.IRPresent() {
		b.logger.Info("IR absent, skipping service build (existing Service preserved)")
		return nil
	}

	b.logger.Info("Building svc for proxy")
	err = b.serviceBuilder.Build(stack, pr, irInfra)
	if err != nil {
		b.logger.Error(err, "Failed to build service for proxy")
	}
	return err
}

func (b *ELBProxyBuilder) buildProxy(ctx context.Context, irInfra *ir.Infra, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, stack core.Stack, backendTargetPorts []ir.PortProtocol, allBackendTargetPorts []ir.PortProtocol, xdsHosts []string) (*elbproxymodel.ProxyResource, error) {
	spec, err := b.buildProxySpec(ctx, irInfra, elbProxyConfig, stack, backendTargetPorts, allBackendTargetPorts, xdsHosts)
	if err != nil {
		return nil, err
	}

	proxyRes, err := elbproxymodel.NewProxyResource(stack, tracking.ResourceIDProxy)
	if err != nil {
		return nil, err
	}
	proxyRes.Name = spec.Name
	proxyRes.ProxyType = spec.Type
	proxyRes.RoleArn = spec.RoleArn
	proxyRes.ProxyVersion = spec.ProxyVersion
	proxyRes.Attributes = spec.Attributes
	proxyRes.Tags = spec.Tags
	proxyRes.Configuration = spec.Configuration
	proxyRes.NetworkConfiguration = spec.NetworkConfiguration
	proxyRes.SecurityGroupTokens = spec.SecurityGroups
	proxyRes.CustomerKmsKeyArn = spec.CustomerKmsKeyArn
	b.logger.Info("built proxy", "name", proxyRes.Name)
	return proxyRes, nil
}

// buildProxySpec assembles the full spec for an ELBProxy proxy by combining:
// - ELBProxyConfiguration CRD (already resolved upstream): name, IAM role, version, network config
// - IR (gateway resources): Envoy bootstrap config with xDS server endpoint
// - Security groups: user-provided or controller-managed (frontend + backend SG tokens)
func (b *ELBProxyBuilder) buildProxySpec(ctx context.Context, irInfra *ir.Infra, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, stack core.Stack, backendTargetPorts []ir.PortProtocol, allBackendTargetPorts []ir.PortProtocol, xdsHosts []string) (elbproxymodel.ProxySpec, error) {
	if irInfra == nil {
		return elbproxymodel.ProxySpec{}, fmt.Errorf("ir infrastructure is nil")
	}

	if elbProxyConfig == nil {
		return elbproxymodel.ProxySpec{}, fmt.Errorf("elbproxy proxy is nil")
	}

	proxyInfra := irInfra.GetProxyInfra()

	name, err := b.buildProxyName(irInfra, elbProxyConfig)
	if err != nil {
		return elbproxymodel.ProxySpec{}, err
	}
	// The bundled Envoy Gateway's version, which is compatible with the generated config.
	proxyVersion := upstreamversion.Get().EnvoyProxyVersion

	irPresent := stack.IRPresent()

	// Skip when IR is absent (teardown/no-op): no meaningful EnvoyProxy config.
	if irPresent {
		if err := validateAccessLog(irInfra.GetProxyConfig()); err != nil {
			return elbproxymodel.ProxySpec{}, err
		}
	}

	networkConfig, err := b.buildProxyNetworkConfiguration(ctx, stack, irInfra, elbProxyConfig, backendTargetPorts, allBackendTargetPorts)
	if err != nil {
		return elbproxymodel.ProxySpec{}, err
	}

	configuration, err := b.buildProxyConfiguration(ctx, irInfra, xdsHosts, networkConfig.zones, irPresent)
	if err != nil {
		return elbproxymodel.ProxySpec{}, err
	}

	mergedTags, err := b.tagHelper.ProcessTags(elbProxyConfig.Spec.Tags)
	if err != nil {
		return elbproxymodel.ProxySpec{}, err
	}

	// Stamp merged gateway indicator so the GC can distinguish merged stacks
	// from per-gateway stacks without relying on naming heuristics.
	if elbproxyutil.IsMergedGatewayKey(proxyInfra.Name) {
		mergedTags[tracking.TagKeyDeployMode] = tracking.TagValueDeployModeMerged
	}

	attributes, err := buildProxyAttributes(elbProxyConfig, irInfra.GetProxyConfig())
	if err != nil {
		return elbproxymodel.ProxySpec{}, err
	}

	return elbproxymodel.ProxySpec{
		Name:                 name,
		Type:                 proxyTypeEnvoy,
		RoleArn:              elbProxyConfig.Spec.RoleArn,
		ProxyVersion:         proxyVersion,
		Attributes:           attributes,
		Tags:                 mergedTags,
		Configuration:        configuration,
		NetworkConfiguration: networkConfig.networkConfig,
		SecurityGroups:       networkConfig.securityGroupTokens,
		CustomerKmsKeyArn:    elbProxyConfig.Spec.CustomerKmsKeyArn,
	}, nil
}

// reservedLogLevelAttributeKeys are ELBProxy attribute keys that this controller
// derives exclusively from the EnvoyProxy spec.logging.level configuration.
// Users must not set them directly via ELBProxyConfiguration spec.attributes; doing so is
// rejected so that EnvoyProxy remains the single source of truth for logging.
var reservedLogLevelAttributeKeys = map[string]struct{}{
	elbproxyaws.AttributeKeyEnvoyLogLevel:          {},
	elbproxyaws.AttributeKeyEnvoyComponentLogLevel: {},
}

// validateAccessLog rejects a File access-log sink, which writes to a local
// path (e.g. the default /dev/stdout) absent on the managed proxy instance and
// breaks the listener. Disabled logging and network sinks (OpenTelemetry, ALS)
// are allowed. Unset telemetry/accessLog/settings is rejected because Envoy
// Gateway defaults to the stdout File sink.
func validateAccessLog(proxyConfig *egv1a1.EnvoyProxy) error {
	const guidance = "access logging on AWS-managed proxy must either be disabled " +
		"(telemetry.accessLog.disable: true) or use a network sink (OpenTelemetry or ALS); " +
		"a File sink writes to a local path such as /dev/stdout that does not exist on the " +
		"proxy instance and breaks the listener"

	tel := proxyConfig.Spec.Telemetry
	if tel == nil || tel.AccessLog == nil {
		return errors.New(guidance)
	}
	al := tel.AccessLog

	if ptr.Deref(al.Disable, false) {
		return nil
	}
	if len(al.Settings) == 0 {
		return errors.New(guidance)
	}

	for _, setting := range al.Settings {
		for _, sink := range setting.Sinks {
			if sink.Type == egv1a1.ProxyAccessLogSinkTypeFile {
				return errors.New(guidance)
			}
		}
	}
	return nil
}

// buildProxyAttributes assembles the ELBProxy proxy attributes.
func buildProxyAttributes(elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, proxyConfig *egv1a1.EnvoyProxy) ([]elbproxyaws.Attribute, error) {
	var attrs []elbproxyaws.Attribute

	for _, a := range elbProxyConfig.Spec.Attributes {
		if _, reserved := reservedLogLevelAttributeKeys[a.Key]; reserved {
			return nil, fmt.Errorf("attribute %q is managed by the controller and cannot be set via ELBProxyConfiguration.spec.attributes; "+
				"configure Envoy logging through EnvoyProxy.spec.logging.level instead", a.Key)
		}
		attrs = append(attrs, elbproxyaws.Attribute{
			Key:   a.Key,
			Value: a.Value,
		})
	}

	attrs = append(attrs, buildLogLevelAttributes(proxyConfig)...)

	return attrs, nil
}

// buildLogLevelAttributes derives Envoy log-level attributes from the
// EnvoyProxy spec.logging.level map:
//   - the "default" component maps to envoy.log_level
//   - all other components map to envoy.component_log_level as a
//     comma-separated list of component:level pairs
func buildLogLevelAttributes(proxyConfig *egv1a1.EnvoyProxy) []elbproxyaws.Attribute {
	if proxyConfig == nil {
		return nil
	}

	levels := proxyConfig.Spec.Logging.Level

	if len(levels) == 0 {
		return nil
	}

	var attrs []elbproxyaws.Attribute

	if defaultLevel := levels[egv1a1.LogComponentDefault]; defaultLevel != "" {
		attrs = append(attrs, elbproxyaws.Attribute{
			Key:   elbproxyaws.AttributeKeyEnvoyLogLevel,
			Value: string(defaultLevel),
		})
	}

	if componentLevel := buildComponentLogLevel(levels); componentLevel != "" {
		attrs = append(attrs, elbproxyaws.Attribute{
			Key:   elbproxyaws.AttributeKeyEnvoyComponentLogLevel,
			Value: componentLevel,
		})
	}

	return attrs
}

// buildComponentLogLevel renders the non-default components of the logging
// level map into a deterministic comma-separated "component:level" string.
func buildComponentLogLevel(levels map[egv1a1.ProxyLogComponent]egv1a1.LogLevel) string {
	args := make([]string, 0, len(levels))
	for component, level := range levels {
		if component == egv1a1.LogComponentDefault || level == "" {
			continue
		}
		args = append(args, fmt.Sprintf("%s:%s", component, level))
	}
	sort.Strings(args)
	return strings.Join(args, ",")
}

func (b *ELBProxyBuilder) buildProxyName(irInfra *ir.Infra, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration) (string, error) {
	if elbProxyConfig.Spec.Name != "" {
		return elbProxyConfig.Spec.Name, nil
	}
	proxyInfra := irInfra.GetProxyInfra()

	// proxyInfra.Name is "namespace/gateway-name" for non-merged gateways, or
	// the bare GatewayClass name for merged gateways.
	// The cluster name is mixed into the hash so two clusters in the same AWS
	// account with an identically-named gateway produce distinct proxy names
	uuidHash := sha256.New()
	_, _ = uuidHash.Write([]byte(b.clusterName))
	_, _ = uuidHash.Write([]byte(proxyInfra.Name))
	// Create-only fields: a change gives the replacement proxy a new name.
	ip := b.buildProxyIPConfiguration(irInfra, elbProxyConfig).IpAddressingConfiguration
	_, _ = uuidHash.Write([]byte(elbProxyConfig.Spec.RoleArn))
	_, _ = uuidHash.Write([]byte(ip.Ipv4Addressability))
	_, _ = uuidHash.Write([]byte(ip.Ipv6Addressability))
	uuid := hex.EncodeToString(uuidHash.Sum(nil))

	sanitizedName := invalidProxyNamePattern.ReplaceAllString(proxyInfra.Name, "")
	return fmt.Sprintf("elbp-%.16s-%.10s", sanitizedName, uuid), nil
}

func (b *ELBProxyBuilder) buildProxyConfiguration(ctx context.Context, irInfra *ir.Infra, xdsHosts []string, zones []string, irPresent bool) (elbproxyaws.ProxyConfiguration, error) {
	// Archive is IR-free — always (re)build it. zones come from the resolved subnets;
	// see buildProxySpec.
	certPaths := DefaultCertPaths()
	configGen := NewConfigGenerator(b.logger, certPaths)
	archiveBytes, err := configGen.GenerateArchive(xdsHosts, zones)
	if err != nil {
		return elbproxyaws.ProxyConfiguration{}, fmt.Errorf("failed to generate config archive: %w", err)
	}

	// Bootstrap is IR-derived. In IR-absent mode, leave it empty so the config
	// reconciler's bootstrapChanged() is false by construction — the deployed
	// bootstrap is preserved, never overwritten with a stale/empty desired.
	if !irPresent {
		b.logger.Info("IR absent, skipping bootstrap generation (archive-only configuration)")
		return elbproxyaws.ProxyConfiguration{
			ArchiveBytes: archiveBytes,
		}, nil
	}

	proxyInfra := irInfra.GetProxyInfra()
	proxyConfig := irInfra.GetProxyConfig()
	bootstrapYAML, err := b.generateBootstrap(proxyInfra, proxyConfig)
	if err != nil {
		return elbproxyaws.ProxyConfiguration{}, fmt.Errorf("failed to generate bootstrap: %w", err)
	}

	b.logger.V(1).Info("generated bootstrap configuration", "bootstrapBytes", len(bootstrapYAML))

	return elbproxyaws.ProxyConfiguration{
		BootstrapFile: []byte(bootstrapYAML),
		ArchiveBytes:  archiveBytes,
	}, nil
}

// networkBuildOutput holds the results of building all network-related configuration.
type networkBuildOutput struct {
	networkConfig       elbproxyaws.NetworkUserConfiguration
	securityGroupTokens []core.StringToken
	// zones are the sorted, distinct AZ names of the resolved subnets, carried out so the
	// archive can publish local_cluster without resolving subnets again.
	zones []string
}

func (b *ELBProxyBuilder) buildProxyNetworkConfiguration(ctx context.Context, stack core.Stack, irInfra *ir.Infra, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, backendTargetPorts []ir.PortProtocol, allBackendTargetPorts []ir.PortProtocol) (networkBuildOutput, error) {
	ipConfig := b.buildProxyIPConfiguration(irInfra, elbProxyConfig)

	// When IPv4 is off, subnet discovery is driven by IPv6 addressability.
	subnetAddressability := ipConfig.IpAddressingConfiguration.Ipv4Addressability
	if subnetAddressability == elbproxyaws.IpAddressabilityOff {
		subnetAddressability = ipConfig.IpAddressingConfiguration.Ipv6Addressability
	}

	// Subnets must carry an associated IPv6 CIDR whenever the proxy enables IPv6.
	ipv6Required := isIPv6EnabledFromResolved(ipConfig.IpAddressingConfiguration.Ipv6Addressability)

	subnets, zones, err := b.buildProxySubnets(ctx, elbProxyConfig, stack.StackID().String(), subnetAddressability, ipv6Required)
	if err != nil {
		return networkBuildOutput{}, fmt.Errorf("failed to resolve subnets: %w", err)
	}

	// Security groups are IR-derived (frontend SG ingress comes from listener ports).
	// In IR-absent mode, skip building them entirely: do not add the frontend SG to
	// the stack and emit no SG tokens. The network-config reconciler treats an empty
	// desired SG set as "preserve" (see proxy_network_config_reconciler.go), so the
	// proxy's live SGs are left untouched rather than revoked.
	if !stack.IRPresent() {
		b.logger.Info("IR absent, skipping security group build (existing SGs preserved)")
		return networkBuildOutput{
			networkConfig: elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{
					Subnets: subnets,
				},
				IpConfiguration: ipConfig,
			},
			zones: zones,
		}, nil
	}

	// Build security group tokens (resolved at deploy time by the synthesizer).
	resolvedIPv6 := ipConfig.IpAddressingConfiguration.Ipv6Addressability
	sgOutput, err := b.sgBuilder.buildSecurityGroups(ctx, stack, irInfra, elbProxyConfig, backendTargetPorts, allBackendTargetPorts, resolvedIPv6)
	if err != nil {
		return networkBuildOutput{}, fmt.Errorf("failed to build security groups: %w", err)
	}

	return networkBuildOutput{
		networkConfig: elbproxyaws.NetworkUserConfiguration{
			VpcConfiguration: elbproxyaws.VpcUserConfiguration{
				Subnets: subnets,
				// SecurityGroups resolved from tokens at deploy time, not set here.
			},
			IpConfiguration: ipConfig,
		},
		securityGroupTokens: sgOutput.securityGroupTokens,
		zones:               zones,
	}, nil
}

// buildProxySubnets resolves the proxy's subnets and returns the subnet details for the
// ELBProxy API plus the distinct AZ names those subnets occupy (used to build
// local_cluster; see GetLocalClusterEndpointsConfigData).
func (b *ELBProxyBuilder) buildProxySubnets(ctx context.Context, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration, stackID string, ipv4Addressability string, ipv6Required bool) ([]elbproxyaws.SubnetDetails, []string, error) {
	crdSubnets := elbProxyConfig.Spec.NetworkConfiguration.VpcConfiguration.Subnets

	ec2Subnets, err := b.subnetsBuilder.resolveSubnets(ctx, crdSubnets, ipv4Addressability, ipv6Required, stackID)
	if err != nil {
		return nil, nil, err
	}

	subnets := make([]elbproxyaws.SubnetDetails, 0, len(ec2Subnets))
	zoneSet := make(map[string]struct{}, len(ec2Subnets))
	for _, s := range ec2Subnets {
		subnets = append(subnets, elbproxyaws.SubnetDetails{SubnetId: *s.SubnetId})
		// AvailabilityZone (the name "us-east-1a"), not AvailabilityZoneId ("use1-az6"):
		// Envoy string-matches it against the zone EG tags on upstream endpoints from
		// topology.kubernetes.io/zone; a mismatch silently disables locality routing.
		if zone := awssdk.ToString(s.AvailabilityZone); zone != "" {
			zoneSet[zone] = struct{}{}
		}
	}

	zones := make([]string, 0, len(zoneSet))
	for zone := range zoneSet {
		zones = append(zones, zone)
	}
	// Sort so archive bytes stay stable across reconciles (hash-deduped downstream).
	sort.Strings(zones)

	return subnets, zones, nil
}

// buildProxyIPConfiguration derives IP addressing from the ELBProxyConfiguration spec.
// User-provided values are always respected. When a value is not set, defaults
// are derived from the EnvoyProxy IPFamily in the wire IR:
//   - IPv4 (or unset): ipv4=internal, ipv6=off
//   - IPv6:            ipv4=off,      ipv6=internal
//   - DualStack:       ipv4=internal, ipv6=internal
//
// "internal" (default) means the proxy is reachable only within the VPC.
// Use "internet-facing" to expose the proxy to the public internet.
func (b *ELBProxyBuilder) buildProxyIPConfiguration(irInfra *ir.Infra, elbProxyConfig *elbproxyv1alpha1.ELBProxyConfiguration) elbproxyaws.IpConfiguration {
	spec := elbProxyConfig.Spec.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration
	ipv4 := spec.Ipv4Addressability
	ipv6 := spec.Ipv6Addressability

	// Derive defaults from EnvoyProxy IPFamily when the user hasn't set them.
	var ipFamily egv1a1.IPFamily
	if irInfra != nil && irInfra.Proxy != nil {
		proxyConfig := irInfra.GetProxyConfig()
		if proxyConfig.Spec.IPFamily != nil {
			ipFamily = *proxyConfig.Spec.IPFamily
		}
	}

	switch ipFamily {
	case egv1a1.IPv6:
		if ipv4 == "" {
			ipv4 = elbproxyaws.IpAddressabilityOff
		}
		if ipv6 == "" {
			ipv6 = elbproxyaws.IpAddressabilityInternal
		}
	case egv1a1.DualStack:
		if ipv4 == "" {
			ipv4 = elbproxyaws.IpAddressabilityInternal
		}
		if ipv6 == "" {
			ipv6 = elbproxyaws.IpAddressabilityInternal
		}
	default: // IPv4 or unset
		if ipv4 == "" {
			ipv4 = elbproxyaws.IpAddressabilityInternal
		}
		if ipv6 == "" {
			ipv6 = elbproxyaws.IpAddressabilityOff
		}
	}

	return elbproxyaws.IpConfiguration{
		IpAddressingConfiguration: elbproxyaws.IpAddressingConfiguration{
			Ipv4Addressability: ipv4,
			Ipv6Addressability: ipv6,
		},
	}
}

func (b *ELBProxyBuilder) generateBootstrap(proxyInfra *pb.ProxyInfra, proxyConfig *egv1a1.EnvoyProxy) (string, error) {
	var proxyMetrics *egv1a1.ProxyMetrics
	if proxyConfig.Spec.Telemetry != nil {
		proxyMetrics = proxyConfig.Spec.Telemetry.Metrics
	}

	bootstrapOpts := &bootstrap.RenderBootstrapConfigOptions{
		ProxyMetrics:        proxyMetrics,
		ResolvedMetricSinks: convertResolvedMetricSinks(proxyInfra.ResolvedMetricSinks),
	}

	bootstrapYAML, err := bootstrap.GetRenderedBootstrapConfig(bootstrapOpts)
	if err != nil {
		return "", err
	}

	// Apply user-provided bootstrap customization
	if proxyConfig.Spec.Bootstrap != nil {
		bootstrapYAML, err = bootstrap.ApplyBootstrapConfig(proxyConfig.Spec.Bootstrap, bootstrapYAML)
		if err != nil {
			return "", fmt.Errorf("failed to apply user bootstrap config: %w", err)
		}
	}

	// Patch node.cluster so the xDS server can route config to the correct gateway.
	// The xDS server uses node.Cluster to match which Gateway configuration to send.
	clusterValue, err := yaml.Marshal(proxyInfra.Name)
	if err != nil {
		return "", fmt.Errorf("failed to marshal cluster name: %w", err)
	}
	nodeClusterPatch := fmt.Sprintf("node:\n  cluster: %s", string(clusterValue))
	return bootstrap.ApplyBootstrapConfig(&egv1a1.ProxyBootstrap{
		Type:  ptr.To(egv1a1.BootstrapTypeMerge),
		Value: ptr.To(nodeClusterPatch),
	}, bootstrapYAML)
}

// convertResolvedMetricSinks converts IR metric sinks to bootstrap format. Copied from Envoy Gateway upstream.
func convertResolvedMetricSinks(irSinks []*pb.ResolvedMetricSink) []bootstrap.MetricSink {
	result := make([]bootstrap.MetricSink, 0, len(irSinks))
	for _, sink := range irSinks {
		if sink == nil || sink.Destination == nil {
			continue
		}
		settings := sink.Destination.Settings
		if len(settings) == 0 || len(settings[0].Endpoints) == 0 {
			continue
		}
		// Metrics are aggregated locally in Envoy and exported to one collector.
		ep := settings[0].Endpoints[0]
		ms := bootstrap.MetricSink{
			Address:                  ep.Host,
			Port:                     ep.Port,
			Authority:                sink.Authority,
			ReportCountersAsDeltas:   sink.ReportCountersAsDeltas,
			ReportHistogramsAsDeltas: sink.ReportHistogramsAsDeltas,
			Headers:                  convertMetricSinkHeaders(sink.Headers),
			ResourceAttributes:       sink.ResourceAttributes,
		}
		if tls := settings[0].Tls; tls != nil {
			ms.TLS = &bootstrap.MetricSinkTLS{
				UseSystemTrustStore: tls.UseSystemTrustStore,
			}
			if tls.Sni != nil {
				ms.TLS.SNI = *tls.Sni
			}
			if tls.TlsConfig != nil && tls.TlsConfig.CaCertificate != nil {
				ms.TLS.CACertificate = tls.TlsConfig.CaCertificate.Certificate
			}
		}
		result = append(result, ms)
	}
	return result
}

// convertMetricSinkHeaders converts the protobuf HTTP headers carried on a
// resolved metric sink into the gateway-api header type used by the bootstrap
// renderer.
func convertMetricSinkHeaders(headers []*pb.HTTPHeader) []gwapiv1.HTTPHeader {
	if len(headers) == 0 {
		return nil
	}
	out := make([]gwapiv1.HTTPHeader, 0, len(headers))
	for _, h := range headers {
		if h == nil {
			continue
		}
		out = append(out, gwapiv1.HTTPHeader{
			Name:  gwapiv1.HTTPHeaderName(h.Name),
			Value: h.Value,
		})
	}
	return out
}
