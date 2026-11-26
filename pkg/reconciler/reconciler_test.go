// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	elbproxyir "github.com/aws/elb-envoy-remote-infra-provider/pkg/elbproxyir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// fakeInfraManager is a test double for InfraManager.
type fakeInfraManager struct {
	mu               sync.Mutex
	createErr        error
	createResult     *elbproxyaws.ProxyDetails
	createCalled     int
	deleteCalled     int
	onCreateOrUpdate func(*elbproxyir.ELBProxyInfra)
	// The arguments of the last ReconcileNodeSGRules call.
	nodeSGPorts          []ir.PortProtocol
	nodeSGGatewaysActive bool
}

func (f *fakeInfraManager) CreateOrUpdateProxyInfra(_ context.Context, infra *elbproxyir.ELBProxyInfra) (*elbproxyaws.ProxyDetails, error) {
	f.mu.Lock()
	f.createCalled++
	f.mu.Unlock()
	if f.onCreateOrUpdate != nil {
		f.onCreateOrUpdate(infra)
	}
	if f.createErr != nil {
		return f.createResult, f.createErr
	}
	if f.createResult != nil {
		return f.createResult, nil
	}
	return &elbproxyaws.ProxyDetails{}, nil
}

func (f *fakeInfraManager) DeleteProxyInfra(_ context.Context, _ *ir.Infra) error {
	f.deleteCalled++
	return nil
}

func (f *fakeInfraManager) ReconcileNodeSGRules(_ context.Context, allBackendPorts []ir.PortProtocol, gatewaysActive bool) error {
	f.nodeSGPorts = allBackendPorts
	f.nodeSGGatewaysActive = gatewaysActive
	return nil
}

const (
	testNamespace    = "ns"
	testGateway      = "gw"
	testGatewayKey   = testNamespace + "/" + testGateway
	testClass        = "eg-class"
	testControllerNS = "envoy-gateway-system"
)

func makeWireInfra() *ir.Infra {
	return ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{}})
}

func makeProxyTargetingGateway(name, namespace, gatewayName string) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Group: gwapiv1.GroupName, Kind: "Gateway", Name: gwapiv1.ObjectName(gatewayName)},
			},
		},
	}
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = gwapiv1.Install(s)
	_ = elbproxyv1alpha1.AddToScheme(s)
	return s
}

func testGatewayObj(name, namespace, className string) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       gwapiv1.GatewaySpec{GatewayClassName: gwapiv1.ObjectName(className)},
	}
}

func testClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(&elbproxyv1alpha1.ELBProxyConfiguration{}).
		Build()
}

func testLogger() logging.Logger {
	return logging.DefaultLogger(os.Stdout, egv1a1.LogLevelInfo)
}

func TestReconcileKey(t *testing.T) {
	tests := []struct {
		name          string
		setupCache    func(*infrastore.Cache)
		gatewayObjs   []client.Object
		createErr     error
		wantCallCount int
		wantErr       bool
	}{
		{
			name:          "no infra in cache — skip",
			setupCache:    func(c *infrastore.Cache) {},
			wantCallCount: 0,
		},
		{
			name: "infra exists but no ELBProxyConfiguration attaches — skip",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(testGatewayKey, makeWireInfra())
			},
			gatewayObjs:   []client.Object{testGatewayObj(testGateway, testNamespace, testClass)},
			wantCallCount: 0,
		},
		{
			name: "gateway-scoped ELBProxyConfiguration attaches — reconcile",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(testGatewayKey, makeWireInfra())
				c.SetELBProxyConfiguration(testNamespace+"/my-proxy", makeProxyTargetingGateway("my-proxy", testNamespace, testGateway))
				c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj(testGateway, testNamespace, testClass)},
			wantCallCount: 1,
		},
		{
			name: "attached ELBProxyConfiguration with xDS host and backend ports",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(testGatewayKey, makeWireInfra())
				c.SetELBProxyConfiguration(testNamespace+"/my-proxy", makeProxyTargetingGateway("my-proxy", testNamespace, testGateway))
				c.SetXDSHosts([]string{"10.0.0.1"})
				c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{{Protocol: "tcp", Port: 8080}})
			},
			gatewayObjs:   []client.Object{testGatewayObj(testGateway, testNamespace, testClass)},
			wantCallCount: 1,
		},
		{
			name: "infra manager returns error",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(testGatewayKey, makeWireInfra())
				c.SetELBProxyConfiguration(testNamespace+"/my-proxy", makeProxyTargetingGateway("my-proxy", testNamespace, testGateway))
				c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj(testGateway, testNamespace, testClass)},
			createErr:     errors.New("AWS error"),
			wantCallCount: 1,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			tt.setupCache(c)
			mgr := &fakeInfraManager{createErr: tt.createErr}
			rec := New(testLogger(), mgr, c, testClient(tt.gatewayObjs...), testControllerNS)

			_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCallCount, mgr.createCalled)
		})
	}
}

