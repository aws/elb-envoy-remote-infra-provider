// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
)

func TestPatchProxyHealth(t *testing.T) {
	tests := []struct {
		name          string
		details       *elbproxyaws.ProxyDetails
		wantEntry     bool
		wantStatus    metav1.ConditionStatus
		wantReason    string
		wantMsgSubstr string
	}{
		{
			name:       "active -> Programmed True",
			details:    &elbproxyaws.ProxyDetails{Arn: "arn:p1", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}},
			wantEntry:  true,
			wantStatus: metav1.ConditionTrue,
			wantReason: "Programmed",
		},
		{
			name:       "create-in-progress -> Programmed False/Pending",
			details:    &elbproxyaws.ProxyDetails{Arn: "arn:p1", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusCreateInProgress}},
			wantEntry:  true,
			wantStatus: metav1.ConditionFalse,
			wantReason: "Pending",
		},
		{
			name:       "create-failed -> Programmed False/CreateFailed",
			details:    &elbproxyaws.ProxyDetails{Arn: "arn:p1", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusCreateFailed}},
			wantEntry:  true,
			wantStatus: metav1.ConditionFalse,
			wantReason: "CreateFailed",
		},
		{
			name: "action-required -> Programmed False/ActionRequired with reasons in message",
			details: &elbproxyaws.ProxyDetails{Arn: "arn:p1", Status: elbproxyaws.ProxyStatus{
				Code: elbproxyaws.ProxyStatusActionRequired,
				Reasons: []elbproxyaws.ProxyStatusReason{
					{Code: "InsufficientFreeAddressesInSubnet", Target: "subnet-abc"},
					{Code: "VcpuLimitExceeded", Message: "Use Service Quotas to request an increase"},
				},
			}},
			wantEntry:     true,
			wantStatus:    metav1.ConditionFalse,
			wantReason:    "ActionRequired",
			wantMsgSubstr: "InsufficientFreeAddressesInSubnet [subnet-abc]; VcpuLimitExceeded: Use Service Quotas to request an increase",
		},
		{
			name:      "update-in-progress -> no entry written (transient)",
			details:   &elbproxyaws.ProxyDetails{Arn: "arn:p1", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusUpdateInProgress}},
			wantEntry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := makeProxyTargetingGateway("my-proxy", testNamespace, testGateway)
			cli := testClient(proxy)
			rec := New(testLogger(), &fakeInfraManager{}, infrastore.New(), cli, testControllerNS)

			err := rec.patchProxyHealth(context.Background(), proxy, testGatewayKey, tt.details)
			require.NoError(t, err)

			live := &elbproxyv1alpha1.ELBProxyConfiguration{}
			require.NoError(t, cli.Get(context.Background(),
				client.ObjectKey{Namespace: testNamespace, Name: "my-proxy"}, live))

			if !tt.wantEntry {
				assert.Empty(t, live.Status.Proxies, "no proxy entry should be written for transient codes")
				return
			}
			require.Len(t, live.Status.Proxies, 1, "one per-Gateway entry expected")
			entry := live.Status.Proxies[0]
			assert.Equal(t, "Gateway", entry.GatewayRef.Kind)
			assert.Equal(t, testNamespace, entry.GatewayRef.Namespace)
			assert.Equal(t, testGateway, entry.GatewayRef.Name)
			assert.Equal(t, "arn:p1", entry.ProxyArn)

			cond := meta.FindStatusCondition(entry.Conditions,
				elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
			require.NotNil(t, cond, "per-Gateway Programmed condition should be written")
			assert.Equal(t, tt.wantStatus, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			if tt.wantMsgSubstr != "" {
				assert.Contains(t, cond.Message, tt.wantMsgSubstr)
			}

			// Aggregate top-level Programmed condition mirrors the single entry.
			agg := meta.FindStatusCondition(live.Status.Conditions,
				elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
			require.NotNil(t, agg, "aggregate Programmed condition should be written")
			assert.Equal(t, tt.wantStatus, agg.Status)
		})
	}
}

