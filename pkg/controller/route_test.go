// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"sort"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	elbproxyreconciler "github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
)

// routeTestScheme registers all route types needed for the fake client.
func routeTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = discoveryv1.AddToScheme(s)
	_ = gwapiv1.Install(s)
	_ = gwapiv1a2.Install(s)
	_ = egv1a1.AddToScheme(s)
	return s
}

func newRouteReconciler(c *infrastore.Cache, objs ...runtime.Object) *RouteReconciler {
	k8sClient := fake.NewClientBuilder().WithScheme(routeTestScheme()).WithRuntimeObjects(objs...).Build()
	rec := elbproxyreconciler.New(watcherTestLogger(), &noopInfraManager{}, c, k8sClient, "envoy-gateway-system")
	return &RouteReconciler{
		client:     k8sClient,
		log:        watcherTestLogger(),
		cache:      c,
		reconciler: rec,
	}
}

func ptr[T any](v T) *T { return &v }

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

// p is a convenience helper for port numbers in test literals.
func p(n uint16) *gwapiv1.PortNumber { v := gwapiv1.PortNumber(n); return &v }

func sortPorts(ports []ir.PortProtocol) {
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		return ports[i].Protocol < ports[j].Protocol
	})
}

func TestIsServiceBackendRef(t *testing.T) {
	svcGroup := gwapiv1.Group("")
	svcKind := gwapiv1.Kind("Service")
	coreGroup := gwapiv1.Group("core")
	otherGroup := gwapiv1.Group("custom.io")
	otherKind := gwapiv1.Kind("Backend")

	tests := []struct {
		name  string
		group *gwapiv1.Group
		kind  *gwapiv1.Kind
		want  bool
	}{
		{"nil group and kind", nil, nil, true},
		{"empty group and kind", &svcGroup, &svcKind, true},
		{"core group", &coreGroup, &svcKind, true},
		{"non-service group", &otherGroup, &svcKind, false},
		{"non-service kind", &svcGroup, &otherKind, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isServiceBackendRef(tt.group, tt.kind))
		})
	}
}

