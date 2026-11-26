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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// listenerSet returns a ListenerSet attached to the Gateway gateway in its own namespace.
// Envoy Gateway's Accepted condition is set to True when accepted is true.
func listenerSet(namespace, name, gateway string, accepted bool, labels map[string]string) *gwapiv1.ListenerSet {
	ls := &gwapiv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		Spec:       gwapiv1.ListenerSetSpec{ParentRef: gwapiv1.ParentGatewayReference{Name: gwapiv1.ObjectName(gateway)}},
	}
	if accepted {
		ls.Status.Conditions = []metav1.Condition{{
			Type:   string(gwapiv1.ListenerSetConditionAccepted),
			Status: metav1.ConditionTrue,
			Reason: string(gwapiv1.ListenerSetReasonAccepted),
		}}
	}
	return ls
}

// listenerSetParentRef builds a route ParentReference pointing to a ListenerSet.
func listenerSetParentRef(namespace, name string) gwapiv1.ParentReference {
	ref := gatewayParentRef(namespace, name)
	kind := gwapiv1.Kind(kindListenerSet)
	ref.Kind = &kind
	return ref
}

func TestListenerSetGatewayKey(t *testing.T) {
	inOtherNamespace := listenerSet("apps", "ls", "gw", true, nil)
	ns := gwapiv1.Namespace("infra")
	inOtherNamespace.Spec.ParentRef.Namespace = &ns

	rejected := listenerSet("apps", "ls", "gw", false, nil)
	rejected.Status.Conditions = []metav1.Condition{{
		Type: string(gwapiv1.ListenerSetConditionAccepted), Status: metav1.ConditionFalse, Reason: string(gwapiv1.ListenerSetReasonNotAllowed),
	}}

	otherKind := listenerSet("apps", "ls", "gw", true, nil)
	kind := gwapiv1.Kind("Other")
	otherKind.Spec.ParentRef.Kind = &kind

	tests := []struct {
		name string
		ls   *gwapiv1.ListenerSet
		want string
	}{
		{name: "accepted, Gateway in the ListenerSet's namespace", ls: listenerSet("apps", "ls", "gw", true, nil), want: "apps/gw"},
		{name: "accepted, Gateway in another namespace", ls: inOtherNamespace, want: "infra/gw"},
		{name: "no Accepted condition yet", ls: listenerSet("apps", "ls", "gw", false, nil), want: ""},
		{name: "rejected by the Gateway", ls: rejected, want: ""},
		{name: "parent that isn't a Gateway", ls: otherKind, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ListenerSetGatewayKey(tt.ls))
		})
	}
}

func TestGatewayKeysForParentRefs_ListenerSet(t *testing.T) {
	parents := ListenerSetParentsFromList([]gwapiv1.ListenerSet{
		*listenerSet("infra", "ls", "gw", true, nil),
		*listenerSet("apps", "local-ls", "gw-local", true, nil),
		*listenerSet("infra", "pending", "gw", false, nil),
	})
	noNamespace := listenerSetParentRef("", "local-ls")
	noNamespace.Namespace = nil

	tests := []struct {
		name   string
		refs   []gwapiv1.ParentReference
		lookup ListenerSetParentFunc
		want   []string
	}{
		{
			name:   "ListenerSet maps to its Gateway",
			refs:   []gwapiv1.ParentReference{listenerSetParentRef("infra", "ls"), gatewayParentRef("apps", "gw-direct")},
			lookup: parents,
			want:   []string{"infra/gw", "apps/gw-direct"},
		},
		{name: "ListenerSet namespace defaults to the route's", refs: []gwapiv1.ParentReference{noNamespace}, lookup: parents, want: []string{"apps/gw-local"}},
		{name: "ListenerSet that isn't accepted is skipped", refs: []gwapiv1.ParentReference{listenerSetParentRef("infra", "pending")}, lookup: parents, want: nil},
		{name: "missing ListenerSet is skipped", refs: []gwapiv1.ParentReference{listenerSetParentRef("infra", "missing")}, lookup: parents, want: nil},
		{name: "no lookup skips ListenerSets", refs: []gwapiv1.ParentReference{listenerSetParentRef("infra", "ls")}, lookup: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GatewayKeysForParentRefs("apps", tt.refs, tt.lookup))
		})
	}
}

