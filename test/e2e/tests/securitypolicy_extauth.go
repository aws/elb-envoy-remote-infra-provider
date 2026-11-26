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
	E2ETests = append(E2ETests, SecurityPolicyExtAuth)
}

// SecurityPolicyExtAuth attaches an HTTP ext_authz SecurityPolicy to a route.
// The auth service listens on a port no route backend uses, so it is reachable
// only if the controller opened that port on the node security group for the
// proxy. If it is unreachable, Envoy answers 403 to every request.
//
// The auth service answers each request with the status code in its path (see
// resources.ExtAuthPathPrefix), while the route backend answers 200 to any path.
// A 401 for /401 can therefore only come from the auth service, which proves the
// policy is applied and its backend is reachable. A 200 for /200 then proves that
// allowed requests reach the route backend.
var SecurityPolicyExtAuth = framework.TestCase{
	ShortName:   "SecurityPolicyExtAuth",
	Description: "Attach an HTTP ext_authz SecurityPolicy whose auth service no route references and verify the proxy reaches it: a denied path returns the auth service's 401, an allowed path returns 200",
	Test: func(t *testing.T, s *framework.Suite) {
		route := resources.BuildHTTPRoute(nil, []gwapiv1.HTTPRouteRule{resources.BuildHTTPRouteRule("/")}, nil)
		stack := newStack(s, "securitypolicy-extauth", httpListeners(), elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.Dps = []*appsv1.Deployment{resources.BuildEchoDeploymentSpec(), resources.BuildExtAuthDeploymentSpec()}
		stack.Svcs = []*corev1.Service{resources.BuildEchoServiceSpec(), resources.BuildExtAuthServiceSpec()}
		stack.Httprs = []*gwapiv1.HTTPRoute{route}
		stack.Policies = []*unstructured.Unstructured{resources.BuildExtAuthSecurityPolicy(route.Name)}
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
