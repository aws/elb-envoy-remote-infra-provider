// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, EnvoyExtensionPolicyWasm)
}

// EnvoyExtensionPolicyWasm attaches Envoy Gateway's HTTP Wasm example to a route.
// Envoy Gateway downloads the module and serves it from its own pods on port 18002,
// and the proxy downloads it from there through wasm_cluster. The wasm_cluster that
// Envoy Gateway sends points at an in-cluster DNS name the proxy can't resolve, so the
// controller defines its own wasm_cluster in the proxy bootstrap with the Envoy Gateway
// pod IPs, and opens port 18002 on the node security group. If the proxy can't download
// the module, Envoy fails closed and the route answers 503. The module adds
// x-wasm-custom: FOO to every response, which proves it ran.
//
// Envoy Gateway downloads the module from GitHub, so its pods need outbound internet access.
var EnvoyExtensionPolicyWasm = framework.TestCase{
	ShortName:   "EnvoyExtensionPolicyWasm",
	Description: "Attach an EnvoyExtensionPolicy with an HTTP Wasm module and verify the ELBProxy-managed proxy downloads it from Envoy Gateway and runs it",
	Test: func(t *testing.T, s *framework.Suite) {
		route := resources.BuildHTTPRoute(nil, nil, nil)
		stack := newStack(s, "envoyextensionpolicy-wasm", httpListeners(), elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.Dps = []*appsv1.Deployment{resources.BuildEchoDeploymentSpec()}
		stack.Svcs = []*corev1.Service{resources.BuildEchoServiceSpec()}
		stack.Httprs = []*gwapiv1.HTTPRoute{route}
		stack.Policies = []*unstructured.Unstructured{resources.BuildWasmEnvoyExtensionPolicy(route.Name)}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)

		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: stack.GetGatewayAddress(), Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{
				StatusCode: 200,
				Header:     map[string]string{resources.WasmResponseHeader: resources.WasmResponseHeaderValue},
			},
		)
	},
}
