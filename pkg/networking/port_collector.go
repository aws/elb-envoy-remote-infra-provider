// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"sort"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"k8s.io/utils/ptr"
)

// PortProtocol represents a protocol/port pair for Node SG rules.
type PortProtocol struct {
	Protocol string
	Port     int32
}

const (
	DefaultXDSServerPort int32 = 18000

	DefaultWasmServerPort int32 = 18002
)

// CollectNodeSGPorts builds the set of per-port rules that need to be opened on the Node SG: the
// backend target ports, and, while any gateway is active, the TCP ports of the Envoy Gateway
// servers the proxy connects to (controlPlanePorts: xDS, and the Wasm module server). A proxy
// needs the xDS port to get its configuration from Envoy Gateway, including before a route gives
// its Gateway a backend port; without it, a new proxy never becomes active. With no active
// gateways and no backend ports, nothing needs to be open.
func CollectNodeSGPorts(backendTargetPorts []PortProtocol, gatewaysActive bool, controlPlanePorts ...int32) []PortProtocol {
	if !gatewaysActive && len(backendTargetPorts) == 0 {
		return nil
	}

	seen := make(map[PortProtocol]struct{})
	var ports []PortProtocol

	add := func(proto string, port int32) {
		pp := PortProtocol{Protocol: proto, Port: port}
		if _, exists := seen[pp]; !exists {
			seen[pp] = struct{}{}
			ports = append(ports, pp)
		}
	}

	// Open the control-plane ports on the node SG so ELBProxy envoy can reach Envoy Gateway:
	// the xDS server for dynamic configuration updates, and the Wasm module server.
	for _, port := range controlPlanePorts {
		add("tcp", port)
	}
	for _, p := range backendTargetPorts {
		add(p.Protocol, p.Port)
	}

	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		return ports[i].Protocol < ports[j].Protocol
	})

	return ports
}

// BuildNodeSGPermissions builds IPPermissionInfos for node SG ingress rules with per-gateway labels.
func BuildNodeSGPermissions(ports []PortProtocol, sourceSGID string, labels map[string]string) []IPPermissionInfo {
	perms := make([]IPPermissionInfo, 0, len(ports))
	for _, pp := range ports {
		perms = append(perms, NewGroupIDIPPermission(
			pp.Protocol,
			ptr.To(pp.Port),
			ptr.To(pp.Port),
			sourceSGID,
			labels,
		))
	}
	return perms
}

// BuildClusterRuleLabels builds the labels for node SG rules managed by this
// controller instance.
func BuildClusterRuleLabels(controllerNS string) map[string]string {
	return map[string]string{
		tracking.TagKeyManagedBy:    tracking.TagValueManagedBy,
		tracking.TagKeyControllerNS: controllerNS,
	}
}
