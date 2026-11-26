// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/controller/eventhandlers"
	elbproxyir "github.com/aws/elb-envoy-remote-infra-provider/pkg/elbproxyir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	elbproxyreconciler "github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
)

func watcherTestLogger() logging.Logger {
	return logging.DefaultLogger(os.Stdout, egv1a1.LogLevelInfo)
}

func watcherTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = gwapiv1.Install(s)
	_ = gwapiv1a2.Install(s)
	_ = elbproxyv1alpha1.AddToScheme(s)
	_ = egv1a1.AddToScheme(s)
	return s
}

// noopInfraManager satisfies reconciler.InfraManager without doing anything.
type noopInfraManager struct{}

func (n *noopInfraManager) CreateOrUpdateProxyInfra(_ context.Context, _ *elbproxyir.ELBProxyInfra) (*elbproxyaws.ProxyDetails, error) {
	return nil, nil
}
func (n *noopInfraManager) DeleteProxyInfra(_ context.Context, _ *ir.Infra) error { return nil }
func (n *noopInfraManager) ReconcileNodeSGRules(_ context.Context, _ []ir.PortProtocol, _ bool) error {
	return nil
}

// makeWireInfra builds a minimal pb.Infra for a gateway key.
func makeWireInfra() *ir.Infra {
	return ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Metadata: &pb.InfraMetadata{}}})
}

func makeProxy(name, namespace string) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

func makeProxyWithFinalizer(name, namespace string) *elbproxyv1alpha1.ELBProxyConfiguration {
	p := makeProxy(name, namespace)
	p.Finalizers = []string{ELBProxyConfigurationFinalizer}
	return p
}

func makeProxyBeingDeleted(name, namespace string) *elbproxyv1alpha1.ELBProxyConfiguration {
	p := makeProxyWithFinalizer(name, namespace)
	now := metav1.NewTime(time.Now())
	p.DeletionTimestamp = &now
	return p
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

func newGatewayReconciler(c *infrastore.Cache, objs ...runtime.Object) *GatewayReconciler {
	k8sClient := fake.NewClientBuilder().WithScheme(watcherTestScheme()).WithRuntimeObjects(objs...).WithStatusSubresource(&elbproxyv1alpha1.ELBProxyConfiguration{}).Build()
	rec := elbproxyreconciler.New(watcherTestLogger(), &noopInfraManager{}, c, k8sClient, "envoy-gateway-system")
	routeRec := &RouteReconciler{
		client:     k8sClient,
		log:        watcherTestLogger(),
		cache:      c,
		reconciler: rec,
	}
	return &GatewayReconciler{
		client:     k8sClient,
		log:        watcherTestLogger(),
		cache:      c,
		reconciler: rec,
		routeRec:   routeRec,
	}
}

func TestGatewayReconciler_HandleProxyUpdate(t *testing.T) {
	tests := []struct {
		name        string
		proxy       *elbproxyv1alpha1.ELBProxyConfiguration
		wantInCache bool
	}{
		{
			name:        "new proxy — finalizer added, returns early before caching",
			proxy:       makeProxy("my-proxy", "default"),
			wantInCache: false,
		},
		{
			name:        "proxy already has finalizer — cached",
			proxy:       makeProxyWithFinalizer("my-proxy", "default"),
			wantInCache: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			r := newGatewayReconciler(c, tt.proxy)

			err := r.handleProxyUpdate(context.Background(), tt.proxy)
			if !tt.wantInCache {
				// First call adds finalizer and requeues via a RequeueNeededAfter error
				// — not yet cached.
				var requeueErr *elbproxyreconciler.RequeueNeededAfter
				require.ErrorAs(t, err, &requeueErr)
			} else {
				require.NoError(t, err)
			}

			_, ok := c.GetELBProxyConfiguration(tt.proxy.Namespace + "/" + tt.proxy.Name)
			assert.Equal(t, tt.wantInCache, ok)
		})
	}
}

