// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, HTTPRouteLiveBackendChanges)
}

// liveBackendPath is the path of the route the test changes.
const liveBackendPath = "/live"

// HTTPRouteLiveBackendChanges changes the backends of a serving proxy: add a route to
// backend A, point it at B, retarget B's Service to C, then delete the route. After each
// step it checks that the new port is opened and the unused one closed.
var HTTPRouteLiveBackendChanges = framework.TestCase{
	ShortName:   "HTTPRouteLiveBackendChanges",
	Description: "Add a route, change its backend and its Service's targetPort, then delete it on a live proxy, and verify the backend ports are opened and removed",
	Test: func(t *testing.T, s *framework.Suite) {
		verifier, awsOK := s.NewAWSVerifier(t)
		expectPort := func(t *testing.T, port int32, allowed bool) {
			t.Helper()
			if awsOK {
				verifier.WaitForNodeSGPort(t, "tcp", port, allowed, s.Timeouts.Accepted)
			}
		}

		stack := newStack(s, "live-backend-changes", httpListeners(), elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.Dps = []*appsv1.Deployment{
			resources.BuildEchoDeploymentSpec(),
			resources.BuildEchoDeploymentOnPort(resources.LiveBackendAName, resources.LiveBackendAPort),
			resources.BuildEchoDeploymentOnPort(resources.LiveBackendBName, resources.LiveBackendBPort),
			resources.BuildEchoDeploymentOnPort(resources.LiveBackendCName, resources.LiveBackendCPort),
		}
		stack.Svcs = []*corev1.Service{
			resources.BuildEchoServiceSpec(),
			resources.BuildEchoServiceOnPort(resources.LiveBackendAName, resources.LiveBackendAPort),
			resources.BuildEchoServiceOnPort(resources.LiveBackendBName, resources.LiveBackendBPort),
		}
		stack.Httprs = []*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)
		addr := stack.GetGatewayAddress()
		liveRequest := framework.HTTPRequest{Host: addr, Path: liveBackendPath}

		t.Run("The proxy is serving", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
			for _, port := range []int32{resources.LiveBackendAPort, resources.LiveBackendBPort, resources.LiveBackendCPort} {
				expectPort(t, port, false)
			}
		})

		route := resources.BuildHTTPRoute(nil, []gwapiv1.HTTPRouteRule{
			resources.BuildHTTPRouteRuleTo(liveBackendPath, resources.LiveBackendAName, resources.EchoServicePort),
		}, nil)
		routeNN := func() client.ObjectKey { return client.ObjectKeyFromObject(route) }

		t.Run("Add a route to backend A", func(t *testing.T) {
			stack.CreateHTTPRoute(t, s, route)
			s.WaitForHTTPRouteAccepted(t, routeNN(), client.ObjectKeyFromObject(stack.Gws[0]))
			s.MakeRequestAndExpectEventuallyConsistentResponse(t, liveRequest, framework.HTTPExpectation{StatusCode: 200})
			expectPort(t, resources.LiveBackendAPort, true)
		})

		t.Run("Point the route at backend B", func(t *testing.T) {
			live := &gwapiv1.HTTPRoute{}
			if err := s.Client.Get(t.Context(), routeNN(), live); err != nil {
				t.Fatalf("get HTTPRoute %s: %v", routeNN(), err)
			}
			live.Spec.Rules[0].BackendRefs[0].Name = gwapiv1.ObjectName(resources.LiveBackendBName)
			if err := s.Client.Update(t.Context(), live); err != nil {
				t.Fatalf("update HTTPRoute %s: %v", routeNN(), err)
			}
			framework.Logf(t, "pointed HTTPRoute %s at %s", routeNN(), resources.LiveBackendBName)
			expectPort(t, resources.LiveBackendBPort, true)
			// Wait for A's port to close, or the 200 could still come from A.
			expectPort(t, resources.LiveBackendAPort, false)
			s.MakeRequestAndExpectEventuallyConsistentResponse(t, liveRequest, framework.HTTPExpectation{StatusCode: 200})
		})

		t.Run("Change backend B's Service to target backend C", func(t *testing.T) {
			svcNN := client.ObjectKey{Namespace: stack.GetNamespace(), Name: resources.LiveBackendBName}
			svc := &corev1.Service{}
			if err := s.Client.Get(t.Context(), svcNN, svc); err != nil {
				t.Fatalf("get Service %s: %v", svcNN, err)
			}
			svc.Spec.Selector = map[string]string{"app": resources.LiveBackendCName}
			svc.Spec.Ports[0].TargetPort = intstr.FromInt32(resources.LiveBackendCPort)
			if err := s.Client.Update(t.Context(), svc); err != nil {
				t.Fatalf("update Service %s: %v", svcNN, err)
			}
			framework.Logf(t, "Service %s now targets %s on %d", svcNN, resources.LiveBackendCName, resources.LiveBackendCPort)
			expectPort(t, resources.LiveBackendCPort, true)
			// Wait for B's port to close, or the 200 could still come from B.
			expectPort(t, resources.LiveBackendBPort, false)
			s.MakeRequestAndExpectEventuallyConsistentResponse(t, liveRequest, framework.HTTPExpectation{StatusCode: 200})
		})

		t.Run("Delete the route", func(t *testing.T) {
			if err := s.DeleteObjectAndWaitGone(t, route); err != nil {
				t.Fatalf("delete HTTPRoute %s: %v", routeNN(), err)
			}
			s.MakeRequestAndExpectEventuallyConsistentResponse(t, liveRequest, framework.HTTPExpectation{StatusCode: 404})
			expectPort(t, resources.LiveBackendCPort, false)
			// The first route still works.
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})
	},
}
