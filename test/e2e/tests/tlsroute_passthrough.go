// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, TLSRoutePassthrough)
}

// TLSRoutePassthrough provisions a Gateway with a TLS passthrough listener, a
// TLSRoute, and an ELBProxyConfiguration. The backend terminates TLS itself (using a
// self-signed cert mounted as a Secret). The test verifies a TLS handshake
// succeeds through the proxy to the backend.
var TLSRoutePassthrough = framework.TestCase{
	ShortName:   "TLSRoutePassthrough",
	Description: "Provision a Gateway+TLSRoute (passthrough)+ELBProxyConfiguration and verify TLS connectivity through the proxy",
	Test: func(t *testing.T, s *framework.Suite) {
		gwListeners := []gwapiv1.Listener{{
			Name:     "tls-passthrough",
			Protocol: gwapiv1.TLSProtocolType,
			Port:     443,
			TLS:      &gwapiv1.ListenerTLSConfig{Mode: ptr.To(gwapiv1.TLSModePassthrough)},
		}}
		tlsr := resources.BuildTLSRoute([]string{resources.TestHostname},
			resources.BuildBackendRef(resources.TLSBackendName, resources.TLSBackendPort), &gwListeners[0].Name)
		stack := deployTLSPassthrough(t, s, "tlsroute", gwListeners,
			[]*gwapiv1.TLSRoute{tlsr}, elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)

		t.Run("TLS handshake succeeds through the proxy (passthrough)", func(t *testing.T) {
			s.MakeTLSConnect(t, stack.GetGatewayAddress()+":443", resources.TestHostname)
		})
	},
}
