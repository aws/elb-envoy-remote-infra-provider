// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationLifecycle)
}

// ELBProxyConfigurationLifecycle follows the create -> serve -> delete -> teardown shape of the upstream
// RemoteInfraTCPRoute test. It drives the full ELBProxyConfiguration lifecycle through
// the remote infrastructure provider, using t.Run subtests for each phase.
var ELBProxyConfigurationLifecycle = framework.TestCase{
	ShortName:   "ELBProxyConfigurationLifecycle",
	Description: "Drive the full ELBProxyConfiguration lifecycle (create, serve traffic, delete) and verify the remote data plane is provisioned and torn down",
	Test: func(t *testing.T, s *framework.Suite) {
		stack := deployHTTP(t, s, "lifecycle", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])
		proxyNN := client.ObjectKeyFromObject(stack.Proxies[0])

		var addr string

		t.Run("Provision the remote data plane", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
			addr = stack.GetGatewayAddress()
		})

		t.Run("Remotely managed data plane serves traffic", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})

		t.Run("Delete the Gateway and verify the remote data plane is cleaned up", func(t *testing.T) {
			// Optional AWS verifier: capture before delete so we can confirm
			// teardown reached AWS, not just Kubernetes.
			verifier, awsOK := s.NewAWSVerifier(t)

			// The controller provisions the proxy per Gateway and only tears
			// it down when the Gateway no longer exists
			deleteGatewayAndWaitGone(t, s, gwNN)

			// Delete the ELBProxyConfiguration afterward
			deleteELBProxyConfigurationAndWaitGone(t, s, proxyNN)

			if awsOK {
				verifier.WaitForProxyGone(t, gwNN.Namespace, gwNN.Name, s.Timeouts.Delete)
			}
		})
	},
}

// deleteELBProxyConfigurationAndWaitGone deletes the ELBProxyConfiguration and waits for it
// to be gone. A stuck delete usually means the finalizer was not released
// (e.g. the Gateway is still referencing the proxy), so the failure says so.
func deleteELBProxyConfigurationAndWaitGone(t *testing.T, s *framework.Suite, proxyNN types.NamespacedName) {
	t.Helper()

	proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: proxyNN.Name, Namespace: proxyNN.Namespace},
	}
	if err := s.DeleteObjectAndWaitGone(t, proxy); err != nil {
		t.Fatalf("ELBProxyConfiguration %s still present after %s; the controller did not release %s "+
			"(is the Gateway still referencing it?): %v",
			proxyNN, s.Timeouts.Delete, "gateway.elbproxy.aws/finalizer", err)
	}
}

// deleteGatewayAndWaitGone deletes the Gateway and waits for it to be fully
// removed. The proxy teardown only starts once the Gateway is gone, so the
// caller can safely check the proxy afterward.
func deleteGatewayAndWaitGone(t *testing.T, s *framework.Suite, gwNN types.NamespacedName) {
	t.Helper()

	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: gwNN.Name, Namespace: gwNN.Namespace},
	}
	if err := s.DeleteObjectAndWaitGone(t, gw); err != nil {
		t.Fatalf("Gateway %s still present after %s: %v", gwNN, s.Timeouts.Delete, err)
	}
}
