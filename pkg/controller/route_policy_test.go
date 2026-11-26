// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

func policyTarget(kind, name string) gwapiv1.LocalPolicyTargetReferenceWithSectionName {
	return gwapiv1.LocalPolicyTargetReferenceWithSectionName{
		LocalPolicyTargetReference: gwapiv1.LocalPolicyTargetReference{
			Group: gwapiv1.GroupName,
			Kind:  gwapiv1.Kind(kind),
			Name:  gwapiv1.ObjectName(name),
		},
	}
}

func egBackendRef(name string, port uint16) egv1a1.BackendRef {
	return egv1a1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: gwapiv1.ObjectName(name), Port: p(port)}}
}

func httpExtAuthPolicy(name, namespace string, target gwapiv1.LocalPolicyTargetReferenceWithSectionName, refs ...egv1a1.BackendRef) *egv1a1.SecurityPolicy {
	return &egv1a1.SecurityPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: egv1a1.SecurityPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{
				TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{target},
			},
			ExtAuth: &egv1a1.ExtAuth{
				HTTP: &egv1a1.HTTPExtAuthService{BackendCluster: egv1a1.BackendCluster{BackendRefs: refs}},
			},
		},
	}
}

func extProcPolicy(name, namespace string, target gwapiv1.LocalPolicyTargetReferenceWithSectionName, procs ...egv1a1.BackendRef) *egv1a1.EnvoyExtensionPolicy {
	eep := &egv1a1.EnvoyExtensionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: egv1a1.EnvoyExtensionPolicySpec{
			PolicyTargetReferences: egv1a1.PolicyTargetReferences{
				TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{target},
			},
		},
	}
	for _, ref := range procs {
		eep.Spec.ExtProc = append(eep.Spec.ExtProc, egv1a1.ExtProc{
			BackendCluster: egv1a1.BackendCluster{BackendRefs: []egv1a1.BackendRef{ref}},
		})
	}
	return eep
}

// httpRouteOn builds an HTTPRoute attached to the given Gateways with one backend on port 80.
func httpRouteOn(name, namespace string, gateways ...string) *gwapiv1.HTTPRoute {
	parents := make([]gwapiv1.ParentReference, 0, len(gateways))
	for _, gw := range gateways {
		parents = append(parents, gatewayParentRef(namespace, gw))
	}
	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: parents},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{
					{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "app", Port: p(80)}}},
				},
			}},
		},
	}
}

