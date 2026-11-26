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
	E2ETests = append(E2ETests, UDPRouteBasic)
}

// UDPRouteBasic provisions a Gateway with a UDP listener, a UDPRoute, and a
// ELBProxyConfiguration, then verifies UDP connectivity through the proxy by sending a
// datagram and receiving an echo response.
var UDPRouteBasic = framework.TestCase{
	ShortName:   "UDPRouteBasic",
	Description: "Provision a Gateway+UDPRoute+ELBProxyConfiguration and verify UDP connectivity through the ELBProxy-managed proxy",
	Test: func(t *testing.T, s *framework.Suite) {
		gwListeners := []gwapiv1.Listener{{Name: "udp", Protocol: gwapiv1.UDPProtocolType, Port: 80}}
		stack := deployUDP(t, s, "udproute", gwListeners,
			[]*gwapiv1.UDPRoute{resources.BuildUDPRoute(&gwListeners[0].Name)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)

		t.Run("UDP echo succeeds through the proxy", func(t *testing.T) {
			s.MakeUDPEcho(t, stack.GetGatewayAddress()+":80")
		})
	},
}