func TestReconcileKey_AssemblesCorrectELBProxyInfra(t *testing.T) {
	c := infrastore.New()
	c.SetInfra(testGatewayKey, makeWireInfra())
	c.SetELBProxyConfiguration(testNamespace+"/my-proxy", makeProxyTargetingGateway("my-proxy", testNamespace, testGateway))
	c.SetXDSHosts([]string{"10.0.0.1"})
	c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{{Protocol: "tcp", Port: 8080}})

	var captured *elbproxyir.ELBProxyInfra
	mgr := &fakeInfraManager{
		onCreateOrUpdate: func(infra *elbproxyir.ELBProxyInfra) { captured = infra },
	}
	rec := New(testLogger(), mgr, c, testClient(testGatewayObj(testGateway, testNamespace, testClass)), testControllerNS)

	_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, "my-proxy", captured.ELBProxyConfiguration.Name)
	assert.Equal(t, []string{"10.0.0.1"}, captured.XDSHosts)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 8080}}, captured.BackendTargetPorts)
}

func TestReconcileAll(t *testing.T) {
	tests := []struct {
		name          string
		setupCache    func(*infrastore.Cache)
		gatewayObjs   []client.Object
		createErr     error
		wantCallCount int
		wantErr       bool
	}{
		{
			name:          "empty cache — no calls",
			setupCache:    func(c *infrastore.Cache) {},
			wantCallCount: 0,
		},
		{
			name: "two gateways both attached — both reconciled",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetInfra("ns/gw2", makeWireInfra())
				c.SetELBProxyConfiguration("ns/proxy1", makeProxyTargetingGateway("proxy1", "ns", "gw1"))
				c.SetELBProxyConfiguration("ns/proxy2", makeProxyTargetingGateway("proxy2", "ns", "gw2"))
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
				c.SetBackendPorts("ns/gw2", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass), testGatewayObj("gw2", "ns", testClass)},
			wantCallCount: 2,
		},
		{
			name: "one gateway unattached — only one reconciled",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetInfra("ns/gw2", makeWireInfra())
				c.SetELBProxyConfiguration("ns/proxy1", makeProxyTargetingGateway("proxy1", "ns", "gw1"))
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
				c.SetBackendPorts("ns/gw2", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass), testGatewayObj("gw2", "ns", testClass)},
			wantCallCount: 1,
		},
		{
			name: "one gateway fails — error returned, others still reconciled",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetInfra("ns/gw2", makeWireInfra())
				c.SetELBProxyConfiguration("ns/proxy1", makeProxyTargetingGateway("proxy1", "ns", "gw1"))
				c.SetELBProxyConfiguration("ns/proxy2", makeProxyTargetingGateway("proxy2", "ns", "gw2"))
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
				c.SetBackendPorts("ns/gw2", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass), testGatewayObj("gw2", "ns", testClass)},
			createErr:     errors.New("AWS error"),
			wantCallCount: 2,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			tt.setupCache(c)
			mgr := &fakeInfraManager{createErr: tt.createErr}
			rec := New(testLogger(), mgr, c, testClient(tt.gatewayObjs...), testControllerNS)

			err := rec.ReconcileAll(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCallCount, mgr.createCalled)
		})
	}
}

func TestReconcileForELBProxyConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		setupCache    func(*infrastore.Cache)
		gatewayObjs   []client.Object
		proxyKey      string
		createErr     error
		wantCallCount int
		wantErr       bool
	}{
		{
			name: "only matching gateway reconciled",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetInfra("ns/gw2", makeWireInfra())
				c.SetELBProxyConfiguration("ns/proxy-a", makeProxyTargetingGateway("proxy-a", "ns", "gw1"))
				c.SetELBProxyConfiguration("ns/proxy-b", makeProxyTargetingGateway("proxy-b", "ns", "gw2"))
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
				c.SetBackendPorts("ns/gw2", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass), testGatewayObj("gw2", "ns", testClass)},
			proxyKey:      "ns/proxy-a",
			wantCallCount: 1,
		},
		{
			name: "no gateways attach to this proxy — no calls",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetELBProxyConfiguration("ns/other", makeProxyTargetingGateway("other", "ns", "gw1"))
				c.SetELBProxyConfiguration("ns/my-proxy", makeProxyTargetingGateway("my-proxy", "ns", "nobody"))
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass)},
			proxyKey:      "ns/my-proxy",
			wantCallCount: 0,
		},
		{
			name: "class-scoped proxy — all gateways in class reconciled",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetInfra("ns/gw2", makeWireInfra())
				c.SetGatewayClass("ns/gw1", testClass)
				c.SetGatewayClass("ns/gw2", testClass)
				c.SetELBProxyConfiguration("system/shared", &elbproxyv1alpha1.ELBProxyConfiguration{
					ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "system"},
					Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
						TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
							{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: testClass},
						},
					},
				})
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
				c.SetBackendPorts("ns/gw2", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass), testGatewayObj("gw2", "ns", testClass)},
			proxyKey:      "system/shared",
			wantCallCount: 2,
		},
		{
			name: "gateway-scoped overrides class-scoped — class proxy reconciles only unoverridden gateways",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetInfra("ns/gw2", makeWireInfra())
				c.SetGatewayClass("ns/gw1", testClass)
				c.SetGatewayClass("ns/gw2", testClass)
				c.SetELBProxyConfiguration("system/shared", &elbproxyv1alpha1.ELBProxyConfiguration{
					ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "system"},
					Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
						TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
							{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: testClass},
						},
					},
				})
				c.SetELBProxyConfiguration("ns/gw1-override", makeProxyTargetingGateway("gw1-override", "ns", "gw1"))
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
				c.SetBackendPorts("ns/gw2", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass), testGatewayObj("gw2", "ns", testClass)},
			proxyKey:      "system/shared",
			wantCallCount: 1, // only gw2 — gw1 is overridden
		},
		{
			name: "reconcile fails — error returned",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(testGatewayKey, makeWireInfra())
				c.SetELBProxyConfiguration(testNamespace+"/my-proxy", makeProxyTargetingGateway("my-proxy", testNamespace, testGateway))
				c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj(testGateway, testNamespace, testClass)},
			proxyKey:      testNamespace + "/my-proxy",
			createErr:     errors.New("AWS error"),
			wantCallCount: 1,
			wantErr:       true,
		},
		{
			name: "single proxy with multiple gateway targetRefs — both gateways reconciled",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra("ns/gw1", makeWireInfra())
				c.SetInfra("ns/gw2", makeWireInfra())
				c.SetELBProxyConfiguration("ns/shared", &elbproxyv1alpha1.ELBProxyConfiguration{
					ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "ns"},
					Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
						TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
							{Group: gwapiv1.GroupName, Kind: "Gateway", Name: "gw1"},
							{Group: gwapiv1.GroupName, Kind: "Gateway", Name: "gw2"},
						},
					},
				})
				c.SetBackendPorts("ns/gw1", []ir.PortProtocol{})
				c.SetBackendPorts("ns/gw2", []ir.PortProtocol{})
			},
			gatewayObjs:   []client.Object{testGatewayObj("gw1", "ns", testClass), testGatewayObj("gw2", "ns", testClass)},
			proxyKey:      "ns/shared",
			wantCallCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			tt.setupCache(c)
			mgr := &fakeInfraManager{createErr: tt.createErr}
			rec := New(testLogger(), mgr, c, testClient(tt.gatewayObjs...), "system")

			err := rec.ReconcileForELBProxyConfiguration(context.Background(), tt.proxyKey)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCallCount, mgr.createCalled)
		})
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name        string
		infra       *ir.Infra
		wantDeletes int
		wantErr     bool
	}{
		{name: "valid infra — delete called", infra: ir.New(&pb.Infra{}), wantDeletes: 1},
		{name: "nil infra — error", infra: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			mgr := &fakeInfraManager{}
			rec := New(testLogger(), mgr, c, testClient(), testControllerNS)

			err := rec.Delete(context.Background(), tt.infra)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantDeletes, mgr.deleteCalled)
		})
	}
}

