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
	E2ETests = append(E2ETests, HTTPRouteBasic)
}

// HTTPRouteBasic stands up a backend, Gateway, HTTPRoute, and
// ELBProxyConfiguration, then drives HTTP traffic through the proxy and expects 200.
var HTTPRouteBasic = framework.TestCase{
	ShortName:   "HTTPRouteBasic",
	Description: "Provision a Gateway+HTTPRoute+ELBProxyConfiguration and verify HTTP traffic round-trips through the ELBProxy-managed proxy",
	Test: func(t *testing.T, s *framework.Suite) {
		stack := deployHTTP(t, s, "httproute-basic", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)

		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: stack.GetGatewayAddress(), Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{StatusCode: 200},
		)
	},
}
