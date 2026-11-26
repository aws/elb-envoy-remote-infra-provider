// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxyir

import (
	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

// ELBProxyInfra wraps the upstream infra IR with ELBProxy-specific typed fields.
type ELBProxyInfra struct {
	*ir.Infra

	// ELBProxyConfiguration is the resolved ELBProxyConfiguration CRD for this gateway.
	ELBProxyConfiguration *elbproxyv1alpha1.ELBProxyConfiguration

	// BackendTargetPorts are the unique backend port/protocol pairs that the gateway's routes target.
	// Used to create restricted per-port ingress rules on the node security group instead of allowing all-traffic.
	BackendTargetPorts []ir.PortProtocol

	// AllBackendTargetPorts is the union of BackendTargetPorts across all active gateways.
	// Used to reconcile node SG rules so that a port shared by multiple gateways is only
	// revoked when no gateway needs it anymore.
	AllBackendTargetPorts []ir.PortProtocol

	// XDSHosts are the VPC-routable IPs of all ready xDS server pods
	XDSHosts []string

	// IRPresent reports whether the upstream Envoy Gateway InfraIR was available for
	// this reconcile. When false (sidecar restarted, EG has not yet replayed), Infra
	// is a minimal placeholder carrying only the gateway key; IR-derived facets
	// (bootstrap, frontend SG, K8s Service) are preserved rather than reverted.
	IRPresent bool
}
