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
	E2ETests = append(E2ETests, ListenerSetHTTPRoute)
}

// listenerSetPort is the ListenerSet's listener port. The Gateway's own listener (80)
// has no routes, so this port is served only through the ListenerSet.
const listenerSetPort = 443

// ListenerSetHTTPRoute attaches an HTTPRoute and an ext_authz SecurityPolicy to a
// ListenerSet, and checks the controller opens both backends' ports for the
// ListenerSet's Gateway. /401 must come from the auth service and /200 from the route
// backend; an unreachable auth service gives 403, an unreachable backend 503.
var ListenerSetHTTPRoute = framework.TestCase{
	ShortName:   "ListenerSetHTTPRoute",
	Description: "Attach an HTTPRoute and an ext_authz SecurityPolicy to a ListenerSet and verify the proxy reaches both backends through the ListenerSet's listener: a denied path returns the auth service's 401, an allowed path returns 200",
	Test: func(t *testing.T, s *framework.Suite) {
		stack := newStack(s, "listenerset-httproute", httpListeners(), elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		// A Gateway accepts no ListenerSets unless allowedListeners allows them.
		stack.Gws[0].Spec.AllowedListeners = &gwapiv1.AllowedListeners{
			Namespaces: &gwapiv1.ListenerNamespaces{From: ptr.To(gwapiv1.NamespacesFromSame)},
		}
		ls := resources.BuildListenerSet(listenerSetPort)
		route := resources.BuildHTTPRouteOnListenerSet(ls.Name, []gwapiv1.HTTPRouteRule{
			resources.BuildHTTPRouteRuleTo("/", resources.ListenerSetEchoName, resources.EchoServicePort),
		})

		stack.ListenerSets = []*gwapiv1.ListenerSet{ls}
		stack.Dps = []*appsv1.Deployment{
			resources.BuildEchoDeploymentOnPort(resources.ListenerSetEchoName, resources.ListenerSetEchoPort),
			resources.BuildExtAuthDeploymentOnPort(resources.ListenerSetExtAuthName, resources.ListenerSetExtAuthPort),
		}
		stack.Svcs = []*corev1.Service{
			resources.BuildEchoServiceOnPort(resources.ListenerSetEchoName, resources.ListenerSetEchoPort),
			resources.BuildExtAuthServiceOnPort(resources.ListenerSetExtAuthName, resources.ListenerSetExtAuthPort),
		}
		stack.Httprs = []*gwapiv1.HTTPRoute{route}
		stack.Policies = []*unstructured.Unstructured{resources.BuildExtAuthSecurityPolicyForListenerSet(
			ls.Name, resources.ListenerSetExtAuthName, resources.ListenerSetExtAuthPort)}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)
		addr := net.JoinHostPort(stack.GetGatewayAddress(), strconv.Itoa(listenerSetPort))

		// Denied by the auth service, which Envoy relays to the client.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: "/401"},
			framework.HTTPExpectation{StatusCode: 401},
		)
		// Allowed by the auth service and served by the route backend.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: "/200"},
			framework.HTTPExpectation{StatusCode: 200},
		)
	},
}
