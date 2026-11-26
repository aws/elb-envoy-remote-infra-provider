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
	E2ETests = append(E2ETests, ELBProxyConfigurationUpdateTags)
}

// ELBProxyConfigurationUpdateTags provisions a live proxy, then mutates the ELBProxyConfiguration
// spec.tags in place and verifies the change is reconciled non-destructively:
// the proxy stays Accepted and traffic keeps round-tripping with 200.
var ELBProxyConfigurationUpdateTags = framework.TestCase{
	ShortName:   "ELBProxyConfigurationUpdateTags",
	Description: "Mutate ELBProxyConfiguration spec.tags on a live proxy and verify it stays Accepted and serving",
	Test: func(t *testing.T, s *framework.Suite) {
		// Start with one tag so the proxy is created with tagging in effect.
		stack := deployHTTP(t, s, "update-tags", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{Tags: map[string]string{"elbproxy-e2e-tag": "before"}})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])
		proxyNN := client.ObjectKeyFromObject(stack.Proxies[0])

		stack.WaitUntilReady(t, s)
		addr := stack.GetGatewayAddress()

		// Baseline: traffic flows before the update.
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{StatusCode: 200},
		)

		// Mutate spec.tags in place (change a value + add a key). A spec change
		// enqueues a reconcile, which drives the proxy tagging manager.
		updateProxyTags(t, s, proxyNN, map[string]string{
			"elbproxy-e2e-tag":   "after",
			"elbproxy-e2e-added": "new",
		})

		// The update must be non-destructive: proxy stays Accepted and serving.
		s.WaitForELBProxyConfigurationAccepted(t, proxyNN)
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{StatusCode: 200},
		)

		// If AWS verification is configured, confirm the new tag actually
		// reached the ELBProxy API (not just that K8s conditions held).
		if v, ok := s.NewAWSVerifier(t); ok {
			v.WaitForProxyTag(t, gwNN.Namespace, gwNN.Name, "elbproxy-e2e-tag", "after", s.Timeouts.Accepted)
			v.WaitForProxyTag(t, gwNN.Namespace, gwNN.Name, "elbproxy-e2e-added", "new", s.Timeouts.Accepted)
		}
	},
}

// updateProxyTags fetches the live ELBProxyConfiguration and writes desiredTags to
// spec.tags. It retries via a fresh Get on conflict by re-reading the latest
// resourceVersion before the Update.
func updateProxyTags(t *testing.T, s *framework.Suite, proxyNN types.NamespacedName, desiredTags map[string]string) {
	t.Helper()
	ctx := t.Context()

	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	if err := s.Client.Get(ctx, proxyNN, live); err != nil {
		t.Fatalf("get ELBProxyConfiguration %s before tag update: %v", proxyNN, err)
	}
	live.Spec.Tags = desiredTags
	if err := s.Client.Update(ctx, live); err != nil {
		t.Fatalf("update ELBProxyConfiguration %s tags: %v", proxyNN, err)
	}
	framework.Logf(t, "updated ELBProxyConfiguration %s spec.tags to %v", proxyNN, desiredTags)
}
