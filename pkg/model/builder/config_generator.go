// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/bootstrap"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

const (
	SdsCAFilename   = "xds-trusted-ca.json"
	SdsCertFilename = "xds-certificate.json"
)

func GetSdsCAConfigMapData(ca string) string {
	return fmt.Sprintf(`{"resources":[{"@type":"type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret",`+
		`"name":"xds_trusted_ca","validation_context":{"trusted_ca":{"filename":"%s"},`+
		`"match_typed_subject_alt_names":[{"san_type":"DNS","matcher":{"exact":"envoy-gateway"}}]}}]}`, ca)
}

func GetSdsCertConfigMapData(tlsCert, tlsKey string) string {
	return fmt.Sprintf(`{"resources":[{"@type":"type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret",`+
		`"name":"xds_certificate","tls_certificate":{"certificate_chain":{"filename":"%s"},`+
		`"private_key":{"filename":"%s"}}}]}`, tlsCert, tlsKey)
}

func buildLBEndpoints(addresses []string, port int) string {
	var lbEndpoints string
	for i, addr := range addresses {
		if i > 0 {
			lbEndpoints += ","
		}
		lbEndpoints += fmt.Sprintf(`{"endpoint":{"address":{"socket_address":{"address":"%s","port_value":%d}}}}`, addr, port)
	}
	return lbEndpoints
}

// clusterLoadAssignment builds a file-based EDS resource that sends clusterName to port
// on each of addresses.
func clusterLoadAssignment(clusterName string, addresses []string, port int) string {
	return fmt.Sprintf(`{"resources":[{"@type":"type.googleapis.com/envoy.config.endpoint.v3.ClusterLoadAssignment",`+
		`"cluster_name":%q,"endpoints":[{"lb_endpoints":[%s]}]}]}`, clusterName, buildLBEndpoints(addresses, port))
}

func GetXDSEndpointsConfigData(addresses []string) string {
	return clusterLoadAssignment(xdsClusterName, addresses, xdsServerPort)
}

// GetWasmEndpointsConfigData builds the wasm_cluster endpoints: the same Envoy Gateway
// pod IPs as xds_cluster, on the port of Envoy Gateway's Wasm module server.
func GetWasmEndpointsConfigData(addresses []string) string {
	return clusterLoadAssignment(wasmClusterName, addresses, wasmServerPort)
}

// GetLocalClusterEndpointsConfigData builds the local_cluster ClusterLoadAssignment with
// one placeholder endpoint per availability zone. Zone-aware routing counts hosts per
// locality, so one endpoint per zone encodes the equal split the ELBProxy service
// provides. The endpoints are never dialled (local_cluster is not a route target).
func GetLocalClusterEndpointsConfigData(zones []string) string {
	localities := make([]string, 0, len(zones))
	for i, zone := range zones {
		localities = append(localities, fmt.Sprintf(
			`{"locality":{"zone":%q},"lb_endpoints":[{"health_status":"HEALTHY",`+
				`"endpoint":{"address":{"socket_address":{"address":%q,"port_value":%d}}}}]}`,
			zone, localClusterPlaceholderAddress, localClusterPlaceholderBasePort+i))
	}
	return fmt.Sprintf(`{"resources":[{"@type":"type.googleapis.com/envoy.config.endpoint.v3.ClusterLoadAssignment",`+
		`"cluster_name":%q,"endpoints":[%s]}]}`,
		localClusterName, strings.Join(localities, ","))
}

