// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationInternal)
}

// ELBProxyConfigurationInternal provisions a
// proxy with ipv4Addressability=internal (VPC-only) and verify the proxy
// reports internal addressability and still becomes Programmed.
var ELBProxyConfigurationInternal = framework.TestCase{
	ShortName:   "ELBProxyConfigurationInternal",
	Description: "Provision a VPC-only (internal) ELBProxyConfiguration and verify it reports internal addressability and becomes Programmed",
	Test: func(t *testing.T, s *framework.Suite) {
		verifier, awsOK := s.NewAWSVerifier(t)

		// On an IPv6 cluster the Envoy Gateway xDS pods only have IPv6 addresses
		// (EKS pods are single-stack IPv6), and those go into the proxy's
		// xds-endpoints.json. A proxy with IPv6 off cannot reach them, so it never
		// connects to xDS. Keep the proxy VPC-only on both families in that case.
		ipv6Addressability := elbproxyaws.IpAddressabilityOff
		if s.IPFamily == framework.IPFamilyIPv6 {
			ipv6Addressability = elbproxyaws.IpAddressabilityInternal
		}

		stack := deployHTTP(t, s, "internal", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{
				NetworkConfiguration: elbproxyv1alpha1.ProxyNetworkUserConfiguration{
					IpConfiguration: elbproxyv1alpha1.ProxyIpConfiguration{
						IpAddressingConfiguration: elbproxyv1alpha1.ProxyIpAddressingConfiguration{
							Ipv4Addressability: elbproxyaws.IpAddressabilityInternal,
							Ipv6Addressability: ipv6Addressability,
						},
					},
				},
			})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])

		t.Run("Provision an internal (VPC-only) proxy", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
		})

		t.Run("Proxy reports internal addressability", func(t *testing.T) {
			if !awsOK {
				t.Skip("AWS verification not configured; set -cluster-name to assert addressability")
			}
			verifier.AssertProxyIPv4Addressability(t, gwNN.Namespace, gwNN.Name, elbproxyaws.IpAddressabilityInternal)
			verifier.AssertProxyIPv6Addressability(t, gwNN.Namespace, gwNN.Name, ipv6Addressability)
		})
	},
}