// TestPatchProxyHealth_MultipleGateways: two Gateways get two entries; the aggregate is True if one is active.
func TestPatchProxyHealth_MultipleGateways(t *testing.T) {
	proxy := makeProxyTargetingGateway("class-proxy", testControllerNS, "")
	cli := testClient(proxy)
	rec := New(testLogger(), &fakeInfraManager{}, infrastore.New(), cli, testControllerNS)

	// Gateway A is active; Gateway B requires action.
	require.NoError(t, rec.patchProxyHealth(context.Background(), proxy, "ns/gw-a", &elbproxyaws.ProxyDetails{Arn: "arn:a", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}}))
	require.NoError(t, rec.patchProxyHealth(context.Background(), proxy, "ns/gw-b", &elbproxyaws.ProxyDetails{Arn: "arn:b", Status: elbproxyaws.ProxyStatus{
		Code:    elbproxyaws.ProxyStatusActionRequired,
		Reasons: []elbproxyaws.ProxyStatusReason{{Code: "VcpuLimitExceeded"}},
	}}))

	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	require.NoError(t, cli.Get(context.Background(),
		client.ObjectKey{Namespace: testControllerNS, Name: "class-proxy"}, live))

	require.Len(t, live.Status.Proxies, 2, "two independent per-Gateway entries expected")

	byName := map[string]elbproxyv1alpha1.ProxyStatusEntry{}
	for _, e := range live.Status.Proxies {
		byName[e.GatewayRef.Name] = e
	}
	condA := meta.FindStatusCondition(byName["gw-a"].Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
	condB := meta.FindStatusCondition(byName["gw-b"].Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
	require.NotNil(t, condA)
	require.NotNil(t, condB)
	assert.Equal(t, metav1.ConditionTrue, condA.Status, "gw-a proxy is active")
	assert.Equal(t, metav1.ConditionFalse, condB.Status, "gw-b proxy requires action")
	assert.Equal(t, "arn:a", byName["gw-a"].ProxyArn)
	assert.Equal(t, "arn:b", byName["gw-b"].ProxyArn)

	// Aggregate is True because at least one backing proxy is active.
	agg := meta.FindStatusCondition(live.Status.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
	require.NotNil(t, agg)
	assert.Equal(t, metav1.ConditionTrue, agg.Status)
}

// teardownInfraManager returns deleteErr from DeleteProxyInfra.
type teardownInfraManager struct {
	fakeInfraManager
	deleteErr error
}

func (f *teardownInfraManager) DeleteProxyInfra(_ context.Context, _ *ir.Infra) error {
	return f.deleteErr
}

// TestReconcileKey_TeardownStatus: Deleting while the proxy is deleted, then the entry is removed.
func TestReconcileKey_TeardownStatus(t *testing.T) {
	programmedTrue := []metav1.Condition{{
		Type: elbproxyutil.ELBProxyConfigurationProgrammedConditionType, Status: metav1.ConditionTrue,
		Reason: elbproxyutil.ELBProxyConfigurationReasonProgrammed, LastTransitionTime: metav1.Now(),
	}}
	seed := func(gateways ...string) *elbproxyv1alpha1.ELBProxyConfiguration {
		proxy := makeClassScopedProxy("class-proxy", testControllerNS, testClass)
		for _, gw := range gateways {
			proxy.Status.Proxies = append(proxy.Status.Proxies, elbproxyv1alpha1.ProxyStatusEntry{
				GatewayRef: elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: testNamespace, Name: gw},
				ProxyArn:   "arn:" + gw,
				Conditions: programmedTrue,
			})
		}
		proxy.Status.Conditions = programmedTrue
		return proxy
	}
	waiting := func() *RequeueNeededAfter {
		return NewRequeueNeededAfter(RequeueReasonWaitingForProxyDeletion, 30*time.Second)
	}

	tests := []struct {
		name       string
		gateways   []string
		deleteErr  error
		wantReason string // Programmed reason on the torn-down entry; "" means the entry is removed
	}{
		{name: "waiting on delete -> Deleting", gateways: []string{testGateway, "other"}, deleteErr: waiting(), wantReason: elbproxyutil.ELBProxyConfigurationReasonDeleting},
		{name: "teardown complete -> entry removed", gateways: []string{testGateway, "other"}},
		{name: "last entry removed -> aggregate removed", gateways: []string{testGateway}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := testClient(seed(tt.gateways...))
			c := infrastore.New()
			c.MarkPendingDeletion(testGatewayKey)
			rec := New(testLogger(), &teardownInfraManager{deleteErr: tt.deleteErr}, c, cli, testControllerNS)

			_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
			if tt.deleteErr != nil {
				require.True(t, IsRequeueError(err))
			} else {
				require.NoError(t, err)
			}

			live := &elbproxyv1alpha1.ELBProxyConfiguration{}
			require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: testControllerNS, Name: "class-proxy"}, live))
			torn := findProxyStatusEntry(live.Status.Proxies, elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: testNamespace, Name: testGateway})
			other := findProxyStatusEntry(live.Status.Proxies, elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: testNamespace, Name: "other"})
			if len(tt.gateways) > 1 {
				require.NotNil(t, other, "other Gateways' entries are kept")
				assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(other.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType).Status)
			}

			if tt.wantReason == "" {
				assert.Nil(t, torn, "entry is removed once teardown completes")
				if len(tt.gateways) == 1 {
					assert.Empty(t, live.Status.Proxies)
					assert.Nil(t, meta.FindStatusCondition(live.Status.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType),
						"aggregate is removed with the last entry")
				}
				return
			}
			require.NotNil(t, torn)
			cond := meta.FindStatusCondition(torn.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
		})
	}
}