func TestEnqueueRequestsForListenerSetEvent(t *testing.T) {
	prod := map[string]string{"env": "prod"}
	annotated := listenerSet("infra", "ls", "gw", true, nil)
	annotated.Annotations = map[string]string{"note": "changed"}

	updates := []struct {
		name     string
		old, new *gwapiv1.ListenerSet
		want     []reconcile.Request
	}{
		{
			name: "accepted by its Gateway",
			old:  listenerSet("infra", "ls", "gw", false, nil),
			new:  listenerSet("infra", "ls", "gw", true, nil),
			want: []reconcile.Request{gatewayRequest("infra", "gw")},
		},
		{
			name: "moved to another Gateway",
			old:  listenerSet("infra", "ls", "gw-a", true, nil),
			new:  listenerSet("infra", "ls", "gw-b", true, nil),
			want: []reconcile.Request{gatewayRequest("infra", "gw-a"), gatewayRequest("infra", "gw-b")},
		},
		{
			name: "labels changed",
			old:  listenerSet("infra", "ls", "gw", true, nil),
			new:  listenerSet("infra", "ls", "gw", true, prod),
			want: []reconcile.Request{gatewayRequest("infra", "gw")},
		},
		{
			name: "other changes are skipped",
			old:  listenerSet("infra", "ls", "gw", true, nil),
			new:  annotated,
			want: nil,
		},
		{
			name: "labels changed on a ListenerSet that isn't attached",
			old:  listenerSet("infra", "ls", "gw", false, nil),
			new:  listenerSet("infra", "ls", "gw", false, prod),
			want: nil,
		},
	}
	for _, tt := range updates {
		t.Run(tt.name, func(t *testing.T) {
			h := NewEnqueueRequestsForListenerSetEvent(testLogger())
			q := &fakeQueue[reconcile.Request]{}
			h.Update(context.Background(), event.TypedUpdateEvent[*gwapiv1.ListenerSet]{ObjectOld: tt.old, ObjectNew: tt.new}, q)
			assert.ElementsMatch(t, tt.want, q.items)
		})
	}

	t.Run("deleting an attached ListenerSet enqueues its Gateway", func(t *testing.T) {
		h := NewEnqueueRequestsForListenerSetEvent(testLogger())
		q := &fakeQueue[reconcile.Request]{}
		h.Delete(context.Background(), event.TypedDeleteEvent[*gwapiv1.ListenerSet]{Object: listenerSet("infra", "ls", "gw", true, nil)}, q)
		h.Delete(context.Background(), event.TypedDeleteEvent[*gwapiv1.ListenerSet]{Object: listenerSet("infra", "other", "gw-x", false, nil)}, q)
		assert.ElementsMatch(t, []reconcile.Request{gatewayRequest("infra", "gw")}, q.items)
	})
}

func TestEnqueueRequestsForRouteEvent_ListenerSetParent(t *testing.T) {
	c := serviceTestClient(listenerSet("infra", "ls", "gw", true, nil))
	route := makeHTTPRoute("r", "apps", []gwapiv1.ParentReference{
		listenerSetParentRef("infra", "ls"),
		listenerSetParentRef("infra", "missing"),
		gatewayParentRef("apps", "gw-direct"),
	})

	h := NewEnqueueRequestsForRouteEvent(testLogger(), c)
	q := &fakeQueue[reconcile.Request]{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: route}, q)
	assert.ElementsMatch(t, []reconcile.Request{gatewayRequest("infra", "gw"), gatewayRequest("apps", "gw-direct")}, q.items)
}

func TestGatewayKeysForPolicyTargets_ListenerSet(t *testing.T) {
	parents := ListenerSetParentsFromList([]gwapiv1.ListenerSet{*listenerSet("ns", "ls", "gw", true, nil)})
	routes := map[string][]gwapiv1.ParentReference{
		"HTTPRoute/ns/web": {listenerSetParentRef("ns", "ls")},
	}
	lookupRoute := func(kind, namespace, name string) []gwapiv1.ParentReference {
		return routes[kind+"/"+namespace+"/"+name]
	}
	onListenerSet := []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("ListenerSet", "ls")}

	assert.Equal(t, []string{"ns/gw"}, GatewayKeysForPolicyTargets("ns", onListenerSet, lookupRoute, parents))
	assert.Equal(t, []string{"ns/gw"}, GatewayKeysForPolicyTargets("ns",
		[]gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTargetRef("HTTPRoute", "web")}, lookupRoute, parents),
		"a route attached to a ListenerSet maps to the ListenerSet's Gateway")
	assert.Empty(t, GatewayKeysForPolicyTargets("ns", onListenerSet, lookupRoute, nil))
}

