// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, HTTPRouteRequestMirror)
}

// HTTPRouteRequestMirror mirrors a route's requests to a second backend and checks the
// request shows up in its logs, since Envoy drops the mirror's response.
var HTTPRouteRequestMirror = framework.TestCase{
	ShortName:   "HTTPRouteRequestMirror",
	Description: "Mirror a route's requests to a backend no route references and verify the mirror backend receives them",
	Test: func(t *testing.T, s *framework.Suite) {
		stack := newStack(s, "httproute-request-mirror", httpListeners(), elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.Dps = []*appsv1.Deployment{
			resources.BuildEchoDeploymentSpec(),
			resources.BuildEchoDeploymentOnPort(resources.MirrorEchoName, resources.MirrorEchoPort),
		}
		stack.Svcs = []*corev1.Service{
			resources.BuildEchoServiceSpec(),
			resources.BuildEchoServiceOnPort(resources.MirrorEchoName, resources.MirrorEchoPort),
		}
		stack.Httprs = []*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, []gwapiv1.HTTPRouteRule{
			resources.BuildHTTPRouteRuleWithMirror(resources.DefaultPathPrefix, resources.MirrorEchoName, resources.EchoServicePort),
		}, nil)}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)

		if verifier, ok := s.NewAWSVerifier(t); ok {
			verifier.WaitForNodeSGPort(t, "tcp", resources.MirrorEchoPort, true, s.Timeouts.Accepted)
		}
		addr := stack.GetGatewayAddress()
		s.MakeRequestAndExpectEventuallyConsistentResponse(t,
			framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
			framework.HTTPExpectation{StatusCode: 200},
		)

		// A unique path, so finding it in the logs proves the mirror got it.
		path := resources.DefaultPathPrefix + "/mirror-" + utilrand.String(8)
		httpClient := &http.Client{Timeout: 5 * time.Second}
		s.WaitForPodLog(t, stack.GetNamespace(), resources.MirrorEchoName, path, s.Timeouts.Accepted, func() {
			resp, err := httpClient.Get("http://" + addr + path)
			if err != nil {
				framework.Logf(t, "GET %s: %v", path, err)
				return
			}
			_ = resp.Body.Close()
		})
	},
}
