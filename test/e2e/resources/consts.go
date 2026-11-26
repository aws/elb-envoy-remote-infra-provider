// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package resources builds the Kubernetes objects e2e tests deploy.
package resources

import "k8s.io/apimachinery/pkg/runtime/schema"

const (
	// DefaultName prefixes every generated name (namespaces, GatewayClasses,
	// EnvoyProxies, and routes).
	DefaultName = "elbproxy-e2e"
	// DefaultGatewayName is the name BuildGatewaySpec gives a Gateway. Routes
	// built here attach to it by this name.
	DefaultGatewayName = DefaultName + "-gateway"
	// DefaultProxyName is the name of a Gateway-scoped ELBProxyConfiguration.
	DefaultProxyName = DefaultName + "-proxy"

	// TestLabelKey labels every object a ResourceStack creates, with the
	// stack's BaseName as the value, so leftovers (for example after
	// -cleanup=false) can be found with `kubectl get ... -l elbproxy-e2e`.
	TestLabelKey = DefaultName

	// TestHostname is the hostname TLS routes and certificates use.
	TestHostname = DefaultName + ".example.com"
	// DefaultPathPrefix is the path the default HTTPRoute rule matches.
	DefaultPathPrefix = "/" + DefaultName
)

// HTTP echo backend. It also backs the TCP and TLS-terminate tests.
const (
	EchoName          = DefaultName + "-backend"
	EchoImage         = "registry.k8s.io/gateway-api/echo-basic:v1.5.1"
	EchoContainerPort = 3000
	EchoServicePort   = 8080
)

// gRPC backend (yages echo server, h2c).
const (
	GRPCName          = DefaultName + "-grpc-backend"
	GRPCImage         = "ghcr.io/envoyproxy/gateway/yages:latest"
	GRPCContainerPort = 9000
	GRPCServicePort   = 8080
)

// UDP backend (socat echo).
const (
	UDPName  = DefaultName + "-udp-backend"
	UDPImage = "alpine:3.20"
	UDPPort  = 9090
)

// TLS backend (nginx terminating TLS itself, for passthrough tests).
const (
	TLSBackendName  = DefaultName + "-tls-backend"
	TLSBackendImage = "nginx:1.27-alpine"
	TLSBackendPort  = 8443
	NginxConfName   = DefaultName + "-nginx-conf"

	// TLSCertSecretName is the self-signed certificate Secret: mounted by the
	// TLS backend for passthrough, referenced by the listener for terminate.
	TLSCertSecretName = DefaultName + "-tls-cert"
)

// nginxTLSConf serves TLS on TLSBackendPort with the certificate from
// TLSCertSecretName. It binds dual-stack: a bare "listen 8443" binds IPv4 only,
// which breaks on IPv6 clusters where the pod endpoint the proxy targets is
// IPv6.
const nginxTLSConf = `server {
    listen 8443 ssl;
    listen [::]:8443 ssl;
    ssl_certificate /etc/nginx/ssl/tls.crt;
    ssl_certificate_key /etc/nginx/ssl/tls.key;
    location / {
        return 200 "TLS passthrough OK\n";
        add_header Content-Type text/plain;
    }
}
`

// Wasm module: the HTTP Wasm example from Envoy Gateway's docs
// (https://gateway.envoyproxy.io/docs/tasks/extensibility/wasm/). It adds
// WasmResponseHeader: WasmResponseHeaderValue to every response. Envoy Gateway
// downloads it from GitHub, so the Envoy Gateway pods need outbound internet access.
const (
	WasmModuleURL           = "https://raw.githubusercontent.com/envoyproxy/examples/main/wasm-cc/lib/envoy_filter_http_wasm_example.wasm"
	WasmModuleSHA256        = "79c9f85128bb0177b6511afa85d587224efded376ac0ef76df56595f1e6315c0"
	WasmResponseHeader      = "x-wasm-custom"
	WasmResponseHeaderValue = "FOO"
)

// EnvoyExtensionPolicyGVK identifies Envoy Gateway's EnvoyExtensionPolicy. Tests
// create it as unstructured, like EnvoyProxy.
var EnvoyExtensionPolicyGVK = schema.GroupVersionKind{
	Group:   "gateway.envoyproxy.io",
	Version: "v1alpha1",
	Kind:    "EnvoyExtensionPolicy",
}

// ClientTrafficPolicyGVK identifies Envoy Gateway's ClientTrafficPolicy.
var ClientTrafficPolicyGVK = schema.GroupVersionKind{
	Group:   "gateway.envoyproxy.io",
	Version: "v1alpha1",
	Kind:    "ClientTrafficPolicy",
}

// EnvoyProxyGVK identifies Envoy Gateway's EnvoyProxy. Tests create it as
// unstructured because the Envoy Gateway Go models lag the cluster CRD.
var EnvoyProxyGVK = schema.GroupVersionKind{
	Group:   "gateway.envoyproxy.io",
	Version: "v1alpha1",
	Kind:    "EnvoyProxy",
}

// SecurityPolicyGVK identifies Envoy Gateway's SecurityPolicy, created as
// unstructured for the same reason as EnvoyProxy.
var SecurityPolicyGVK = schema.GroupVersionKind{
	Group:   "gateway.envoyproxy.io",
	Version: "v1alpha1",
	Kind:    "SecurityPolicy",
}

// HTTP ext_authz backend (Gateway API echo-basic). It listens on a port no route
// backend uses, so the proxy can only reach it if the controller opens that port
// for the policy. The policy prefixes every auth request path with
// ExtAuthPathPrefix, so echo-basic's /status/<code> handler makes the decision:
// a request for /200 is allowed and a request for /401 is denied with 401.
const (
	ExtAuthName       = DefaultName + "-ext-auth"
	ExtAuthPort       = 9001
	ExtAuthPathPrefix = "/status"
)

// Backends on ports no other test uses. The node security group is shared by all
// Gateways, so reaching one of these proves the controller opened its port for this
// test's Gateway; otherwise the proxy answers 503 (403 for an auth service).
const (
	ExtAuthSelectorName = DefaultName + "-ext-auth-selector"
	ExtAuthSelectorPort = 9011

	// DefaultListenerSetName is the name BuildListenerSet gives a ListenerSet.
	DefaultListenerSetName = DefaultName + "-listenerset"
	ListenerSetEchoName    = DefaultName + "-listenerset-backend"
	ListenerSetEchoPort    = 3011
	ListenerSetExtAuthName = DefaultName + "-listenerset-ext-auth"
	ListenerSetExtAuthPort = 9012

	MergedListenerSetEchoName    = DefaultName + "-merged-listenerset-backend"
	MergedListenerSetEchoPort    = 3012
	MergedListenerSetExtAuthName = DefaultName + "-merged-listenerset-ext-auth"
	MergedListenerSetExtAuthPort = 9013

	MergedSecondEchoName     = DefaultName + "-merged-second-backend"
	MergedSecondEchoPort     = 3017
	MergedLateMemberEchoName = DefaultName + "-merged-late-member-backend"
	MergedLateMemberEchoPort = 3013

	// Backends of HTTPRouteLiveBackendChanges.
	LiveBackendAName = DefaultName + "-live-backend-a"
	LiveBackendAPort = 3020
	LiveBackendBName = DefaultName + "-live-backend-b"
	LiveBackendBPort = 3021
	LiveBackendCName = DefaultName + "-live-backend-c"
	LiveBackendCPort = 3022

	// Backend of HTTPRouteRequestMirror.
	MirrorEchoName = DefaultName + "-mirror-backend"
	MirrorEchoPort = 3014
)