func TestCollectPortsByGateway_PolicyAndMirrorBackends(t *testing.T) {
	tcp := func(ports ...int32) []ir.PortProtocol {
		out := make([]ir.PortProtocol, 0, len(ports))
		for _, port := range ports {
			out = append(out, ir.PortProtocol{Protocol: "tcp", Port: port})
		}
		return out
	}
	authNS := gwapiv1.Namespace("auth")
	egGroup := gwapiv1.Group(egv1a1.GroupName)
	backendKind := gwapiv1.Kind("Backend")

	tests := []struct {
		name      string
		objs      []runtime.Object
		wantPorts map[string][]ir.PortProtocol
	}{
		{
			name: "ext_authz (HTTP) on an HTTPRoute opens the auth service port on the route's Gateway",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw"),
				httpExtAuthPolicy("sp", "default", policyTarget("HTTPRoute", "r"), egBackendRef("authz", 9001)),
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80, 9001)},
		},
		{
			name: "ext_authz (gRPC) with the deprecated targetRef and backendRef on a Gateway",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw"),
				&egv1a1.SecurityPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: "sp", Namespace: "default"},
					Spec: egv1a1.SecurityPolicySpec{
						PolicyTargetReferences: egv1a1.PolicyTargetReferences{TargetRef: ptr(policyTarget("Gateway", "gw"))},
						ExtAuth: &egv1a1.ExtAuth{GRPC: &egv1a1.GRPCExtAuthService{BackendCluster: egv1a1.BackendCluster{
							BackendRef: &gwapiv1.BackendObjectReference{Name: "authz-grpc", Port: p(9100)},
						}}},
					},
				},
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80, 9100)},
		},
		{
			name: "ext_proc with two processors on a GRPCRoute",
			objs: []runtime.Object{
				&gwapiv1.GRPCRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"},
					Spec: gwapiv1.GRPCRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")}},
						Rules: []gwapiv1.GRPCRouteRule{{
							BackendRefs: []gwapiv1.GRPCBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "grpc", Port: p(50051)}}},
							},
						}},
					},
				},
				extProcPolicy("eep", "default", policyTarget("GRPCRoute", "g"), egBackendRef("proc-a", 9002), egBackendRef("proc-b", 9003)),
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(9002, 9003, 50051)},
		},
		{
			name: "a policy on a route with two parent Gateways opens the port on both",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw-a", "gw-b"),
				extProcPolicy("eep", "default", policyTarget("HTTPRoute", "r"), egBackendRef("proc", 9002)),
			},
			wantPorts: map[string][]ir.PortProtocol{
				"default/gw-a": tcp(80, 9002),
				"default/gw-b": tcp(80, 9002),
			},
		},
		{
			name: "the policy backend Service's targetPort is opened, not its service port",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw"),
				httpExtAuthPolicy("sp", "default", policyTarget("HTTPRoute", "r"), egBackendRef("authz", 9001)),
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Name: "authz", Namespace: "default"},
					Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9001, TargetPort: intstr.FromInt32(19001)}}},
				},
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80, 19001)},
		},
		{
			name: "a cross-namespace policy backend resolves in the backend's namespace",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw"),
				httpExtAuthPolicy("sp", "default", policyTarget("HTTPRoute", "r"), egv1a1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{Name: "authz", Namespace: &authNS, Port: p(9001)},
				}),
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Name: "authz", Namespace: "auth"},
					Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9001, TargetPort: intstr.FromInt32(7000)}}},
				},
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80, 7000)},
		},
		{
			name: "a non-Service policy backend is skipped",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw"),
				httpExtAuthPolicy("sp", "default", policyTarget("HTTPRoute", "r"), egv1a1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{Group: &egGroup, Kind: &backendKind, Name: "external-auth", Port: p(9001)},
				}),
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80)},
		},
		{
			name: "a policy targeting a missing route adds nothing",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw"),
				httpExtAuthPolicy("sp", "default", policyTarget("HTTPRoute", "missing"), egBackendRef("authz", 9001)),
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80)},
		},
		{
			name: "a SecurityPolicy without ext_authz adds nothing",
			objs: []runtime.Object{
				httpRouteOn("r", "default", "gw"),
				&egv1a1.SecurityPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: "sp", Namespace: "default"},
					Spec: egv1a1.SecurityPolicySpec{
						PolicyTargetReferences: egv1a1.PolicyTargetReferences{
							TargetRefs: []gwapiv1.LocalPolicyTargetReferenceWithSectionName{policyTarget("HTTPRoute", "r")},
						},
						CORS: &egv1a1.CORS{AllowOrigins: []egv1a1.Origin{"https://allowed.example.com"}},
					},
				},
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80)},
		},
		{
			name: "RequestMirror targets on a rule and on a backendRef",
			objs: []runtime.Object{
				&gwapiv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
					Spec: gwapiv1.HTTPRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")}},
						Rules: []gwapiv1.HTTPRouteRule{{
							Filters: []gwapiv1.HTTPRouteFilter{{
								Type:          gwapiv1.HTTPRouteFilterRequestMirror,
								RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{BackendRef: gwapiv1.BackendObjectReference{Name: "mirror-a", Port: p(9200)}},
							}},
							BackendRefs: []gwapiv1.HTTPBackendRef{{
								BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "app", Port: p(80)}},
								Filters: []gwapiv1.HTTPRouteFilter{{
									Type:          gwapiv1.HTTPRouteFilterRequestMirror,
									RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{BackendRef: gwapiv1.BackendObjectReference{Name: "mirror-b", Port: p(9201)}},
								}},
							}},
						}},
					},
				},
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(80, 9200, 9201)},
		},
		{
			name: "a RequestMirror target on a GRPCRoute",
			objs: []runtime.Object{
				&gwapiv1.GRPCRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"},
					Spec: gwapiv1.GRPCRouteSpec{
						CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")}},
						Rules: []gwapiv1.GRPCRouteRule{{
							Filters: []gwapiv1.GRPCRouteFilter{{
								Type:          gwapiv1.GRPCRouteFilterRequestMirror,
								RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{BackendRef: gwapiv1.BackendObjectReference{Name: "mirror", Port: p(9300)}},
							}},
							BackendRefs: []gwapiv1.GRPCBackendRef{
								{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "grpc", Port: p(50051)}}},
							},
						}},
					},
				},
			},
			wantPorts: map[string][]ir.PortProtocol{"default/gw": tcp(9300, 50051)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRouteReconciler(infrastore.New(), tt.objs...)
			_, ports, err := r.collectPortsByGateway(context.Background(), "default")
			require.NoError(t, err)
			for key, want := range tt.wantPorts {
				got := ports[key]
				sortPorts(got)
				sortPorts(want)
				assert.Equal(t, want, got, "ports for %s", key)
			}
		})
	}
}

