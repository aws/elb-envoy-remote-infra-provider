# No admin access logger: the ELB Proxy API rejects file, stdout and stderr loggers.
admin:
  address:
    socket_address:
      address: {{ .AdminServer.Address }}
      port_value: {{ .AdminServer.Port }}
cluster_manager:
  local_cluster_name: {{ .ServiceClusterName }}
# node.locality is intentionally omitted; the per-instance zone is set via --service-zone.
stats_config:
  use_all_default_tags: true
  stats_tags:
  - regex: \.zone(\.(([^\.]+)\.))
    tag_name: from_zone
  - regex: \.zone\.[^\.]+\.(([^\.]+)\.)
    tag_name: to_zone
  - regex: "^cluster(\\..+\\.(.+))\\.total_match_count$"
    tag_name: socket_match_name
  - regex: "circuit_breakers\\.((.+?)\\.).+"
    tag_name: priority
{{- if .StatsMatcher  }}
  stats_matcher:
    inclusion_list:
      patterns:
      {{- range $_, $item := .StatsMatcher.Exacts }}
      - exact: {{$item}}
      {{- end}}
      {{- range $_, $item := .StatsMatcher.Prefixes }}
      - prefix: {{$item}}
      {{- end}}
      {{- range $_, $item := .StatsMatcher.Suffixes }}
      - suffix: {{$item}}
      {{- end}}
      {{- range $_, $item := .StatsMatcher.RegularExpressions }}
      - safe_regex:
          google_re2: {}
          regex: {{js $item}}
      {{- end}}
{{- end }}
# The connection cap and regex limits are set by the ELB Proxy data plane, which rejects
# a second copy from this config, so the controller does not emit those two fields.
dynamic_resources:
  ads_config:
    api_type: DELTA_GRPC
    transport_api_version: V3
    grpc_services:
    - envoy_grpc:
        cluster_name: xds_cluster
    set_node_on_first_message_only: true
  lds_config:
    ads: {}
    initial_fetch_timeout: 0s
    resource_api_version: V3
  cds_config:
    ads: {}
    initial_fetch_timeout: 0s
    resource_api_version: V3
{{- if .OtelMetricSinks }}
stats_sinks:
{{- range $idx, $sink := .OtelMetricSinks }}
- name: "envoy.stat_sinks.open_telemetry"
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.stat_sinks.open_telemetry.v3.SinkConfig
    grpc_service:
      envoy_grpc:
        cluster_name: otel_metric_sink_{{ $idx }}
        {{- if $sink.Authority }}
        authority: {{ $sink.Authority }}
        {{- end }}
      {{- if $sink.Headers }}
      initial_metadata:
      {{- range $sink.Headers }}
      - key: "{{ .Name }}"
        value: "{{ .Value }}"
      {{- end }}
      {{- end }}
    {{- if $sink.ReportCountersAsDeltas }}
    report_counters_as_deltas: true
    {{- end }}
    {{- if $sink.ReportHistogramsAsDeltas }}
    report_histograms_as_deltas: true
    {{- end }}
    {{- if $sink.ResourceAttributes }}
    resource_detectors:
    - name: envoy.tracers.opentelemetry.resource_detectors.static_config
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.tracers.opentelemetry.resource_detectors.v3.StaticConfigResourceDetectorConfig
        attributes:
        {{- range $key, $value := $sink.ResourceAttributes }}
          {{ $key }}: "{{ $value }}"
        {{- end }}
    {{- end }}
{{- end }}
{{- end }}
static_resources:
  {{- if .EnablePrometheus }}
  listeners:
  - name: envoy-gateway-proxy-stats-{{ .StatsServer.Address }}-{{ .StatsServer.Port }}
    address:
      socket_address:
        address: '{{ .StatsServer.Address }}'
        port_value: {{ .StatsServer.Port }}
        protocol: TCP
        {{- if eq .IPFamily "DualStack" "IPv6" }}
        ipv4_compat: true
        {{- end }}
    bypass_overload_manager: true
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: eg-stats-http
          normalize_path: true
          route_config:
            name: local_route
            virtual_hosts:
            - name: prometheus_stats
              domains:
              - "*"
              {{- if .EnablePrometheusCompression }}
              typed_per_filter_config:
                envoy.filters.http.compression:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.compressor.v3.CompressorPerRoute
                  disabled: true
              {{- end }}
              routes:
              - match:
                  path: /stats/prometheus
                  headers:
                  - name: ":method"
                    string_match:
                      exact: GET
                route:
                  cluster: prometheus_stats
                {{- if .EnablePrometheusCompression }}
                typed_per_filter_config:
                  envoy.filters.http.compression:
                    "@type": type.googleapis.com/envoy.extensions.filters.http.compressor.v3.CompressorPerRoute
                    overrides:
                      response_direction_config:
                {{- end }}
          http_filters:
          {{- if .EnablePrometheusCompression }}
          - name: envoy.filters.http.compressor
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.compressor.v3.Compressor
              {{- if eq .PrometheusCompressionLibrary "Gzip"}}
              compressor_library:
                name: text_optimized
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.compression.gzip.compressor.v3.Gzip
              {{- end }}
              {{- if eq .PrometheusCompressionLibrary "Brotli"}}
              compressor_library:
                name: text_optimized
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.compression.brotli.compressor.v3.Brotli
              {{- end }}
              {{- if eq .PrometheusCompressionLibrary "Zstd"}}
              compressor_library:
                name: text_optimized
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.compression.zstd.compressor.v3.Zstd
              {{- end }}
          {{- end }}
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  {{- end }}
  clusters:
  {{- if .EnablePrometheus }}
  - name: prometheus_stats
    connect_timeout: 0.250s
    type: STATIC
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: prometheus_stats
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: {{ .AdminServer.Address }}
                port_value: {{ .AdminServer.Port }}
  {{- end }}
  {{- range $idx, $sink := .OtelMetricSinks }}
  - name: otel_metric_sink_{{ $idx }}
    connect_timeout: 0.250s
    type: STRICT_DNS
    typed_extension_protocol_options:
      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
        "@type": "type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions"
        explicit_http_config:
          http2_protocol_options: {}
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: otel_metric_sink_{{ $idx }}
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: {{ $sink.Address }}
                port_value: {{ $sink.Port }}
    {{- if $sink.TLS }}
    transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        {{- if $sink.TLS.SNI }}
        sni: {{ $sink.TLS.SNI }}
        {{- end }}
        {{- if $sink.TLS.CACertificate }}
        common_tls_context:
          validation_context:
            trusted_ca:
              inline_bytes: {{ $sink.TLS.CACertificate | base64 }}
        {{- else if $sink.TLS.UseSystemTrustStore }}
        common_tls_context:
          validation_context:
            trusted_ca:
              filename: {{ $.SystemCACertPath }}
        {{- end }}
    {{- end }}
  {{- end }}
  # local_cluster carries the proxy fleet's per-AZ spread for zone-aware routing. It
  # comes from a file, not ADS: Envoy Gateway builds this from proxy Pods, which the
  # remote provider has none of, so ADS never delivered it (and the wait cost 15s).
  - connect_timeout: 10s
    eds_cluster_config:
      eds_config:
        path_config_source:
          path: {{ .LocalClusterEndpointsPath }}
        resource_api_version: 'V3'
      service_name: {{ .ServiceClusterName }}
    load_balancing_policy:
      policies:
      - typed_extension_config:
          name: 'envoy.load_balancing_policies.least_request'
          typed_config:
            '@type': 'type.googleapis.com/envoy.extensions.load_balancing_policies.least_request.v3.LeastRequest'
            locality_lb_config:
              zone_aware_lb_config:
                min_cluster_size: '1'
    name: {{ .ServiceClusterName }}
    type: EDS
  - connect_timeout: 10s
    type: EDS
    eds_cluster_config:
      eds_config:
        path_config_source:
          path: {{ .XdsEndpointsPath }}
        resource_api_version: V3
      service_name: xds_cluster
    typed_extension_protocol_options:
      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
        "@type": "type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions"
        explicit_http_config:
          http2_protocol_options:
            connection_keepalive:
              interval: 30s
              timeout: 5s
    name: xds_cluster
    transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        common_tls_context:
          tls_params:
            tls_maximum_protocol_version: TLSv1_3
          tls_certificate_sds_secret_configs:
          - name: xds_certificate
            sds_config:
              path_config_source:
                path: {{ .SdsCertificatePath }}
              resource_api_version: V3
          validation_context_sds_secret_config:
            name: xds_trusted_ca
            sds_config:
              path_config_source:
                path: {{ .SdsTrustedCAPath }}
              resource_api_version: V3
  # wasm_cluster is how Envoy downloads the modules of Wasm extensions with an HTTP or
  # Image code source. Envoy Gateway downloads each module and serves it from its own
  # pods on port 18002, and sends a wasm_cluster over ADS that points at the in-cluster
  # name envoy-gateway.<namespace>.svc.cluster.local, which the proxy can't resolve.
  # Envoy keeps a cluster defined here and skips the ADS cluster of the same name, so
  # downloads go to the Envoy Gateway pod IPs, read from a file like xds_cluster's. The
  # module server uses the same certificates as the xDS server, so does the TLS config.
  - connect_timeout: 10s
    type: EDS
    eds_cluster_config:
      eds_config:
        path_config_source:
          path: {{ .WasmEndpointsPath }}
        resource_api_version: V3
      service_name: wasm_cluster
    typed_extension_protocol_options:
      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
        "@type": "type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions"
        explicit_http_config:
          http2_protocol_options: {}
    name: wasm_cluster
    transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        common_tls_context:
          tls_params:
            tls_maximum_protocol_version: TLSv1_3
          tls_certificate_sds_secret_configs:
          - name: xds_certificate
            sds_config:
              path_config_source:
                path: {{ .SdsCertificatePath }}
              resource_api_version: V3
          validation_context_sds_secret_config:
            name: xds_trusted_ca
            sds_config:
              path_config_source:
                path: {{ .SdsTrustedCAPath }}
              resource_api_version: V3
