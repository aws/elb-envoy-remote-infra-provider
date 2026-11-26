// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package resources

import (
	"maps"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
)

// ResourceStack holds the resources one test deploys. Build the fields with
// the Build* functions, then call Deploy.
//
// A stack shares nothing with other tests, so tests can run in parallel:
// Deploy puts every object that has no namespace into a namespace of its own,
// and the objects that can't live there get random names. Those are the
// GatewayClass (cluster-scoped), its EnvoyProxy, and GatewayClass-scoped
// ELBProxyConfigurations (both in the Envoy Gateway namespace).
type ResourceStack struct {
	// configurations
	BaseName   string // namespace base name, also the TestLabelKey label value
	EnvoyProxy *unstructured.Unstructured
	Gwc        *gwapiv1.GatewayClass
	Gws        []*gwapiv1.Gateway
	// ListenerSets are created after the Gateways and before the routes.
	ListenerSets []*gwapiv1.ListenerSet
	Secrets      []*corev1.Secret
	Cms          []*corev1.ConfigMap
	Dps          []*appsv1.Deployment
	Svcs         []*corev1.Service
	Httprs       []*gwapiv1.HTTPRoute
	Grpcrs       []*gwapiv1.GRPCRoute
	Tcprs        []*gwapiv1.TCPRoute
	Udprs        []*gwapiv1.UDPRoute
	Tlsrs        []*gwapiv1.TLSRoute
	// Policies are Envoy Gateway policies (e.g. SecurityPolicy), unstructured
	// like the EnvoyProxy. They are created after the routes they target.
	Policies []*unstructured.Unstructured
	Proxies  []*elbproxyv1alpha1.ELBProxyConfiguration

	// runtime variables
	Ns *corev1.Namespace
	// GatewayAddresses holds each Gateway's address, in Gws order. Set by
	// WaitUntilReady.
	GatewayAddresses []string
}

// Deploy allocates the stack's namespace, then creates the EnvoyProxy, GatewayClass,
// backends, Gateways, ListenerSets, routes, policies, and ELBProxyConfigurations, in
// that order. Cleanup deletes Gateways first, then the rest in reverse order.
func (st *ResourceStack) Deploy(t *testing.T, s *framework.Suite) {
	t.Helper()
	for _, p := range st.Proxies {
		requireRoleArn(t, p)
	}

	st.Ns = AllocateNamespace(t, s, st.BaseName, map[string]string{TestLabelKey: st.BaseName})
	for _, obj := range st.objects() {
		st.create(t, s, obj)
	}
}

// CreateHTTPRoute adds httpr to a deployed stack, for tests that change the
// config of a live proxy.
func (st *ResourceStack) CreateHTTPRoute(t *testing.T, s *framework.Suite, httpr *gwapiv1.HTTPRoute) {
	t.Helper()
	st.create(t, s, httpr)
	st.Httprs = append(st.Httprs, httpr)
}

// CreateGateway adds gw to a deployed stack without waiting for it; use
// Suite.WaitForGatewayAccepted.
func (st *ResourceStack) CreateGateway(t *testing.T, s *framework.Suite, gw *gwapiv1.Gateway) {
	t.Helper()
	st.create(t, s, gw)
	st.Gws = append(st.Gws, gw)
}

// CreateELBProxyConfiguration adds proxy to a deployed stack.
func (st *ResourceStack) CreateELBProxyConfiguration(t *testing.T, s *framework.Suite, proxy *elbproxyv1alpha1.ELBProxyConfiguration) {
	t.Helper()
	requireRoleArn(t, proxy)
	st.create(t, s, proxy)
	st.Proxies = append(st.Proxies, proxy)
}

// WaitUntilReady waits until every ELBProxyConfiguration, ListenerSet and Gateway
// route is Accepted and every Gateway is Programmed with an address, and records the
// addresses. Routes on a ListenerSet aren't waited on; tests retry their requests.
// Tests that expect a resource to never become ready wait on resources individually.
func (st *ResourceStack) WaitUntilReady(t *testing.T, s *framework.Suite) {
	t.Helper()
	for _, p := range st.Proxies {
		s.WaitForELBProxyConfigurationAccepted(t, client.ObjectKeyFromObject(p))
	}

	st.GatewayAddresses = make([]string, len(st.Gws))
	for i, gw := range st.Gws {
		st.GatewayAddresses[i] = s.WaitForGatewayAccepted(t, client.ObjectKeyFromObject(gw))
	}
	for _, ls := range st.ListenerSets {
		s.WaitForListenerSetAccepted(t, client.ObjectKeyFromObject(ls))
	}

	for _, r := range st.Httprs {
		for _, gwNN := range parentGateways(r, r.Spec.ParentRefs) {
			s.WaitForHTTPRouteAccepted(t, client.ObjectKeyFromObject(r), gwNN)
		}
	}
	for _, r := range st.Grpcrs {
		for _, gwNN := range parentGateways(r, r.Spec.ParentRefs) {
			s.WaitForGRPCRouteAccepted(t, client.ObjectKeyFromObject(r), gwNN)
		}
	}
	for _, r := range st.Tcprs {
		for _, gwNN := range parentGateways(r, r.Spec.ParentRefs) {
			s.WaitForTCPRouteAccepted(t, client.ObjectKeyFromObject(r), gwNN)
		}
	}
	for _, r := range st.Udprs {
		for _, gwNN := range parentGateways(r, r.Spec.ParentRefs) {
			s.WaitForUDPRouteAccepted(t, client.ObjectKeyFromObject(r), gwNN)
		}
	}
	for _, r := range st.Tlsrs {
		for _, gwNN := range parentGateways(r, r.Spec.ParentRefs) {
			s.WaitForTLSRouteAccepted(t, client.ObjectKeyFromObject(r), gwNN)
		}
	}
}

