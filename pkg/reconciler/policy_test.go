// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

func makeClassProxy(name, ns, className string) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: gwapiv1.ObjectName(className)},
			},
		},
	}
}

func acceptedCondition(t *testing.T, cli client.Client, ns, name string) *metav1.Condition {
	t.Helper()
	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, live))
	for i := range live.Status.Conditions {
		if live.Status.Conditions[i].Type == "Accepted" {
			return &live.Status.Conditions[i]
		}
	}
	return nil
}

func TestImpactedGatewayKeys(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*infrastore.Cache)
		proxyKey string
		want     []string
	}{
		{
			name:     "unknown proxy, empty cache",
			setup:    func(c *infrastore.Cache) {},
			proxyKey: "ns/p",
			want:     nil,
		},
		{
			name: "only reverse-index entries (drop scenario)",
			setup: func(c *infrastore.Cache) {
				c.SetEffectiveProxy("ns/gw1", "ns/p")
				c.SetEffectiveProxy("ns/gw2", "ns/p")
			},
			proxyKey: "ns/p",
			want:     []string{"ns/gw1", "ns/gw2"},
		},
		{
			name: "gateway-scoped targetRef adds exact key",
			setup: func(c *infrastore.Cache) {
				c.SetELBProxyConfiguration("ns/p", makeProxyTargetingGateway("p", "ns", "gw1"))
			},
			proxyKey: "ns/p",
			want:     []string{"ns/gw1"},
		},
		{
			name: "class-scoped targetRef fans out to gateways in class",
			setup: func(c *infrastore.Cache) {
				c.SetELBProxyConfiguration("system/sp", makeClassProxy("sp", "system", "eg"))
				c.SetGatewayClass("ns/gwA", "eg")
				c.SetGatewayClass("ns/gwB", "eg")
			},
			proxyKey: "system/sp",
			want:     []string{"ns/gwA", "ns/gwB"},
		},
		{
			name: "union: reverse index + targetRefs (no dedup regression)",
			setup: func(c *infrastore.Cache) {
				c.SetELBProxyConfiguration("ns/p", makeProxyTargetingGateway("p", "ns", "gw1"))
				c.SetEffectiveProxy("ns/gw1", "ns/p")
				c.SetEffectiveProxy("ns/gw2", "ns/p") // about to be dropped
			},
			proxyKey: "ns/p",
			want:     []string{"ns/gw1", "ns/gw2"},
		},
		{
			name: "class fanout limited to gateways in that class",
			setup: func(c *infrastore.Cache) {
				c.SetELBProxyConfiguration("system/sp", makeClassProxy("sp", "system", "eg"))
				c.SetGatewayClass("ns/in-class", "eg")
				c.SetGatewayClass("ns/not-in-class", "other")
			},
			proxyKey: "system/sp",
			want:     []string{"ns/in-class"},
		},
		{
			name: "class-scoped proxy outside controller namespace — no class fanout",
			setup: func(c *infrastore.Cache) {
				c.SetELBProxyConfiguration("tenant-b/sp", makeClassProxy("sp", "tenant-b", "eg"))
				c.SetGatewayClass("ns/gw1", "eg")
			},
			proxyKey: "tenant-b/sp",
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			tt.setup(c)
			rec := New(testLogger(), &fakeInfraManager{}, c, testClient(), "system")

			got := rec.impactedGatewayKeys(tt.proxyKey)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestReconcileForELBProxyConfiguration_StatusSync(t *testing.T) {
	older := metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))

	mkGwProxy := func(name string, ts metav1.Time) *elbproxyv1alpha1.ELBProxyConfiguration {
		p := makeProxyTargetingGateway(name, testNamespace, testGateway)
		p.CreationTimestamp = ts
		return p
	}

	tests := []struct {
		name       string
		proxies    []*elbproxyv1alpha1.ELBProxyConfiguration
		trigger    string
		wantStatus map[string]metav1.ConditionStatus
		wantReason map[string]string
	}{
		{
			name:       "single matching proxy — Accepted=True",
			proxies:    []*elbproxyv1alpha1.ELBProxyConfiguration{mkGwProxy("solo", older)},
			trigger:    testNamespace + "/solo",
			wantStatus: map[string]metav1.ConditionStatus{"solo": metav1.ConditionTrue},
			wantReason: map[string]string{"solo": "Accepted"},
		},
		{
			name: "two competing proxies — older Accepted, newer Conflicted",
			proxies: []*elbproxyv1alpha1.ELBProxyConfiguration{
				mkGwProxy("old", older),
				mkGwProxy("new", newer),
			},
			trigger: testNamespace + "/new",
			wantStatus: map[string]metav1.ConditionStatus{
				"old": metav1.ConditionTrue,
				"new": metav1.ConditionFalse,
			},
			wantReason: map[string]string{
				"old": "Accepted",
				"new": "Conflicted",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			c.SetInfra(testGatewayKey, makeWireInfra())
			c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
			for _, p := range tt.proxies {
				c.SetELBProxyConfiguration(p.Namespace+"/"+p.Name, p)
			}

			objs := []client.Object{testGatewayObj(testGateway, testNamespace, testClass)}
			for _, p := range tt.proxies {
				objs = append(objs, p)
			}
			cli := testClient(objs...)

			rec := New(testLogger(), &fakeInfraManager{}, c, cli, "system")
			require.NoError(t, rec.ReconcileForELBProxyConfiguration(context.Background(), tt.trigger))

			for name, wantStatus := range tt.wantStatus {
				cond := acceptedCondition(t, cli, testNamespace, name)
				require.NotNilf(t, cond, "proxy %s missing Accepted condition", name)
				assert.Equalf(t, wantStatus, cond.Status, "proxy %s status", name)
				assert.Equalf(t, tt.wantReason[name], cond.Reason, "proxy %s reason", name)
			}
		})
	}
}

