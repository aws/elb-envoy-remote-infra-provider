// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationGatewayClassScope)
}

// ELBProxyConfigurationGatewayClassScope creates an ELBProxyConfiguration that targets the
// GatewayClass (not an individual Gateway), living in the controller namespace
// as required for GatewayClass scope, then verifies the proxy attaches to a
// Gateway in that class and serves traffic — proving class-level fan-out.
//
// The proxy MUST live in the controller namespace (the EnvoyProxy /
// envoy-gateway install namespace): GatewayClass-scoped targetRefs from any
// other namespace are ignored. The proxy is created in that namespace while the
// Gateway/HTTPRoute live in their own. The test's GatewayClass is its own, so
// the class-wide proxy can't attach to other tests' Gateways.
var ELBProxyConfigurationGatewayClassScope = framework.TestCase{
	ShortName:   "ELBProxyConfigurationGatewayClassScope",
	Description: "A GatewayClass-scoped ELBProxyConfiguration attaches to Gateways in the class and serves traffic",
	Test: func(t *testing.T, s *framework.Suite) {
		envoyProxy := resources.BuildEnvoyProxySpec(s, false)
		gwc := resources.BuildGatewayClassSpec(s, envoyProxy)
		stack := &resources.ResourceStack{
			BaseName:   "gc-scope",
			EnvoyProxy: envoyProxy,
			Gwc:        gwc,
			Gws:        []*gwapiv1.Gateway{resources.BuildGatewaySpec(gwc, httpListeners())},
			Dps:        []*appsv1.Deployment{resources.BuildEchoDeploymentSpec()},
			Svcs:       []*corev1.Service{resources.BuildEchoServiceSpec()},
			Httprs:     []*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			Proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				resources.BuildGatewayClassELBProxyConfiguration(s, gwc, elbproxyv1alpha1.ELBProxyConfigurationSpec{}),
			},
		}
		stack.Deploy(t, s)

		var addr string

		t.Run("GatewayClass-scoped ELBProxyConfiguration in the controller namespace is Accepted", func(t *testing.T) {
			s.WaitForELBProxyConfigurationAccepted(t, client.ObjectKeyFromObject(stack.Proxies[0]))
		})

		t.Run("Proxy attaches to a Gateway in the class and provisions", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
			addr = stack.GetGatewayAddress()
		})

		t.Run("Class-scoped data plane serves traffic", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})
	},
}