// A policy's ports belong to the Gateways it applies to and are released with them:
// after the policy is gone, a fresh collection no longer includes its backend port.
func TestCollectPortsByGateway_PolicyRemoved(t *testing.T) {
	route := httpRouteOn("r", "default", "gw")
	policy := httpExtAuthPolicy("sp", "default", policyTarget("HTTPRoute", "r"), egBackendRef("authz", 9001))
	r := newRouteReconciler(infrastore.New(), route, policy)

	_, ports, err := r.collectPortsByGateway(context.Background(), "default")
	require.NoError(t, err)
	got := ports["default/gw"]
	sortPorts(got)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 80}, {Protocol: "tcp", Port: 9001}}, got)

	require.NoError(t, r.client.Delete(context.Background(), policy))
	_, ports, err = r.collectPortsByGateway(context.Background(), "default")
	require.NoError(t, err)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, ports["default/gw"])
}

// A route can attach to a non-merged Gateway in another namespace. The route's backends, and
// the backends of the policies that target the route, belong to that Gateway.
func TestRouteReconciler_CrossNamespaceRouteAndPolicies(t *testing.T) {
	ctx := context.Background()
	objs := func() []runtime.Object {
		return []runtime.Object{
			&gwapiv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
				Spec: gwapiv1.HTTPRouteSpec{
					CommonRouteSpec: gwapiv1.CommonRouteSpec{
						ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("infra", "gw")},
					},
					Rules: []gwapiv1.HTTPRouteRule{{
						BackendRefs: []gwapiv1.HTTPBackendRef{
							{BackendRef: gwapiv1.BackendRef{BackendObjectReference: gwapiv1.BackendObjectReference{Name: "app", Port: p(80)}}},
						},
					}},
				},
			},
			&gwapiv1.GRPCRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps"},
				Spec: gwapiv1.GRPCRouteSpec{
					CommonRouteSpec: gwapiv1.CommonRouteSpec{
						ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("infra", "gw")},
					},
				},
			},
			httpExtAuthPolicy("web-authz", "apps", policyTarget("HTTPRoute", "web"), egBackendRef("authz", 9001)),
			extProcPolicy("web-proc", "apps", policyTarget("HTTPRoute", "web"), egBackendRef("proc", 9002)),
			httpExtAuthPolicy("api-authz", "apps", policyTarget("GRPCRoute", "api"), egBackendRef("authz-grpc", 9003)),
		}
	}
	want := []ir.PortProtocol{
		{Protocol: "tcp", Port: 80},
		{Protocol: "tcp", Port: 9001},
		{Protocol: "tcp", Port: 9002},
		{Protocol: "tcp", Port: 9003},
	}

	t.Run("updateBackendPorts", func(t *testing.T) {
		c := infrastore.New()
		c.SetInfra("infra/gw", makeWireInfra())
		r := newRouteReconciler(c, objs()...)

		require.NoError(t, r.updateBackendPorts(ctx, "infra/gw"))
		got := c.GetBackendPorts("infra/gw")
		sortPorts(got)
		assert.Equal(t, want, got)
	})

	t.Run("SyncBackendPorts", func(t *testing.T) {
		c := infrastore.New()
		c.SetInfra("infra/gw", makeWireInfra())
		r := newRouteReconciler(c, objs()...)

		require.NoError(t, r.SyncBackendPorts(ctx, ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Name: "infra/gw"}})))
		got := c.GetBackendPorts("infra/gw")
		sortPorts(got)
		assert.Equal(t, want, got)
	})
}

