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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ListenerSetMergedGateway)
}

// Each member's own listener port, and the ListenerSet's listener on the second member.
const (
	mergedListenerSetGW1Port = 80
	mergedListenerSetGW2Port = 8080
	mergedListenerSetLSPort  = 443
)

// ListenerSetMergedGateway is ListenerSetHTTPRoute on the second member of a merged
// GatewayClass: the backend ports must be opened on the shared proxy, and the first
// member's route must keep working.
var ListenerSetMergedGateway = framework.TestCase{
	ShortName:   "ListenerSetMergedGateway",
	Description: "Attach an HTTPRoute and an ext_authz SecurityPolicy to a ListenerSet on a member of merged Gateways and verify the shared proxy reaches both backends through the ListenerSet's listener",
	Test: func(t *testing.T, s *framework.Suite) {
		envoyProxy := resources.BuildEnvoyProxySpec(s, true)
		gwc := resources.BuildGatewayClassSpec(s, envoyProxy)
		gw1 := resources.BuildGatewaySpec(gwc, []gwapiv1.Listener{{Name: "http", Protocol: gwapiv1.HTTPProtocolType, Port: mergedListenerSetGW1Port}})
		gw2 := resources.BuildGatewaySpec(gwc, []gwapiv1.Listener{{Name: "http", Protocol: gwapiv1.HTTPProtocolType, Port: mergedListenerSetGW2Port}})
		gw2.Name = resources.DefaultGatewayName + "-2"
		// A Gateway accepts no ListenerSets unless allowedListeners allows them.
		gw2.Spec.AllowedListeners = &gwapiv1.AllowedListeners{
			Namespaces: &gwapiv1.ListenerNamespaces{From: ptr.To(gwapiv1.NamespacesFromSame)},
		}

		ls := resources.BuildListenerSet(mergedListenerSetLSPort)
		ls.Spec.ParentRef.Name = gwapiv1.ObjectName(gw2.Name)
		lsRoute := resources.BuildHTTPRouteOnListenerSet(ls.Name, []gwapiv1.HTTPRouteRule{
			resources.BuildHTTPRouteRuleTo("/", resources.MergedListenerSetEchoName, resources.EchoServicePort),
		})
		gw1Route := resources.BuildHTTPRoute(nil, nil, nil)

		stack := &resources.ResourceStack{
			BaseName:     "listenerset-merged",
			EnvoyProxy:   envoyProxy,
			Gwc:          gwc,
			Gws:          []*gwapiv1.Gateway{gw1, gw2},
			ListenerSets: []*gwapiv1.ListenerSet{ls},
			Dps: []*appsv1.Deployment{
				resources.BuildEchoDeploymentSpec(),
				resources.BuildEchoDeploymentOnPort(resources.MergedListenerSetEchoName, resources.MergedListenerSetEchoPort),
				resources.BuildExtAuthDeploymentOnPort(resources.MergedListenerSetExtAuthName, resources.MergedListenerSetExtAuthPort),
			},
			Svcs: []*corev1.Service{
				resources.BuildEchoServiceSpec(),
				resources.BuildEchoServiceOnPort(resources.MergedListenerSetEchoName, resources.MergedListenerSetEchoPort),
				resources.BuildExtAuthServiceOnPort(resources.MergedListenerSetExtAuthName, resources.MergedListenerSetExtAuthPort),
			},
			Httprs: []*gwapiv1.HTTPRoute{gw1Route, lsRoute},
			Policies: []*unstructured.Unstructured{resources.BuildExtAuthSecurityPolicyForListenerSet(
				ls.Name, resources.MergedListenerSetExtAuthName, resources.MergedListenerSetExtAuthPort)},
			// One GatewayClass-scoped proxy backs the whole merged fleet.
			Proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				resources.BuildGatewayClassELBProxyConfiguration(s, gwc, elbproxyv1alpha1.ELBProxyConfigurationSpec{}),
			},
		}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)
		if stack.GatewayAddresses[0] != stack.GatewayAddresses[1] {
			t.Fatalf("merged gateways should share one address, got %q and %q", stack.GatewayAddresses[0], stack.GatewayAddresses[1])
		}
		addr := stack.GatewayAddresses[0]
		lsAddr := net.JoinHostPort(addr, strconv.Itoa(mergedListenerSetLSPort))

		// The first member's own route still works on the shared proxy.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{StatusCode: 200},
		)
		// Through the ListenerSet: denied by the auth service.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: lsAddr, Path: "/401"},
			framework.HTTPExpectation{StatusCode: 401},
		)
		// Through the ListenerSet: allowed and served by the route backend.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: lsAddr, Path: "/200"},
			framework.HTTPExpectation{StatusCode: 200},
		)
	},
}
