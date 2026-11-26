// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// gatewayParentRef builds a ParentReference pointing to a Gateway.
func gatewayParentRef(namespace, name string) gwapiv1.ParentReference {
	group := gwapiv1.Group(gwapiv1.GroupName)
	kind := gwapiv1.Kind("Gateway")
	ns := gwapiv1.Namespace(namespace)
	return gwapiv1.ParentReference{
		Group:     &group,
		Kind:      &kind,
		Namespace: &ns,
		Name:      gwapiv1.ObjectName(name),
	}
}

func makeHTTPRoute(name, namespace string, parentRefs []gwapiv1.ParentReference) *gwapiv1.HTTPRoute {
	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       gwapiv1.HTTPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: parentRefs}},
	}
}

func TestEnqueueRequestsForRouteEvent_Create(t *testing.T) {
	h := NewEnqueueRequestsForRouteEvent(testLogger(), serviceTestClient())
	q := &fakeQueue[reconcile.Request]{}
	route := makeHTTPRoute("r", "default", []gwapiv1.ParentReference{
		gatewayParentRef("default", "gw1"),
		gatewayParentRef("default", "gw2"),
	})

	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{Object: route}, q)

	assert.Equal(t, 2, len(q.items))
	assert.Contains(t, q.items, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "gw1"}})
	assert.Contains(t, q.items, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "gw2"}})
}

func TestEnqueueRequestsForRouteEvent_Delete(t *testing.T) {
	h := NewEnqueueRequestsForRouteEvent(testLogger(), serviceTestClient())
	q := &fakeQueue[reconcile.Request]{}
	route := makeHTTPRoute("r", "default", []gwapiv1.ParentReference{gatewayParentRef("default", "gw")})

	h.Delete(context.Background(), event.TypedDeleteEvent[client.Object]{Object: route}, q)

	assert.Equal(t, 1, len(q.items))
	assert.Equal(t, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "gw"}}, q.items[0])
}

func TestEnqueueRequestsForRouteEvent_Update(t *testing.T) {
	port80 := gwapiv1.PortNumber(80)
	port443 := gwapiv1.PortNumber(443)

	makeRouteWithBackend := func(name, namespace string, parentRefs []gwapiv1.ParentReference, port gwapiv1.PortNumber) *gwapiv1.HTTPRoute {
		return &gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: parentRefs},
				Rules: []gwapiv1.HTTPRouteRule{{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: &port}}},
					},
				}},
			},
		}
	}

	tests := []struct {
		name      string
		old       client.Object
		new       client.Object
		wantItems int
	}{
		{
			name:      "no change — skipped",
			old:       makeHTTPRoute("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw")}),
			new:       makeHTTPRoute("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw")}),
			wantItems: 0,
		},
		{
			name:      "parentRef changed — enqueued for both old and new",
			old:       makeHTTPRoute("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw-old")}),
			new:       makeHTTPRoute("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw-new")}),
			wantItems: 2,
		},
		{
			name:      "backendRef port changed — enqueued",
			old:       makeRouteWithBackend("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw")}, port80),
			new:       makeRouteWithBackend("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw")}, port443),
			wantItems: 1,
		},
		{
			name: "labels changed — enqueued, since policy targetSelectors match labels",
			old:  makeHTTPRoute("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw")}),
			new: func() client.Object {
				r := makeHTTPRoute("r", "ns", []gwapiv1.ParentReference{gatewayParentRef("ns", "gw")})
				r.Labels = map[string]string{"env": "prod"}
				return r
			}(),
			wantItems: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewEnqueueRequestsForRouteEvent(testLogger(), serviceTestClient())
			q := &fakeQueue[reconcile.Request]{}
			h.Update(context.Background(), event.TypedUpdateEvent[client.Object]{
				ObjectOld: tt.old,
				ObjectNew: tt.new,
			}, q)
			assert.Equal(t, tt.wantItems, len(q.items))
		})
	}
}

