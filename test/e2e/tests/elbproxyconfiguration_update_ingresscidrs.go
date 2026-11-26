// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationUpdateIngressCidrs)
}

// ELBProxyConfigurationUpdateIngressCidrs provisions a live proxy with an open IPv4
// ingress, then mutates spec.ingressCidrs in place and verifies the change is
// reconciled: the proxy stays Accepted and IPv4 traffic keeps round-tripping
// with 200.
var ELBProxyConfigurationUpdateIngressCidrs = framework.TestCase{
	ShortName:   "ELBProxyConfigurationUpdateIngressCidrs",
	Description: "Mutate ELBProxyConfiguration spec.ingressCidrs on a live proxy and verify it stays Accepted and serving (non-destructive frontend-SG update)",
	Test: func(t *testing.T, s *framework.Suite) {
		verifier, awsOK := s.NewAWSVerifier(t)

		// Start fully open so the proxy comes up reachable from anywhere.
		stack := deployHTTP(t, s, "update-ingresscidrs", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{IngressCidrs: []string{"0.0.0.0/0", "10.0.0.0/8"}})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])
		proxyNN := client.ObjectKeyFromObject(stack.Proxies[0])

		stack.WaitUntilReady(t, s)
		addr := stack.GetGatewayAddress()

		// Baseline: traffic flows before the update.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{StatusCode: 200},
		)

		// Baseline AWS check: the frontend SG reflects the initial open CIDRs.
		if awsOK {
			verifier.WaitForFrontendSGIngressCidrs(t, gwNN.Namespace, gwNN.Name,
				[]string{"0.0.0.0/0", "10.0.0.0/8"}, s.Timeouts.Accepted)
		}

		// Mutate spec.ingressCidrs in place. Keep 0.0.0.0/0 so the runner stays
		// allowed; narrow the second CIDR to force a real frontend-SG reconcile.
		updateProxyIngressCidrs(t, s, proxyNN, []string{"0.0.0.0/0", "172.16.0.0/12"})

		// The update must be non-destructive: proxy stays Accepted and serving
		// over IPv4.
		s.WaitForELBProxyConfigurationAccepted(t, proxyNN)
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{StatusCode: 200},
		)

		// AWS-side: the frontend SG ingress now reflects exactly the new CIDRs
		// (proves the controller reprogrammed the SG, not just held K8s status).
		if awsOK {
			verifier.WaitForFrontendSGIngressCidrs(t, gwNN.Namespace, gwNN.Name,
				[]string{"0.0.0.0/0", "172.16.0.0/12"}, s.Timeouts.Accepted)
		}
	},
}

// updateProxyIngressCidrs fetches the live ELBProxyConfiguration and writes desiredCidrs
// to spec.ingressCidrs.
func updateProxyIngressCidrs(t *testing.T, s *framework.Suite, proxyNN types.NamespacedName, desiredCidrs []string) {
	t.Helper()
	ctx := t.Context()

	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	if err := s.Client.Get(ctx, proxyNN, live); err != nil {
		t.Fatalf("get ELBProxyConfiguration %s before ingressCidrs update: %v", proxyNN, err)
	}
	live.Spec.IngressCidrs = desiredCidrs
	if err := s.Client.Update(ctx, live); err != nil {
		t.Fatalf("update ELBProxyConfiguration %s ingressCidrs: %v", proxyNN, err)
	}
	framework.Logf(t, "updated ELBProxyConfiguration %s spec.ingressCidrs to %v", proxyNN, desiredCidrs)
}
