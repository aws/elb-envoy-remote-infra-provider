// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func labeledGateway(namespace, name string, labels map[string]string) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels}}
}

func labeledNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func selectorPolicy(namespace string, selectors ...egv1a1.TargetSelector) *egv1a1.SecurityPolicy {
	return &egv1a1.SecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "sp", Namespace: namespace},
		Spec: egv1a1.SecurityPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{TargetSelectors: selectors},
		},
	}
}

func selectKind(kind string, labels map[string]string, namespaces *egv1a1.TargetSelectorNamespaces) egv1a1.TargetSelector {
	return egv1a1.TargetSelector{Kind: gwapiv1.Kind(kind), MatchLabels: labels, Namespaces: namespaces}
}

// gatewayGrant returns a ReferenceGrant in namespace that lets SecurityPolicies in
// fromNamespace reference Gateways.
func gatewayGrant(namespace, fromNamespace string) *gwapiv1.ReferenceGrant {
	return &gwapiv1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-policies", Namespace: namespace},
		Spec: gwapiv1.ReferenceGrantSpec{
			From: []gwapiv1.ReferenceGrantFrom{{
				Group: egv1a1.GroupName, Kind: egv1a1.KindSecurityPolicy, Namespace: gwapiv1.Namespace(fromNamespace),
			}},
			To: []gwapiv1.ReferenceGrantTo{{Group: gwapiv1.GroupName, Kind: kindGateway}},
		},
	}
}

func TestGatewayKeysForTargetSelectors(t *testing.T) {
	prod := map[string]string{"env": "prod"}
	fromAll := &egv1a1.TargetSelectorNamespaces{From: egv1a1.TargetNamespaceFromAll}
	fromTeamA := &egv1a1.TargetSelectorNamespaces{
		From:     egv1a1.TargetNamespaceFromSelector,
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}},
	}
	labeledRoute := httpRouteToSvc("r", "default", "gw-route", "app", nil)
	labeledRoute.Labels = prod

	base := []client.Object{
		labeledGateway("default", "gw-prod", prod),
		labeledGateway("default", "gw-dev", map[string]string{"env": "dev"}),
		labeledGateway("team-a", "gw-a", prod),
		labeledNamespace("default", nil),
		labeledNamespace("team-a", map[string]string{"team": "a"}),
	}
	tests := []struct {
		name   string
		extra  []client.Object
		policy client.Object
		want   []string
	}{
		{
			name:   "Gateways with matching labels in the policy's namespace",
			policy: selectorPolicy("default", selectKind("Gateway", prod, nil)),
			want:   []string{"default/gw-prod"},
		},
		{
			name: "matchExpressions",
			policy: selectorPolicy("default", egv1a1.TargetSelector{Kind: "Gateway", MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"dev"}},
			}}),
			want: []string{"default/gw-dev"},
		},
		{
			name:   "Gateway in another namespace without a ReferenceGrant is ignored",
			policy: selectorPolicy("default", selectKind("Gateway", prod, fromAll)),
			want:   []string{"default/gw-prod"},
		},
		{
			name:   "Gateway in another namespace with a ReferenceGrant",
			extra:  []client.Object{gatewayGrant("team-a", "default")},
			policy: selectorPolicy("default", selectKind("Gateway", prod, fromAll)),
			want:   []string{"default/gw-prod", "team-a/gw-a"},
		},
		{
			name:   "ReferenceGrant for policies in a different namespace is ignored",
			extra:  []client.Object{gatewayGrant("team-a", "other")},
			policy: selectorPolicy("default", selectKind("Gateway", prod, fromAll)),
			want:   []string{"default/gw-prod"},
		},
		{
			name:   "namespaces selected by label",
			extra:  []client.Object{gatewayGrant("team-a", "default")},
			policy: selectorPolicy("default", selectKind("Gateway", prod, fromTeamA)),
			want:   []string{"team-a/gw-a"},
		},
		{
			name:   "HTTPRoute with matching labels maps to its parent Gateways",
			extra:  []client.Object{labeledRoute},
			policy: selectorPolicy("default", selectKind("HTTPRoute", prod, nil)),
			want:   []string{"default/gw-route"},
		},
		{
			name:   "unsupported kind is ignored",
			policy: selectorPolicy("default", selectKind("TCPRoute", prod, nil)),
			want:   nil,
		},
		{
			name: "other group is ignored",
			policy: selectorPolicy("default", egv1a1.TargetSelector{
				Group: ptrTo(gwapiv1.Group("example.com")), Kind: "Gateway", MatchLabels: prod,
			}),
			want: nil,
		},
		{
			name: "invalid selector matches nothing",
			policy: selectorPolicy("default", egv1a1.TargetSelector{Kind: "Gateway", MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "env", Operator: "NotAnOperator"},
			}}),
			want: nil,
		},
		{
			name:   "policy without targetSelectors",
			policy: extAuthPolicy("sp", "default", policyTargetRef("Gateway", "gw-prod"), "authz"),
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := serviceTestClient(append(append([]client.Object{}, base...), tt.extra...)...)
			got, err := GatewayKeysForTargetSelectors(context.Background(), c, tt.policy, listenerSetParentFromClient(context.Background(), c, testLogger()))
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestEnqueueRequestsForPolicyEvent_TargetSelectors(t *testing.T) {
	prod := map[string]string{"env": "prod"}
	c := serviceTestClient(labeledGateway("default", "gw", prod), labeledGateway("default", "other", nil))
	h := NewEnqueueRequestsForPolicyEvent(testLogger(), c)

	q := &fakeQueue[reconcile.Request]{}
	h.Create(context.Background(), event.TypedCreateEvent[client.Object]{
		Object: selectorPolicy("default", selectKind("Gateway", prod, nil)),
	}, q)
	assert.ElementsMatch(t, []reconcile.Request{gatewayRequest("default", "gw")}, q.items)
}

func TestPolicyUnchanged_TargetSelectors(t *testing.T) {
	old := selectorPolicy("default", selectKind("Gateway", map[string]string{"env": "prod"}, nil))
	changed := selectorPolicy("default", selectKind("Gateway", map[string]string{"env": "dev"}, nil))
	assert.True(t, policyUnchanged(old, old.DeepCopy()))
	assert.False(t, policyUnchanged(old, changed))
}
