// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

// The deploy* helpers follow LBC's ALBTestStack.DeployHTTP and friends. Each
// one deploys a backend behind one Gateway with gwListeners, the routes the
// test built, and a Gateway-scoped ELBProxyConfiguration built from proxySpec.
// Tests with other shapes (GatewayClass-scoped proxies, several Gateways)
// assemble a resources.ResourceStack themselves.

// httpListeners returns the single HTTP :80 listener most tests use.
func httpListeners() []gwapiv1.Listener {
	return []gwapiv1.Listener{{Name: "http", Protocol: gwapiv1.HTTPProtocolType, Port: 80}}
}

// deployHTTP deploys the echo backend and httprs.
func deployHTTP(t *testing.T, s *framework.Suite, baseName string, gwListeners []gwapiv1.Listener,
	httprs []*gwapiv1.HTTPRoute, proxySpec elbproxyv1alpha1.ELBProxyConfigurationSpec) *resources.ResourceStack {
	t.Helper()
	stack := newStack(s, baseName, gwListeners, proxySpec)
	stack.Dps = []*appsv1.Deployment{resources.BuildEchoDeploymentSpec()}
	stack.Svcs = []*corev1.Service{resources.BuildEchoServiceSpec()}
	stack.Httprs = httprs
	stack.Deploy(t, s)
	return stack
}

// deployGRPC deploys the gRPC backend and grpcrs.
func deployGRPC(t *testing.T, s *framework.Suite, baseName string, gwListeners []gwapiv1.Listener,
	grpcrs []*gwapiv1.GRPCRoute, proxySpec elbproxyv1alpha1.ELBProxyConfigurationSpec) *resources.ResourceStack {
	t.Helper()
	stack := newStack(s, baseName, gwListeners, proxySpec)
	stack.Dps = []*appsv1.Deployment{resources.BuildGRPCDeploymentSpec()}
	stack.Svcs = []*corev1.Service{resources.BuildGRPCServiceSpec()}
	stack.Grpcrs = grpcrs
	stack.Deploy(t, s)
	return stack
}

// deployTCP deploys the echo backend and tcprs.
func deployTCP(t *testing.T, s *framework.Suite, baseName string, gwListeners []gwapiv1.Listener,
	tcprs []*gwapiv1.TCPRoute, proxySpec elbproxyv1alpha1.ELBProxyConfigurationSpec) *resources.ResourceStack {
	t.Helper()
	stack := newStack(s, baseName, gwListeners, proxySpec)
	stack.Dps = []*appsv1.Deployment{resources.BuildEchoDeploymentSpec()}
	stack.Svcs = []*corev1.Service{resources.BuildEchoServiceSpec()}
	stack.Tcprs = tcprs
	stack.Deploy(t, s)
	return stack
}

// deployUDP deploys the UDP echo backend and udprs.
func deployUDP(t *testing.T, s *framework.Suite, baseName string, gwListeners []gwapiv1.Listener,
	udprs []*gwapiv1.UDPRoute, proxySpec elbproxyv1alpha1.ELBProxyConfigurationSpec) *resources.ResourceStack {
	t.Helper()
	stack := newStack(s, baseName, gwListeners, proxySpec)
	stack.Dps = []*appsv1.Deployment{resources.BuildUDPDeploymentSpec()}
	stack.Svcs = []*corev1.Service{resources.BuildUDPServiceSpec()}
	stack.Udprs = udprs
	stack.Deploy(t, s)
	return stack
}

// deployTLSPassthrough deploys the TLS backend, which terminates TLS itself
// with the resources.TLSCertSecretName certificate, and tlsrs.
func deployTLSPassthrough(t *testing.T, s *framework.Suite, baseName string, gwListeners []gwapiv1.Listener,
	tlsrs []*gwapiv1.TLSRoute, proxySpec elbproxyv1alpha1.ELBProxyConfigurationSpec) *resources.ResourceStack {
	t.Helper()
	stack := newStack(s, baseName, gwListeners, proxySpec)
	stack.Secrets = []*corev1.Secret{buildTLSSecret(t)}
	stack.Cms = []*corev1.ConfigMap{resources.BuildNginxTLSConfigMap()}
	stack.Dps = []*appsv1.Deployment{resources.BuildTLSBackendDeploymentSpec()}
	stack.Svcs = []*corev1.Service{resources.BuildTLSBackendServiceSpec()}
	stack.Tlsrs = tlsrs
	stack.Deploy(t, s)
	return stack
}

// deployTLSTerminate deploys the echo backend, the
// resources.TLSCertSecretName certificate for a Terminate listener in
// gwListeners to reference, and tlsrs.
func deployTLSTerminate(t *testing.T, s *framework.Suite, baseName string, gwListeners []gwapiv1.Listener,
	tlsrs []*gwapiv1.TLSRoute, proxySpec elbproxyv1alpha1.ELBProxyConfigurationSpec) *resources.ResourceStack {
	t.Helper()
	stack := newStack(s, baseName, gwListeners, proxySpec)
	stack.Secrets = []*corev1.Secret{buildTLSSecret(t)}
	stack.Dps = []*appsv1.Deployment{resources.BuildEchoDeploymentSpec()}
	stack.Svcs = []*corev1.Service{resources.BuildEchoServiceSpec()}
	stack.Tlsrs = tlsrs
	stack.Deploy(t, s)
	return stack
}

// newStack builds what every deploy* stack has: its own EnvoyProxy and
// GatewayClass, a Gateway with gwListeners, and a Gateway-scoped
// ELBProxyConfiguration built from proxySpec.
func newStack(s *framework.Suite, baseName string, gwListeners []gwapiv1.Listener,
	proxySpec elbproxyv1alpha1.ELBProxyConfigurationSpec) *resources.ResourceStack {
	envoyProxy := resources.BuildEnvoyProxySpec(s, false)
	gwc := resources.BuildGatewayClassSpec(s, envoyProxy)
	gw := resources.BuildGatewaySpec(gwc, gwListeners)
	return &resources.ResourceStack{
		BaseName:   baseName,
		EnvoyProxy: envoyProxy,
		Gwc:        gwc,
		Gws:        []*gwapiv1.Gateway{gw},
		Proxies:    []*elbproxyv1alpha1.ELBProxyConfiguration{resources.BuildELBProxyConfiguration(s, gw, proxySpec)},
	}
}

// buildTLSSecret builds the resources.TLSCertSecretName certificate, valid for
// resources.TestHostname and its subdomains.
func buildTLSSecret(t *testing.T) *corev1.Secret {
	t.Helper()
	secret, err := resources.BuildTLSSecret(resources.TestHostname, "*."+resources.TestHostname)
	if err != nil {
		t.Fatalf("build TLS secret: %v", err)
	}
	return secret
}
