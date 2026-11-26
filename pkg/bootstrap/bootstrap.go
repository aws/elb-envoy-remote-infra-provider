// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.
//
// Copied from https://github.com/envoyproxy/gateway/blob/v1.10.0/internal/xds/bootstrap/bootstrap.go
// because internal packages can't be imported from another module.
// Modified by Amazon.

package bootstrap

import (
	// Register embed
	_ "embed"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/template"

	"regexp"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/bootstrap/proto"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// envoyCfgFileName is the name of the Envoy configuration file.
	envoyCfgFileName = "bootstrap.yaml"
	// EnvoyAdminAddress is the listening v4 address of the envoy admin interface.
	EnvoyAdminAddress   = "127.0.0.1"
	EnvoyAdminAddressV6 = "::1"
	// EnvoyAdminPort is the port used to expose admin interface.
	EnvoyAdminPort = 19000

	// DefaultXdsServerPort is the default listening port of the xds-server.
	DefaultXdsServerPort = 18000

	// XDSEndpointsPath is the path Envoy reads for xds_cluster EDS endpoints.
	XDSEndpointsPath = "/xds-endpoints.json"

	// DefaultWasmServerPort is the port of Envoy Gateway's Wasm module server
	DefaultWasmServerPort = 18002

	// WasmEndpointsPath is the path Envoy reads for wasm_cluster EDS endpoints. They are
	// the Envoy Gateway pod IPs, like xds_cluster's, on DefaultWasmServerPort.
	WasmEndpointsPath = "/wasm-endpoints.json"

	// LocalClusterEndpointsPath is the path Envoy reads for local_cluster EDS endpoints.
	// local_cluster holds the proxy fleet's per-AZ spread for zone-aware routing. Envoy
	// Gateway builds it from proxy Pods, which the remote provider has none of, so it
	// never arrives over ADS — we publish it as a file, like xds_cluster's endpoints.
	LocalClusterEndpointsPath = "/local-cluster-endpoints.json"

	EnvoyStatsPort = 19001

	EnvoyReadinessPort = 19003
	EnvoyReadinessPath = "/ready"

	defaultSdsTrustedCAPath   = "/sds/xds-trusted-ca.json"
	defaultSdsCertificatePath = "/sds/xds-certificate.json"

	defaultServiceClusterName = "local_cluster"

	elbproxySystemCaPath = "/etc/ssl/certs/ca-certificates.crt"
)

func ApplyBootstrapConfig(boostrapConfig *egv1a1.ProxyBootstrap, defaultBootstrap string) (string, error) {
	bootstrapType := boostrapConfig.Type
	if bootstrapType == nil {
		// The documentation defines that a nil bootstrapType defaults to the "Replace" operation
		bootstrapType = ptr.To(egv1a1.BootstrapTypeReplace)
	}
	switch *bootstrapType {
	case egv1a1.BootstrapTypeMerge:
		mergedBootstrap, err := mergeBootstrap(defaultBootstrap, boostrapConfig.Value)
		if err != nil {
			return "", err
		}
		return mergedBootstrap, nil
	default:
		// This is unreachable code due to the CEL validation on egv1a1.ProxyBootstrap
		return defaultBootstrap, fmt.Errorf("unsupported bootstrap patch type %s", *bootstrapType)
	}
}

func mergeBootstrap(base string, override *string) (string, error) {
	if override == nil {
		return base, nil
	}
	dst := &bootstrapv3.Bootstrap{}
	if err := proto.FromYAML([]byte(base), dst); err != nil {
		return "", fmt.Errorf("failed to parse default bootstrap config: %w", err)
	}

	src := &bootstrapv3.Bootstrap{}
	if err := proto.FromYAML([]byte(*override), src); err != nil {
		return "", fmt.Errorf("failed to parse override bootstrap config: %w", err)
	}

	proto.Merge(dst, src)

	if err := dst.Validate(); err != nil {
		return "", fmt.Errorf("failed to validate merged bootstrap config: %w", err)
	}

	data, err := proto.ToYAML(dst)
	if err != nil {
		return "", fmt.Errorf("failed to convert proto message to YAML: %w", err)
	}

	return string(data), nil
}