const (
	archiveBootstrapFile    = "bootstrap.yaml"
	archiveSdsCAFile        = "sds/xds-trusted-ca.json"
	archiveSdsCertFile      = "sds/xds-certificate.json"
	archiveTLSCertFile      = "certs/tls.crt"
	archiveCACertFile       = "certs/ca.crt"
	archiveTLSKeyFile       = "certs/tls.key"
	archiveXDSEndpointsFile = "xds-endpoints.json"

	// archiveWasmEndpointsFile sits at the archive root, so Envoy reads it at
	// bootstrap.WasmEndpointsPath.
	archiveWasmEndpointsFile = "wasm-endpoints.json"

	// archiveLocalClusterFile sits at the archive root alongside xds-endpoints.json.
	archiveLocalClusterFile = "local-cluster-endpoints.json"

	// XDSEndpointsPath is the path Envoy reads for xds_cluster EDS endpoints.
	XDSEndpointsPath = bootstrap.XDSEndpointsPath

	// xdsClusterName and wasmClusterName must match the cluster names and EDS
	// service_names in the bootstrap template. wasm_cluster is also the name Envoy
	// Gateway's Wasm filters download modules through.
	xdsClusterName  = "xds_cluster"
	wasmClusterName = "wasm_cluster"

	// LocalClusterEndpointsPath is the path Envoy reads for local_cluster EDS endpoints.
	LocalClusterEndpointsPath = bootstrap.LocalClusterEndpointsPath

	// localClusterName must match cluster_manager.local_cluster_name and the
	// local_cluster EDS service_name in the bootstrap template.
	localClusterName = "local_cluster"

	// Placeholder endpoint address for local_cluster
	localClusterPlaceholderAddress = "127.0.0.1"

	// localClusterPlaceholderBasePort is the first of one port per zone; zone N gets
	// base+N. The ports must differ: Envoy discards any endpoint whose address:port it
	// has already seen in the same ClusterLoadAssignment
	localClusterPlaceholderBasePort = 1

	// xdsServerPort is the default port of the xDS server (envoy-gateway).
	xdsServerPort = bootstrap.DefaultXdsServerPort

	// wasmServerPort is the port of the Wasm module server (envoy-gateway).
	wasmServerPort = bootstrap.DefaultWasmServerPort

	// Controller pod cert paths (mounted from elbproxy-gateway Secret).
	podCertPath = "/tmp/certs/tls.crt"
	podKeyPath  = "/tmp/certs/tls.key"
	podCAPath   = "/tmp/certs/ca.crt"

	// Envoy EC2 instance cert paths (referenced in SDS configs).
	envoyCertPath = "/certs/tls.crt"
	envoyKeyPath  = "/certs/tls.key"
	envoyCAPath   = "/certs/ca.crt"
)

// CertPaths holds TLS certificate filesystem paths.
type CertPaths struct {
	TLSCert string
	TLSKey  string
	CACert  string
}

func DefaultCertPaths() CertPaths {
	return CertPaths{
		TLSCert: podCertPath,
		TLSKey:  podKeyPath,
		CACert:  podCAPath,
	}
}

// ConfigGenerator produces a zip archive of Envoy bootstrap and TLS config.
type ConfigGenerator struct {
	logger    logging.Logger
	certPaths CertPaths
}

func NewConfigGenerator(logger logging.Logger, certPaths CertPaths) *ConfigGenerator {
	return &ConfigGenerator{
		logger:    logger,
		certPaths: certPaths,
	}
}

// GenerateArchive builds a zip with SDS configs, TLS certs, the xDS and Wasm endpoints
// files, and the local_cluster endpoints file. xdsAddresses are the Envoy Gateway pod IPs,
// which serve both xDS and Wasm modules. zones are the proxy's subnet AZ names, sorted so
// the archive bytes stay stable across reconciles.
func (g *ConfigGenerator) GenerateArchive(xdsAddresses []string, zones []string) ([]byte, error) {
	tlsCert, err := os.ReadFile(g.certPaths.TLSCert)
	if err != nil {
		return nil, fmt.Errorf("failed to read TLS cert %s: %w", g.certPaths.TLSCert, err)
	}
	tlsKey, err := os.ReadFile(g.certPaths.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read TLS key %s: %w", g.certPaths.TLSKey, err)
	}
	caCert, err := os.ReadFile(g.certPaths.CACert)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert %s: %w", g.certPaths.CACert, err)
	}

	sdsCAConfig := GetSdsCAConfigMapData(envoyCAPath)
	sdsCertConfig := GetSdsCertConfigMapData(envoyCertPath, envoyKeyPath)
	xdsEndpointsConfig := GetXDSEndpointsConfigData(xdsAddresses)
	wasmEndpointsConfig := GetWasmEndpointsConfigData(xdsAddresses)
	localClusterConfig := GetLocalClusterEndpointsConfigData(zones)

	files := map[string][]byte{
		archiveSdsCAFile:         []byte(sdsCAConfig),
		archiveSdsCertFile:       []byte(sdsCertConfig),
		archiveTLSCertFile:       tlsCert,
		archiveCACertFile:        caCert,
		archiveTLSKeyFile:        tlsKey,
		archiveXDSEndpointsFile:  []byte(xdsEndpointsConfig),
		archiveWasmEndpointsFile: []byte(wasmEndpointsConfig),
		archiveLocalClusterFile:  []byte(localClusterConfig),
	}

	archive, err := createZip(files)
	if err != nil {
		return nil, fmt.Errorf("failed to create config archive: %w", err)
	}

	return archive, nil
}

func createZip(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		content := files[name]
		w, err := zipWriter.Create(name)
		if err != nil {
			return nil, fmt.Errorf("failed to create zip entry for %s: %w", name, err)
		}
		if _, err := w.Write(content); err != nil {
			return nil, fmt.Errorf("failed to write zip content for %s: %w", name, err)
		}
	}

	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close zip writer: %w", err)
	}

	return buf.Bytes(), nil
}