func TestReconcileKey_PendingDeletion(t *testing.T) {
	tests := []struct {
		name            string
		setupCache      func(*infrastore.Cache)
		infraManager    InfraManager
		key             string
		wantErr         string
		wantPending     bool
		wantDeleteCalls int
	}{
		{
			name: "pending deletion triggers teardown and forgets key",
			setupCache: func(c *infrastore.Cache) {
				c.MarkPendingDeletion("default/gw")
			},
			infraManager:    &fakeInfraManager{},
			key:             "default/gw",
			wantPending:     false,
			wantDeleteCalls: 1,
		},
		{
			name: "pending deletion with delete error returns error for requeue",
			setupCache: func(c *infrastore.Cache) {
				c.MarkPendingDeletion("default/gw")
			},
			infraManager:    &failingDeleteInfraManager{},
			key:             "default/gw",
			wantErr:         "teardown incomplete for",
			wantPending:     true,
			wantDeleteCalls: 0, // not tracked on failingDeleteInfraManager
		},
		{
			name:            "non-pending unknown key is skipped",
			setupCache:      func(c *infrastore.Cache) {},
			infraManager:    &fakeInfraManager{},
			key:             "default/gw",
			wantPending:     false,
			wantDeleteCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			tt.setupCache(c)
			rec := New(testLogger(), tt.infraManager, c, testClient(), testControllerNS)

			_, err := rec.ReconcileKey(context.Background(), tt.key)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPending, c.IsPendingDeletion(tt.key))
			if mgr, ok := tt.infraManager.(*fakeInfraManager); ok {
				assert.Equal(t, tt.wantDeleteCalls, mgr.deleteCalled)
			}
		})
	}
}

// failingDeleteInfraManager is a test double that fails on DeleteProxyInfra.
type failingDeleteInfraManager struct {
	fakeInfraManager
}

func (f *failingDeleteInfraManager) DeleteProxyInfra(_ context.Context, _ *ir.Infra) error {
	return errors.New("SG DependencyViolation")
}

// reapplyDuringDeleteInfraManager re-pushes the gateway from a separate goroutine while
// DeleteProxyInfra is running, then blocks until that re-apply has committed. This mirrors
// the real race: the gRPC SetInfra and the teardown both mutate the cache under c.mu, outside
// the per-key reconcile lock (which is held across this call), so the two genuinely interleave.
type reapplyDuringDeleteInfraManager struct {
	fakeInfraManager
	cache *infrastore.Cache
	key   string
}

func (f *reapplyDuringDeleteInfraManager) DeleteProxyInfra(_ context.Context, _ *ir.Infra) error {
	done := make(chan struct{})
	go func() {
		f.cache.SetInfra(f.key, makeWireInfra())
		close(done)
	}()
	<-done
	return nil
}

