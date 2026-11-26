// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, HTTPRoutePathRouting)
}

// HTTPRoutePathRouting provisions a Gateway+HTTPRoute+ELBProxyConfiguration whose route
// only matches the /elbproxy-e2e-foo prefix, then verifies that a matching path
// returns 200 and an unmatched path returns 404
var HTTPRoutePathRouting = framework.TestCase{
	ShortName:   "HTTPRoutePathRouting",
	Description: "Verify HTTPRoute path-prefix matching: a matched path returns 200, an unmatched path returns 404",
	Test: func(t *testing.T, s *framework.Suite) {
		httpr := resources.BuildHTTPRoute(nil,
			[]gwapiv1.HTTPRouteRule{resources.BuildHTTPRouteRule("/elbproxy-e2e-foo")}, nil)
		stack := deployHTTP(t, s, "path-routing", httpListeners(),
			[]*gwapiv1.HTTPRoute{httpr}, elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)
		addr := stack.GetGatewayAddress()

		// Matched prefix round-trips to the backend.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: "/elbproxy-e2e-foo"},
			framework.HTTPExpectation{StatusCode: 200},
		)

		// Unmatched path has no route and must return 404 from the proxy.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: "/elbproxy-e2e-nope"},
			framework.HTTPExpectation{StatusCode: 404},
		)
	},
}