func getProxyStatusEntry(t *testing.T, cli client.Client, name, gwNamespace, gwName string) elbproxyv1alpha1.ProxyStatusEntry {
	t.Helper()
	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, live))
	entry := findProxyStatusEntry(live.Status.Proxies, elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: gwNamespace, Name: gwName})
	require.NotNil(t, entry)
	return *entry
}

func getProxyStatusEntries(t *testing.T, cli client.Client, name string) []elbproxyv1alpha1.ProxyStatusEntry {
	t.Helper()
	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: name}, live))
	return live.Status.Proxies
}

func activeProxy(arn string) *elbproxyaws.ProxyDetails {
	return &elbproxyaws.ProxyDetails{Arn: arn, Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}}
}

// testClientWithInterceptor is testClient with interceptor funcs, for injecting races and failures.
func testClientWithInterceptor(funcs interceptor.Funcs, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(&elbproxyv1alpha1.ELBProxyConfiguration{}).
		WithInterceptorFuncs(funcs).
		Build()
}

// TestReconcileKey_PendingWhileProvisioning: a provisioning requeue reports Pending and keeps a known ARN.
func TestReconcileKey_PendingWhileProvisioning(t *testing.T) {
	tests := []struct {
		name     string
		existing []elbproxyv1alpha1.ProxyStatusEntry
		wantArn  string
	}{
		{name: "first creation: entry written without an ARN"},
		{
			name: "existing entry: ARN kept",
			existing: []elbproxyv1alpha1.ProxyStatusEntry{{
				GatewayRef: elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: testNamespace, Name: testGateway},
				ProxyArn:   "arn:proxy-1",
			}},
			wantArn: "arn:proxy-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := makeProxyTargetingGateway("my-proxy", testNamespace, testGateway)
			proxy.Status.Proxies = tt.existing
			cli := testClient(proxy)
			c := infrastore.New()
			c.SetInfra(testGatewayKey, makeWireInfra())
			c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
			c.SetELBProxyConfiguration(testNamespace+"/my-proxy", proxy)
			mgr := &fakeInfraManager{createErr: NewRequeueNeededAfter(RequeueReasonProxyProvisioning, 2*time.Minute)}
			rec := New(testLogger(), mgr, c, cli, testControllerNS)

			_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
			require.True(t, IsRequeueAfterReason(err, RequeueReasonProxyProvisioning), "the requeue is returned unchanged")

			entry := getProxyStatusEntry(t, cli, "my-proxy", testNamespace, testGateway)
			assert.Equal(t, tt.wantArn, entry.ProxyArn)
			cond := meta.FindStatusCondition(entry.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, elbproxyutil.ELBProxyConfigurationReasonPending, cond.Reason)
		})
	}
}