// TestReconcileKey_ReapplyDuringTeardown verifies a Gateway re-applied mid-teardown keeps its
// pushed IR and is requeued, instead of being forgotten with no proxy. The re-apply races the
// teardown from another goroutine; run with -race to catch unsynchronized cache access.
func TestReconcileKey_ReapplyDuringTeardown(t *testing.T) {
	c := infrastore.New()
	c.MarkPendingDeletion(testGatewayKey)
	mgr := &reapplyDuringDeleteInfraManager{cache: c, key: testGatewayKey}
	rec := New(testLogger(), mgr, c, testClient(), testControllerNS)

	_, err := rec.ReconcileKey(context.Background(), testGatewayKey)
	require.True(t, IsRequeueError(err))
	assert.False(t, c.IsPendingDeletion(testGatewayKey))
	assert.True(t, c.IsKnownKey(testGatewayKey))
	_, ok := c.GetInfra(testGatewayKey)
	assert.True(t, ok, "re-applied IR must survive teardown")
}

// ---------------------------------------------------------------------------
// Merged gateway tests
// ---------------------------------------------------------------------------

const (
	mergedClass     = "merged-eg"
	mergedKey       = mergedClass // bare GatewayClass name — no namespace prefix
	mergedNamespace = "envoy-gateway-system"
)

// makeClassScopedProxy builds an ELBProxyConfiguration with a GatewayClass-scoped targetRef.
func makeClassScopedProxy(name, ns, className string) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
			TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
				{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: gwapiv1.ObjectName(className)},
			},
		},
	}
}

// TestReconcileKey_MergedGateway verifies that a merged gateway key (bare GatewayClass
// name, no namespace prefix) resolves only via a GatewayClass-scoped ELBProxyConfiguration and
// that CreateOrUpdateProxyInfra is called with the correct ELBProxyInfra.
func TestReconcileKey_MergedGateway(t *testing.T) {
	classProxy := makeClassScopedProxy("class-proxy", mergedNamespace, mergedClass)
	// A Gateway-scoped proxy targeting the bare class name as if it were a Gateway name.
	// This must NOT win for a merged gateway key.
	gwScopedProxy := makeProxyTargetingGateway("gw-proxy", mergedNamespace, mergedClass)

	tests := []struct {
		name          string
		setupCache    func(*infrastore.Cache)
		wantCallCount int
		wantProxy     string // expected ELBProxyConfiguration name passed to CreateOrUpdate
	}{
		{
			name: "class-scoped proxy attaches to merged gateway",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(mergedKey, makeWireInfra())
				c.SetELBProxyConfiguration(mergedNamespace+"/class-proxy", classProxy)
				c.SetGatewayClass(mergedKey, mergedClass)
				c.SetBackendPorts(mergedKey, []ir.PortProtocol{})
			},
			wantCallCount: 1,
			wantProxy:     "class-proxy",
		},
		{
			name: "gateway-scoped proxy ignored for merged gateway — no reconcile",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(mergedKey, makeWireInfra())
				c.SetELBProxyConfiguration(mergedNamespace+"/gw-proxy", gwScopedProxy)
				c.SetGatewayClass(mergedKey, mergedClass)
				c.SetBackendPorts(mergedKey, []ir.PortProtocol{})
			},
			wantCallCount: 0,
		},
		{
			name: "gateway-scoped proxy does not override class-scoped for merged gateway",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(mergedKey, makeWireInfra())
				c.SetELBProxyConfiguration(mergedNamespace+"/class-proxy", classProxy)
				c.SetELBProxyConfiguration(mergedNamespace+"/gw-proxy", gwScopedProxy)
				c.SetGatewayClass(mergedKey, mergedClass)
				c.SetBackendPorts(mergedKey, []ir.PortProtocol{})
			},
			wantCallCount: 1,
			wantProxy:     "class-proxy", // gateway-scoped must not win
		},
		{
			name: "no proxy at all — no reconcile",
			setupCache: func(c *infrastore.Cache) {
				c.SetInfra(mergedKey, makeWireInfra())
				c.SetGatewayClass(mergedKey, mergedClass)
				c.SetBackendPorts(mergedKey, []ir.PortProtocol{})
			},
			wantCallCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			tt.setupCache(c)

			var capturedProxy string
			mgr := &fakeInfraManager{
				onCreateOrUpdate: func(infra *elbproxyir.ELBProxyInfra) {
					if infra.ELBProxyConfiguration != nil {
						capturedProxy = infra.ELBProxyConfiguration.Name
					}
				},
			}
			// No Gateway object needed — merged key bypasses the Gateway API lookup
			// because gatewayClassFor finds the class in the cache directly.
			rec := New(testLogger(), mgr, c, testClient(), testControllerNS)

			_, err := rec.ReconcileKey(context.Background(), mergedKey)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCallCount, mgr.createCalled)
			if tt.wantProxy != "" {
				assert.Equal(t, tt.wantProxy, capturedProxy)
			}
		})
	}
}