func TestGatewayReconciler_HandleProxyDelete(t *testing.T) {
	tests := []struct {
		name            string
		proxy           *elbproxyv1alpha1.ELBProxyConfiguration
		cacheInfraKeys  []string
		wantInCache     bool
		wantErr         bool
		wantErrContains string
	}{
		{
			name:        "proxy without finalizer — no-op",
			proxy:       makeProxy("my-proxy", "default"),
			wantInCache: true,
		},
		{
			name:        "proxy with finalizer, not in use — removed from cache",
			proxy:       makeProxyBeingDeleted("my-proxy", "default"),
			wantInCache: false,
		},
		{
			name: "proxy in use by gateway — error returned",
			proxy: func() *elbproxyv1alpha1.ELBProxyConfiguration {
				p := makeProxyTargetingGateway("my-proxy", "default", "gw")
				p.Finalizers = []string{ELBProxyConfigurationFinalizer}
				now := metav1.NewTime(time.Now())
				p.DeletionTimestamp = &now
				return p
			}(),
			cacheInfraKeys:  []string{"default/gw"},
			wantInCache:     true,
			wantErr:         true,
			wantErrContains: "still in use",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			key := tt.proxy.Namespace + "/" + tt.proxy.Name
			c.SetELBProxyConfiguration(key, tt.proxy)
			for _, k := range tt.cacheInfraKeys {
				c.SetInfra(k, makeWireInfra())
			}

			objs := []runtime.Object{tt.proxy}
			if len(tt.cacheInfraKeys) > 0 {
				objs = append(objs, &gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-class"},
				})
			}
			r := newGatewayReconciler(c, objs...)

			err := r.handleProxyDelete(context.Background(), tt.proxy)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
			} else {
				require.NoError(t, err)
			}

			_, ok := c.GetELBProxyConfiguration(key)
			assert.Equal(t, tt.wantInCache, ok)
		})
	}
}

func TestGatewayReconciler_Reconcile_ELBProxyConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		proxy       *elbproxyv1alpha1.ELBProxyConfiguration
		wantInCache bool
	}{
		{name: "proxy not found — no-op", proxy: nil, wantInCache: false},
		{name: "active proxy — cached", proxy: makeProxy("my-proxy", "default"), wantInCache: true},
		{name: "proxy being deleted — removed from cache", proxy: makeProxyBeingDeleted("my-proxy", "default"), wantInCache: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			var objs []runtime.Object
			key := "default/my-proxy"
			if tt.proxy != nil {
				objs = append(objs, tt.proxy)
				c.SetELBProxyConfiguration(key, tt.proxy)
			}
			r := newGatewayReconciler(c, objs...)

			req := eventhandlers.ELBProxyConfigurationRequest(types.NamespacedName{Name: "my-proxy", Namespace: "default"})
			_, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)

			_, ok := c.GetELBProxyConfiguration(key)
			assert.Equal(t, tt.wantInCache, ok)
		})
	}
}

// A Gateway's events must refresh its backend ports even when an ELBProxyConfiguration has the same
// namespace and name. They used to be handled as that ELBProxyConfiguration's events, which skip the
// refresh, so a route added after the first sync never opened its port on the node security groups.
func TestGatewayReconciler_Reconcile_GatewayNamedLikeItsProxy(t *testing.T) {
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: "default"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "my-gw")},
			},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{
					{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "echo", Port: p(8080)}}},
				},
			}},
		},
	}
	gateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gw", Namespace: "default"},
		Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-class"},
	}
	proxy := makeProxyTargetingGateway("my-gw", "default", "my-gw")
	proxy.Finalizers = []string{ELBProxyConfigurationFinalizer}

	c := infrastore.New()
	c.SetInfra("default/my-gw", makeWireInfra())
	// The first sync ran before the route existed.
	c.SetBackendPorts("default/my-gw", []ir.PortProtocol{})
	r := newGatewayReconciler(c, gateway, proxy, route)
	recorder := record.NewFakeRecorder(10)
	r.recorder = recorder

	gatewayKey := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-gw"}}
	_, err := r.Reconcile(context.Background(), gatewayKey)
	require.NoError(t, err)
	// The Service isn't in the fake client, so the route's service port is used.
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 8080}}, c.GetBackendPorts("default/my-gw"))
	require.Len(t, recorder.Events, 1, "the Gateway path emits one event")
	assert.Contains(t, <-recorder.Events, GatewayEventReasonSuccessfullyReconciled)

	// The proxy's own item still takes the ELBProxyConfiguration path, which caches the proxy.
	c.DeleteELBProxyConfiguration("default/my-gw")
	_, err = r.Reconcile(context.Background(), eventhandlers.ELBProxyConfigurationRequest(types.NamespacedName{Namespace: "default", Name: "my-gw"}))
	require.NoError(t, err)
	_, cached := c.GetELBProxyConfiguration("default/my-gw")
	assert.True(t, cached)
}