//go:embed bootstrap.yaml.tpl
var bootstrapTmplStr string

var bootstrapTmpl = template.Must(template.New(envoyCfgFileName).Funcs(template.FuncMap{
	"base64": func(data []byte) string {
		return base64.StdEncoding.EncodeToString(data)
	},
}).Parse(bootstrapTmplStr))

// bootstrapConfig defines the envoy Bootstrap configuration.
type bootstrapConfig struct {
	// parameters defines configurable bootstrap configuration parameters.
	parameters bootstrapParameters
	// rendered is the rendered bootstrap configuration.
	rendered string
}

// bootstrapParameters defines the envoy Bootstrap configuration.
type bootstrapParameters struct {
	// AdminServer defines the configuration of the Envoy admin interface.
	AdminServer adminServerParameters
	// StatsServer defines the configuration for stats listener
	StatsServer serverParameters
	// SdsCertificatePath defines the path to SDS certificate config. TODO - This probably should not be configurable.
	SdsCertificatePath string
	// SdsTrustedCAPath defines the path to SDS trusted CA config. TODO - This probably should not be configurable.
	SdsTrustedCAPath string
	// SystemCACertPath is the path to the system CA certificate bundle. TODO - This probably should not be configurable.
	SystemCACertPath string

	// EnablePrometheus defines whether to enable metrics endpoint for prometheus.
	EnablePrometheus bool
	// EnablePrometheusCompression defines whether to enable HTTP compression on metrics endpoint for prometheus.
	EnablePrometheusCompression bool
	// PrometheusCompressionLibrary defines the HTTP compression library for metrics endpoint for prometheus.
	// TODO: remove this field because it is not used.
	PrometheusCompressionLibrary string

	// OtelMetricSinks defines the configuration of the OpenTelemetry sinks.
	OtelMetricSinks []MetricSink
	// StatsMatcher is to control creation of custom Envoy stats with prefix,
	// suffix, and regex expressions match on the name of the stats.
	StatsMatcher *StatsMatcherParameters

	// IPFamily of the Listener
	IPFamily string
	// ServiceClusterName is the generated name of the Envoy ServiceCluster.
	ServiceClusterName string
	// XdsEndpointsPath is the path to the file-based EDS endpoints file for xds_cluster.
	XdsEndpointsPath string
	// WasmEndpointsPath is the path to the file-based EDS endpoints file for wasm_cluster.
	WasmEndpointsPath string
	// LocalClusterEndpointsPath is the path to the file-based EDS endpoints file for
	// local_cluster, which carries the proxy fleet's per-AZ distribution.
	LocalClusterEndpointsPath string
}

type serverParameters struct {
	// Address is the address of the XDS Server that Envoy is managed by.
	Address string
	// Port is the port of the XDS Server that Envoy is managed by.
	Port int32
}

// MetricSink defines an OpenTelemetry metrics sink destination for template rendering.
type MetricSink struct {
	// Address is the address of the metrics sink.
	Address string
	// Port is the port of the metrics sink.
	Port uint32
	// Authority is the gRPC authority header value (typically SNI or hostname).
	Authority string
	// ReportCountersAsDeltas configures counters to use delta temporality.
	ReportCountersAsDeltas bool
	// ReportHistogramsAsDeltas configures histograms to use delta temporality.
	ReportHistogramsAsDeltas bool
	// Headers is a list of headers to send with OTLP export requests.
	Headers []gwapiv1.HTTPHeader
	// ResourceAttributes is a map of resource attributes for the metrics sink.
	ResourceAttributes map[string]string
	// TLS contains upstream TLS configuration for the metrics sink.
	// Nil means no TLS.
	TLS *MetricSinkTLS
}

// MetricSinkTLS defines TLS settings for a metrics sink.
type MetricSinkTLS struct {
	// SNI is the Server Name Indication for TLS.
	SNI string
	// UseSystemTrustStore indicates whether to use the system CA trust store.
	UseSystemTrustStore bool
	// CACertificate is the CA certificate for validating the upstream TLS certificate.
	// When set, this takes precedence over UseSystemTrustStore.
	CACertificate []byte
}

