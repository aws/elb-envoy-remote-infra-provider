// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationDynamicConfig)
}

// The listener and route ELBProxyConfigurationDynamicConfig adds to the live
// proxy. :443 is reachable from outside the VPC, so the probe works from a
// laptop or CI runner.
const (
	dynamicListenerName = "http-443"
	dynamicHostname     = "api." + resources.TestHostname
	dynamicPath         = resources.DefaultPathPrefix + "-api"
)

// ELBProxyConfigurationDynamicConfig stands up a live proxy with one listener, then adds
// a second listener
// (:443) and a route bound to it at runtime, and verifies the new route serves
// without recreating the proxy — a pure dynamic xDS update pushed to the
// running fleet.
var ELBProxyConfigurationDynamicConfig = framework.TestCase{
	ShortName:   "ELBProxyConfigurationDynamicConfig",
	Description: "Add a listener and route to a live proxy and verify the new config is served via dynamic xDS without recreating the proxy",
	Test: func(t *testing.T, s *framework.Suite) {
		verifier, awsOK := s.NewAWSVerifier(t)

		stack := deployHTTP(t, s, "dynamic", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])

		var addr, arnBefore string

		t.Run("Provision proxy with a single listener", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
			addr = stack.GetGatewayAddress()

			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)

			// Capture the proxy ARN to later prove the update reconfigured the
			// same proxy rather than recreating it.
			if awsOK {
				arnBefore = verifier.ProxyARN(t, gwNN.Namespace, gwNN.Name)
			}
		})

		t.Run("Add a second listener and route to the live proxy", func(t *testing.T) {
			addGatewayListener(t, s, gwNN, gwapiv1.Listener{
				Name:     dynamicListenerName,
				Protocol: gwapiv1.HTTPProtocolType,
				Port:     443,
			})

			route := resources.BuildHTTPRoute([]string{dynamicHostname},
				[]gwapiv1.HTTPRouteRule{resources.BuildHTTPRouteRule(dynamicPath)},
				ptr.To[gwapiv1.SectionName](dynamicListenerName))
			stack.CreateHTTPRoute(t, s, route)
			s.WaitForHTTPRouteAccepted(t, client.ObjectKeyFromObject(route), gwNN)
		})

		t.Run("New listener serves via dynamic xDS; original still works", func(t *testing.T) {
			// New listener (:443) serves the new route.
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{
					Host:   addr + ":443",
					Path:   dynamicPath,
					Header: map[string]string{"Host": dynamicHostname},
				},
				framework.HTTPExpectation{StatusCode: 200},
			)
			// Original listener (:80) keeps serving — update was additive.
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)

			// The listener add must reconfigure the SAME proxy via dynamic xDS,
			// not recreate it: the ARN is unchanged.
			if awsOK {
				arnAfter := verifier.ProxyARN(t, gwNN.Namespace, gwNN.Name)
				if arnAfter != arnBefore {
					t.Fatalf("proxy was recreated on listener add: ARN before=%q after=%q", arnBefore, arnAfter)
				}
				framework.Logf(t, "proxy ARN unchanged across listener add (%s) — reconfigured in place", arnAfter)
			}
		})
	},
}

// addGatewayListener appends a listener to the live Gateway, preserving existing
// listeners (additive update).
func addGatewayListener(t *testing.T, s *framework.Suite, gwNN types.NamespacedName, l gwapiv1.Listener) {
	t.Helper()
	ctx := t.Context()

	gw := &gwapiv1.Gateway{}
	if err := s.Client.Get(ctx, gwNN, gw); err != nil {
		t.Fatalf("get Gateway %s before listener add: %v", gwNN, err)
	}
	gw.Spec.Listeners = append(gw.Spec.Listeners, l)
	if err := s.Client.Update(ctx, gw); err != nil {
		t.Fatalf("add listener %q to Gateway %s: %v", l.Name, gwNN, err)
	}
	framework.Logf(t, "added listener %q (:%d) to Gateway %s", l.Name, l.Port, gwNN)
}
