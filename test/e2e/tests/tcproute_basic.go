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
	E2ETests = append(E2ETests, TCPRouteBasic)
}

// TCPRouteBasic provisions a Gateway with a TCP listener, a TCPRoute, and a
// ELBProxyConfiguration, then verifies raw TCP connectivity through the proxy.
var TCPRouteBasic = framework.TestCase{
	ShortName:   "TCPRouteBasic",
	Description: "Provision a Gateway+TCPRoute+ELBProxyConfiguration and verify raw TCP connectivity through the ELBProxy-managed proxy",
	Test: func(t *testing.T, s *framework.Suite) {
		gwListeners := []gwapiv1.Listener{{Name: "tcp", Protocol: gwapiv1.TCPProtocolType, Port: 80}}
		stack := deployTCP(t, s, "tcproute", gwListeners,
			[]*gwapiv1.TCPRoute{resources.BuildTCPRoute(&gwListeners[0].Name)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)

		t.Run("TCP connection succeeds through the proxy", func(t *testing.T) {
			s.MakeTCPConnect(t, stack.GetGatewayAddress()+":80")
		})
	},
}
