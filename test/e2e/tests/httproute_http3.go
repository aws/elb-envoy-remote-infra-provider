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
	E2ETests = append(E2ETests, HTTPRouteHTTP3)
}

// The HTTPS port, which Envoy also serves QUIC on, and the route's hostname.
const (
	http3Port     = 443
	http3Hostname = "h3." + resources.TestHostname
)

// HTTPRouteHTTP3 turns on HTTP/3 for an HTTPS listener and checks that responses
// advertise it and the frontend SG allows UDP on the port. Serving QUIC is Envoy's
// job, so the test sends no QUIC request.
var HTTPRouteHTTP3 = framework.TestCase{
	ShortName:   "HTTPRouteHTTP3",
	Description: "Turn on HTTP/3 for an HTTPS listener and verify the proxy advertises it and the frontend SG allows UDP",
	Test: func(t *testing.T, s *framework.Suite) {
		verifier, awsOK := s.NewAWSVerifier(t)

		gwListeners := []gwapiv1.Listener{{
			Name:     "https",
			Protocol: gwapiv1.HTTPSProtocolType,
			Port:     http3Port,
			Hostname: ptr.To(gwapiv1.Hostname("*." + resources.TestHostname)),
			TLS: &gwapiv1.ListenerTLSConfig{
				Mode:            ptr.To(gwapiv1.TLSModeTerminate),
				CertificateRefs: []gwapiv1.SecretObjectReference{{Name: resources.TLSCertSecretName}},
			},
		}}
		secret := buildTLSSecret(t)
		stack := newStack(s, "httproute-http3", gwListeners, elbproxyv1alpha1.ELBProxyConfigurationSpec{})
		stack.Secrets = []*corev1.Secret{secret}
		stack.Dps = []*appsv1.Deployment{resources.BuildEchoDeploymentSpec()}
		stack.Svcs = []*corev1.Service{resources.BuildEchoServiceSpec()}
		stack.Httprs = []*gwapiv1.HTTPRoute{resources.BuildHTTPRoute([]string{http3Hostname}, nil, nil)}
		stack.Policies = []*unstructured.Unstructured{resources.BuildHTTP3ClientTrafficPolicy(stack.Gws[0].Name)}
		stack.Deploy(t, s)
		stack.WaitUntilReady(t, s)

		request := framework.HTTPRequest{
			Host:       net.JoinHostPort(stack.GetGatewayAddress(), strconv.Itoa(http3Port)),
			Path:       resources.DefaultPathPrefix,
			Header:     map[string]string{"Host": http3Hostname},
			ServerName: http3Hostname,
			RootCAPEM:  secret.Data[corev1.TLSCertKey],
		}

		t.Run("HTTPS over TCP advertises HTTP/3", func(t *testing.T) {
			s.MakeRequestAndExpectEventuallyConsistentResponse(t, request, framework.HTTPExpectation{
				StatusCode: 200,
				Header:     map[string]string{"alt-svc": `h3=":443"; ma=86400`},
			})
		})

		t.Run("The frontend security group allows UDP for QUIC", func(t *testing.T) {
			if !awsOK {
				t.Skip("AWS verification not configured; set -cluster-name")
			}
			verifier.WaitForFrontendSGPort(t, stack.GetNamespace(), stack.Gws[0].Name, "udp", http3Port, s.Timeouts.Accepted)
		})
	},
}
