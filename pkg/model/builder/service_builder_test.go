// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"strings"
	"testing"

	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

func Test_buildServiceNameAndLabels(t *testing.T) {
	tests := []struct {
		name           string
		proxyInfra     *pb.ProxyInfra
		wantName       string
		wantLabels     map[string]string
		wantNotInLabel []string
	}{
		{
			name: "merged: uses owning-gatewayclass label and GatewayClass name",
			proxyInfra: &pb.ProxyInfra{
				Name: "eg7",
				Metadata: &pb.InfraMetadata{Labels: map[string]string{
					owningGatewayClassLabel: "eg7",
				}},
			},
			wantName: buildServiceNameFromKey("eg7"),
			wantLabels: map[string]string{
				owningGatewayClassLabel:           "eg7",
				"app.kubernetes.io/component":     "proxy",
				"app.kubernetes.io/managed-by":    "envoy-gateway",
				"app.kubernetes.io/name":          "envoy",
				"gateway.envoyproxy.io/delegated": "elbproxy-extension-server",
			},
			wantNotInLabel: []string{
				owningGatewayNameLabel,
				owningGatewayNamespaceLabel,
			},
		},
		{
			name: "non-merged: uses owning-gateway labels from IR metadata",
			proxyInfra: &pb.ProxyInfra{
				Name: "default/my-gw",
				Metadata: &pb.InfraMetadata{Labels: map[string]string{
					owningGatewayNameLabel:      "my-gw",
					owningGatewayNamespaceLabel: "default",
				}},
			},
			wantName: buildServiceNameFromKey("default/my-gw"),
			wantLabels: map[string]string{
				owningGatewayNameLabel:            "my-gw",
				owningGatewayNamespaceLabel:       "default",
				"app.kubernetes.io/component":     "proxy",
				"app.kubernetes.io/managed-by":    "envoy-gateway",
				"app.kubernetes.io/name":          "envoy",
				"gateway.envoyproxy.io/delegated": "elbproxy-extension-server",
			},
			wantNotInLabel: []string{owningGatewayClassLabel},
		},
		{
			name:       "nil metadata: only base controller labels are emitted",
			proxyInfra: &pb.ProxyInfra{Name: "default/my-gw"},
			// No owning-* labels, so the name key is just "/".
			wantName: buildServiceNameFromKey("/"),
			wantLabels: map[string]string{
				"app.kubernetes.io/component":     "proxy",
				"app.kubernetes.io/managed-by":    "envoy-gateway",
				"app.kubernetes.io/name":          "envoy",
				"gateway.envoyproxy.io/delegated": "elbproxy-extension-server",
			},
			wantNotInLabel: []string{
				owningGatewayClassLabel,
				owningGatewayNameLabel,
				owningGatewayNamespaceLabel,
			},
		},
		{
			name: "extra IR metadata labels are passed through",
			proxyInfra: &pb.ProxyInfra{
				Name: "default/my-gw",
				Metadata: &pb.InfraMetadata{Labels: map[string]string{
					owningGatewayNameLabel:      "my-gw",
					owningGatewayNamespaceLabel: "default",
					"custom.example.com/team":   "platform",
				}},
			},
			wantName: buildServiceNameFromKey("default/my-gw"),
			wantLabels: map[string]string{
				owningGatewayNameLabel:      "my-gw",
				owningGatewayNamespaceLabel: "default",
				"custom.example.com/team":   "platform",
			},
		},
		{
			name: "IR metadata labels override base controller labels",
			proxyInfra: &pb.ProxyInfra{
				Name: "default/my-gw",
				Metadata: &pb.InfraMetadata{Labels: map[string]string{
					"app.kubernetes.io/component": "custom-component",
				}},
			},
			wantLabels: map[string]string{
				"app.kubernetes.io/component": "custom-component",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &serviceBuilder{}
			name, labels := s.buildServiceNameAndLabels(tt.proxyInfra)

			if tt.wantName != "" {
				assert.Equal(t, tt.wantName, name)
			}
			for k, v := range tt.wantLabels {
				assert.Equal(t, v, labels[k], "label %q mismatch", k)
			}
			for _, k := range tt.wantNotInLabel {
				assert.NotContains(t, labels, k)
			}
		})
	}

	// Extra: merged "eg7" and non-merged "default/eg7" must not collide.
	t.Run("merged and non-merged with same base string differ", func(t *testing.T) {
		s := &serviceBuilder{}
		merged, _ := s.buildServiceNameAndLabels(&pb.ProxyInfra{
			Name: "eg7",
			Metadata: &pb.InfraMetadata{Labels: map[string]string{
				owningGatewayClassLabel: "eg7",
			}},
		})
		nonMerged, _ := s.buildServiceNameAndLabels(&pb.ProxyInfra{
			Name: "default/eg7",
			Metadata: &pb.InfraMetadata{Labels: map[string]string{
				owningGatewayNameLabel:      "eg7",
				owningGatewayNamespaceLabel: "default",
			}},
		})
		assert.NotEqual(t, merged, nonMerged)
	})
}