func TestExtractParentRefs(t *testing.T) {
	ref := gatewayParentRef("ns", "gw")

	tests := []struct {
		name string
		obj  client.Object
		want []gwapiv1.ParentReference
	}{
		{
			name: "HTTPRoute",
			obj:  makeHTTPRoute("r", "ns", []gwapiv1.ParentReference{ref}),
			want: []gwapiv1.ParentReference{ref},
		},
		{
			name: "GRPCRoute",
			obj: &gwapiv1.GRPCRoute{
				Spec: gwapiv1.GRPCRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{ref}}},
			},
			want: []gwapiv1.ParentReference{ref},
		},
		{
			name: "TCPRoute",
			obj: &gwapiv1.TCPRoute{
				Spec: gwapiv1.TCPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{ref}}},
			},
			want: []gwapiv1.ParentReference{ref},
		},
		{
			name: "TLSRoute",
			obj: &gwapiv1.TLSRoute{
				Spec: gwapiv1.TLSRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{ref}}},
			},
			want: []gwapiv1.ParentReference{ref},
		},
		{
			name: "UDPRoute",
			obj: &gwapiv1.UDPRoute{
				Spec: gwapiv1.UDPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{ref}}},
			},
			want: []gwapiv1.ParentReference{ref},
		},
		{
			name: "unknown type — nil",
			obj:  &gwapiv1.Gateway{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractParentRefs(tt.obj))
		})
	}
}

func TestRequestFromGatewayKey(t *testing.T) {
	tests := []struct {
		key           string
		wantNamespace string
		wantName      string
	}{
		{"default/my-gw", "default", "my-gw"},
		{"eg7", "", "eg7"},
		{"elbproxy-merged-prod", "", "elbproxy-merged-prod"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			req := requestFromGatewayKey(tt.key)
			assert.Equal(t, tt.wantNamespace, req.Namespace)
			assert.Equal(t, tt.wantName, req.Name)
		})
	}
}

func TestGatewayKeysForParentRefs(t *testing.T) {
	group := gwapiv1.Group(gwapiv1.GroupName)
	kind := gwapiv1.Kind("Gateway")
	otherGroup := gwapiv1.Group("other.io")
	otherKind := gwapiv1.Kind("Service")
	crossNS := gwapiv1.Namespace("other-ns")

	tests := []struct {
		name           string
		routeNamespace string
		parentRefs     []gwapiv1.ParentReference
		wantKeys       []string
	}{
		{
			name:           "single gateway same namespace",
			routeNamespace: "default",
			parentRefs:     []gwapiv1.ParentReference{gatewayParentRef("default", "my-gw")},
			wantKeys:       []string{"default/my-gw"},
		},
		{
			name:           "cross-namespace parentRef",
			routeNamespace: "default",
			parentRefs: []gwapiv1.ParentReference{{
				Group:     &group,
				Kind:      &kind,
				Namespace: &crossNS,
				Name:      "my-gw",
			}},
			wantKeys: []string{"other-ns/my-gw"},
		},
		{
			name:           "nil group and kind defaults to Gateway",
			routeNamespace: "default",
			parentRefs:     []gwapiv1.ParentReference{{Name: "my-gw"}},
			wantKeys:       []string{"default/my-gw"},
		},
		{
			name:           "non-gateway group skipped",
			routeNamespace: "default",
			parentRefs:     []gwapiv1.ParentReference{{Group: &otherGroup, Name: "my-gw"}},
			wantKeys:       nil,
		},
		{
			name:           "non-gateway kind skipped",
			routeNamespace: "default",
			parentRefs:     []gwapiv1.ParentReference{{Kind: &otherKind, Name: "my-svc"}},
			wantKeys:       nil,
		},
		{
			name:           "multiple parentRefs",
			routeNamespace: "default",
			parentRefs: []gwapiv1.ParentReference{
				gatewayParentRef("default", "gw-1"),
				gatewayParentRef("default", "gw-2"),
			},
			wantKeys: []string{"default/gw-1", "default/gw-2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantKeys, GatewayKeysForParentRefs(tt.routeNamespace, tt.parentRefs, nil))
		})
	}
}
