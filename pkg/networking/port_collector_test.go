// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_CollectNodeSGPorts(t *testing.T) {
	xdsOnly := []int32{DefaultXDSServerPort}
	xdsAndWasm := []int32{DefaultXDSServerPort, DefaultWasmServerPort}

	tests := []struct {
		name               string
		backendTargetPorts []PortProtocol
		controlPlanePorts  []int32
		noActiveGateways   bool
		want               []PortProtocol
	}{
		{
			name:               "no backend ports and no active gateways: nothing to open",
			backendTargetPorts: nil,
			controlPlanePorts:  xdsAndWasm,
			noActiveGateways:   true,
			want:               nil,
		},
		{
			// A new gateway's proxy needs xDS to get its configuration before any route exists.
			name:               "no backend ports, active gateway: xDS port only",
			backendTargetPorts: nil,
			controlPlanePorts:  xdsOnly,
			want:               []PortProtocol{{Protocol: "tcp", Port: 18000}},
		},
		{
			name:               "no backend ports, active gateway, custom xDS port",
			backendTargetPorts: nil,
			controlPlanePorts:  []int32{19000},
			want:               []PortProtocol{{Protocol: "tcp", Port: 19000}},
		},
		{
			name:               "no backend ports, active gateway: xDS and Wasm ports",
			backendTargetPorts: nil,
			controlPlanePorts:  xdsAndWasm,
			want: []PortProtocol{
				{Protocol: "tcp", Port: 18000},
				{Protocol: "tcp", Port: 18002},
			},
		},
		{
			name: "backend ports added alongside xDS",
			backendTargetPorts: []PortProtocol{
				{Protocol: "tcp", Port: 8080},
				{Protocol: "tcp", Port: 9090},
			},
			controlPlanePorts: xdsOnly,
			want: []PortProtocol{
				{Protocol: "tcp", Port: 8080},
				{Protocol: "tcp", Port: 9090},
				{Protocol: "tcp", Port: 18000},
			},
		},
		{
			name: "backend ports added alongside xDS and Wasm",
			backendTargetPorts: []PortProtocol{
				{Protocol: "tcp", Port: 8080},
			},
			controlPlanePorts: xdsAndWasm,
			want: []PortProtocol{
				{Protocol: "tcp", Port: 8080},
				{Protocol: "tcp", Port: 18000},
				{Protocol: "tcp", Port: 18002},
			},
		},
		{
			name: "duplicate ports deduplicated",
			backendTargetPorts: []PortProtocol{
				{Protocol: "tcp", Port: 8080},
				{Protocol: "tcp", Port: 8080},
				{Protocol: "tcp", Port: 18000}, // same as default xDS
				{Protocol: "tcp", Port: 18002}, // same as default Wasm
			},
			controlPlanePorts: xdsAndWasm,
			want: []PortProtocol{
				{Protocol: "tcp", Port: 8080},
				{Protocol: "tcp", Port: 18000},
				{Protocol: "tcp", Port: 18002},
			},
		},
		{
			name: "mixed protocols",
			backendTargetPorts: []PortProtocol{
				{Protocol: "udp", Port: 53},
				{Protocol: "tcp", Port: 53},
			},
			controlPlanePorts: xdsOnly,
			want: []PortProtocol{
				{Protocol: "tcp", Port: 53},
				{Protocol: "udp", Port: 53},
				{Protocol: "tcp", Port: 18000},
			},
		},
		{
			name: "sorted by port then protocol",
			backendTargetPorts: []PortProtocol{
				{Protocol: "tcp", Port: 9090},
				{Protocol: "udp", Port: 53},
				{Protocol: "tcp", Port: 80},
			},
			controlPlanePorts: xdsOnly,
			want: []PortProtocol{
				{Protocol: "udp", Port: 53},
				{Protocol: "tcp", Port: 80},
				{Protocol: "tcp", Port: 9090},
				{Protocol: "tcp", Port: 18000},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectNodeSGPorts(tt.backendTargetPorts, !tt.noActiveGateways, tt.controlPlanePorts...)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_BuildNodeSGPermissions(t *testing.T) {
	ports := []PortProtocol{
		{Protocol: "tcp", Port: 8080},
		{Protocol: "udp", Port: 53},
	}
	labels := BuildClusterRuleLabels("envoy-gateway-system")

	perms := BuildNodeSGPermissions(ports, "sg-backend", labels)
	assert.Len(t, perms, 2)
	assert.Equal(t, "tcp", *perms[0].Permission.IpProtocol)
	assert.Equal(t, "sg-backend", *perms[0].Permission.UserIdGroupPairs[0].GroupId)
	assert.Equal(t, "udp", *perms[1].Permission.IpProtocol)
}

func Test_BuildClusterRuleLabels(t *testing.T) {
	tests := []struct {
		name         string
		controllerNS string
		wantLabels   map[string]string
	}{
		{
			name:         "default namespace",
			controllerNS: "envoy-gateway-system",
			wantLabels: map[string]string{
				"gateway.elbproxy.aws/managed-by":    "elb-envoy-remote-infra-provider",
				"gateway.elbproxy.aws/controller-ns": "envoy-gateway-system",
			},
		},
		{
			name:         "custom namespace — multi-tenant",
			controllerNS: "team-alpha",
			wantLabels: map[string]string{
				"gateway.elbproxy.aws/managed-by":    "elb-envoy-remote-infra-provider",
				"gateway.elbproxy.aws/controller-ns": "team-alpha",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildClusterRuleLabels(tt.controllerNS)
			assert.Equal(t, tt.wantLabels, got)
		})
	}
}

func Test_BuildClusterRuleLabels_DifferentNamespacesProduceDifferentLabels(t *testing.T) {
	labelsA := BuildClusterRuleLabels("ns-a")
	labelsB := BuildClusterRuleLabels("ns-b")

	// Same managed-by value
	assert.Equal(t, labelsA["gateway.elbproxy.aws/managed-by"], labelsB["gateway.elbproxy.aws/managed-by"])
	// Different controller-ns — this is what prevents cross-controller revocation
	assert.NotEqual(t, labelsA["gateway.elbproxy.aws/controller-ns"], labelsB["gateway.elbproxy.aws/controller-ns"])
}
