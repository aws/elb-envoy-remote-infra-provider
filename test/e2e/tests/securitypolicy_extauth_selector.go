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
	E2ETests = append(E2ETests, SecurityPolicyExtAuthTargetSelector)
}

// SecurityPolicyExtAuthTargetSelector is SecurityPolicyExtAuth with the policy attached
// by route label through targetSelectors. An unreachable auth service gives 403.
var SecurityPolicyExtAuthTargetSelector = framework.TestCase{
	ShortName:   "SecurityPolicyExtAuthTargetSelector",
	Description: "Attach an HTTP ext_authz SecurityPolicy to a route through targetSelectors and verify the proxy reaches its auth service: a denied path returns the auth service's 401, an allowed path returns 200",
	Test: func(t *testing.T, s *framework.Suite) {
		selector := map[string]string{resources.DefaultName + "-ext-auth": "selector"}
		route := resources.BuildHTTPRoute(nil, []gwapiv1.HTTPRouteRule{resources.BuildHTTPRouteRule("/")}, nil)
		route.Labels = selector

		stack := newStack(s, "securitypolicy-extauth-selector", httpListeners(), elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.Dps = []*appsv1.Deployment{
			resources.BuildEchoDeploymentSpec(),
			resources.BuildExtAuthDeploymentOnPort(resources.ExtAuthSelectorName, resources.ExtAuthSelectorPort),
		}
		stack.Svcs = []*corev1.Service{
			resources.BuildEchoServiceSpec(),
			resources.BuildExtAuthServiceOnPort(resources.ExtAuthSelectorName, resources.ExtAuthSelectorPort),
		}
		stack.Httprs = []*gwapiv1.HTTPRoute{route}
		stack.Policies = []*unstructured.Unstructured{resources.BuildExtAuthSecurityPolicyForSelector(
			route.Name+"-ext-auth", selector, resources.ExtAuthSelectorName, resources.ExtAuthSelectorPort)}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)
		addr := stack.GetGatewayAddress()

		// Denied by the auth service, which Envoy relays to the client.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: "/401"},
			framework.HTTPExpectation{StatusCode: 401},
		)
		// Allowed by the auth service and served by the route backend.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: "/200"},
			framework.HTTPExpectation{StatusCode: 200},
		)
	},
}