func TestPatchAcceptedCondition_Idempotent(t *testing.T) {
	proxy := makeProxyTargetingGateway("p", testNamespace, testGateway)
	proxy.Generation = 1

	tests := []struct {
		name        string
		firstCond   metav1.Condition
		secondCond  metav1.Condition
		wantPatched bool // true if LastTransitionTime should change on second call
	}{
		{
			name: "identical condition — no-op",
			firstCond: metav1.Condition{
				Type:    "Accepted",
				Status:  metav1.ConditionTrue,
				Reason:  "Accepted",
				Message: "ELBProxyConfiguration is attached to at least one target",
			},
			secondCond: metav1.Condition{
				Type:    "Accepted",
				Status:  metav1.ConditionTrue,
				Reason:  "Accepted",
				Message: "ELBProxyConfiguration is attached to at least one target",
			},
			wantPatched: false,
		},
		{
			name: "status changes — patch applied",
			firstCond: metav1.Condition{
				Type:    "Accepted",
				Status:  metav1.ConditionTrue,
				Reason:  "Accepted",
				Message: "ELBProxyConfiguration is attached to at least one target",
			},
			secondCond: metav1.Condition{
				Type:    "Accepted",
				Status:  metav1.ConditionFalse,
				Reason:  "Conflicted",
				Message: "Another ELBProxyConfiguration targeting the same resource outranks this one in tiebreak",
			},
			wantPatched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := testClient(proxy)
			c := infrastore.New()
			rec := New(testLogger(), &fakeInfraManager{}, c, cli, "system")

			require.NoError(t, rec.patchAcceptedCondition(context.Background(), testNamespace+"/p", proxy, tt.firstCond))

			live := &elbproxyv1alpha1.ELBProxyConfiguration{}
			require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "p"}, live))
			firstTransition := live.Status.Conditions[0].LastTransitionTime

			require.NoError(t, rec.patchAcceptedCondition(context.Background(), testNamespace+"/p", proxy, tt.secondCond))

			require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: "p"}, live))
			if tt.wantPatched {
				assert.Equal(t, tt.secondCond.Status, live.Status.Conditions[0].Status, "condition status should be updated")
				assert.Equal(t, tt.secondCond.Reason, live.Status.Conditions[0].Reason, "condition reason should be updated")
			} else {
				assert.Equal(t, firstTransition, live.Status.Conditions[0].LastTransitionTime, "no-op patch must not update LastTransitionTime")
			}
		})
	}
}
