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
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationAttributes)
}

// ELBProxyConfigurationAttributes provisions
// a proxy with deletion_protection.enabled=true, verifies the attribute reaches
// AWS, then flips it back to false and verifies that too
var ELBProxyConfigurationAttributes = framework.TestCase{
	ShortName:   "ELBProxyConfigurationAttributes",
	Description: "Toggle ELBProxyConfiguration spec.attributes (deletion_protection.enabled) on a live proxy and verify the change reaches AWS",
	Test: func(t *testing.T, s *framework.Suite) {
		// Optional AWS verifier for confirming attribute values reach AWS.
		verifier, awsOK := s.NewAWSVerifier(t)

		stack := deployHTTP(t, s, "attributes", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{
				Attributes: []elbproxyv1alpha1.Attribute{
					{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
				},
			})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])
		proxyNN := client.ObjectKeyFromObject(stack.Proxies[0])

		var addr string

		t.Run("Provision proxy with deletion_protection.enabled=true", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
			addr = stack.GetGatewayAddress()

			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)

			if awsOK {
				verifier.WaitForProxyAttribute(t, gwNN.Namespace, gwNN.Name,
					elbproxyaws.AttributeKeyDeletionProtectionEnabled, "true", s.Timeouts.Accepted)
			}
		})

		t.Run("Disable deletion_protection so the proxy can be torn down", func(t *testing.T) {
			updateProxyAttributes(t, s, proxyNN, []elbproxyv1alpha1.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false"},
			})

			// Non-destructive: proxy stays Accepted and serving after the update.
			s.WaitForELBProxyConfigurationAccepted(t, proxyNN)
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)

			// Always wait for the attribute to reach ELBProxy before test cleanup
			// triggers deletion — otherwise deletion protection may still be
			// enabled on the ELBProxy side and the proxy cannot be torn down.
			if awsOK {
				verifier.WaitForProxyAttribute(t, gwNN.Namespace, gwNN.Name,
					elbproxyaws.AttributeKeyDeletionProtectionEnabled, "false", s.Timeouts.Accepted)
			} else {
				// Without AWS verification, wait for the controller to reconcile
				// the attribute change to ELBProxy before cleanup deletes the proxy.
				// The Accepted condition confirms the controller saw the change;
				// the traffic probe above confirms a full reconcile cycle completed.
				s.WaitForReconcile(t, proxyNN)
			}
		})
	},
}

// updateProxyAttributes fetches the live ELBProxyConfiguration and writes desiredAttrs
// to spec.attributes.
func updateProxyAttributes(t *testing.T, s *framework.Suite, proxyNN types.NamespacedName, desiredAttrs []elbproxyv1alpha1.Attribute) {
	t.Helper()
	ctx := t.Context()

	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	if err := s.Client.Get(ctx, proxyNN, live); err != nil {
		t.Fatalf("get ELBProxyConfiguration %s before attribute update: %v", proxyNN, err)
	}
	live.Spec.Attributes = desiredAttrs
	if err := s.Client.Update(ctx, live); err != nil {
		t.Fatalf("update ELBProxyConfiguration %s attributes: %v", proxyNN, err)
	}
	framework.Logf(t, "updated ELBProxyConfiguration %s spec.attributes to %v", proxyNN, desiredAttrs)
}
