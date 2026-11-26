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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func serviceTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = gwapiv1.Install(s)
	_ = gwapiv1a2.Install(s)
	_ = egv1a1.AddToScheme(s)
	return s
}

func serviceTestClient(objs ...client.Object) client.Client {
	b := fake.NewClientBuilder().WithScheme(serviceTestScheme()).WithObjects(objs...)
	// Register the same backend serviceRef indexers the controller registers via
	// RegisterRouteBackendIndexers and RegisterPolicyBackendIndexers, so MatchingFields
	// lookups resolve in tests.
	for _, routeType := range routeTypesForIndex() {
		b = b.WithIndex(routeType, IndexKeyBackendServiceRefName, indexFuncBackendServiceRefName)
	}
	for _, policyType := range PolicyTypes() {
		b = b.WithIndex(policyType, IndexKeyBackendServiceRefName, indexFuncPolicyBackendServiceRefName)
	}
	return b.Build()
}

func svcPort(n uint16) *gwapiv1.PortNumber { v := gwapiv1.PortNumber(n); return &v }

func intstrFromInt(n int32) intstr.IntOrString { return intstr.FromInt32(n) }

// httpRouteToSvc builds an HTTPRoute referencing a Service backend.
func httpRouteToSvc(name, namespace, gw, svc string, svcNamespace *string) *gwapiv1.HTTPRoute {
	ref := gwapiv1.BackendObjectReference{Name: gwapiv1.ObjectName(svc), Port: svcPort(80)}
	if svcNamespace != nil {
		ns := gwapiv1.Namespace(*svcNamespace)
		ref.Namespace = &ns
	}
	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{gatewayParentRef(namespace, gw)},
			},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{
					{BackendRef: gwapiv1.BackendRef{BackendObjectReference: ref}},
				},
			}},
		},
	}
}

func makeService(name, namespace string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

func TestGatewayKeysForService(t *testing.T) {
	otherNS := "other-ns"
	tests := []struct {
		name         string
		objs         []client.Object
		svcNamespace string
		svcName      string
		wantKeys     []string
	}{
		{
			name:         "single route references service",
			objs:         []client.Object{httpRouteToSvc("r", "default", "gw", "svc", nil)},
			svcNamespace: "default",
			svcName:      "svc",
			wantKeys:     []string{"default/gw"},
		},
		{
			name: "two routes, two gateways reference same service",
			objs: []client.Object{
				httpRouteToSvc("r1", "default", "gw1", "svc", nil),
				httpRouteToSvc("r2", "default", "gw2", "svc", nil),
			},
			svcNamespace: "default",
			svcName:      "svc",
			wantKeys:     []string{"default/gw1", "default/gw2"},
		},
		{
			name:         "service not referenced — no keys",
			objs:         []client.Object{httpRouteToSvc("r", "default", "gw", "other-svc", nil)},
			svcNamespace: "default",
			svcName:      "svc",
			wantKeys:     nil,
		},
		{
			name:         "cross-namespace backendRef matches by ref namespace",
			objs:         []client.Object{httpRouteToSvc("r", "default", "gw", "svc", &otherNS)},
			svcNamespace: "other-ns",
			svcName:      "svc",
			wantKeys:     []string{"default/gw"},
		},
		{
			name:         "same service name in different namespace not matched without ref namespace",
			objs:         []client.Object{httpRouteToSvc("r", "default", "gw", "svc", nil)},
			svcNamespace: "other-ns",
			svcName:      "svc",
			wantKeys:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := serviceTestClient(tt.objs...)
			keys, err := gatewayKeysForService(context.Background(), c, testLogger(), tt.svcNamespace, tt.svcName)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantKeys, keys)
		})
	}
}

func TestEnqueueRequestsForServiceEvent_Create(t *testing.T) {
	c := serviceTestClient(httpRouteToSvc("r", "default", "gw", "svc", nil))
	h := NewEnqueueRequestsForServiceEvent(testLogger(), c)
	q := &fakeQueue[reconcile.Request]{}

	h.Create(context.Background(),
		event.TypedCreateEvent[*corev1.Service]{Object: makeService("svc", "default")}, q)

	require.Equal(t, 1, len(q.items))
	assert.Equal(t, "default", q.items[0].Namespace)
	assert.Equal(t, "gw", q.items[0].Name)
}

func TestEnqueueRequestsForServiceEvent_Update(t *testing.T) {
	tests := []struct {
		name      string
		oldPorts  []corev1.ServicePort
		newPorts  []corev1.ServicePort
		wantItems int
	}{
		{
			name:      "ports unchanged — skipped",
			oldPorts:  []corev1.ServicePort{{Port: 80, TargetPort: intstrFromInt(8080)}},
			newPorts:  []corev1.ServicePort{{Port: 80, TargetPort: intstrFromInt(8080)}},
			wantItems: 0,
		},
		{
			name:      "targetPort changed — enqueued",
			oldPorts:  []corev1.ServicePort{{Port: 80, TargetPort: intstrFromInt(8080)}},
			newPorts:  []corev1.ServicePort{{Port: 80, TargetPort: intstrFromInt(9090)}},
			wantItems: 1,
		},
		{
			name:      "port added — enqueued",
			oldPorts:  []corev1.ServicePort{{Port: 80, TargetPort: intstrFromInt(8080)}},
			newPorts:  []corev1.ServicePort{{Port: 80, TargetPort: intstrFromInt(8080)}, {Port: 443, TargetPort: intstrFromInt(8443)}},
			wantItems: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := serviceTestClient(httpRouteToSvc("r", "default", "gw", "svc", nil))
			h := NewEnqueueRequestsForServiceEvent(testLogger(), c)
			q := &fakeQueue[reconcile.Request]{}

			h.Update(context.Background(), event.TypedUpdateEvent[*corev1.Service]{
				ObjectOld: makeService("svc", "default", tt.oldPorts...),
				ObjectNew: makeService("svc", "default", tt.newPorts...),
			}, q)

			assert.Equal(t, tt.wantItems, len(q.items))
		})
	}
}