func Test_buildServiceNameFromKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"namespace/name key", "default/my-gateway"},
		{"bare GatewayClass name", "eg7"},
		{"long key truncated to 63 chars", strings.Repeat("a", 200)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := buildServiceNameFromKey(tt.key)
			assert.True(t, strings.HasPrefix(name, "elbp-"))
			assert.LessOrEqual(t, len(name), 63)
			assert.Regexp(t, `^[a-z0-9-]+$`, name)
		})
	}

	t.Run("keys with same sanitized prefix differ via hash", func(t *testing.T) {
		assert.NotEqual(t, buildServiceNameFromKey("default/eg"), buildServiceNameFromKey("defaulteg"))
	})
}

func Test_buildServiceName(t *testing.T) {
	tests := []struct {
		name    string
		labels  map[string]string
		wantKey string
	}{
		{
			name:    "merged: derives key from owning-gatewayclass",
			labels:  map[string]string{owningGatewayClassLabel: "eg7"},
			wantKey: "eg7",
		},
		{
			name: "non-merged: derives key from namespace/name",
			labels: map[string]string{
				owningGatewayNamespaceLabel: "default",
				owningGatewayNameLabel:      "my-gw",
			},
			wantKey: "default/my-gw",
		},
		{
			name: "merged label takes precedence over per-gateway labels",
			labels: map[string]string{
				owningGatewayClassLabel:     "eg7",
				owningGatewayNamespaceLabel: "default",
				owningGatewayNameLabel:      "my-gw",
			},
			wantKey: "eg7",
		},
		{
			name:    "no owning labels: key is bare separator",
			labels:  map[string]string{},
			wantKey: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, buildServiceNameFromKey(tt.wantKey), buildServiceName(tt.labels))
		})
	}
}

func Test_buildServicePorts(t *testing.T) {
	tests := []struct {
		name      string
		infra     *pb.ProxyInfra
		wantLen   int
		wantProto corev1.Protocol
		wantErr   bool
	}{
		{
			name: "basic TCP ports",
			infra: &pb.ProxyInfra{Listeners: []*pb.ProxyListener{{
				Ports: []*pb.ListenerPort{
					{Name: "http", Protocol: ir.HTTPProtocolType, ServicePort: 80, ContainerPort: 8080},
					{Name: "https", Protocol: ir.HTTPSProtocolType, ServicePort: 443, ContainerPort: 8443},
				},
			}}},
			wantLen: 2,
		},
		{
			name: "duplicate port+protocol deduplicated",
			infra: &pb.ProxyInfra{Listeners: []*pb.ProxyListener{{
				Ports: []*pb.ListenerPort{
					{Name: "http", Protocol: ir.HTTPProtocolType, ServicePort: 80, ContainerPort: 8080},
					{Name: "http-dup", Protocol: ir.HTTPProtocolType, ServicePort: 80, ContainerPort: 8080},
				},
			}}},
			wantLen: 1,
		},
		{
			name: "UDP protocol mapped correctly",
			infra: &pb.ProxyInfra{Listeners: []*pb.ProxyListener{{
				Ports: []*pb.ListenerPort{
					{Name: "dns", Protocol: ir.UDPProtocolType, ServicePort: 53, ContainerPort: 5353},
				},
			}}},
			wantLen:   1,
			wantProto: corev1.ProtocolUDP,
		},
		{
			name:    "empty listeners returns error",
			infra:   &pb.ProxyInfra{Listeners: []*pb.ProxyListener{}},
			wantErr: true,
		},
		{
			name:    "nil infra returns error",
			infra:   nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ports, err := buildServicePorts(tt.infra)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, ports, tt.wantLen)
			if tt.wantProto != "" {
				assert.Equal(t, tt.wantProto, ports[0].Protocol)
			}
		})
	}
}

func Test_sanitizeServicePortName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http", "http"},
		{"HTTP", "http"},
		{"http-8080", "http-8080"},
		{"http_8080", "http-8080"},
		{"-http-", "http"},
		{"123abc", "123abc"},
		{strings.Repeat("a", 20), strings.Repeat("a", 15)},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeServicePortName(tt.input))
		})
	}
}