// GetNamespace returns the stack's namespace. Set by Deploy.
func (st *ResourceStack) GetNamespace() string {
	return st.Ns.Name
}

// GetGatewayAddress returns the first Gateway's address, or "" before
// WaitUntilReady has recorded it.
func (st *ResourceStack) GetGatewayAddress() string {
	if len(st.GatewayAddresses) == 0 {
		return ""
	}
	return st.GatewayAddresses[0]
}

// objects returns the stack's objects in creation order: the EnvoyProxy before
// the GatewayClass that references it, backends before the Gateways and routes
// that use them, and ELBProxyConfigurations last.
func (st *ResourceStack) objects() []client.Object {
	var objs []client.Object
	if st.EnvoyProxy != nil {
		objs = append(objs, st.EnvoyProxy)
	}
	if st.Gwc != nil {
		objs = append(objs, st.Gwc)
	}
	objs = appendObjects(objs, st.Secrets)
	objs = appendObjects(objs, st.Cms)
	objs = appendObjects(objs, st.Dps)
	objs = appendObjects(objs, st.Svcs)
	objs = appendObjects(objs, st.Gws)
	objs = appendObjects(objs, st.ListenerSets)
	objs = appendObjects(objs, st.Httprs)
	objs = appendObjects(objs, st.Grpcrs)
	objs = appendObjects(objs, st.Tcprs)
	objs = appendObjects(objs, st.Udprs)
	objs = appendObjects(objs, st.Tlsrs)
	objs = appendObjects(objs, st.Policies)
	return appendObjects(objs, st.Proxies)
}

// create places obj in the stack's namespace unless it already has one (or is
// the cluster-scoped GatewayClass), labels it, and creates it.
func (st *ResourceStack) create(t *testing.T, s *framework.Suite, obj client.Object) {
	t.Helper()
	if _, clusterScoped := obj.(*gwapiv1.GatewayClass); !clusterScoped && obj.GetNamespace() == "" {
		obj.SetNamespace(st.Ns.Name)
	}
	// Copy before adding the label: builders may share label maps with
	// selectors.
	labels := maps.Clone(obj.GetLabels())
	if labels == nil {
		labels = map[string]string{}
	}
	labels[TestLabelKey] = st.BaseName
	obj.SetLabels(labels)

	var opts []client.CreateOption
	if _, ok := obj.(*unstructured.Unstructured); ok {
		// No Go type checks unstructured objects (the EnvoyProxy), so have the
		// API server reject unknown fields, as kubectl apply does.
		opts = append(opts, client.FieldValidation(metav1.FieldValidationStrict))
	}
	if err := s.CreateObject(t, obj, opts...); err != nil {
		t.Fatalf("deploy %q stack: %v", st.BaseName, err)
	}
}

// requireRoleArn fails the test before anything is created when an
// ELBProxyConfiguration has no role ARN, which it needs to provision a data
// plane.
func requireRoleArn(t *testing.T, p *elbproxyv1alpha1.ELBProxyConfiguration) {
	t.Helper()
	if p.Spec.RoleArn == "" {
		t.Fatalf("ELBProxyConfiguration %s has no spec.roleArn; set -role-arn or E2E_ROLE_ARN", p.Name)
	}
}

// parentGateways returns the Gateways that a route's parentRefs point at, once
// each. A parentRef with no namespace points into the route's namespace.
func parentGateways(route client.Object, refs []gwapiv1.ParentReference) []types.NamespacedName {
	var out []types.NamespacedName
	seen := map[types.NamespacedName]bool{}
	for _, ref := range refs {
		if ref.Kind != nil && *ref.Kind != "Gateway" {
			continue
		}
		nn := types.NamespacedName{Namespace: route.GetNamespace(), Name: string(ref.Name)}
		if ref.Namespace != nil && *ref.Namespace != "" {
			nn.Namespace = string(*ref.Namespace)
		}
		if !seen[nn] {
			seen[nn] = true
			out = append(out, nn)
		}
	}
	return out
}

func appendObjects[T client.Object](objs []client.Object, items []T) []client.Object {
	for _, item := range items {
		objs = append(objs, item)
	}
	return objs
}
