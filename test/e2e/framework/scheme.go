// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
)

// buildScheme registers the kinds the typed client uses: core, Gateway API
// (v1 + v1alpha2), and ELBProxyConfiguration. EnvoyProxy is omitted on purpose:
// tests create it as unstructured, because the Envoy Gateway Go models lag the
// cluster CRD.
func buildScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	mustAdd(s, clientgoscheme.AddToScheme)
	mustAdd(s, gwapiv1.Install)
	mustAdd(s, gwapiv1a2.Install)
	mustAdd(s, elbproxyv1alpha1.AddToScheme)
	return s
}

func mustAdd(s *runtime.Scheme, fn func(*runtime.Scheme) error) {
	if err := fn(s); err != nil {
		panic(err)
	}
}