// TestReconcileKey_MergedGateway_ELBProxyInfra verifies the ELBProxyInfra assembled
// for a merged gateway has the correct wire IR, proxy, ports, and xDS hosts.
func TestReconcileKey_MergedGateway_ELBProxyInfra(t *testing.T) {
	c := infrastore.New()
	wireInfra := makeWireInfra()
	classProxy := makeClassScopedProxy("class-proxy", mergedNamespace, mergedClass)
	ports := []ir.PortProtocol{{Protocol: "tcp", Port: 8080}, {Protocol: "tcp", Port: 8443}}

	c.SetInfra(mergedKey, wireInfra)
	c.SetELBProxyConfiguration(mergedNamespace+"/class-proxy", classProxy)
	c.SetGatewayClass(mergedKey, mergedClass)
	c.SetXDSHosts([]string{"10.0.0.1", "10.0.0.2"})
	c.SetBackendPorts(mergedKey, ports)

	var captured *elbproxyir.ELBProxyInfra
	mgr := &fakeInfraManager{
		onCreateOrUpdate: func(infra *elbproxyir.ELBProxyInfra) { captured = infra },
	}
	rec := New(testLogger(), mgr, c, testClient(), testControllerNS)

	_, err := rec.ReconcileKey(context.Background(), mergedKey)
	require.NoError(t, err)
	require.NotNil(t, captured, "CreateOrUpdateProxyInfra must be called")

	assert.Equal(t, wireInfra, captured.Infra, "wire IR must be passed through unchanged")
	assert.Equal(t, "class-proxy", captured.ELBProxyConfiguration.Name)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, captured.XDSHosts)
	assert.ElementsMatch(t, ports, captured.BackendTargetPorts)
}

// TestReconcileForELBProxyConfiguration_MergedGateway verifies that when a GatewayClass-scoped
// ELBProxyConfiguration changes, all gateways in that class are reconciled via the merged key.
func TestReconcileForELBProxyConfiguration_MergedGateway(t *testing.T) {
	classProxy := makeClassScopedProxy("class-proxy", mergedNamespace, mergedClass)
	proxyKey := mergedNamespace + "/class-proxy"

	c := infrastore.New()
	// The merged gateway key is the bare class name.
	c.SetInfra(mergedKey, makeWireInfra())
	c.SetELBProxyConfiguration(proxyKey, classProxy)
	// The merged key is registered in the class index so impactedGatewayKeys finds it.
	c.SetGatewayClass(mergedKey, mergedClass)
	c.SetBackendPorts(mergedKey, []ir.PortProtocol{})

	mgr := &fakeInfraManager{}
	rec := New(testLogger(), mgr, c, testClient(), testControllerNS)

	err := rec.ReconcileForELBProxyConfiguration(context.Background(), proxyKey)
	require.NoError(t, err)
	assert.Equal(t, 1, mgr.createCalled, "merged gateway must be reconciled when its class-scoped proxy changes")
}

