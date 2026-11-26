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
	E2ETests = append(E2ETests, HTTPRouteHostMatch)
}

// hostMatchHostname is the only hostname HTTPRouteHostMatch's route accepts.
const hostMatchHostname = resources.TestHostname

// HTTPRouteHostMatch provisions a Gateway+HTTPRoute+ELBProxyConfiguration whose route is
// scoped to a single hostname, then verifies that a request carrying the
// matching Host header returns 200 while a non-matching Host returns 404
var HTTPRouteHostMatch = framework.TestCase{
	ShortName:   "HTTPRouteHostMatch",
	Description: "Verify HTTPRoute hostname matching: matching Host returns 200, non-matching Host returns 404",
	Test: func(t *testing.T, s *framework.Suite) {
		stack := deployHTTP(t, s, "host-match", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute([]string{hostMatchHostname}, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)
		addr := stack.GetGatewayAddress()

		// Matching Host header routes to the backend.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{
				Host:   addr,
				Path:   resources.DefaultPathPrefix,
				Header: map[string]string{"Host": hostMatchHostname},
			},
			framework.HTTPExpectation{StatusCode: 200},
		)

		// Non-matching Host has no route and must return 404 from the proxy.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{
				Host:   addr,
				Path:   resources.DefaultPathPrefix,
				Header: map[string]string{"Host": "not-" + hostMatchHostname},
			},
			framework.HTTPExpectation{StatusCode: 404},
		)
	},
}