func TestCollectPortsByGateway(t *testing.T) {
	customGroup := gwapiv1.Group("custom.io")
	customKind := gwapiv1.Kind("Backend")

	tests := []struct {
		name         string
		namespace    string
		objs         []runtime.Object
		wantAffected []string
		wantPorts    map[string][]ir.PortProtocol
	}{
		{
			name:      "HTTPRoute — two ports on same gateway",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "a", Port: p(80)}}},
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "b", Port: p(443)}}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw"},
			wantPorts: map[string][]ir.PortProtocol{
				"default/gw": {{Protocol: "tcp", Port: 80}, {Protocol: "tcp", Port: 443}},
			},
		},
		{
			name:      "GRPCRoute — tcp",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.GRPCRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
					Spec: gwapiv1.GRPCRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.GRPCRouteRule{{
							BackendRefs: []gwapiv1.GRPCBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: p(50051)}}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw"},
			wantPorts:    map[string][]ir.PortProtocol{"default/gw": {{Protocol: "tcp", Port: 50051}}},
		},
		{
			name:      "TCPRoute — tcp",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.TCPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
					Spec: gwapiv1.TCPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.TCPRouteRule{{
							BackendRefs: []gwapiv1.BackendRef{
								{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "db", Port: p(5432)}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw"},
			wantPorts:    map[string][]ir.PortProtocol{"default/gw": {{Protocol: "tcp", Port: 5432}}},
		},
		{
			name:      "TLSRoute — tcp",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.TLSRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
					Spec: gwapiv1.TLSRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.TLSRouteRule{{
							BackendRefs: []gwapiv1.BackendRef{
								{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: p(8443)}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw"},
			wantPorts:    map[string][]ir.PortProtocol{"default/gw": {{Protocol: "tcp", Port: 8443}}},
		},
		{
			name:      "UDPRoute — udp",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.UDPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
					Spec: gwapiv1.UDPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.UDPRouteRule{{
							BackendRefs: []gwapiv1.BackendRef{
								{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "dns", Port: p(53)}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw"},
			wantPorts:    map[string][]ir.PortProtocol{"default/gw": {{Protocol: "udp", Port: 53}}},
		},
		{
			name:      "duplicate ports across routes — deduplicated",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "a", Port: p(80)}}},
							},
						}},
					},
				},
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "b", Port: p(80)}}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw"},
			wantPorts:    map[string][]ir.PortProtocol{"default/gw": {{Protocol: "tcp", Port: 80}}},
		},
		{
			name:      "non-Service backend — gateway tracked but no ports",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{
									Group: &customGroup,
									Kind:  &customKind,
									Name:  "custom",
									Port:  p(8080),
								}}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw"},
			wantPorts:    map[string][]ir.PortProtocol{},
		},
		{
			name:      "multiple gateways — ports isolated per gateway",
			namespace: "default",
			objs: []runtime.Object{
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "http", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw-1")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: p(80)}}},
							},
						}},
					},
				},
				&gwapiv1.TCPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "tcp", Namespace: "default"},
					Spec: gwapiv1.TCPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw-2")},
						},
						Rules: []gwapiv1.TCPRouteRule{{
							BackendRefs: []gwapiv1.BackendRef{
								{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "db", Port: p(9000)}},
							},
						}},
					},
				},
			},
			wantAffected: []string{"default/gw-1", "default/gw-2"},
			wantPorts: map[string][]ir.PortProtocol{
				"default/gw-1": {{Protocol: "tcp", Port: 80}},
				"default/gw-2": {{Protocol: "tcp", Port: 9000}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			r := newRouteReconciler(c, tt.objs...)
			affected, ports, err := r.collectPortsByGateway(context.Background(), tt.namespace)
			require.NoError(t, err)

			for _, key := range tt.wantAffected {
				assert.Contains(t, affected, key)
			}
			for key, want := range tt.wantPorts {
				got := ports[key]
				sortPorts(got)
				sortPorts(want)
				assert.Equal(t, want, got, "ports for %s", key)
			}
		})
	}
}

func TestRouteReconciler_Reconcile(t *testing.T) {
	port80 := gwapiv1.PortNumber(80)
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "my-route", Namespace: "default"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "my-gw")},
			},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{
					{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: &port80}}},
				},
			}},
		},
	}

	c := infrastore.New()
	c.SetInfra("default/my-gw", makeWireInfra())

	r := newRouteReconciler(c, route)
	err := r.updateBackendPorts(context.Background(), "default/my-gw")
	require.NoError(t, err)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, c.GetBackendPorts("default/my-gw"))
}

func TestRouteReconciler_Reconcile_NoChange(t *testing.T) {
	port80 := gwapiv1.PortNumber(80)
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "my-route", Namespace: "default"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "my-gw")},
			},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{
					{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: &port80}}},
				},
			}},
		},
	}

	c := infrastore.New()
	c.SetInfra("default/my-gw", makeWireInfra())
	c.SetBackendPorts("default/my-gw", []ir.PortProtocol{{Protocol: "tcp", Port: 80}})

	r := newRouteReconciler(c, route)
	// updateBackendPorts only updates the cache — it does not call ReconcileKey.
	// The no-change case is verified by checking the cache is unchanged.
	err := r.updateBackendPorts(context.Background(), "default/my-gw")
	require.NoError(t, err)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, c.GetBackendPorts("default/my-gw"))
}

