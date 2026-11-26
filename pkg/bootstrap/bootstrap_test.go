// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.
//
// Copied from https://github.com/envoyproxy/gateway/blob/v1.10.0/internal/xds/bootstrap/bootstrap_test.go
// because internal packages can't be imported from another module.
// Modified by Amazon.

package bootstrap

import (
	"encoding/base64"
	"strconv"
	"testing"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/bootstrap/proto"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	tlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	httpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// parsesAsValidBootstrap asserts the rendered YAML unmarshals into and validates
// as an Envoy v3 Bootstrap proto. This guards every combination against
// producing structurally invalid Envoy configuration.
func parsesAsValidBootstrap(t *testing.T, rendered string) {
	t.Helper()
	bs := &bootstrapv3.Bootstrap{}
	require.NoError(t, proto.FromYAML([]byte(rendered), bs), "rendered config should parse as an Envoy bootstrap proto")
	require.NoError(t, bs.Validate(), "rendered config should be a valid Envoy bootstrap proto")
}

func TestGetRenderedBootstrapConfig(t *testing.T) {
	tests := []struct {
		name        string
		opts        *RenderBootstrapConfigOptions
		contains    []string
		notContains []string
		wantErr     string
	}{
		{
			name: "nil options renders defaults",
			opts: nil,
			contains: []string{
				"local_cluster_name: " + defaultServiceClusterName,
				"address: " + EnvoyAdminAddress,
				"port_value: " + strconv.Itoa(EnvoyAdminPort),
				// Prometheus is enabled by default.
				"name: prometheus_stats",
				"path: /stats/prometheus",
				"service_name: xds_cluster",
				"service_name: wasm_cluster",
				defaultSdsCertificatePath,
				defaultSdsTrustedCAPath,
				XDSEndpointsPath,
				WasmEndpointsPath,
				LocalClusterEndpointsPath,
			},
			notContains: []string{
				"stats_sinks:",
				"stats_matcher:",
				// The connection cap and regex limits are set by the ELB Proxy data plane.
				// Anchor on a leading newline so bypass_overload_manager (a real field) still matches.
				"\noverload_manager:",
				"\nlayered_runtime:",
				"ipv4_compat: true",
				"envoy.filters.http.compressor",
				"locality:",
				"$(ENVOY_SERVICE_ZONE)",
			},
		},
		{
			name: "empty options renders defaults",
			opts: &RenderBootstrapConfigOptions{},
			contains: []string{
				"local_cluster_name: " + defaultServiceClusterName,
				"name: prometheus_stats",
			},
			notContains: []string{
				"stats_sinks:",
				"\noverload_manager:",
				"\nlayered_runtime:",
			},
		},
		{
			name: "prometheus disabled drops the stats listener and cluster",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Prometheus: &egv1a1.ProxyPrometheusProvider{Disable: true},
				},
			},
			notContains: []string{
				"name: prometheus_stats",
				"path: /stats/prometheus",
				"envoy-gateway-proxy-stats",
			},
		},
		{
			name: "prometheus gzip compression",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Prometheus: &egv1a1.ProxyPrometheusProvider{
						Compression: &egv1a1.Compression{Type: egv1a1.GzipCompressorType},
					},
				},
			},
			contains: []string{
				"envoy.filters.http.compressor",
				"envoy.extensions.compression.gzip.compressor.v3.Gzip",
			},
			notContains: []string{
				"compression.brotli.compressor",
				"compression.zstd.compressor",
			},
		},
		{
			name: "prometheus brotli compression",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Prometheus: &egv1a1.ProxyPrometheusProvider{
						Compression: &egv1a1.Compression{Type: egv1a1.BrotliCompressorType},
					},
				},
			},
			contains: []string{
				"envoy.filters.http.compressor",
				"envoy.extensions.compression.brotli.compressor.v3.Brotli",
			},
			notContains: []string{
				"compression.gzip.compressor",
				"compression.zstd.compressor",
			},
		},
		{
			name: "prometheus zstd compression",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Prometheus: &egv1a1.ProxyPrometheusProvider{
						Compression: &egv1a1.Compression{Type: egv1a1.ZstdCompressorType},
					},
				},
			},
			contains: []string{
				"envoy.extensions.compression.zstd.compressor.v3.Zstd",
			},
			notContains: []string{
				"compression.gzip.compressor",
				"compression.brotli.compressor",
			},
		},
		{
			name: "ipv4 family keeps default loopback admin address",
			opts: &RenderBootstrapConfigOptions{
				IPFamily: ptr.To(egv1a1.IPv4),
			},
			contains: []string{
				"address: " + EnvoyAdminAddress,
			},
			notContains: []string{
				"ipv4_compat: true",
			},
		},
		{
			name: "ipv6 family switches admin and stats addresses",
			opts: &RenderBootstrapConfigOptions{
				IPFamily: ptr.To(egv1a1.IPv6),
			},
			contains: []string{
				"address: " + EnvoyAdminAddressV6,
				"address: '::'",
				"ipv4_compat: true",
			},
		},
		{
			name: "dualstack family enables ipv4_compat on the stats listener",
			opts: &RenderBootstrapConfigOptions{
				IPFamily: ptr.To(egv1a1.DualStack),
			},
			contains: []string{
				// Admin stays on the v4 loopback for dual-stack.
				"address: " + EnvoyAdminAddress,
				"address: '::'",
				"ipv4_compat: true",
			},
		},
		{
			name: "single otel metric sink without proxy metrics",
			opts: &RenderBootstrapConfigOptions{
				// ProxyMetrics intentionally nil: resolved sinks must render
				// independently of the EnvoyProxy Telemetry.Metrics spec.
				ResolvedMetricSinks: []MetricSink{
					{Address: "otel.example.com", Port: 4317},
				},
			},
			contains: []string{
				"stats_sinks:",
				"envoy.stat_sinks.open_telemetry",
				"cluster_name: otel_metric_sink_0",
				"name: otel_metric_sink_0",
				"address: otel.example.com",
				"port_value: 4317",
			},
			notContains: []string{
				"otel_metric_sink_1",
				"report_counters_as_deltas: true",
				"report_histograms_as_deltas: true",
			},
		},
		{
			name: "multiple otel metric sinks are indexed",
			opts: &RenderBootstrapConfigOptions{
				ResolvedMetricSinks: []MetricSink{
					{Address: "otel-a.example.com", Port: 4317},
					{Address: "otel-b.example.com", Port: 4318},
				},
			},
			contains: []string{
				"cluster_name: otel_metric_sink_0",
				"cluster_name: otel_metric_sink_1",
				"address: otel-a.example.com",
				"address: otel-b.example.com",
				"port_value: 4318",
			},
		},
		{
			name: "otel sink with delta temporality and authority and headers",
			opts: &RenderBootstrapConfigOptions{
				ResolvedMetricSinks: []MetricSink{
					{
						Address:                  "otel.example.com",
						Port:                     4317,
						Authority:                "otel.example.com",
						ReportCountersAsDeltas:   true,
						ReportHistogramsAsDeltas: true,
						Headers: []gwapiv1.HTTPHeader{
							{Name: "x-api-key", Value: "secret"},
						},
						ResourceAttributes: map[string]string{
							"service.name": "elbproxy-proxy",
						},
					},
				},
			},
			contains: []string{
				"authority: otel.example.com",
				"report_counters_as_deltas: true",
				"report_histograms_as_deltas: true",
				"initial_metadata:",
				`key: "x-api-key"`,
				`value: "secret"`,
				"resource_detectors:",
				`service.name: "elbproxy-proxy"`,
			},
		},
		{
			name: "otel sink with TLS and inline CA certificate",
			opts: &RenderBootstrapConfigOptions{
				ResolvedMetricSinks: []MetricSink{
					{
						Address: "otel.example.com",
						Port:    4317,
						TLS: &MetricSinkTLS{
							SNI:           "otel.example.com",
							CACertificate: []byte("ca-cert-bytes"),
						},
					},
				},
			},
			contains: []string{
				"transport_socket:",
				"envoy.transport_sockets.tls",
				"sni: otel.example.com",
				"inline_bytes: " + base64Encode("ca-cert-bytes"),
			},
			notContains: []string{
				"filename: " + elbproxySystemCaPath,
			},
		},
		{
			name: "otel sink with TLS using system trust store",
			opts: &RenderBootstrapConfigOptions{
				ResolvedMetricSinks: []MetricSink{
					{
						Address: "otel.example.com",
						Port:    4317,
						TLS: &MetricSinkTLS{
							SNI:                 "otel.example.com",
							UseSystemTrustStore: true,
						},
					},
				},
			},
			contains: []string{
				"sni: otel.example.com",
				"filename: " + elbproxySystemCaPath,
			},
			notContains: []string{
				"inline_bytes:",
			},
		},
		{
			name: "stats matcher exact",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Matches: []egv1a1.StringMatch{
						{Type: ptr.To(egv1a1.StringMatchExact), Value: "http.foo.rq_total"},
					},
				},
			},
			contains: []string{
				"stats_matcher:",
				"inclusion_list:",
				"exact: http.foo.rq_total",
			},
		},
		{
			name: "stats matcher prefix suffix regex",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Matches: []egv1a1.StringMatch{
						{Type: ptr.To(egv1a1.StringMatchPrefix), Value: "cluster."},
						{Type: ptr.To(egv1a1.StringMatchSuffix), Value: ".rq_total"},
						{Type: ptr.To(egv1a1.StringMatchRegularExpression), Value: "http..*"},
					},
				},
			},
			contains: []string{
				"prefix: cluster.",
				"suffix: .rq_total",
				"safe_regex:",
				"google_re2: {}",
			},
		},
		{
			name: "stats matcher defaults to exact when type omitted",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Matches: []egv1a1.StringMatch{
						{Value: "http.bar.rq_total"},
					},
				},
			},
			contains: []string{
				"exact: http.bar.rq_total",
			},
		},
		{
			name: "legacy host/port sink is resolved",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Sinks: []egv1a1.ProxyMetricSink{
						{
							Type: egv1a1.MetricSinkTypeOpenTelemetry,
							OpenTelemetry: &egv1a1.ProxyOpenTelemetrySink{
								Host: ptr.To("legacy-otel.example.com"),
								Port: 4317,
							},
						},
					},
				},
			},
			contains: []string{
				"name: otel_metric_sink_0",
				"address: legacy-otel.example.com",
			},
		},
		{
			name: "legacy duplicate sinks are de-duplicated",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Sinks: []egv1a1.ProxyMetricSink{
						{
							Type: egv1a1.MetricSinkTypeOpenTelemetry,
							OpenTelemetry: &egv1a1.ProxyOpenTelemetrySink{
								Host: ptr.To("dup-otel.example.com"),
								Port: 4317,
							},
						},
						{
							Type: egv1a1.MetricSinkTypeOpenTelemetry,
							OpenTelemetry: &egv1a1.ProxyOpenTelemetrySink{
								Host: ptr.To("dup-otel.example.com"),
								Port: 4317,
							},
						},
					},
				},
			},
			contains: []string{
				"name: otel_metric_sink_0",
			},
			notContains: []string{
				"otel_metric_sink_1",
			},
		},
		{
			name: "legacy backendRef sink with namespace resolves to service FQDN",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Sinks: []egv1a1.ProxyMetricSink{
						{
							Type: egv1a1.MetricSinkTypeOpenTelemetry,
							OpenTelemetry: &egv1a1.ProxyOpenTelemetrySink{
								BackendCluster: egv1a1.BackendCluster{
									BackendRefs: []egv1a1.BackendRef{
										{
											BackendObjectReference: gwapiv1.BackendObjectReference{
												Name:      "otel-collector",
												Namespace: ptr.To(gwapiv1.Namespace("monitoring")),
												Port:      ptr.To(gwapiv1.PortNumber(4317)),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			contains: []string{
				"name: otel_metric_sink_0",
				"address: otel-collector.monitoring.svc",
				"port_value: 4317",
			},
		},
		{
			name: "legacy backendRef sink without namespace resolves to bare name",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Sinks: []egv1a1.ProxyMetricSink{
						{
							Type: egv1a1.MetricSinkTypeOpenTelemetry,
							OpenTelemetry: &egv1a1.ProxyOpenTelemetrySink{
								BackendCluster: egv1a1.BackendCluster{
									BackendRefs: []egv1a1.BackendRef{
										{
											BackendObjectReference: gwapiv1.BackendObjectReference{
												Name: "otel-collector",
												Port: ptr.To(gwapiv1.PortNumber(4317)),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			contains: []string{
				"address: otel-collector",
				"port_value: 4317",
			},
		},
		{
			name: "legacy backendRef sink missing port errors",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Sinks: []egv1a1.ProxyMetricSink{
						{
							Type: egv1a1.MetricSinkTypeOpenTelemetry,
							OpenTelemetry: &egv1a1.ProxyOpenTelemetrySink{
								BackendCluster: egv1a1.BackendCluster{
									BackendRefs: []egv1a1.BackendRef{
										{
											BackendObjectReference: gwapiv1.BackendObjectReference{
												Name: "otel-collector",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: "has no port",
		},
		{
			name: "stats matcher with invalid regex errors",
			opts: &RenderBootstrapConfigOptions{
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Matches: []egv1a1.StringMatch{
						{Type: ptr.To(egv1a1.StringMatchRegularExpression), Value: "http.(.*"},
					},
				},
			},
			wantErr: "is invalid",
		},
		{
			name: "resolved sinks take precedence over legacy sinks",
			opts: &RenderBootstrapConfigOptions{
				ResolvedMetricSinks: []MetricSink{
					{Address: "resolved-otel.example.com", Port: 4317},
				},
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Sinks: []egv1a1.ProxyMetricSink{
						{
							Type: egv1a1.MetricSinkTypeOpenTelemetry,
							OpenTelemetry: &egv1a1.ProxyOpenTelemetrySink{
								Host: ptr.To("legacy-otel.example.com"),
								Port: 4317,
							},
						},
					},
				},
			},
			contains: []string{
				"address: resolved-otel.example.com",
			},
			notContains: []string{
				"address: legacy-otel.example.com",
			},
		},
		{
			name: "custom SDS config paths",
			opts: &RenderBootstrapConfigOptions{
				SdsConfig: SdsConfigPath{
					Certificate: "/custom/cert.json",
					TrustedCA:   "/custom/ca.json",
				},
			},
			contains: []string{
				"path: /custom/cert.json",
				"path: /custom/ca.json",
			},
			notContains: []string{
				"path: " + defaultSdsCertificatePath,
				"path: " + defaultSdsTrustedCAPath,
			},
		},
		{
			name: "custom admin and stats ports",
			opts: &RenderBootstrapConfigOptions{
				AdminServerPort: ptr.To(int32(29000)),
				StatsServerPort: ptr.To(int32(29001)),
			},
			contains: []string{
				"port_value: 29000",
				"port_value: 29001",
			},
		},
		{
			name: "all features combined",
			opts: &RenderBootstrapConfigOptions{
				IPFamily: ptr.To(egv1a1.DualStack),
				ProxyMetrics: &egv1a1.ProxyMetrics{
					Prometheus: &egv1a1.ProxyPrometheusProvider{
						Compression: &egv1a1.Compression{Type: egv1a1.GzipCompressorType},
					},
					Matches: []egv1a1.StringMatch{
						{Type: ptr.To(egv1a1.StringMatchExact), Value: "http.foo.rq_total"},
					},
				},
				ResolvedMetricSinks: []MetricSink{
					{
						Address: "otel.example.com",
						Port:    4317,
						TLS:     &MetricSinkTLS{UseSystemTrustStore: true},
					},
				},
				SdsConfig: SdsConfigPath{
					Certificate: "/custom/cert.json",
					TrustedCA:   "/custom/ca.json",
				},
				AdminServerPort: ptr.To(int32(29000)),
				StatsServerPort: ptr.To(int32(29001)),
			},
			contains: []string{
				"ipv4_compat: true",
				"envoy.extensions.compression.gzip.compressor.v3.Gzip",
				"exact: http.foo.rq_total",
				"name: otel_metric_sink_0",
				"filename: " + elbproxySystemCaPath,
				"path: /custom/cert.json",
				"port_value: 29000",
				"local_cluster_name: " + defaultServiceClusterName,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered, err := GetRenderedBootstrapConfig(tt.opts)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)

			parsesAsValidBootstrap(t, rendered)

			for _, want := range tt.contains {
				assert.Contains(t, rendered, want)
			}
			for _, notWant := range tt.notContains {
				assert.NotContains(t, rendered, notWant)
			}
		})
	}
}

func base64Encode(s string) string {
	// Mirror the template's base64 helper (standard encoding).
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestApplyBootstrapConfig(t *testing.T) {
	// A minimal but valid default bootstrap to merge against.
	defaultBootstrap, err := GetRenderedBootstrapConfig(nil)
	require.NoError(t, err)

	tests := []struct {
		name        string
		cfg         *egv1a1.ProxyBootstrap
		contains    []string
		wantErr     string
		wantDefault bool
	}{
		{
			name: "nil type defaults to replace and is unsupported",
			cfg: &egv1a1.ProxyBootstrap{
				Type:  nil,
				Value: ptr.To("ignored"),
			},
			wantErr: "unsupported bootstrap patch type Replace",
		},
		{
			name: "explicit replace type is unsupported",
			cfg: &egv1a1.ProxyBootstrap{
				Type:  ptr.To(egv1a1.BootstrapTypeReplace),
				Value: ptr.To("ignored"),
			},
			wantErr: "unsupported bootstrap patch type Replace",
		},
		{
			name: "merge with nil value returns the base unchanged",
			cfg: &egv1a1.ProxyBootstrap{
				Type:  ptr.To(egv1a1.BootstrapTypeMerge),
				Value: nil,
			},
			wantDefault: true,
		},
		{
			name: "merge overrides a scalar field",
			cfg: &egv1a1.ProxyBootstrap{
				Type: ptr.To(egv1a1.BootstrapTypeMerge),
				Value: ptr.To(`
admin:
  address:
    socket_address:
      port_value: 29999
`),
			},
			contains: []string{
				"port_value: 29999",
			},
		},
		{
			name: "merge with invalid YAML override errors",
			cfg: &egv1a1.ProxyBootstrap{
				Type:  ptr.To(egv1a1.BootstrapTypeMerge),
				Value: ptr.To("admin: [this is not: valid bootstrap"),
			},
			wantErr: "failed to parse override bootstrap config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplyBootstrapConfig(tt.cfg, defaultBootstrap)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)

			if tt.wantDefault {
				assert.Equal(t, defaultBootstrap, got)
			}

			parsesAsValidBootstrap(t, got)

			for _, want := range tt.contains {
				assert.Contains(t, got, want)
			}
		})
	}
}

// wasm_cluster must be a static cluster, so Envoy keeps it instead of the wasm_cluster
// Envoy Gateway sends over ADS (an in-cluster DNS name the proxy can't resolve). It
// reads the Envoy Gateway pod IPs from WasmEndpointsPath, speaks HTTP/2, and uses
// xds_cluster's TLS settings, because Envoy Gateway's Wasm module server shares the xDS
// server's certificates.
func TestGetRenderedBootstrapConfig_WasmCluster(t *testing.T) {
	rendered, err := GetRenderedBootstrapConfig(nil)
	require.NoError(t, err)
	bs := &bootstrapv3.Bootstrap{}
	require.NoError(t, proto.FromYAML([]byte(rendered), bs))

	clusters := make(map[string]*clusterv3.Cluster)
	for _, c := range bs.GetStaticResources().GetClusters() {
		clusters[c.GetName()] = c
	}
	wasm, xds := clusters["wasm_cluster"], clusters["xds_cluster"]
	require.NotNil(t, wasm, "wasm_cluster must be a static cluster")
	require.NotNil(t, xds, "xds_cluster must be a static cluster")

	assert.Equal(t, clusterv3.Cluster_EDS, wasm.GetType())
	eds := wasm.GetEdsClusterConfig()
	assert.Equal(t, "wasm_cluster", eds.GetServiceName())
	assert.Equal(t, WasmEndpointsPath, eds.GetEdsConfig().GetPathConfigSource().GetPath())

	httpOpts := &httpv3.HttpProtocolOptions{}
	require.NoError(t, wasm.GetTypedExtensionProtocolOptions()["envoy.extensions.upstreams.http.v3.HttpProtocolOptions"].UnmarshalTo(httpOpts))
	assert.NotNil(t, httpOpts.GetExplicitHttpConfig().GetHttp2ProtocolOptions(), "the Wasm module server speaks HTTP/2")

	assert.Equal(t, xds.GetTransportSocket().GetName(), wasm.GetTransportSocket().GetName())
	wasmTLS, xdsTLS := &tlsv3.UpstreamTlsContext{}, &tlsv3.UpstreamTlsContext{}
	require.NoError(t, wasm.GetTransportSocket().GetTypedConfig().UnmarshalTo(wasmTLS))
	require.NoError(t, xds.GetTransportSocket().GetTypedConfig().UnmarshalTo(xdsTLS))
	assert.True(t, gproto.Equal(xdsTLS, wasmTLS), "wasm_cluster TLS %v, want xds_cluster's %v", wasmTLS, xdsTLS)
}

// The ELB Proxy API rejects a bootstrap with any file, stdout or stderr access logger.
func TestGetRenderedBootstrapConfig_NoAccessLogger(t *testing.T) {
	rendered, err := GetRenderedBootstrapConfig(&RenderBootstrapConfigOptions{})
	require.NoError(t, err)
	parsesAsValidBootstrap(t, rendered)
	bs := &bootstrapv3.Bootstrap{}
	require.NoError(t, proto.FromYAML([]byte(rendered), bs))
	assert.Empty(t, bs.GetAdmin().GetAccessLog(), "admin must not have an access logger")
	assert.Empty(t, bs.GetAdmin().GetAccessLogPath(), "admin.access_log_path is not supported")
	assert.NotContains(t, rendered, "access_log")
}