func TestGatewayReconciler_ProxyInUse(t *testing.T) {
	tests := []struct {
		name        string
		proxy       *elbproxyv1alpha1.ELBProxyConfiguration
		infraKeys   []string
		gatewayObjs []runtime.Object
		wantInUse   bool
	}{
		{
			name:      "no gateways in cache — not in use",
			proxy:     makeProxy("my-proxy", "default"),
			wantInUse: false,
		},
		{
			name:      "gateway-scoped proxy, gateway in cache — in use",
			proxy:     makeProxyTargetingGateway("my-proxy", "default", "gw"),
			infraKeys: []string{"default/gw"},
			gatewayObjs: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-class"},
				},
			},
			wantInUse: true,
		},
		{
			name:      "gateway-scoped proxy, different gateway in cache — not in use",
			proxy:     makeProxyTargetingGateway("my-proxy", "default", "gw"),
			infraKeys: []string{"other/other-gw"},
			gatewayObjs: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "other"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-class"},
				},
			},
			wantInUse: false,
		},
		{
			name: "class-scoped proxy, gateway in matching class — in use",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "envoy-gateway-system"},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: "eg-class"},
					},
				},
			},
			infraKeys: []string{"app/gw"},
			gatewayObjs: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "app"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-class"},
				},
			},
			wantInUse: true,
		},
		{
			name: "class-scoped proxy, gateway in different class — not in use",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "envoy-gateway-system"},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: "other-class"},
					},
				},
			},
			infraKeys: []string{"app/gw"},
			gatewayObjs: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "app"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-class"},
				},
			},
			wantInUse: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			c.SetELBProxyConfiguration(tt.proxy.Namespace+"/"+tt.proxy.Name, tt.proxy)
			for _, k := range tt.infraKeys {
				c.SetInfra(k, makeWireInfra())
			}

			p := tt.proxy.DeepCopy()
			p.Finalizers = []string{ELBProxyConfigurationFinalizer}
			now := metav1.Now()
			p.DeletionTimestamp = &now

			objs := []runtime.Object{p}
			objs = append(objs, tt.gatewayObjs...)
			r := newGatewayReconciler(c, objs...)

			err := r.handleProxyDelete(context.Background(), p)
			if tt.wantInUse {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "still in use")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestResolveReadyEndpoints tests the endpoint address extraction used by
// EnqueueRequestsForEndpointEvent. The actual filtering is tested via the
// handler's Update method in pkg/controller/eventhandlers.
func TestResolveReadyEndpoints(t *testing.T) {
	// This test is now covered by TestEnqueueRequestsForEndpointEvent_Update
	// in pkg/controller/eventhandlers/endpoint_test.go.
	// Kept as a placeholder to document the behavior.
	t.Skip("covered by eventhandlers.TestEnqueueRequestsForEndpointEvent_Update")
}

