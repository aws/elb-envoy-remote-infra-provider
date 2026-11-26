// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func policyTargetRef(kind, name string) gwapiv1.LocalPolicyTargetReferenceWithSectionName {
	return gwapiv1.LocalPolicyTargetReferenceWithSectionName{
		LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
			Group: gwapiv1.GroupName,
			Kind:  gwapiv1.Kind(kind),
			Name:  gwapiv1.ObjectName(name),
		},
	}
}

func objRef(name string, port uint16) gwapiv1.BackendObjectReference {
	return gwapiv1.BackendObjectReference{Name: gwapiv1.ObjectName(name), Port: svcPort(port)}
}

func extAuthPolicy(name, namespace string, target gwapiv1.LocalPolicyTargetReferenceWithSectionName, backend string) *egv1a1.SecurityPolicy {
	return &egv1a1.SecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: egv1a1.SecurityPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{
				TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{target},
			},
			ExtAuth: &egv1a1.ExtAuth{HTTP: &egv1a1.HTTPExtAuthService{BackendCluster: egv1a1.BackendCluster{
				BackendRefs: []egv1a1.BackendRef{{BackendObjectReference: objRef(backend, 9001)}},
			}}},
		},
	}
}

func extProcPolicyOn(name, namespace string, target gwapiv1.LocalPolicyTargetReferenceWithSectionName, backend string) *egv1a1.EnvoyExtensionPolicy {
	return &egv1a1.EnvoyExtensionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: egv1a1.EnvoyExtensionPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{
				TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{target},
			},
			ExtProc: []egv1a1.ExtProc{{BackendCluster: egv1a1.BackendCluster{
				BackendRefs: []egv1a1.BackendRef{{BackendObjectReference: objRef(backend, 9002)}},
			}}},
		},
	}
}

func TestPolicyBackendRefs(t *testing.T) {
	sp := &egv1a1.SecurityPolicy{Spec: egv1a1.SecurityPolicySpec{ExtAuth: &egv1a1.ExtAuth{
		GRPC: &egv1a1.GRPCExtAuthService{BackendCluster: egv1a1.BackendCluster{
			BackendRef:  ptrTo(objRef("grpc-old", 9100)),
			BackendRefs: []egv1a1.BackendRef{{BackendObjectReference: objRef("grpc-new", 9101)}},
		}},
		HTTP: &egv1a1.HTTPExtAuthService{BackendCluster: egv1a1.BackendCluster{
			BackendRefs: []egv1a1.BackendRef{{BackendObjectReference: objRef("http", 9102)}},
		}},
	}}}
	assert.Equal(t, []gwapiv1.BackendObjectReference{objRef("grpc-old", 9100), objRef("grpc-new", 9101), objRef("http", 9102)},
		PolicyBackendRefs(sp))

	eep := &egv1a1.EnvoyExtensionPolicy{Spec: egv1a1.EnvoyExtensionPolicySpec{ExtProc: []egv1a1.ExtProc{
		{BackendCluster: egv1a1.BackendCluster{BackendRefs: []egv1a1.BackendRef{{BackendObjectReference: objRef("proc-a", 9002)}}}},
		{BackendCluster: egv1a1.BackendCluster{BackendRefs: []egv1a1.BackendRef{{BackendObjectReference: objRef("proc-b", 9003)}}}},
	}}}
	assert.Equal(t, []gwapiv1.BackendObjectReference{objRef("proc-a", 9002), objRef("proc-b", 9003)}, PolicyBackendRefs(eep))

	assert.Empty(t, PolicyBackendRefs(&egv1a1.SecurityPolicy{}), "no ext_authz")
	assert.Empty(t, PolicyBackendRefs(makeHTTPRoute("r", "ns", nil)), "not a policy")
}

func TestPolicyTargetRefs(t *testing.T) {
	target := policyTargetRef("Gateway", "gw")
	deprecated := &egv1a1.SecurityPolicy{Spec: egv1a1.SecurityPolicySpec{
		PolicyTargetReferences: egv1a1.PolicyTargetReferences{TargetRef: &target},
	}}
	assert.Equal(t, []gwapiv1.LocalPolicyTargetReferenceWithSectionName{target}, PolicyTargetRefs(deprecated))
	assert.Equal(t, []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("HTTPRoute", "r")},
		PolicyTargetRefs(extProcPolicyOn("eep", "ns", policyTargetRef("HTTPRoute", "r"), "proc")))
	assert.Nil(t, PolicyTargetRefs(makeHTTPRoute("r", "ns", nil)))
}

