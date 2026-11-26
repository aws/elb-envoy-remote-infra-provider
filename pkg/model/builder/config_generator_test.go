// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	bootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/bootstrap"
	bootstrapproto "github.com/aws/elb-envoy-remote-infra-provider/pkg/bootstrap/proto"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
)

func TestGenerateArchive_BootstrapNotInArchive(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("dummy"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	g := NewConfigGenerator(logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo), CertPaths{
		TLSCert: filepath.Join(dir, "tls.crt"),
		TLSKey:  filepath.Join(dir, "tls.key"),
		CACert:  filepath.Join(dir, "ca.crt"),
	})

	archive, err := g.GenerateArchive([]string{"10.0.0.1"}, []string{"us-east-1a", "us-east-1b"})
	if err != nil {
		t.Fatalf("GenerateArchive() error: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	for _, f := range r.File {
		if f.Name == archiveBootstrapFile {
			t.Errorf("bootstrap.yaml should not be in archive — it is sent via ConfigurationFile")
		}
	}
}

func TestGenerateArchive_AllFilesPresent(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("dummy-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}

	g := NewConfigGenerator(logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo), CertPaths{
		TLSCert: filepath.Join(dir, "tls.crt"),
		TLSKey:  filepath.Join(dir, "tls.key"),
		CACert:  filepath.Join(dir, "ca.crt"),
	})

	archive, err := g.GenerateArchive([]string{"10.0.0.1"}, []string{"us-east-1a", "us-east-1b"})
	if err != nil {
		t.Fatalf("GenerateArchive() error: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	wantFiles := map[string]bool{
		"sds/xds-trusted-ca.json":      false,
		"sds/xds-certificate.json":     false,
		"certs/tls.crt":                false,
		"certs/ca.crt":                 false,
		"certs/tls.key":                false,
		"xds-endpoints.json":           false,
		"wasm-endpoints.json":          false,
		"local-cluster-endpoints.json": false,
	}

	for _, f := range r.File {
		if _, ok := wantFiles[f.Name]; ok {
			wantFiles[f.Name] = true
		} else {
			t.Errorf("unexpected file in archive: %s", f.Name)
		}
	}

	for name, found := range wantFiles {
		if !found {
			t.Errorf("expected file missing from archive: %s", name)
		}
	}
}

func TestGenerateArchive_XDSEndpointsContent(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("dummy"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	g := NewConfigGenerator(logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo), CertPaths{
		TLSCert: filepath.Join(dir, "tls.crt"),
		TLSKey:  filepath.Join(dir, "tls.key"),
		CACert:  filepath.Join(dir, "ca.crt"),
	})

	hosts := []string{"10.0.0.1", "10.0.0.2"}
	archive, err := g.GenerateArchive(hosts, []string{"us-east-1a"})
	if err != nil {
		t.Fatalf("GenerateArchive() error: %v", err)
	}

	content := extractFile(t, archive, archiveXDSEndpointsFile)
	for _, host := range hosts {
		if !strings.Contains(content, host) {
			t.Errorf("xds-endpoints.json missing host %q, got: %s", host, content)
		}
	}
}

func TestGenerateArchive_LocalClusterEndpointsContent(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("dummy"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	g := NewConfigGenerator(logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo), CertPaths{
		TLSCert: filepath.Join(dir, "tls.crt"),
		TLSKey:  filepath.Join(dir, "tls.key"),
		CACert:  filepath.Join(dir, "ca.crt"),
	})

	zones := []string{"us-east-1a", "us-east-1b", "us-east-1c"}
	archive, err := g.GenerateArchive([]string{"10.0.0.1"}, zones)
	if err != nil {
		t.Fatalf("GenerateArchive() error: %v", err)
	}

	content := extractFile(t, archive, archiveLocalClusterFile)
	for _, zone := range zones {
		if !strings.Contains(content, `"zone":"`+zone+`"`) {
			t.Errorf("local-cluster-endpoints.json missing zone %q, got: %s", zone, content)
		}
	}
	// cluster_name must match cluster_manager.local_cluster_name / the EDS service_name,
	// or Envoy subscribes to one resource name while this file supplies another.
	if !strings.Contains(content, `"cluster_name":"local_cluster"`) {
		t.Errorf("local-cluster-endpoints.json has wrong cluster_name, got: %s", content)
	}
}

// Guards against the duplicate-port bug: Envoy drops endpoints sharing an address:port
// across localities, so repeated placeholders would silently disable zone-aware routing
// in every zone but the first.
func TestGetLocalClusterEndpointsConfigData_AddressesAreUniquePerZone(t *testing.T) {
	zones := []string{"us-east-1a", "us-east-1b", "us-east-1c", "us-east-1d", "us-east-1f"}
	got := GetLocalClusterEndpointsConfigData(zones)

	re := regexp.MustCompile(`"address":"([^"]+)","port_value":(\d+)`)
	matches := re.FindAllStringSubmatch(got, -1)
	if len(matches) != len(zones) {
		t.Fatalf("found %d socket_address entries, want %d: %s", len(matches), len(zones), got)
	}

	seen := make(map[string]int, len(matches))
	for i, m := range matches {
		endpoint := m[1] + ":" + m[2]
		if prev, dup := seen[endpoint]; dup {
			t.Errorf("locality %d reuses endpoint %s already used by locality %d; "+
				"Envoy will discard it and that zone will lose zone-aware routing",
				i, endpoint, prev)
		}
		seen[endpoint] = i
	}
}

func TestGetLocalClusterEndpointsConfigData(t *testing.T) {
	tests := []struct {
		name           string
		zones          []string
		wantLocalities int
		wantContains   []string
	}{
		{
			name:           "single zone",
			zones:          []string{"us-east-1a"},
			wantLocalities: 1,
			wantContains:   []string{`"zone":"us-east-1a"`, `"health_status":"HEALTHY"`},
		},
		{
			name:           "one locality group per zone",
			zones:          []string{"us-east-1a", "us-east-1b", "us-east-1c"},
			wantLocalities: 3,
			wantContains:   []string{`"zone":"us-east-1a"`, `"zone":"us-east-1b"`, `"zone":"us-east-1c"`},
		},
		{
			// No zones must still produce a parseable resource with an empty endpoints
			// list — disables zone-aware routing without a malformed file.
			name:           "no zones yields an empty endpoints list",
			zones:          nil,
			wantLocalities: 0,
			wantContains:   []string{`"cluster_name":"local_cluster"`, `"endpoints":[]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetLocalClusterEndpointsConfigData(tt.zones)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in output: %s", want, got)
				}
			}

			if n := strings.Count(got, `"locality"`); n != tt.wantLocalities {
				t.Errorf("locality group count = %d, want %d: %s", n, tt.wantLocalities, got)
			}

			// One endpoint per zone: equal per-AZ weighting via host count.
			if n := strings.Count(got, `"lb_endpoints"`); n != tt.wantLocalities {
				t.Errorf("lb_endpoints count = %d, want %d: %s", n, tt.wantLocalities, got)
			}
		})
	}
}

// Identical input must yield identical bytes: the archive is hash-deduped against the
// live ConfigArchiveHash, so nondeterminism would trigger an UpdateProxyConfiguration
// every reconcile.
func TestGenerateArchive_DeterministicForSameInput(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("dummy"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	g := NewConfigGenerator(logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo), CertPaths{
		TLSCert: filepath.Join(dir, "tls.crt"),
		TLSKey:  filepath.Join(dir, "tls.key"),
		CACert:  filepath.Join(dir, "ca.crt"),
	})

	hosts := []string{"10.0.0.1", "10.0.0.2"}
	zones := []string{"us-east-1a", "us-east-1b"}

	first, err := g.GenerateArchive(hosts, zones)
	if err != nil {
		t.Fatalf("GenerateArchive() error: %v", err)
	}
	second, err := g.GenerateArchive(hosts, zones)
	if err != nil {
		t.Fatalf("GenerateArchive() error: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Error("GenerateArchive is not deterministic for identical input; downstream hash dedup would break")
	}
}

func TestGenerateArchive_MissingCerts(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name      string
		writeCert bool
		writeKey  bool
		writeCA   bool
		wantErr   string
	}{
		{
			name:      "missing TLS cert",
			writeCert: false,
			writeKey:  true,
			writeCA:   true,
			wantErr:   "failed to read TLS cert",
		},
		{
			name:      "missing TLS key",
			writeCert: true,
			writeKey:  false,
			writeCA:   true,
			wantErr:   "failed to read TLS key",
		},
		{
			name:      "missing CA cert",
			writeCert: true,
			writeKey:  true,
			writeCA:   false,
			wantErr:   "failed to read CA cert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := filepath.Join(dir, tt.name)
			if err := os.MkdirAll(testDir, 0755); err != nil {
				t.Fatal(err)
			}

			if tt.writeCert {
				if err := os.WriteFile(filepath.Join(testDir, "tls.crt"), []byte("cert"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.writeKey {
				if err := os.WriteFile(filepath.Join(testDir, "tls.key"), []byte("key"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.writeCA {
				if err := os.WriteFile(filepath.Join(testDir, "ca.crt"), []byte("ca"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			g := NewConfigGenerator(logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo), CertPaths{
				TLSCert: filepath.Join(testDir, "tls.crt"),
				TLSKey:  filepath.Join(testDir, "tls.key"),
				CACert:  filepath.Join(testDir, "ca.crt"),
			})

			_, err := g.GenerateArchive([]string{"10.0.0.1"}, []string{"us-east-1a"})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestGetXDSEndpointsConfigData(t *testing.T) {
	tests := []struct {
		name      string
		addresses []string
		wantHosts []string
	}{
		{
			name:      "single host",
			addresses: []string{"10.0.0.1"},
			wantHosts: []string{"10.0.0.1"},
		},
		{
			name:      "multiple hosts — all appear as lb_endpoints",
			addresses: []string{"10.0.0.1", "10.0.0.2"},
			wantHosts: []string{"10.0.0.1", "10.0.0.2"},
		},
		{
			name:      "empty",
			addresses: []string{},
			wantHosts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetXDSEndpointsConfigData(tt.addresses)
			if !strings.Contains(result, "xds_cluster") {
				t.Errorf("missing xds_cluster in output: %s", result)
			}
			for _, host := range tt.wantHosts {
				if !strings.Contains(result, host) {
					t.Errorf("missing host %q in output: %s", host, result)
				}
			}
			// Verify all hosts appear as separate lb_endpoints entries
			if len(tt.addresses) > 1 {
				count := strings.Count(result, `"endpoint"`)
				if count != len(tt.addresses) {
					t.Errorf("expected %d endpoint entries, got %d in: %s", len(tt.addresses), count, result)
				}
			}
		})
	}
}

// The xDS and Wasm endpoints files send their cluster to every Envoy Gateway pod IP, on
// the xDS server port and the Wasm module server port respectively.
func TestEndpointsConfigData(t *testing.T) {
	hosts := []string{"10.0.0.1", "10.0.0.2"}
	tests := []struct {
		name        string
		got         string
		wantCluster string
		wantPort    uint32
	}{
		{
			name:        "xds_cluster",
			got:         GetXDSEndpointsConfigData(hosts),
			wantCluster: "xds_cluster",
			wantPort:    18000,
		},
		{
			name:        "wasm_cluster",
			got:         GetWasmEndpointsConfigData(hosts),
			wantCluster: "wasm_cluster",
			wantPort:    18002,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cla := parseClusterLoadAssignment(t, tt.got)
			assert.Equal(t, tt.wantCluster, cla.GetClusterName())
			assert.Equal(t, []string{"10.0.0.1:" + strconv.Itoa(int(tt.wantPort)), "10.0.0.2:" + strconv.Itoa(int(tt.wantPort))},
				endpointAddresses(cla))
		})
	}
}

func TestGenerateArchive_WasmEndpointsContent(t *testing.T) {
	archive, err := newTestConfigGenerator(t).GenerateArchive([]string{"10.0.0.1", "10.0.0.2"}, []string{"us-east-1a"})
	require.NoError(t, err)

	cla := parseClusterLoadAssignment(t, extractFile(t, archive, archiveWasmEndpointsFile))
	assert.Equal(t, "wasm_cluster", cla.GetClusterName())
	assert.Equal(t, []string{"10.0.0.1:18002", "10.0.0.2:18002"}, endpointAddresses(cla))
}

// The bootstrap's wasm_cluster, the archive's endpoints file and the node SG rule must
// agree: the cluster reads the file the archive writes, the file's cluster_name matches
// the cluster's EDS service_name, and its endpoints use the port the node SG opens.
func TestWasmClusterWiring(t *testing.T) {
	rendered, err := bootstrap.GetRenderedBootstrapConfig(nil)
	require.NoError(t, err)
	bs := &bootstrapv3.Bootstrap{}
	require.NoError(t, bootstrapproto.FromYAML([]byte(rendered), bs))
	var eds *clusterv3.Cluster_EdsClusterConfig
	for _, c := range bs.GetStaticResources().GetClusters() {
		if c.GetName() == "wasm_cluster" {
			eds = c.GetEdsClusterConfig()
		}
	}
	require.NotNil(t, eds, "the bootstrap must define a static wasm_cluster with EDS")

	archive, err := newTestConfigGenerator(t).GenerateArchive([]string{"10.0.0.1"}, []string{"us-east-1a"})
	require.NoError(t, err)

	// The archive is extracted at the root of the filesystem Envoy sees.
	path := eds.GetEdsConfig().GetPathConfigSource().GetPath()
	cla := parseClusterLoadAssignment(t, extractFile(t, archive, strings.TrimPrefix(path, "/")))
	assert.Equal(t, eds.GetServiceName(), cla.GetClusterName())
	assert.Equal(t, []string{"10.0.0.1:" + strconv.Itoa(int(networking.DefaultWasmServerPort))}, endpointAddresses(cla))
}

// newTestConfigGenerator returns a ConfigGenerator that reads dummy certificates.
func newTestConfigGenerator(t *testing.T) *ConfigGenerator {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("dummy-"+name), 0600))
	}
	return NewConfigGenerator(logging.DefaultLogger(io.Discard, egv1a1.LogLevelInfo), CertPaths{
		TLSCert: filepath.Join(dir, "tls.crt"),
		TLSKey:  filepath.Join(dir, "tls.key"),
		CACert:  filepath.Join(dir, "ca.crt"),
	})
}

// parseClusterLoadAssignment parses a file-based EDS file the way Envoy reads it: an xDS
// DiscoveryResponse holding one ClusterLoadAssignment.
func parseClusterLoadAssignment(t *testing.T, content string) *endpointv3.ClusterLoadAssignment {
	t.Helper()
	resp := &discoveryv3.DiscoveryResponse{}
	require.NoError(t, bootstrapproto.FromJSON([]byte(content), resp), "endpoints file must parse as an xDS DiscoveryResponse: %s", content)
	require.Len(t, resp.GetResources(), 1)
	cla := &endpointv3.ClusterLoadAssignment{}
	require.NoError(t, resp.GetResources()[0].UnmarshalTo(cla))
	require.NoError(t, cla.ValidateAll())
	return cla
}

// endpointAddresses returns the "address:port" of every endpoint in cla.
func endpointAddresses(cla *endpointv3.ClusterLoadAssignment) []string {
	var out []string
	for _, locality := range cla.GetEndpoints() {
		for _, lbEndpoint := range locality.GetLbEndpoints() {
			sa := lbEndpoint.GetEndpoint().GetAddress().GetSocketAddress()
			out = append(out, sa.GetAddress()+":"+strconv.Itoa(int(sa.GetPortValue())))
		}
	}
	return out
}

// extractFile reads a single file from a zip archive.
func extractFile(t *testing.T, archive []byte, target string) string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	for _, f := range r.File {
		if f.Name == target {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("Open %s: %v", target, err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("io.ReadAll: %v", err)
			}
			return string(data)
		}
	}
	t.Fatalf("file %q not found in archive", target)
	return ""
}