func TestGatewayKeysForTargetSelectors_ListenerSet(t *testing.T) {
	prod := map[string]string{"env": "prod"}
	routeOnListenerSet := httpRouteToSvc("r", "default", "unused", "app", nil)
	routeOnListenerSet.Labels = prod
	routeOnListenerSet.Spec.ParentRefs = []gwapiv1.ParentReference{listenerSetParentRef("default", "ls-prod")}

	c := serviceTestClient(
		listenerSet("default", "ls-prod", "gw-a", true, prod),
		listenerSet("default", "ls-pending", "gw-b", false, prod),
		routeOnListenerSet,
	)
	resolve := func(sel egv1a1.TargetSelector) []string {
		keys, err := GatewayKeysForTargetSelectors(context.Background(), c, selectorPolicy("default", sel),
			listenerSetParentFromClient(context.Background(), c, testLogger()))
		require.NoError(t, err)
		return keys
	}

	assert.Equal(t, []string{"default/gw-a"}, resolve(selectKind("ListenerSet", prod, nil)),
		"selects only the ListenerSet its Gateway accepted")
	assert.Equal(t, []string{"default/gw-a"}, resolve(selectKind("HTTPRoute", prod, nil)),
		"a selected route attached to a ListenerSet maps to the ListenerSet's Gateway")
}

func TestEnqueueRequestsForRouteEvent_ListenerSetParentUpdateAndDelete(t *testing.T) {
	c := serviceTestClient(listenerSet("infra", "ls", "gw", true, nil))
	onListenerSet := makeHTTPRoute("r", "apps", []gwapiv1.ParentReference{listenerSetParentRef("infra", "ls")})
	onGateway := makeHTTPRoute("r", "apps", []gwapiv1.ParentReference{gatewayParentRef("apps", "gw-direct")})
	h := NewEnqueueRequestsForRouteEvent(testLogger(), c)

	q := &fakeQueue[reconcile.Request]{}
	h.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: onListenerSet, ObjectNew: onGateway}, q)
	assert.ElementsMatch(t, []reconcile.Request{gatewayRequest("infra", "gw"), gatewayRequest("apps", "gw-direct")}, q.items,
		"moving a route off a ListenerSet also re-checks the ListenerSet's Gateway")

	q = &fakeQueue[reconcile.Request]{}
	h.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: onListenerSet}, q)
	assert.ElementsMatch(t, []reconcile.Request{gatewayRequest("infra", "gw")}, q.items)
}

func TestGatewayKeysForService_ListenerSetParent(t *testing.T) {
	route := httpRouteToSvc("r", "apps", "unused", "app", nil)
	route.Spec.ParentRefs = []gwapiv1.ParentReference{listenerSetParentRef("infra", "ls")}
	c := serviceTestClient(listenerSet("infra", "ls", "gw", true, nil), route)

	keys, err := gatewayKeysForService(context.Background(), c, testLogger(), "apps", "app")
	require.NoError(t, err)
	assert.Equal(t, []string{"infra/gw"}, keys)
}

func TestEnqueueRequestsForPolicyEvent_ListenerSetTarget(t *testing.T) {
	c := serviceTestClient(listenerSet("default", "ls", "gw", true, nil))
	h := NewEnqueueRequestsForPolicyEvent(testLogger(), c)

	q := &fakeQueue[reconcile.Request]{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{
		Object: extAuthPolicy("sp", "default", policyTargetRef("ListenerSet", "ls"), "authz"),
	}, q)
	assert.ElementsMatch(t, []reconcile.Request{gatewayRequest("default", "gw")}, q.items)
}

func TestGatewayKeysForTargetSelectors_CrossNamespaceListenerSet(t *testing.T) {
	prod := map[string]string{"env": "prod"}
	fromAll := &egv1a1.TargetSelectorNamespaces{From: egv1a1.TargetNamespaceFromAll}
	policy := selectorPolicy("default", selectKind("ListenerSet", prod, fromAll))
	listenerSetGrant := func() *gwapiv1.ReferenceGrant {
		grant := gatewayGrant("team-a", "default")
		grant.Spec.To[0].Kind = kindListenerSet
		return grant
	}
	resolve := func(objs ...client.Object) []string {
		c := serviceTestClient(objs...)
		keys, err := GatewayKeysForTargetSelectors(context.Background(), c, policy,
			listenerSetParentFromClient(context.Background(), c, testLogger()))
		require.NoError(t, err)
		return keys
	}

	assert.Empty(t, resolve(listenerSet("team-a", "ls", "gw-a", true, prod)),
		"a ListenerSet in another namespace needs a ReferenceGrant")
	assert.Equal(t, []string{"team-a/gw-a"}, resolve(listenerSet("team-a", "ls", "gw-a", true, prod), listenerSetGrant()))
}