// TestReconcileKey_DeletingWhileReplacing: waiting on the old proxy's deletion reports Deleting.
func TestReconcileKey_DeletingWhileReplacing(t *testing.T) {
	proxy := makeProxyTargetingGateway("my-proxy", testNamespace, testGateway)
	proxy.Status.Proxies = []elbproxyv1alpha1.ProxyStatusEntry{{
		GatewayRef: elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: testNamespace, Name: testGateway},
		ProxyArn:   "arn:old",
		Conditions: []metav1.Condition{{
			Type: elbproxyutil.ELBProxyConfigurationProgrammedConditionType, Status: metav1.ConditionTrue,
			Reason: elbproxyutil.ELBProxyConfigurationReasonProgrammed, LastTransitionTime: metav1.Now(),
		}},
	}}
	cli := testClient(proxy)
	c := infrastore.New()
	c.SetInfra(testGatewayKey, makeWireInfra())
	c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
	c.SetELBProxyConfiguration(testNamespace+"/my-proxy", proxy)
	mgr := &fakeInfraManager{createErr: NewRequeueNeededAfter(RequeueReasonPreviousProxyDeleting, 30*time.Second)}
	rec := New(testLogger(), mgr, c, cli, testControllerNS)

	_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
	require.True(t, IsRequeueAfterReason(err, RequeueReasonPreviousProxyDeleting), "the requeue is returned unchanged")

	entry := getProxyStatusEntry(t, cli, "my-proxy", testNamespace, testGateway)
	assert.Equal(t, "arn:old", entry.ProxyArn)
	cond := meta.FindStatusCondition(entry.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, elbproxyutil.ELBProxyConfigurationReasonDeleting, cond.Reason)
	assert.Equal(t, "Replacing the proxy: deleting the old proxy", cond.Message)
}

// TestPatchProxyHealth_ConcurrentWriteNotLost: another Gateway's write lands between our read
// and patch; the resourceVersion check makes our patch conflict and retry, so both entries stay.
func TestPatchProxyHealth_ConcurrentWriteNotLost(t *testing.T) {
	proxy := makeClassScopedProxy("class-proxy", testControllerNS, "eg")
	var rec *Reconciler
	patches, raced := 0, false
	cli := testClientWithInterceptor(interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			patches++
			if !raced {
				// Gateway B writes first, after Gateway A has read the object.
				raced = true
				require.NoError(t, rec.patchProxyHealth(ctx, proxy, "ns/gw-b", activeProxy("arn:b")))
			}
			return c.SubResource(sub).Patch(ctx, obj, p, opts...)
		},
	}, proxy)
	rec = New(testLogger(), &fakeInfraManager{}, infrastore.New(), cli, testControllerNS)

	require.NoError(t, rec.patchProxyHealth(context.Background(), proxy, "ns/gw-a", activeProxy("arn:a")))

	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKeyFromObject(proxy), live))
	var arns []string
	for _, e := range live.Status.Proxies {
		arns = append(arns, e.ProxyArn)
	}
	assert.ElementsMatch(t, []string{"arn:a", "arn:b"}, arns, "neither Gateway's entry is lost")
	assert.Equal(t, 3, patches, "A's first patch conflicts, B's succeeds, A retries")
}

// TestReconcileKey_TeardownStatusRemovalRetried: if removing the entry fails, teardown requeues
// and keeps the key, so a later reconcile removes it.
func TestReconcileKey_TeardownStatusRemovalRetried(t *testing.T) {
	proxy := makeProxyTargetingGateway("my-proxy", testNamespace, testGateway)
	proxy.Status.Proxies = []elbproxyv1alpha1.ProxyStatusEntry{{
		GatewayRef: elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: testNamespace, Name: testGateway},
		ProxyArn:   "arn:p1",
	}}
	failList := true
	cli := testClientWithInterceptor(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if failList {
				return fmt.Errorf("transient list error")
			}
			return c.List(ctx, list, opts...)
		},
	}, proxy)
	c := infrastore.New()
	c.MarkPendingDeletion(testGatewayKey)
	rec := New(testLogger(), &teardownInfraManager{}, c, cli, testControllerNS)

	_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
	require.True(t, IsRequeueError(err), "a failed removal requeues")
	assert.True(t, c.IsPendingDeletion(testGatewayKey), "the key is kept for the retry")

	failList = false
	_, err = rec.ReconcileKey(context.Background(), testGatewayKey)
	require.NoError(t, err)
	assert.False(t, c.IsPendingDeletion(testGatewayKey))
	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKeyFromObject(proxy), live))
	assert.Empty(t, live.Status.Proxies, "the entry is removed on retry")
}

