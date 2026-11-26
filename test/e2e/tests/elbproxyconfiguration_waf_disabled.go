// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package tests

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/resources"
)

func init() {
	E2ETests = append(E2ETests, ELBProxyConfigurationWAFDisabled)
}

// ELBProxyConfigurationWAFDisabled creates a proxy without waf.web_acl_arn and
// verifies WAF is disabled by default: the proxy comes up with an empty
// waf.web_acl_arn and serves traffic without WAF inspection.
var ELBProxyConfigurationWAFDisabled = framework.TestCase{
	ShortName:   "ELBProxyConfigurationWAFDisabled",
	Description: "Create an ELBProxyConfiguration without a WAF web ACL and verify WAF is disabled and traffic flows",
	Test: func(t *testing.T, s *framework.Suite) {
		verifier, awsOK := s.NewAWSVerifier(t)

		// No waf.web_acl_arn attribute: WAF is disabled by default.
		stack := deployHTTP(t, s, "waf-disabled", httpListeners(),
			[]*gwapiv1.HTTPRoute{resources.BuildHTTPRoute(nil, nil, nil)},
			elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		gwNN := client.ObjectKeyFromObject(stack.Gws[0])

		var addr string

		t.Run("Provision proxy without a WAF web ACL", func(t *testing.T) {
			stack.WaitUntilReady(t, s)
			addr = stack.GetGatewayAddress()
		})

		t.Run("Traffic flows without WAF inspection", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t,
				framework.HTTPRequest{Host: addr, Path: resources.DefaultPathPrefix},
				framework.HTTPExpectation{StatusCode: 200},
			)
		})

		t.Run("No WAF web ACL is associated", func(t *testing.T) {
			if !awsOK {
				t.Skip("AWS verification not configured; set -cluster-name to assert WAF attributes")
			}
			verifier.WaitForProxyAttribute(t, gwNN.Namespace, gwNN.Name,
				elbproxyaws.AttributeKeyWafWebAclArn, "", s.Timeouts.Accepted)
		})
	},
}