type adminServerParameters struct {
	// Address is the address of the Envoy admin interface.
	Address string
	// Port is the port of the Envoy admin interface.
	Port int32
}

type StatsMatcherParameters struct {
	Exacts             []string
	Prefixes           []string
	Suffixes           []string
	RegularExpressions []string
}

type RenderBootstrapConfigOptions struct {
	IPFamily     *egv1a1.IPFamily
	ProxyMetrics *egv1a1.ProxyMetrics
	// TODO: remove after deprecating host/port in favor of backendRefs.
	ResolvedMetricSinks []MetricSink
	SdsConfig           SdsConfigPath
	XdsServerHost       *string
	XdsServerPort       *int32
	AdminServerPort     *int32
	StatsServerPort     *int32
}

type SdsConfigPath struct {
	Certificate string
	TrustedCA   string
}

// render the stringified bootstrap config in yaml format.
func (b *bootstrapConfig) render() error {
	buf := new(strings.Builder)
	if err := bootstrapTmpl.Execute(buf, b.parameters); err != nil {
		return fmt.Errorf("failed to render bootstrap config: %w", err)
	}
	b.rendered = buf.String()

	return nil
}

// GetRenderedBootstrapConfig renders the bootstrap YAML string
func GetRenderedBootstrapConfig(opts *RenderBootstrapConfigOptions) (string, error) {
	var (
		enablePrometheus             = true
		enablePrometheusCompression  = false
		prometheusCompressionLibrary = "Gzip"
		metricSinks                  []MetricSink
		StatsMatcher                 StatsMatcherParameters
	)

	// Pre-resolved metric sinks apply regardless of ProxyMetrics. They are resolved
	// upstream during gateway-api translation from BackendRefs, independently of
	// whether the EnvoyProxy spec sets Telemetry.Metrics.
	if opts != nil && len(opts.ResolvedMetricSinks) > 0 {
		metricSinks = opts.ResolvedMetricSinks
	}

	if opts != nil && opts.ProxyMetrics != nil {
		proxyMetrics := opts.ProxyMetrics

		if proxyMetrics.Prometheus != nil {
			enablePrometheus = !proxyMetrics.Prometheus.Disable

			if proxyMetrics.Prometheus.Compression != nil {
				enablePrometheusCompression = true
				prometheusCompressionLibrary = string(proxyMetrics.Prometheus.Compression.Type)
			}
		}

		// Fall back to resolving sinks from ProxyMetrics only when no pre-resolved
		// sinks were provided (legacy path for Service-type backends).
		if len(metricSinks) == 0 {
			addresses := sets.NewString()
			for _, sink := range proxyMetrics.Sinks {
				if sink.OpenTelemetry == nil {
					continue
				}

				var host string
				var port uint32
				if sink.OpenTelemetry.Host != nil {
					host, port = *sink.OpenTelemetry.Host, uint32(sink.OpenTelemetry.Port)
				}
				if len(sink.OpenTelemetry.BackendRefs) > 0 {
					ref := sink.OpenTelemetry.BackendRefs[0].BackendObjectReference
					if ref.Port == nil {
						return "", fmt.Errorf("metrics sink backendRef %s has no port", ref.Name)
					}
					host, port = backendHostAndPort(ref, "")
				}

				// skip duplicate sinks
				addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
				if addresses.Has(addr) {
					continue
				}
				addresses.Insert(addr)

				metricSinks = append(metricSinks, MetricSink{
					Address:                  host,
					Port:                     port,
					ReportCountersAsDeltas:   ptr.Deref(sink.OpenTelemetry.ReportCountersAsDeltas, false),
					ReportHistogramsAsDeltas: ptr.Deref(sink.OpenTelemetry.ReportHistogramsAsDeltas, false),
					Headers:                  sink.OpenTelemetry.Headers,
				})
			}
		}

		if proxyMetrics.Matches != nil {
			// Add custom envoy proxy stats
			for _, match := range proxyMetrics.Matches {
				// matchType default to exact
				matchType := egv1a1.StringMatchExact
				if match.Type != nil {
					matchType = *match.Type
				}
				switch matchType {
				case egv1a1.StringMatchExact:
					StatsMatcher.Exacts = append(StatsMatcher.Exacts, match.Value)
				case egv1a1.StringMatchPrefix:
					StatsMatcher.Prefixes = append(StatsMatcher.Prefixes, match.Value)
				case egv1a1.StringMatchSuffix:
					StatsMatcher.Suffixes = append(StatsMatcher.Suffixes, match.Value)
				case egv1a1.StringMatchRegularExpression:
					if err := validateRegex(match.Value); err != nil {
						return "", err
					}
					StatsMatcher.RegularExpressions = append(StatsMatcher.RegularExpressions, match.Value)
				}
			}
		}
	}

	cfg := &bootstrapConfig{
		parameters: bootstrapParameters{
			AdminServer: adminServerParameters{
				Address: EnvoyAdminAddress,
				Port:    EnvoyAdminPort,
			},
			StatsServer: serverParameters{
				Address: "0.0.0.0", // TODO - Verify this.
				Port:    EnvoyStatsPort,
			},
			SdsCertificatePath:           defaultSdsCertificatePath,
			SdsTrustedCAPath:             defaultSdsTrustedCAPath,
			SystemCACertPath:             elbproxySystemCaPath,
			EnablePrometheus:             enablePrometheus,
			EnablePrometheusCompression:  enablePrometheusCompression,
			PrometheusCompressionLibrary: prometheusCompressionLibrary,
			OtelMetricSinks:              metricSinks,
			ServiceClusterName:           defaultServiceClusterName,
		},
	}

	// Bootstrap config override
	if opts != nil {
		if opts.ProxyMetrics != nil && opts.ProxyMetrics.Matches != nil {
			cfg.parameters.StatsMatcher = &StatsMatcher
		}

		// Override Sds configs
		if len(opts.SdsConfig.Certificate) > 0 {
			cfg.parameters.SdsCertificatePath = opts.SdsConfig.Certificate
		}
		if len(opts.SdsConfig.TrustedCA) > 0 {
			cfg.parameters.SdsTrustedCAPath = opts.SdsConfig.TrustedCA
		}

		if opts.AdminServerPort != nil {
			cfg.parameters.AdminServer.Port = *opts.AdminServerPort
		}
		if opts.StatsServerPort != nil {
			cfg.parameters.StatsServer.Port = *opts.StatsServerPort
		}

		if opts.IPFamily != nil {
			cfg.parameters.IPFamily = string(*opts.IPFamily)
			switch *opts.IPFamily {
			case egv1a1.IPv6:
				cfg.parameters.AdminServer.Address = EnvoyAdminAddressV6
				cfg.parameters.StatsServer.Address = "::"
			case egv1a1.DualStack:
				cfg.parameters.StatsServer.Address = "::"
			}
		}
	}

	if cfg.parameters.XdsEndpointsPath == "" {
		cfg.parameters.XdsEndpointsPath = XDSEndpointsPath
	}

	if cfg.parameters.WasmEndpointsPath == "" {
		cfg.parameters.WasmEndpointsPath = WasmEndpointsPath
	}

	if cfg.parameters.LocalClusterEndpointsPath == "" {
		cfg.parameters.LocalClusterEndpointsPath = LocalClusterEndpointsPath
	}

	if err := cfg.render(); err != nil {
		return "", err
	}

	return cfg.rendered, nil
}

func backendHostAndPort(backendRef gwapiv1.BackendObjectReference, defaultNamespace string) (string, uint32) {
	ns := defaultNamespace
	if backendRef.Namespace != nil {
		ns = string(*backendRef.Namespace)
	}

	if ns == "" {
		return string(backendRef.Name), uint32(*backendRef.Port)
	}

	return fmt.Sprintf("%s.%s.svc", backendRef.Name, ns), uint32(*backendRef.Port)
}

// Validate validates a regex string.
func validateRegex(regex string) error {
	if _, err := regexp.Compile(regex); err != nil {
		return fmt.Errorf("regex %q is invalid: %w", regex, err)
	}
	return nil
}
