// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationCustomSubnets)
}

// ELBProxyConfigurationCustomSubnets pins the
// proxy to explicit subnet IDs (overriding auto-discovery) and verify it
// provisions on exactly those subnets and serves traffic.
//
// Requires -subnet-ids (the subnets to pin); skips otherwise since there's
// nothing to assert without them. The subnet assertion against the ELBProxy API
// additionally requires -cluster-name.
var ELBProxyConfigurationCustomSubnets = framework.TestCase{
	ShortName:   "ELBProxyConfigurationCustomSubnets",
	Description: "Pin an ELBProxyConfiguration to explicit subnets and verify it provisions on those subnets and serves traffic",
	Test: func(t *testing.T, s *framework.Suite) {
		if len(s.SubnetIDs) == 0 {
			t.Skip("ELBProxyConfigurationCustomSubnets requires explicit subnets; set -subnet-ids or E2E_SUBNET_IDS")
		}

		verifier, awsOK := s.NewAWSVerifier(t)

		// The builder fills spec subnets from -subnet-ids: explicit subnets, no
		// auto-discovery.
		stack := deployHTTP(t, s, "custom-subnets", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])

		var addr string

		t.Run("Provision proxy pinned to explicit subnets", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
			addr = stack.GetGatewayAddress()
		})

		t.Run("Proxy is deployed on exactly the pinned subnets", func(t *testing.T) {
			if !awsOK {
				t.Skip("AWS verification not configured; set -cluster-name to assert subnets")
			}
			verifier.AssertProxySubnets(t, gwNN.Namespace, gwNN.Name, s.SubnetIDs)
		})

		t.Run("Proxy serves traffic", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})
	},
}
