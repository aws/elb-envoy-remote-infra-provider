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
	E2ETests = append(E2ETests, TLSRouteTerminate)
}

// terminateHostname is the SNI TLSRouteTerminate's route matches, under the
// listener's wildcard hostname.
const terminateHostname = "www." + resources.TestHostname

// TLSRouteTerminate provisions a Gateway with a TLS listener in Terminate mode
// (proxy holds the cert and decrypts), a TLSRoute with hostname matching, and a
// ELBProxyConfiguration. The proxy terminates TLS and forwards plain TCP to an HTTP backend.
// Verifies TLS handshake succeeds through the proxy.
var TLSRouteTerminate = framework.TestCase{
	ShortName:   "TLSRouteTerminate",
	Description: "Provision a Gateway+TLSRoute (terminate mode)+ELBProxyConfiguration and verify TLS termination at the proxy with plain HTTP backend",
	Test: func(t *testing.T, s *framework.Suite) {
		gwListeners := []gwapiv1.Listener{{
			Name:     "tls-terminate",
			Protocol: gwapiv1.TLSProtocolType,
			Port:     443,
			Hostname: ptr.To(gwapiv1.Hostname("*." + resources.TestHostname)),
			TLS: &gwapiv1.ListenerTLSConfig{
				Mode:            ptr.To(gwapiv1.TLSModeTerminate),
				CertificateRefs: []gwapiv1.SecretObjectReference{{Name: resources.TLSCertSecretName}},
			},
		}}
		tlsr := resources.BuildTLSRoute([]string{terminateHostname},
			resources.BuildBackendRef(resources.EchoName, resources.EchoServicePort), &gwListeners[0].Name)
		stack := deployTLSTerminate(t, s, "tls-terminate", gwListeners,
			[]*gwapiv1.TLSRoute{tlsr}, elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.WaitUntilReady(t, s)

		t.Run("TLS handshake succeeds with proxy-terminated TLS", func(t *testing.T) {
			s.MakeTLSConnect(t, stack.GetGatewayAddress()+":443", terminateHostname)
		})
	},
}