func TestGatewayKeysForPolicyTargets(t *testing.T) {
	routes := map[string][]gwapiv1.ParentReference{
		"HTTPRoute/ns/web":  {gatewayParentRef("ns", "gw-a"), gatewayParentRef("ns", "gw-b")},
		"GRPCRoute/ns/grpc": {gatewayParentRef("ns", "gw-a")},
	}
	lookup := func(kind, namespace, name string) []gwapiv1.ParentReference {
		return routes[kind+"/"+namespace+"/"+name]
	}
	otherGroup := policyTargetRef("Gateway", "gw-x")
	otherGroup.Group = "example.com"

	tests := []struct {
		name string
		refs []gwapiv1.LocalPolicyTargetReferenceWithSectionName
		want []string
	}{
		{name: "Gateway target", refs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("Gateway", "gw-a")}, want: []string{"ns/gw-a"}},
		{name: "HTTPRoute target maps to the route's Gateways", refs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("HTTPRoute", "web")}, want: []string{"ns/gw-a", "ns/gw-b"}},
		{name: "GRPCRoute target", refs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("GRPCRoute", "grpc")}, want: []string{"ns/gw-a"}},
		{name: "missing route", refs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("HTTPRoute", "missing")}, want: nil},
		{name: "unsupported kind is ignored", refs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("TCPRoute", "tcp")}, want: nil},
		{name: "other API group is ignored", refs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{otherGroup}, want: nil},
		{
			name: "keys are deduplicated",
			refs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("Gateway", "gw-a"), policyTargetRef("HTTPRoute", "web"), policyTargetRef("GRPCRoute", "grpc")},
			want: []string{"ns/gw-a", "ns/gw-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, tt.want, GatewayKeysForPolicyTargets("ns", tt.refs, lookup, nil))
		})
	}
}

func gatewayRequest(namespace, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

func TestEnqueueRequestsForPolicyEvent_Create(t *testing.T) {
	tests := []struct {
		name   string
		objs   []client.Object
		policy client.Object
		want   []reconcile.Request
	}{
		{
			name:   "policy on a Gateway",
			policy: extAuthPolicy("sp", "default", policyTargetRef("Gateway", "gw"), "authz"),
			want:   []reconcile.Request{gatewayRequest("default", "gw")},
		},
		{
			name:   "policy on an HTTPRoute enqueues the route's Gateways",
			objs:   []client.Object{httpRouteToSvc("r", "default", "gw", "app", nil)},
			policy: extProcPolicyOn("eep", "default", policyTargetRef("HTTPRoute", "r"), "proc"),
			want:   []reconcile.Request{gatewayRequest("default", "gw")},
		},
		{
			name:   "policy on a missing route enqueues nothing",
			policy: extAuthPolicy("sp", "default", policyTargetRef("HTTPRoute", "missing"), "authz"),
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewEnqueueRequestsForPolicyEvent(testLogger(), serviceTestClient(tt.objs...))
			q := &fakeQueue[reconcile.Request]{}
			h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: tt.policy}, q)
			assert.ElementsMatch(t, tt.want, q.items)
		})
	}
}

func TestEnqueueRequestsForPolicyEvent_Update(t *testing.T) {
	onGW := func(gw, backend string) client.Object {
		return extAuthPolicy("sp", "default", policyTargetRef("Gateway", gw), backend)
	}
	tests := []struct {
		name     string
		old, new client.Object
		want     []reconcile.Request
	}{
		{
			name: "targets and backends unchanged — skipped",
			old:  onGW("gw", "authz"),
			new:  onGW("gw", "authz"),
			want: nil,
		},
		{
			name: "target changed — old and new Gateways enqueued",
			old:  onGW("gw-old", "authz"),
			new:  onGW("gw-new", "authz"),
			want: []reconcile.Request{gatewayRequest("default", "gw-old"), gatewayRequest("default", "gw-new")},
		},
		{
			name: "backend changed — Gateway enqueued once",
			old:  onGW("gw", "authz"),
			new:  onGW("gw", "authz-v2"),
			want: []reconcile.Request{gatewayRequest("default", "gw")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewEnqueueRequestsForPolicyEvent(testLogger(), serviceTestClient())
			q := &fakeQueue[reconcile.Request]{}
			h.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: tt.old, ObjectNew: tt.new}, q)
			assert.ElementsMatch(t, tt.want, q.items)
		})
	}
}

func TestEnqueueRequestsForPolicyEvent_Delete(t *testing.T) {
	h := NewEnqueueRequestsForPolicyEvent(testLogger(), serviceTestClient())
	q := &fakeQueue[reconcile.Request]{}
	h.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{
		Object: extProcPolicyOn("eep", "default", policyTargetRef("Gateway", "gw"), "proc"),
	}, q)
	assert.Equal(t, []reconcile.Request{gatewayRequest("default", "gw")}, q.items)
}

// A policy backend Service's port change is mapped back to the Gateways the policy
// applies to, through the policy backend index.
func TestGatewayKeysForService_PolicyBackends(t *testing.T) {
	c := serviceTestClient(
		httpRouteToSvc("r", "default", "gw-route", "app", nil),
		extAuthPolicy("sp", "default", policyTargetRef("HTTPRoute", "r"), "authz"),
		extProcPolicyOn("eep", "default", policyTargetRef("Gateway", "gw-direct"), "proc"),
	)

	keys, err := gatewayKeysForService(context.Background(), c, testLogger(), "default", "authz")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"default/gw-route"}, keys)

	keys, err = gatewayKeysForService(context.Background(), c, testLogger(), "default", "proc")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"default/gw-direct"}, keys)

	keys, err = gatewayKeysForService(context.Background(), c, testLogger(), "other", "authz")
	require.NoError(t, err)
	assert.Empty(t, keys, "same name in another namespace")
}

func ptrTo[T any](v T) *T { return &v }