func TestGatewayReconciler_AllGatewayRequests(t *testing.T) {
	// requestFromGatewayKey converts gateway keys to reconcile.Requests.
	// When xDS pod IPs change, all cached gateway keys are enqueued so each
	// proxy gets an updated xds-endpoints.json on the next reconcile.
	tests := []struct {
		name      string
		infraKeys []string
		wantKeys  []string
	}{
		{
			name:      "empty cache — no requests",
			infraKeys: nil,
			wantKeys:  nil,
		},
		{
			name:      "non-merged gateways — one request per key",
			infraKeys: []string{"default/gw1", "default/gw2"},
			wantKeys:  []string{"default/gw1", "default/gw2"},
		},
		{
			name:      "merged gateway — bare class name enqueued",
			infraKeys: []string{"eg7", "default/gw1"},
			wantKeys:  []string{"eg7", "default/gw1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			for _, k := range tt.infraKeys {
				c.SetInfra(k, makeWireInfra())
			}

			keys := c.AllInfraKeys()
			reqs := make([]reconcile.Request, 0, len(keys))
			for _, k := range keys {
				reqs = append(reqs, requestFromGatewayKey(k))
			}

			got := make([]string, 0, len(reqs))
			for _, req := range reqs {
				got = append(got, gatewayKeyFromRequest(req))
			}

			assert.ElementsMatch(t, tt.wantKeys, got)
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
		{"eg7", "", "eg7"},                                   // merged: no namespace in request
		{"elbproxy-merged-prod", "", "elbproxy-merged-prod"}, // merged with dashes
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			req := requestFromGatewayKey(tt.key)
			assert.Equal(t, tt.wantNamespace, req.Namespace)
			assert.Equal(t, tt.wantName, req.Name)
			// Round-trip: key → request → key must be stable.
			assert.Equal(t, tt.key, gatewayKeyFromRequest(req))
		})
	}
}

func TestGatewayReconciler_HandleProxyUpdate_InvalidTargetRef(t *testing.T) {
	tests := []struct {
		name          string
		proxy         *elbproxyv1alpha1.ELBProxyConfiguration
		wantCondition bool
		wantReason    string
	}{
		{
			name: "class-scoped proxy outside controller namespace — InvalidTargetRef condition set",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "bad-proxy",
					Namespace:  "tenant-a",
					Finalizers: []string{ELBProxyConfigurationFinalizer},
				},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: "my-class"},
					},
				},
			},
			wantCondition: true,
			wantReason:    "InvalidTargetRef",
		},
		{
			name: "class-scoped proxy IN controller namespace — no InvalidTargetRef condition",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "good-proxy",
					Namespace:  "envoy-gateway-system",
					Finalizers: []string{ELBProxyConfigurationFinalizer},
				},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: "my-class"},
					},
				},
			},
			wantCondition: false,
		},
		{
			name: "gateway-scoped proxy outside controller namespace — no InvalidTargetRef condition",
			proxy: &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "gw-proxy",
					Namespace:  "tenant-a",
					Finalizers: []string{ELBProxyConfigurationFinalizer},
				},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{
					TargetRefs: []gwapiv1a2.LocalPolicyTargetReference{
						{Group: gwapiv1.GroupName, Kind: "Gateway", Name: "my-gw"},
					},
				},
			},
			wantCondition: false,
			wantReason:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			r := newGatewayReconciler(c, tt.proxy)

			err := r.handleProxyUpdate(context.Background(), tt.proxy)
			require.NoError(t, err)

			live := &elbproxyv1alpha1.ELBProxyConfiguration{}
			err = r.client.Get(context.Background(), types.NamespacedName{
				Namespace: tt.proxy.Namespace,
				Name:      tt.proxy.Name,
			}, live)
			require.NoError(t, err)

			if tt.wantCondition {
				require.NotEmpty(t, live.Status.Conditions, "expected status condition to be set")
				cond := meta.FindStatusCondition(live.Status.Conditions, "Accepted")
				require.NotNil(t, cond, "expected Accepted condition")
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, tt.wantReason, cond.Reason)
				assert.Contains(t, cond.Message, "GatewayClass-scoped targetRef")
				assert.Contains(t, cond.Message, tt.proxy.Namespace)
			} else {
				cond := meta.FindStatusCondition(live.Status.Conditions, "Accepted")
				if cond != nil {
					assert.NotEqual(t, "InvalidTargetRef", cond.Reason,
						"expected no InvalidTargetRef condition on a valid proxy")
				}
			}
		})
	}
}