// When a Gateway's last backend-port source goes away, its cached ports must become empty,
// not stay stale, so the next reconcile revokes the node security group rules.
func TestRouteReconciler_UpdateBackendPorts_ClearsRemovedSources(t *testing.T) {
	ctx := context.Background()
	// redirectOnly has no backends, so it gives the Gateway no ports of its own.
	redirectOnly := func() *gwapiv1.HTTPRoute {
		return &gwapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
			Spec: gwapiv1.HTTPRouteSpec{
				CommonRouteSpec: gwapiv1.CommonRouteSpec{
					ParentRefs: []gwapiv1.ParentReference{gatewayParentRef("default", "gw")},
				},
				Rules: []gwapiv1.HTTPRouteRule{{
					Filters: []gwapiv1.HTTPRouteFilter{{
						Type:            gwapiv1.HTTPRouteFilterRequestRedirect,
						RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: ptr("https")},
					}},
				}},
			},
		}
	}
	policyKey := client.ObjectKey{Namespace: "default", Name: "policy"}

	tests := []struct {
		name      string
		key       string
		objs      []runtime.Object
		wantFirst []ir.PortProtocol
		remove    func(t *testing.T, c client.Client)
	}{
		{
			name:      "SecurityPolicy deleted",
			key:       "default/gw",
			objs:      []runtime.Object{redirectOnly(), httpExtAuthPolicy("policy", "default", policyTarget("HTTPRoute", "r"), egBackendRef("authz", 9001))},
			wantFirst: []ir.PortProtocol{{Protocol: "tcp", Port: 9001}},
			remove: func(t *testing.T, c client.Client) {
				require.NoError(t, c.Delete(ctx, &egv1a1.SecurityPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "policy"}}))
			},
		},
		{
			name:      "extAuth cleared",
			key:       "default/gw",
			objs:      []runtime.Object{redirectOnly(), httpExtAuthPolicy("policy", "default", policyTarget("HTTPRoute", "r"), egBackendRef("authz", 9001))},
			wantFirst: []ir.PortProtocol{{Protocol: "tcp", Port: 9001}},
			remove: func(t *testing.T, c client.Client) {
				sp := &egv1a1.SecurityPolicy{}
				require.NoError(t, c.Get(ctx, policyKey, sp))
				sp.Spec.ExtAuth = nil
				require.NoError(t, c.Update(ctx, sp))
			},
		},
		{
			name:      "extProc cleared",
			key:       "default/gw",
			objs:      []runtime.Object{redirectOnly(), extProcPolicy("policy", "default", policyTarget("HTTPRoute", "r"), egBackendRef("proc", 9002))},
			wantFirst: []ir.PortProtocol{{Protocol: "tcp", Port: 9002}},
			remove: func(t *testing.T, c client.Client) {
				eep := &egv1a1.EnvoyExtensionPolicy{}
				require.NoError(t, c.Get(ctx, policyKey, eep))
				eep.Spec.ExtProc = nil
				require.NoError(t, c.Update(ctx, eep))
			},
		},
		{
			name:      "last route deleted",
			key:       "default/gw",
			objs:      []runtime.Object{httpRouteOn("r", "default", "gw")},
			wantFirst: []ir.PortProtocol{{Protocol: "tcp", Port: 80}},
			remove: func(t *testing.T, c client.Client) {
				require.NoError(t, c.Delete(ctx, &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r"}}))
			},
		},
		{
			name: "merged gateway: last member route deleted",
			key:  "merged-eg",
			objs: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Name: "member", Namespace: "default"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
				},
				httpRouteOn("r", "default", "member"),
			},
			wantFirst: []ir.PortProtocol{{Protocol: "tcp", Port: 80}},
			remove: func(t *testing.T, c client.Client) {
				require.NoError(t, c.Delete(ctx, &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "r"}}))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := infrastore.New()
			c.SetInfra(tt.key, makeWireInfra())
			r := newRouteReconciler(c, tt.objs...)

			require.NoError(t, r.updateBackendPorts(ctx, tt.key))
			got := c.GetBackendPorts(tt.key)
			sortPorts(got)
			require.Equal(t, tt.wantFirst, got)

			tt.remove(t, r.client)
			require.NoError(t, r.updateBackendPorts(ctx, tt.key))
			got = c.GetBackendPorts(tt.key)
			assert.NotNil(t, got, "want an empty slice: nil deletes the entry and defers the gateway's reconcile")
			assert.Empty(t, got)
			assert.Empty(t, c.AllBackendPorts(), "no ports should remain for the node security groups")
		})
	}
}

