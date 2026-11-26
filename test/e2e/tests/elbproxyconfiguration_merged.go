// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationMerged)
}

// Hostnames of the two merged Gateways' routes.
const (
	mergedHostname1 = "www.merged-1.example.com"
	mergedHostname2 = "www.merged-2.example.com"
)

// ELBProxyConfigurationMerged verifies merged gateway mode. With mergeGateways=true on
// the GatewayClass's EnvoyProxy, multiple member Gateways collapse into a single
// shared proxy fleet keyed by the GatewayClass (not per-Gateway). A single
// GatewayClass-scoped ELBProxyConfiguration backs the whole fleet.
//
// The test verifies:
//   - both member Gateways become Accepted+Programmed with the SAME address
//     (the shared merged fleet), and
//   - each member's listener/host routes correctly through that one address
//     (gw-1 on :80 / www.merged-1, gw-2 on :443 / www.merged-2).
//
// gw-2's backend has its own port, so its 200 also proves gw-2 was registered.
var ELBProxyConfigurationMerged = framework.TestCase{
	ShortName:   "ELBProxyConfigurationMerged",
	Description: "Merged gateways (mergeGateways=true) share one ELBProxy fleet and address; each member's listener routes correctly",
	Test: func(t *testing.T, s *framework.Suite) {
		envoyProxy := resources.BuildEnvoyProxySpec(s, true)
		gwc := resources.BuildGatewayClassSpec(s, envoyProxy)

		// Two member Gateways on different ports (both reachable from outside
		// the VPC, so the probe works from a laptop or CI runner).
		gw1 := resources.BuildGatewaySpec(gwc, []gwapiv1.Listener{{Name: "http-80", Protocol: gwapiv1.HTTPProtocolType, Port: 80}})
		gw2 := resources.BuildGatewaySpec(gwc, []gwapiv1.Listener{{Name: "http-443", Protocol: gwapiv1.HTTPProtocolType, Port: 443}})
		gw2.Name = resources.DefaultGatewayName + "-2"

		route1 := resources.BuildHTTPRoute([]string{mergedHostname1}, nil, nil)
		route2 := resources.BuildHTTPRoute([]string{mergedHostname2}, []gwapiv1.HTTPRouteRule{
			resources.BuildHTTPRouteRuleTo(resources.DefaultPathPrefix, resources.MergedSecondEchoName, resources.EchoServicePort),
		}, nil)
		route2.Spec.ParentRefs[0].Name = gwapiv1.ObjectName(gw2.Name)

		stack := &resources.ResourceStack{
			BaseName:   "merged",
			EnvoyProxy: envoyProxy,
			Gwc:        gwc,
			Gws:        []*gwapiv1.Gateway{gw1, gw2},
			Dps: []*appsv1.Deployment{
				resources.BuildEchoDeploymentSpec(),
				resources.BuildEchoDeploymentOnPort(resources.MergedSecondEchoName, resources.MergedSecondEchoPort),
			},
			Svcs: []*corev1.Service{
				resources.BuildEchoServiceSpec(),
				resources.BuildEchoServiceOnPort(resources.MergedSecondEchoName, resources.MergedSecondEchoPort),
			},
			Httprs: []*gwapiv1.HTTPRoute{route1, route2},
			// One GatewayClass-scoped proxy backs the whole merged fleet.
			Proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				resources.BuildGatewayClassELBProxyConfiguration(s, gwc, elbproxyv1alpha1.ELBProxyConfigurationSpec{}),
			},
		}
		stack.Deploy(t, s)

		var addr1, addr2 string

		t.Run("GatewayClass-scoped ELBProxyConfiguration for the merged fleet is Accepted", func(t *testing.T) {
			s.WaitForELBProxyConfigurationAccepted(t, client.ObjectKeyFromObject(stack.Proxies[0]))
		})

		t.Run("Both member Gateways are Programmed with the same shared address", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
			addr1, addr2 = stack.GatewayAddresses[0], stack.GatewayAddresses[1]

			if addr1 != addr2 {
				t.Fatalf("merged gateways should share one address, got gw-1=%q gw-2=%q", addr1, addr2)
			}
			framework.Logf(t, "both merged gateways share address %s", addr1)
		})

		t.Run("Each merged listener routes through the shared fleet", func(t *testing.T) {
			if verifier, ok := s.NewAWSVerifier(t); ok {
				verifier.WaitForNodeSGPort(t, "tcp", resources.MergedSecondEchoPort, true, s.Timeouts.Accepted)
			}
			// gw-1's listener on :80
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{
					Host:   addr1,
					Path:   resources.DefaultPathPrefix,
					Header: map[string]string{"Host": mergedHostname1},
				},
				framework.HTTPExpectation{StatusCode: 200},
			)
			// gw-2's listener on :443
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{
					Host:   addr1 + ":443",
					Path:   resources.DefaultPathPrefix,
					Header: map[string]string{"Host": mergedHostname2},
				},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})
	},
}