// TestReconcileKey_PerKeyMutex verifies that two concurrent direct ReconcileKey
// calls for the same gateway key are serialized — only one CreateOrUpdateProxyInfra
// runs at a time. Uses an entered channel to make the test deterministic.
func TestReconcileKey_PerKeyMutex(t *testing.T) {
	c := infrastore.New()
	c.SetInfra(testGatewayKey, makeWireInfra())
	c.SetELBProxyConfiguration(testNamespace+"/my-proxy", makeProxyTargetingGateway("my-proxy", testNamespace, testGateway))
	c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})

	var (
		mu      sync.Mutex
		overlap bool
		inside  int
		barrier = make(chan struct{})
		entered = make(chan struct{}, 1)
	)

	mgr := &fakeInfraManager{
		onCreateOrUpdate: func(_ *elbproxyir.ELBProxyInfra) {
			mu.Lock()
			inside++
			if inside > 1 {
				overlap = true
			}
			mu.Unlock()
			select {
			case entered <- struct{}{}:
			default:
			}
			<-barrier
			mu.Lock()
			inside--
			mu.Unlock()
		},
	}

	rec := New(testLogger(), mgr, c, testClient(testGatewayObj(testGateway, testNamespace, testClass)), testControllerNS)

	done1 := make(chan error, 1)
	go func() { _, err := rec.ReconcileKey(context.Background(), testGatewayKey); done1 <- err }()

	<-entered // goroutine 1 is confirmed inside CreateOrUpdate

	done2 := make(chan error, 1)
	go func() { _, err := rec.ReconcileKey(context.Background(), testGatewayKey); done2 <- err }()

	time.Sleep(10 * time.Millisecond) // give goroutine 2 time to block on mutex
	close(barrier)

	require.NoError(t, <-done1)
	require.NoError(t, <-done2)
	assert.False(t, overlap, "same key: two ReconcileKey calls must not overlap inside CreateOrUpdateProxyInfra")
	assert.Equal(t, 2, mgr.createCalled, "both reconciles must complete")
}

// TestReconcileKey_ELBProxyConfigurationPathVsGatewayKeyPath verifies the actual cold-start
// overlap scenario: ReconcileForELBProxyConfiguration (proxy queue item) and ReconcileKey
// (gateway key queue item) both target the same gateway key concurrently.
// Without the keyedMutex both would call CreateOrUpdateProxyInfra simultaneously,
// causing PriorRequestNotCompletedException from the ELBProxy API.
func TestReconcileKey_ELBProxyConfigurationPathVsGatewayKeyPath(t *testing.T) {
	c := infrastore.New()
	c.SetInfra(testGatewayKey, makeWireInfra())
	c.SetELBProxyConfiguration(testNamespace+"/my-proxy", makeProxyTargetingGateway("my-proxy", testNamespace, testGateway))
	c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})

	var (
		mu      sync.Mutex
		overlap bool
		inside  int
		barrier = make(chan struct{})
		entered = make(chan struct{}, 1)
	)

	mgr := &fakeInfraManager{
		onCreateOrUpdate: func(_ *elbproxyir.ELBProxyInfra) {
			mu.Lock()
			inside++
			if inside > 1 {
				overlap = true
			}
			mu.Unlock()
			select {
			case entered <- struct{}{}:
			default:
			}
			<-barrier
			mu.Lock()
			inside--
			mu.Unlock()
		},
	}

	rec := New(testLogger(), mgr, c, testClient(testGatewayObj(testGateway, testNamespace, testClass)), testControllerNS)

	// Goroutine 1: gateway key queue path — direct ReconcileKey call.
	done1 := make(chan error, 1)
	go func() { _, err := rec.ReconcileKey(context.Background(), testGatewayKey); done1 <- err }()

	<-entered // goroutine 1 is confirmed inside CreateOrUpdate

	// Goroutine 2: ELBProxyConfiguration queue path — ReconcileForELBProxyConfiguration fans out to
	// the same gateway key via ReconcileKey internally.
	done2 := make(chan error, 1)
	go func() {
		err := rec.ReconcileForELBProxyConfiguration(context.Background(), testNamespace+"/my-proxy")
		done2 <- err
	}()

	time.Sleep(10 * time.Millisecond) // give goroutine 2 time to block on mutex
	close(barrier)

	require.NoError(t, <-done1)
	require.NoError(t, <-done2)
	assert.False(t, overlap, "ReconcileForELBProxyConfiguration and ReconcileKey must not overlap inside CreateOrUpdateProxyInfra for the same gateway key")
}