// failingPolicyListClient fails List for SecurityPolicies. Routes are listed first, so the
// scan fails part way through.
type failingPolicyListClient struct {
	client.Client
}

func (c failingPolicyListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*egv1a1.SecurityPolicyList); ok {
		return errors.New("injected list error")
	}
	return c.Client.List(ctx, list, opts...)
}

// A failed scan must leave the cached ports alone, so an API error can't revoke ports in use.
func TestRouteReconciler_UpdateBackendPorts_FailedScanKeepsPorts(t *testing.T) {
	c := infrastore.New()
	c.SetInfra("default/gw", makeWireInfra())
	cached := []ir.PortProtocol{{Protocol: "tcp", Port: 9001}}
	c.SetBackendPorts("default/gw", cached)
	r := newRouteReconciler(c)
	r.client = failingPolicyListClient{Client: r.client}

	require.Error(t, r.updateBackendPorts(context.Background(), "default/gw"))
	assert.Equal(t, cached, c.GetBackendPorts("default/gw"))
}

// A policy that selects Gateways by label opens its backend port only on the selected
// Gateways.
func TestCollectPortsByGateway_PolicyTargetSelectors(t *testing.T) {
	policy := httpExtAuthPolicy("sp", "default", policyTarget("Gateway", "unused"), egBackendRef("authz", 9000))
	policy.Spec.TargetRefs = nil
	policy.Spec.TargetSelectors = []egv1a1.TargetSelector{{Kind: "Gateway", MatchLabels: map[string]string{"env": "prod"}}}
	r := newRouteReconciler(infrastore.New(),
		&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw-prod", Labels: map[string]string{"env": "prod"}}},
		&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw-dev", Labels: map[string]string{"env": "dev"}}},
		policy,
	)

	_, ports, err := r.collectPortsByGateway(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 9000}}, ports["default/gw-prod"])
	assert.NotContains(t, ports, "default/gw-dev")
}

// listenerSetFor returns a ListenerSet attached to the Gateway gateway in its namespace.
// Envoy Gateway's Accepted condition is set to True when accepted is true.
func listenerSetFor(namespace, name, gateway string, accepted bool) *gwapiv1.ListenerSet {
	ls := &gwapiv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       gwapiv1.ListenerSetSpec{ParentRef: gwapiv1.ParentGatewayReference{Name: gwapiv1.ObjectName(gateway)}},
	}
	if accepted {
		ls.Status.Conditions = []metav1.Condition{{
			Type: string(gwapiv1.ListenerSetConditionAccepted), Status: metav1.ConditionTrue, Reason: string(gwapiv1.ListenerSetReasonAccepted),
		}}
	}
	return ls
}

// httpRouteOnListenerSet builds an HTTPRoute attached to a ListenerSet, with one backend
// on the given port.
func httpRouteOnListenerSet(name, namespace, lsNamespace, lsName string, port uint16) *gwapiv1.HTTPRoute {
	route := httpRouteOn(name, namespace)
	ref := gatewayParentRef(lsNamespace, lsName)
	kind := gwapiv1.Kind("ListenerSet")
	ref.Kind = &kind
	route.Spec.ParentRefs = []gwapiv1.ParentReference{ref}
	route.Spec.Rules[0].BackendRefs[0].Port = p(port)
	return route
}

// Routes and policies attached to a ListenerSet open their backend ports on the Gateway the
// ListenerSet is attached to, but only once Envoy Gateway has accepted the ListenerSet.
func TestCollectPortsByGateway_ListenerSet(t *testing.T) {
	r := newRouteReconciler(infrastore.New(),
		listenerSetFor("infra", "ls", "gw", true),
		listenerSetFor("infra", "ls-pending", "gw", false),
		httpRouteOnListenerSet("r", "apps", "infra", "ls", 8080),
		httpRouteOnListenerSet("r-pending", "apps", "infra", "ls-pending", 8081),
		httpExtAuthPolicy("sp", "infra", policyTarget("ListenerSet", "ls"), egBackendRef("authz", 9000)),
	)

	_, ports, err := r.collectPortsByGateway(context.Background(), "")
	require.NoError(t, err)
	got := ports["infra/gw"]
	sortPorts(got)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 8080}, {Protocol: "tcp", Port: 9000}}, got)
}

