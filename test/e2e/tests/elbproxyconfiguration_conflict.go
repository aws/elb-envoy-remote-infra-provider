// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationConflict)
}

// ELBProxyConfigurationConflict exercises the same-tier tiebreak: two Gateway-scoped
// ELBProxyConfigurations target the
// same Gateway. Exactly one wins (Accepted=True) and provisions the data plane;
// the other reports Accepted=False with reason Conflicted.
var ELBProxyConfigurationConflict = framework.TestCase{
	ShortName:   "ELBProxyConfigurationConflict",
	Description: "Two Gateway-scoped ELBProxyConfigurations on one Gateway: one wins and serves, the other reports Accepted=False/Conflicted",
	Test: func(t *testing.T, s *framework.Suite) {
		// The stack's own proxy is the winner.
		stack := deployHTTP(t, s, "conflict", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])
		routeNN := client.ObjectKeyFromObject(stack.Httprs[0])
		winnerNN := client.ObjectKeyFromObject(stack.Proxies[0])

		// The loser is created after the winner, so it loses the
		// (creationTimestamp, namespace, name) tiebreak. If both land in the same
		// second, its name, which extends the winner's, still sorts after it.
		loser := resources.BuildELBProxyConfiguration(s, stack.Gws[0], elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		loser.Name = resources.DefaultProxyName + "-b"

		var addr string

		t.Run("Create a second Gateway-scoped proxy targeting the same Gateway", func(t *testing.T) {
			stack.CreateELBProxyConfiguration(t, s, loser)
		})

		t.Run("Winner is Accepted and provisions the data plane", func(t *testing.T) {
			s.WaitForELBProxyConfigurationAccepted(t, winnerNN)
			addr = s.WaitForGatewayAccepted(t, gwNN)
			s.WaitForHTTPRouteAccepted(t, routeNN, gwNN)
		})

		t.Run("Loser reports Accepted=False/Conflicted", func(t *testing.T) {
			s.WaitForELBProxyConfigurationConditionReason(t, client.ObjectKeyFromObject(loser),
				elbproxyutil.ELBProxyConfigurationAcceptedConditionType,
				metav1.ConditionFalse,
				elbproxyutil.ELBProxyConfigurationReasonConflicted,
			)
		})

		t.Run("Winning data plane serves traffic", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})
	},
}
