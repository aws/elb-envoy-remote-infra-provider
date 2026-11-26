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
	E2ETests = append(E2ETests, GRPCRouteBasic)
}

// GRPCRouteBasic provisions a Gateway with an HTTP/2 listener, a GRPCRoute, and
// an ELBProxyConfiguration, then verifies gRPC connectivity through the proxy using a
// grpc-health-check probe against the backend's gRPC health service.
var GRPCRouteBasic = framework.TestCase{
	ShortName:   "GRPCRouteBasic",
	Description: "Provision a Gateway+GRPCRoute+ELBProxyConfiguration and verify gRPC connectivity through the ELBProxy-managed proxy",
	Test: func(t *testing.T, s *framework.Suite) {
		gwListeners := []gwapiv1.Listener{{Name: "grpc", Protocol: gwapiv1.HTTPProtocolType, Port: 80}}
		stack := deployGRPC(t, s, "grpcroute", gwListeners,
			[]*gwapiv1.GRPCRoute{resources.BuildGRPCRoute(&gwListeners[0].Name)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)

		t.Run("gRPC request succeeds through the proxy", func(t *testing.T) {
			s.MakeGRPCHealthCheck(t, stack.GetGatewayAddress()+":80")
		})
	},
}