func TestRouteReconciler_SyncBackendPorts(t *testing.T) {
	port80 := gwapiv1.PortNumber(80)
	port3000 := gwapiv1.PortNumber(3000)

	makeInfra := func(name string) *ir.Infra {
		return ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Name: name}})
	}

	tests := []struct {
		name      string
		infra     *ir.Infra
		cacheKey  string
		objs      []runtime.Object
		wantPorts []ir.PortProtocol
	}{
		{
			name:     "non-merged: populates backend ports from existing routes on cold cache",
			infra:    makeInfra("default/my-gw"),
			cacheKey: "default/my-gw",
			objs: []runtime.Object{
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "my-route", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "my-gw")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: &port80}}},
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc2", Port: &port3000}}},
							},
						}},
					},
				},
			},
			wantPorts: []ir.PortProtocol{
				{Protocol: "tcp", Port: 80},
				{Protocol: "tcp", Port: 3000},
			},
		},
		{
			name:      "non-merged: sets empty slice for gateway with no routes",
			infra:     makeInfra("default/my-gw"),
			cacheKey:  "default/my-gw",
			objs:      nil,
			wantPorts: []ir.PortProtocol{},
		},
		{
			name:     "merged: aggregates ports from all individual gateways",
			infra:    makeInfra("merged-eg"),
			cacheKey: "merged-eg",
			objs: []runtime.Object{
				// Gateway objects needed so registerGatewayClassMembers can populate the class index.
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-1", Namespace: "default"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
				},
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-2", Namespace: "default"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
				},
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "merged-eg-1")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: &port80}}},
							},
						}},
					},
				},
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "route-2", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{
							ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "merged-eg-2")},
						},
						Rules: []gwapiv1.HTTPRouteRule{{
							BackendRefs: []gwapiv1.HTTPBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc2", Port: &port3000}}},
							},
						}},
					},
				},
			},
			wantPorts: []ir.PortProtocol{
				{Protocol: "tcp", Port: 80},
				{Protocol: "tcp", Port: 3000},
			},
		},
		{
			name:      "merged: sets empty slice when no routes exist",
			infra:     makeInfra("merged-eg"),
			cacheKey:  "merged-eg",
			objs:      nil,
			wantPorts: []ir.PortProtocol{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			c.SetInfra(tt.cacheKey, makeWireInfra())
			require.Nil(t, c.GetBackendPorts(tt.cacheKey), "precondition: cold cache")

			r := newRouteReconciler(c, tt.objs...)
			err := r.SyncBackendPorts(context.Background(), tt.infra)
			require.NoError(t, err)

			got := c.GetBackendPorts(tt.cacheKey)
			assert.NotNil(t, got, "should be empty slice, not nil")
			sortPorts(got)
			sortPorts(tt.wantPorts)
			assert.Equal(t, tt.wantPorts, got)
		})
	}
}

func TestRouteReconciler_Reconcile_MergedGateway(t *testing.T) {
	port80 := gwapiv1.PortNumber(80)
	port3000 := gwapiv1.PortNumber(3000)

	// Two routes referencing two individual gateways that belong to a merged fleet.
	// The infra cache key is the GatewayClass name "merged-eg".
	// Gateway objects are required so registerGatewayClassMembers can populate the
	// gatewaysByClass index — without them the merged key is skipped in Reconcile.
	objs := []runtime.Object{
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-1", Namespace: "default"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
		},
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-2", Namespace: "default"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
		},
		&gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "merged-eg-1")},
				},
				Rules: []gwapiv1.HTTPRouteRule{{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: &port80}}},
					},
				}},
			},
		},
		&gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-2", Namespace: "default"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "merged-eg-2")},
				},
				Rules: []gwapiv1.HTTPRouteRule{{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc2", Port: &port3000}}},
					},
				}},
			},
		},
	}

	c := infrastore.New()
	// Cache key is the GatewayClass name, not a namespace/name.
	c.SetInfra("merged-eg", makeWireInfra())

	r := newRouteReconciler(c, objs...)
	err := r.updateBackendPorts(context.Background(), "merged-eg")
	require.NoError(t, err)

	got := c.GetBackendPorts("merged-eg")
	require.NotNil(t, got)
	sortPorts(got)
	want := []ir.PortProtocol{{Protocol: "tcp", Port: 80}, {Protocol: "tcp", Port: 3000}}
	assert.Equal(t, want, got)
}

