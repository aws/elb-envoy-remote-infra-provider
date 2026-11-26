// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// fromNamespaces returns targetSelector namespaces with the given from.
func fromNamespaces(from egv1a1.TargetNamespaceFrom) *egv1a1.TargetSelectorNamespaces {
	ns := &egv1a1.TargetSelectorNamespaces{From: from}
	if from == egv1a1.TargetNamespaceFromSelector {
		ns.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}
	}
	return ns
}

func TestEnqueueRequestsForNamespaceEvent_Update(t *testing.T) {
	allKeys := []reconcile.Request{gatewayRequest("default", "gw1"), gatewayRequest("other", "gw2")}
	oldNS := labeledNamespace("other", nil)
	tests := []struct {
		name   string
		newNS  *corev1.Namespace
		policy client.Object
		want   []reconcile.Request
	}{
		{
			name:   "labels changed, a policy selects namespaces by label",
			newNS:  labeledNamespace("other", map[string]string{"team": "a"}),
			policy: selectorPolicy("default", selectKind(kindGateway, nil, fromNamespaces(egv1a1.TargetNamespaceFromSelector))),
			want:   allKeys,
		},
		{
			name:   "labels changed, policies only select in their own namespace",
			newNS:  labeledNamespace("other", map[string]string{"team": "a"}),
			policy: selectorPolicy("default", selectKind(kindGateway, nil, nil)),
			want:   nil,
		},
		{
			name:   "labels changed, a policy selects from all namespaces",
			newNS:  labeledNamespace("other", map[string]string{"team": "a"}),
			policy: selectorPolicy("default", selectKind(kindGateway, nil, fromNamespaces(egv1a1.TargetNamespaceFromAll))),
			want:   nil,
		},
		{
			name:   "labels unchanged",
			newNS:  labeledNamespace("other", nil),
			policy: selectorPolicy("default", selectKind(kindGateway, nil, fromNamespaces(egv1a1.TargetNamespaceFromSelector))),
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewEnqueueRequestsForNamespaceEvent(testLogger(), serviceTestClient(tt.policy), makeCache("default/gw1", "other/gw2"))
			q := &fakeQueue[reconcile.Request]{}
			h.Update(context.Background(), event.TypedUpdateEvent[*corev1.Namespace]{ObjectOld: oldNS, ObjectNew: tt.newNS}, q)
			assert.ElementsMatch(t, tt.want, q.items)
		})
	}
}

func TestEnqueueRequestsForReferenceGrantEvent(t *testing.T) {
	allKeys := []reconcile.Request{gatewayRequest("default", "gw1"), gatewayRequest("other", "gw2")}
	grant := gatewayGrant("other", "default")
	extProcPolicy := &egv1a1.EnvoyExtensionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "eep", Namespace: "default"},
		Spec: egv1a1.EnvoyExtensionPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{TargetSelectors: []egv1a1.TargetSelector{
				selectKind(kindHTTPRoute, nil, fromNamespaces(egv1a1.TargetNamespaceFromSelector)),
			}},
		},
	}
	newHandler := func(objs ...client.Object) *EnqueueRequestsForReferenceGrantEvent {
		return NewEnqueueRequestsForReferenceGrantEvent(testLogger(), serviceTestClient(objs...), makeCache("default/gw1", "other/gw2"))
	}

	t.Run("create, a SecurityPolicy selects from all namespaces", func(t *testing.T) {
		h := newHandler(selectorPolicy("default", selectKind(kindGateway, nil, fromNamespaces(egv1a1.TargetNamespaceFromAll))))
		q := &fakeQueue[reconcile.Request]{}
		h.Create(context.Background(), event.TypedCreateEvent[*gwapiv1.ReferenceGrant]{Object: grant}, q)
		assert.ElementsMatch(t, allKeys, q.items)
	})
	t.Run("create from the initial list", func(t *testing.T) {
		h := newHandler(selectorPolicy("default", selectKind(kindGateway, nil, fromNamespaces(egv1a1.TargetNamespaceFromAll))))
		q := &fakeQueue[reconcile.Request]{}
		h.Create(context.Background(), event.TypedCreateEvent[*gwapiv1.ReferenceGrant]{Object: grant, IsInInitialList: true}, q)
		assert.Empty(t, q.items)
	})
	t.Run("create, no policy selects in other namespaces", func(t *testing.T) {
		h := newHandler(selectorPolicy("default", selectKind(kindGateway, nil, nil)))
		q := &fakeQueue[reconcile.Request]{}
		h.Create(context.Background(), event.TypedCreateEvent[*gwapiv1.ReferenceGrant]{Object: grant}, q)
		assert.Empty(t, q.items)
	})
	t.Run("delete, an EnvoyExtensionPolicy selects namespaces by label", func(t *testing.T) {
		h := newHandler(extProcPolicy)
		q := &fakeQueue[reconcile.Request]{}
		h.Delete(context.Background(), event.TypedDeleteEvent[*gwapiv1.ReferenceGrant]{Object: grant}, q)
		assert.ElementsMatch(t, allKeys, q.items)
	})
	t.Run("update, spec unchanged", func(t *testing.T) {
		h := newHandler(extProcPolicy)
		q := &fakeQueue[reconcile.Request]{}
		relabeled := grant.DeepCopy()
		relabeled.Labels = map[string]string{"x": "y"}
		h.Update(context.Background(), event.TypedUpdateEvent[*gwapiv1.ReferenceGrant]{ObjectOld: grant, ObjectNew: relabeled}, q)
		assert.Empty(t, q.items)
	})
	t.Run("update, spec changed", func(t *testing.T) {
		h := newHandler(extProcPolicy)
		q := &fakeQueue[reconcile.Request]{}
		changed := grant.DeepCopy()
		changed.Spec.To[0].Kind = kindHTTPRoute
		h.Update(context.Background(), event.TypedUpdateEvent[*gwapiv1.ReferenceGrant]{ObjectOld: grant, ObjectNew: changed}, q)
		assert.ElementsMatch(t, allKeys, q.items)
	})
}
