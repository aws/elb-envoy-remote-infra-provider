// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import "os"

const (
	// GatewayControllerName is the name of the GatewayClass controller.
	GatewayControllerName = "gateway.elbproxy.aws/gatewayclass-controller"

	// EnvoyGatewayServiceName is the Kubernetes Service name for the Envoy Gateway
	// control plane. Used to watch EndpointSlices and resolve the xDS server pod IP.
	EnvoyGatewayServiceName = "envoy-gateway"

	// EnvoyGatewayCertSecretName is the Kubernetes Secret that holds the TLS
	// certificates mounted into the extension container at /tmp/certs.
	// When this Secret is rotated, all proxy archives must be regenerated.
	EnvoyGatewayCertSecretName = "envoy-gateway"

	// EnvoyGatewayNamespaceEnvVar is the environment variable that overrides the
	// default Envoy Gateway namespace.
	EnvoyGatewayNamespaceEnvVar = "ENVOY_GATEWAY_NAMESPACE"

	// DefaultEnvoyGatewayNamespace is the fallback namespace for the Envoy Gateway
	// control plane when ENVOY_GATEWAY_NAMESPACE is not set.
	DefaultEnvoyGatewayNamespace = "envoy-gateway-system"
)

// EnvoyGatewayNamespace resolves the namespace where the Envoy Gateway control plane
// (and proxy Services managed by this controller) runs. It reads the
// ENVOY_GATEWAY_NAMESPACE environment variable at call time and falls back to
// DefaultEnvoyGatewayNamespace when unset.
func EnvoyGatewayNamespace() string {
	if ns := os.Getenv(EnvoyGatewayNamespaceEnvVar); ns != "" {
		return ns
	}
	return DefaultEnvoyGatewayNamespace
}