// TestRouteReconciler_Reconcile_NoPortLeakage verifies that a route change for one
// gateway does not update backend ports for an unrelated merged gateway fleet.
func TestRouteReconciler_Reconcile_NoPortLeakage(t *testing.T) {
	port80 := gwapiv1.PortNumber(80)
	port3000 := gwapiv1.PortNumber(3000)

	objs := []runtime.Object{
		// eg5 is a non-merged gateway with its own route.
		&gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "eg5-route", Namespace: "default"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "eg5")},
				},
				Rules: []gwapiv1.HTTPRouteRule{{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc", Port: &port80}}},
					},
				}},
			},
		},
		// merged-eg has its own gateway and route on a different port.
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-1", Namespace: "default"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
		},
		&gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "merged-route", Namespace: "default"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "merged-eg-1")},
				},
				Rules: []gwapiv1.HTTPRouteRule{{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc2", Port: &port3000}}},
					},
				}},
			},
		},
	}

	c := infrastore.New()
	c.SetInfra("default/eg5", makeWireInfra())
	c.SetInfra("merged-eg", makeWireInfra())

	r := newRouteReconciler(c, objs...)

	// Reconcile triggered by eg5-route change.
	err := r.updateBackendPorts(context.Background(), "default/eg5")
	require.NoError(t, err)

	// eg5 gets port 80.
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, c.GetBackendPorts("default/eg5"))

	// merged-eg must NOT pick up eg5's port 80.
	for _, p := range c.GetBackendPorts("merged-eg") {
		assert.NotEqual(t, ir.PortProtocol{Protocol: "tcp", Port: 80}, p,
			"merged-eg must not contain eg5's port 80")
	}
}

// TestRouteReconciler_RegisterGatewayClassMembers verifies that listing Gateways
// by GatewayClass name correctly populates the gatewaysByClass cache index.
func TestRouteReconciler_RegisterGatewayClassMembers(t *testing.T) {
	objs := []runtime.Object{
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-1", Namespace: "default"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "my-class"},
		},
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-2", Namespace: "default"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "my-class"},
		},
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "default"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "other-class"},
		},
	}

	c := infrastore.New()
	r := newRouteReconciler(c, objs...)

	err := r.registerGatewayClassMembers(context.Background(), "my-class")
	require.NoError(t, err)

	members := c.GatewaysInClass("my-class")
	assert.ElementsMatch(t, []string{"default/gw-1", "default/gw-2"}, members)

	// other-class gateway must not appear in my-class.
	assert.NotContains(t, members, "default/other-gw")
}

// TestRouteReconciler_Reconcile_MergedGateway_CrossNamespace verifies that when a
// merged gateway's member gateways span multiple namespaces, a route change in one
// namespace still aggregates ports from routes in all namespaces.
func TestRouteReconciler_Reconcile_MergedGateway_CrossNamespace(t *testing.T) {
	port80 := gwapiv1.PortNumber(80)
	port9090 := gwapiv1.PortNumber(9090)

	objs := []runtime.Object{
		// Two member gateways in different namespaces.
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-a", Namespace: "ns-a"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
		},
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "gw-b", Namespace: "ns-b"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
		},
		// Route in ns-a referencing gw-a.
		&gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-a", Namespace: "ns-a"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("ns-a", "gw-a")},
				},
				Rules: []gwapiv1.HTTPRouteRule{{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-a", Port: &port80}}},
					},
				}},
			},
		},
		// Route in ns-b referencing gw-b.
		&gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route-b", Namespace: "ns-b"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("ns-b", "gw-b")},
				},
				Rules: []gwapiv1.HTTPRouteRule{{
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "svc-b", Port: &port9090}}},
					},
				}},
			},
		},
	}

	c := infrastore.New()
	c.SetInfra("merged-eg", makeWireInfra())

	r := newRouteReconciler(c, objs...)

	// Reconcile triggered by route-a in ns-a only.
	err := r.updateBackendPorts(context.Background(), "merged-eg")
	require.NoError(t, err)

	got := c.GetBackendPorts("merged-eg")
	require.NotNil(t, got)
	sortPorts(got)

	// Must include ports from both ns-a (80) and ns-b (9090).
	want := []ir.PortProtocol{
		{Protocol: "tcp", Port: 80},
		{Protocol: "tcp", Port: 9090},
	}
	assert.Equal(t, want, got, "merged gateway must aggregate ports from all namespaces")
}