// A ListenerSet attached to a member of a merged Gateway class opens its routes' backend
// ports on the merged proxy.
func TestRouteReconciler_UpdateBackendPorts_MergedGatewayListenerSet(t *testing.T) {
	c := infrastore.New()
	c.SetInfra("merged-eg", makeWireInfra())
	r := newRouteReconciler(c,
		&gwapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-1", Namespace: "default"},
			Spec:       gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"},
		},
		listenerSetFor("default", "ls", "merged-eg-1", true),
		httpRouteOnListenerSet("r", "default", "default", "ls", 8080),
	)

	require.NoError(t, r.updateBackendPorts(context.Background(), "merged-eg"))
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 8080}}, c.GetBackendPorts("merged-eg"))
}

// failingListenerSetListClient fails List for ListenerSets.
type failingListenerSetListClient struct {
	client.Client
}

func (c failingListenerSetListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*gwapiv1.ListenerSetList); ok {
		return errors.New("injected list error")
	}
	return c.Client.List(ctx, list, opts...)
}

// A failed ListenerSet list fails the scan, so the cached ports are kept rather than revoked.
func TestRouteReconciler_UpdateBackendPorts_ListenerSetListFailureKeepsPorts(t *testing.T) {
	c := infrastore.New()
	c.SetInfra("default/gw", makeWireInfra())
	cached := []ir.PortProtocol{{Protocol: "tcp", Port: 9001}}
	c.SetBackendPorts("default/gw", cached)
	r := newRouteReconciler(c)
	r.client = failingListenerSetListClient{Client: r.client}

	require.Error(t, r.updateBackendPorts(context.Background(), "default/gw"))
	assert.Equal(t, cached, c.GetBackendPorts("default/gw"))
}

// A Gateway that joins a merged class after the first members were registered still gets
// its routes' backend ports opened on the merged proxy. The class key itself is already
// in the index, as after the class's first infra push, so the class never has no members.
func TestRouteReconciler_UpdateBackendPorts_MergedGatewayLaterMember(t *testing.T) {
	c := infrastore.New()
	c.SetInfra("merged-eg", makeWireInfra())
	c.SetGatewayClass("merged-eg", "merged-eg")
	c.SetGatewayClass("default/merged-eg-1", "merged-eg")
	later := httpRouteOn("r-2", "default", "merged-eg-2")
	later.Spec.Rules[0].BackendRefs[0].Port = p(8081)
	r := newRouteReconciler(c,
		&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-1", Namespace: "default"}, Spec: gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"}},
		&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "merged-eg-2", Namespace: "default"}, Spec: gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"}},
		httpRouteOn("r-1", "default", "merged-eg-1"),
		later,
	)

	require.NoError(t, r.updateBackendPorts(context.Background(), "merged-eg"))
	got := c.GetBackendPorts("merged-eg")
	sortPorts(got)
	assert.Equal(t, []ir.PortProtocol{{Protocol: "tcp", Port: 80}, {Protocol: "tcp", Port: 8081}}, got)
	assert.ElementsMatch(t, []string{"merged-eg", "default/merged-eg-1", "default/merged-eg-2"}, c.GatewaysInClass("merged-eg"))
}

// A Gateway that leaves a merged class, or is deleted, stops counting for it.
func TestRouteReconciler_RegisterGatewayClassMembers_RemovesFormerMembers(t *testing.T) {
	c := infrastore.New()
	c.SetGatewayClass("merged-eg", "merged-eg")
	c.SetGatewayClass("default/moved", "merged-eg")
	c.SetGatewayClass("default/deleted", "merged-eg")
	r := newRouteReconciler(c,
		&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "stays", Namespace: "default"}, Spec: gwapiv1.GatewaySpec{GatewayClassName: "merged-eg"}},
		&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "moved", Namespace: "default"}, Spec: gwapiv1.GatewaySpec{GatewayClassName: "other"}},
	)

	require.NoError(t, r.registerGatewayClassMembers(context.Background(), "merged-eg"))
	assert.ElementsMatch(t, []string{"merged-eg", "default/stays"}, c.GatewaysInClass("merged-eg"))
	_, ok := c.GetGatewayClass("default/deleted")
	assert.False(t, ok)
}