// TestGatewayReconciler_HandleProxyUpdate_ClearsInvalidTargetRef verifies that
// the InvalidTargetRef condition set on a misconfigured proxy is cleared once
// the proxy is fixed, and that clearing never disturbs an unrelated
// Accepted/Conflicted condition written by the tiebreak resolver.
func TestGatewayReconciler_HandleProxyUpdate_ReconcileInvalidTargetRefCondition(t *testing.T) {
	classRef := []gwapiv1a2.LocalPolicyTargetReference{
		{Group: gwapiv1.GroupName, Kind: "GatewayClass", Name: "my-class"},
	}
	gatewayRef := []gwapiv1a2.LocalPolicyTargetReference{
		{Group: gwapiv1.GroupName, Kind: "Gateway", Name: "my-gw"},
	}

	tests := []struct {
		name string
		// seedReason, if non-empty, pre-seeds an Accepted=False condition with this reason.
		seedReason string
		namespace  string
		targetRefs []gwapiv1a2.LocalPolicyTargetReference
		// wantReason is the expected Accepted-condition reason after reconcile.
		// Empty means the condition should be absent.
		wantReason string
	}{
		{
			name:       "stale InvalidTargetRef cleared when class targetRef removed",
			seedReason: "InvalidTargetRef",
			namespace:  "tenant-a",
			targetRefs: gatewayRef,
			wantReason: "",
		},
		{
			name:       "stale InvalidTargetRef cleared when moved into controller namespace",
			seedReason: "InvalidTargetRef",
			namespace:  "envoy-gateway-system",
			targetRefs: classRef,
			wantReason: "",
		},
		{
			name:       "Conflicted condition preserved when reconciling a valid proxy",
			seedReason: "Conflicted",
			namespace:  "envoy-gateway-system",
			targetRefs: classRef,
			wantReason: "Conflicted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := &elbproxyv1alpha1.ELBProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "proxy",
					Namespace:  tt.namespace,
					Finalizers: []string{ELBProxyConfigurationFinalizer},
				},
				Spec: elbproxyv1alpha1.ELBProxyConfigurationSpec{TargetRefs: tt.targetRefs},
			}
			if tt.seedReason != "" {
				meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
					Type:    "Accepted",
					Status:  metav1.ConditionFalse,
					Reason:  tt.seedReason,
					Message: "seeded condition",
				})
			}

			c := infrastore.New()
			r := newGatewayReconciler(c, proxy)

			err := r.handleProxyUpdate(context.Background(), proxy)
			require.NoError(t, err)

			live := &elbproxyv1alpha1.ELBProxyConfiguration{}
			require.NoError(t, r.client.Get(context.Background(), types.NamespacedName{
				Namespace: proxy.Namespace,
				Name:      proxy.Name,
			}, live))

			cond := meta.FindStatusCondition(live.Status.Conditions, "Accepted")
			if tt.wantReason == "" {
				if cond != nil {
					assert.NotEqual(t, "InvalidTargetRef", cond.Reason,
						"stale InvalidTargetRef condition should be cleared once the proxy is fixed")
				}
				return
			}
			require.NotNil(t, cond, "expected Accepted condition to be preserved")
			assert.Equal(t, tt.wantReason, cond.Reason)
		})
	}
}

// A member Gateway that isn't in the class index yet is found from the Gateway itself,
// as long as its class has a merged proxy.
func TestGatewayReconciler_GatewayClassOfMember(t *testing.T) {
	gw := func(name, class string) *gwapiv1.Gateway {
		return &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: gwapiv1.GatewaySpec{GatewayClassName: gwapiv1.ObjectName(class)}}
	}
	c := infrastore.New()
	c.SetInfra("merged-eg", makeWireInfra())
	r := newGatewayReconciler(c, gw("new-member", "merged-eg"), gw("plain", "plain-class"))
	req := func(name string) reconcile.Request {
		return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}}
	}

	className, ok, err := r.gatewayClassOfMember(context.Background(), req("new-member"), "default/new-member")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "merged-eg", className)
	assert.Contains(t, c.GatewaysInClass("merged-eg"), "default/new-member")

	_, ok, err = r.gatewayClassOfMember(context.Background(), req("plain"), "default/plain")
	require.NoError(t, err)
	assert.False(t, ok, "a class without a merged proxy isn't a merged class")

	_, ok, err = r.gatewayClassOfMember(context.Background(), req("missing"), "default/missing")
	require.NoError(t, err)
	assert.False(t, ok)

	_, ok, err = r.gatewayClassOfMember(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "merged-eg"}}, "merged-eg")
	require.NoError(t, err)
	assert.False(t, ok, "the merged key itself isn't a member")
}