// TestReconcileKey_DifferentKeysConcurrent verifies that different gateway keys
// are NOT serialized — they can run concurrently.
func TestReconcileKey_DifferentKeysConcurrent(t *testing.T) {
	key1 := "ns/gw1"
	key2 := "ns/gw2"

	c := infrastore.New()
	c.SetInfra(key1, makeWireInfra())
	c.SetInfra(key2, makeWireInfra())
	c.SetELBProxyConfiguration("ns/proxy1", makeProxyTargetingGateway("proxy1", "ns", "gw1"))
	c.SetELBProxyConfiguration("ns/proxy2", makeProxyTargetingGateway("proxy2", "ns", "gw2"))
	c.SetBackendPorts(key1, []ir.PortProtocol{})
	c.SetBackendPorts(key2, []ir.PortProtocol{})

	var (
		mu            sync.Mutex
		maxConcurrent int
		inside        int
		barrier       = make(chan struct{})
	)

	mgr := &fakeInfraManager{
		onCreateOrUpdate: func(_ *elbproxyir.ELBProxyInfra) {
			mu.Lock()
			inside++
			if inside > maxConcurrent {
				maxConcurrent = inside
			}
			mu.Unlock()

			<-barrier

			mu.Lock()
			inside--
			mu.Unlock()
		},
	}

	gw1 := testGatewayObj("gw1", "ns", testClass)
	gw2 := testGatewayObj("gw2", "ns", testClass)
	rec := New(testLogger(), mgr, c, testClient(gw1, gw2), testControllerNS)

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)

	go func() { _, err := rec.ReconcileKey(context.Background(), key1); done1 <- err }()
	go func() { _, err := rec.ReconcileKey(context.Background(), key2); done2 <- err }()

	// Give both goroutines time to enter CreateOrUpdate before releasing.
	time.Sleep(20 * time.Millisecond)
	close(barrier)

	require.NoError(t, <-done1)
	require.NoError(t, <-done2)

	assert.Equal(t, 2, maxConcurrent, "different keys must be able to run concurrently")
}

// The xDS port must stay open while any gateway remains, even one whose routes give it no backend
// ports, and close once the last gateway is gone.
func TestReconcileNodeSGRules_KeepsXDSWhileGatewaysRemain(t *testing.T) {
	mgr := &fakeInfraManager{}
	c := infrastore.New()
	rec := New(testLogger(), mgr, c, testClient(), testControllerNS)

	c.SetInfra(testGatewayKey, makeWireInfra())
	c.SetBackendPorts(testGatewayKey, []ir.PortProtocol{})
	require.NoError(t, rec.ReconcileNodeSGRules(context.Background()))
	assert.Empty(t, mgr.nodeSGPorts)
	assert.True(t, mgr.nodeSGGatewaysActive, "a gateway with no backend ports is still active")

	c.MarkPendingDeletion(testGatewayKey)
	require.NoError(t, rec.ReconcileNodeSGRules(context.Background()))
	assert.False(t, mgr.nodeSGGatewaysActive, "no gateway remains after the last one is deleted")
}

// After a restart, gateways discovered from AWS are known before Envoy Gateway pushes their IR.
// Deleting another gateway in that window must keep the xDS port open for them.
func TestReconcileNodeSGRules_KeepsXDSForGatewaysSeededWithoutIR(t *testing.T) {
	mgr := &fakeInfraManager{}
	c := infrastore.New()
	rec := New(testLogger(), mgr, c, testClient(), testControllerNS)

	seededKey := testNamespace + "/seeded-gw"
	c.AddKnownKey(seededKey)
	c.SetInfra(testGatewayKey, makeWireInfra())

	c.MarkPendingDeletion(testGatewayKey)
	require.NoError(t, rec.ReconcileNodeSGRules(context.Background()))
	assert.True(t, mgr.nodeSGGatewaysActive, "a gateway seeded from AWS discovery is active before its IR arrives")

	c.MarkPendingDeletion(seededKey)
	require.NoError(t, rec.ReconcileNodeSGRules(context.Background()))
	assert.False(t, mgr.nodeSGGatewaysActive, "no gateway remains after the seeded one is deleted too")
}