func TestResolveTargetPort(t *testing.T) {
	httpPortName := "http"
	tests := []struct {
		name        string
		svc         *corev1.Service
		extraObjs   []runtime.Object
		servicePort int32
		wantPort    int32
		wantErr     bool
	}{
		{
			name: "numeric targetPort differs from servicePort",
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default"},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 80, TargetPort: intstr.FromInt32(8080)},
					},
				},
			},
			servicePort: 80,
			wantPort:    8080,
		},
		{
			name: "targetPort omitted — defaults to servicePort",
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default"},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 3000},
					},
				},
			},
			servicePort: 3000,
			wantPort:    3000,
		},
		{
			name: "named targetPort — resolved from EndpointSlice",
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default"},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Name: "http", Port: 80, TargetPort: intstr.FromString("http")},
					},
				},
			},
			extraObjs: []runtime.Object{
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "svc-abc",
						Namespace: "default",
						Labels:    map[string]string{discoveryv1.LabelServiceName: "svc"},
					},
					Ports: []discoveryv1.EndpointPort{
						{Name: &httpPortName, Port: ptr(int32(8080))},
					},
				},
			},
			servicePort: 80,
			wantPort:    8080,
		},
		{
			name: "named targetPort — no EndpointSlice, falls back to servicePort",
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default"},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Name: "http", Port: 80, TargetPort: intstr.FromString("http")},
					},
				},
			},
			servicePort: 80,
			wantPort:    80,
		},
		{
			name:        "service not found — returns error",
			svc:         nil,
			servicePort: 80,
			wantErr:     true,
		},
		{
			name: "servicePort not found on Service — returns error",
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default"},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 443, TargetPort: intstr.FromInt32(8443)},
					},
				},
			},
			servicePort: 80,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			if tt.svc != nil {
				objs = append(objs, tt.svc)
			}
			objs = append(objs, tt.extraObjs...)
			c := infrastore.New()
			r := newRouteReconciler(c, objs...)

			got, err := r.resolveTargetPort(context.Background(), "default", "svc", tt.servicePort)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPort, got)
		})
	}
}

func TestCollectPortsByGateway_ResolvesTargetPort(t *testing.T) {
	// Service "backend" has servicePort=8080, targetPort=3000.
	// The route references servicePort 8080, but the collected port should be 3000.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 8080, TargetPort: intstr.FromInt32(3000)},
			},
		},
	}
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
			},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{
					{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "backend", Port: p(8080)}}},
				},
			}},
		},
	}

	c := infrastore.New()
	r := newRouteReconciler(c, svc, route)
	_, ports, err := r.collectPortsByGateway(context.Background(), "default")
	require.NoError(t, err)

	want := []ir.PortProtocol{{Protocol: "tcp", Port: 3000}}
	assert.Equal(t, want, ports["default/gw"])
}

func TestCollectPortsByGateway_FallsBackToServicePort(t *testing.T) {
	// No Service exists — should fall back to the service port from backendRef.
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
			},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{
					{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "missing-svc", Port: p(9090)}}},
				},
			}},
		},
	}

	c := infrastore.New()
	r := newRouteReconciler(c, route)
	_, ports, err := r.collectPortsByGateway(context.Background(), "default")
	require.NoError(t, err)

	// Falls back to service port since Service doesn't exist.
	want := []ir.PortProtocol{{Protocol: "tcp", Port: 9090}}
	assert.Equal(t, want, ports["default/gw"])
}