// TestReconcileKey_DetachedConfigurationEntryRemoved: when a Gateway's effective configuration
// changes (A to B, or to none), its entry is removed from A.
func TestReconcileKey_DetachedConfigurationEntryRemoved(t *testing.T) {
	tests := []struct {
		name      string
		switchToB bool
	}{
		{name: "A to B", switchToB: true},
		{name: "A to none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := makeProxyTargetingGateway("proxy-a", testNamespace, testGateway)
			b := makeProxyTargetingGateway("proxy-b", testNamespace, testGateway)
			cli := testClient(a, b)
			c := infrastore.New()
			c.SetInfra(testGatewayKey, makeWireInfra())
			c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
			c.SetELBProxyConfiguration(testNamespace+"/proxy-a", a)
			rec := New(testLogger(), &fakeInfraManager{createResult: activeProxy("arn:p1")}, c, cli, testControllerNS)

			_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
			require.NoError(t, err)
			require.Len(t, getProxyStatusEntries(t, cli, "proxy-a"), 1, "A reports the Gateway")

			// A no longer targets the Gateway; B takes over, or nothing does.
			c.DeleteELBProxyConfiguration(testNamespace + "/proxy-a")
			if tt.switchToB {
				c.SetELBProxyConfiguration(testNamespace+"/proxy-b", b)
			}
			_, err = rec.ReconcileKey(context.Background(), testGatewayKey)
			require.NoError(t, err)

			assert.Empty(t, getProxyStatusEntries(t, cli, "proxy-a"), "A's entry is removed")
			if tt.switchToB {
				assert.Len(t, getProxyStatusEntries(t, cli, "proxy-b"), 1, "B reports the Gateway")
			}
		})
	}
}

// TestReconcileKey_PendingReportsReplacementArn: while the replacement provisions, Pending shows
// its ARN, not the deleted proxy's.
func TestReconcileKey_PendingReportsReplacementArn(t *testing.T) {
	proxy := makeProxyTargetingGateway("my-proxy", testNamespace, testGateway)
	proxy.Status.Proxies = []elbproxyv1alpha1.ProxyStatusEntry{{
		GatewayRef: elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: testNamespace, Name: testGateway},
		ProxyArn:   "arn:old",
	}}
	cli := testClient(proxy)
	c := infrastore.New()
	c.SetInfra(testGatewayKey, makeWireInfra())
	c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
	c.SetELBProxyConfiguration(testNamespace+"/my-proxy", proxy)
	mgr := &fakeInfraManager{
		createResult: &elbproxyaws.ProxyDetails{Arn: "arn:new", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusCreateInProgress}},
		createErr:    NewRequeueNeededAfter(RequeueReasonProxyProvisioning, 2*time.Minute),
	}
	rec := New(testLogger(), mgr, c, cli, testControllerNS)

	_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
	require.True(t, IsRequeueAfterReason(err, RequeueReasonProxyProvisioning))

	entry := getProxyStatusEntry(t, cli, "my-proxy", testNamespace, testGateway)
	assert.Equal(t, "arn:new", entry.ProxyArn)
	assert.Equal(t, elbproxyutil.ELBProxyConfigurationReasonPending,
		meta.FindStatusCondition(entry.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType).Reason)
}

// TestPatchProxyHealth_FullListSkipsNewGateway: at the CRD limit a new Gateway isn't added (the
// API server would reject the patch), and existing entries still update.
func TestPatchProxyHealth_FullListSkipsNewGateway(t *testing.T) {
	proxy := makeClassScopedProxy("class-proxy", testControllerNS, "eg")
	for i := range maxProxyStatusEntries {
		proxy.Status.Proxies = append(proxy.Status.Proxies, elbproxyv1alpha1.ProxyStatusEntry{
			GatewayRef: elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: "ns", Name: fmt.Sprintf("gw-%d", i)},
		})
	}
	cli := testClient(proxy)
	rec := New(testLogger(), &fakeInfraManager{}, infrastore.New(), cli, testControllerNS)

	require.NoError(t, rec.patchProxyHealth(context.Background(), proxy, "ns/gw-extra", activeProxy("arn:extra")))
	require.NoError(t, rec.patchProxyHealth(context.Background(), proxy, "ns/gw-0", activeProxy("arn:0")))

	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	require.NoError(t, cli.Get(context.Background(), client.ObjectKeyFromObject(proxy), live))
	assert.Len(t, live.Status.Proxies, maxProxyStatusEntries)
	assert.Nil(t, findProxyStatusEntry(live.Status.Proxies, elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: "ns", Name: "gw-extra"}))
	existing := findProxyStatusEntry(live.Status.Proxies, elbproxyv1alpha1.GatewayReference{Kind: "Gateway", Namespace: "ns", Name: "gw-0"})
	require.NotNil(t, existing)
	assert.Equal(t, "arn:0", existing.ProxyArn)
}
