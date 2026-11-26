// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"net"
	"strconv"
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
	E2ETests = append(E2ETests, ELBProxyConfigurationMergedLateMember)
}

// Listener ports of the two members: both reachable from outside the VPC.
const (
	mergedLateMemberGW1Port = 80
	mergedLateMemberGW2Port = 443
)

// ELBProxyConfigurationMergedLateMember adds a Gateway to a merged GatewayClass whose
// proxy is already serving, and checks the proxy reaches the new member's backend.
var ELBProxyConfigurationMergedLateMember = framework.TestCase{
	ShortName:   "ELBProxyConfigurationMergedLateMember",
	Description: "Add a Gateway to a merged GatewayClass whose proxy is already serving and verify the shared proxy reaches the new member's backend",
	Test: func(t *testing.T, s *framework.Suite) {
		envoyProxy := resources.BuildEnvoyProxySpec(s, true)
		gwc := resources.BuildGatewayClassSpec(s, envoyProxy)
		gw1 := resources.BuildGatewaySpec(gwc, []gwapiv1.Listener{{Name: "http", Protocol: gwapiv1.HTTPProtocolType, Port: mergedLateMemberGW1Port}})

		stack := &resources.ResourceStack{
			BaseName:   "merged-late-member",
			EnvoyProxy: envoyProxy,
			Gwc:        gwc,
			Gws:        []*gwapiv1.Gateway{gw1},
			// The new member's backend port stays closed until its route exists.
			Dps: []*appsv1.Deployment{
				resources.BuildEchoDeploymentSpec(),
				resources.BuildEchoDeploymentOnPort(resources.MergedLateMemberEchoName, resources.MergedLateMemberEchoPort),
			},
			Svcs: []*corev1.Service{
				resources.BuildEchoServiceSpec(),
				resources.BuildEchoServiceOnPort(resources.MergedLateMemberEchoName, resources.MergedLateMemberEchoPort),
			},
			Httprs: []*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			// One GatewayClass-scoped proxy backs the whole merged fleet.
			Proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				resources.BuildGatewayClassELBProxyConfiguration(s, gwc, elbproxyv1alpha1.ELBProxyConfigurationSpec{}),
			},
		}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)
		addr := stack.GetGatewayAddress()

		t.Run("The first member is serving", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})

		gw2 := resources.BuildGatewaySpec(gwc, []gwapiv1.Listener{{Name: "http", Protocol: gwapiv1.HTTPProtocolType, Port: mergedLateMemberGW2Port}})
		gw2.Name = resources.DefaultGatewayName + "-2"
		route2 := resources.BuildHTTPRoute(nil, []gwapiv1.HTTPRouteRule{
			resources.BuildHTTPRouteRuleTo("/", resources.MergedLateMemberEchoName, resources.EchoServicePort),
		}, nil)
		route2.Spec.ParentRefs[0].Name = gwapiv1.ObjectName(gw2.Name)

		t.Run("A Gateway added to the live class shares the proxy", func(t *testing.T) {
			stack.CreateGateway(t, s, gw2)
			stack.CreateHTTPRoute(t, s, route2)
			addr2 := s.WaitForGatewayAccepted(t, client.ObjectKeyFromObject(gw2))
			if addr2 != addr {
				t.Fatalf("the new member should share the merged proxy's address %q, got %q", addr, addr2)
			}
			s.WaitForHTTPRouteAccepted(t, client.ObjectKeyFromObject(route2), client.ObjectKeyFromObject(gw2))
		})

		t.Run("The proxy reaches the new member's backend", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: net.JoinHostPort(addr, strconv.Itoa(mergedLateMemberGW2Port)), Path: "/"},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})

		t.Run("The first member still serves", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})
	},
}
