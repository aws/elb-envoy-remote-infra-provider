// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// httpRouteWithMirror returns an HTTPRoute to "app" whose rule mirrors to mirrorSvc.
func httpRouteWithMirror(name, namespace, gw, mirrorSvc string) *gwapiv1.HTTPRoute {
	route := httpRouteToSvc(name, namespace, gw, "app", nil)
	route.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
		Type:          gwapiv1.HTTPRouteFilterRequestMirror,
		RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{BackendRef: objRef(mirrorSvc, 9200)},
	}}
	return route
}

func TestMirrorBackendRefs(t *testing.T) {
	route := httpRouteWithMirror("r", "ns", "gw", "mirror-rule")
	route.Spec.Rules[0].BackendRefs[0].Filters = []gwapiv1.HTTPRouteFilter{{
		Type:          gwapiv1.HTTPRouteFilterRequestMirror,
		RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{BackendRef: objRef("mirror-backend", 9201)},
	}}
	assert.Equal(t, []gwapiv1.BackendObjectReference{objRef("mirror-rule", 9200), objRef("mirror-backend", 9201)}, MirrorBackendRefs(route))

	grpc := &gwapiv1.GRPCRoute{Spec: gwapiv1.GRPCRouteSpec{Rules: []gwapiv1.GRPCRouteRule{{
		Filters: []gwapiv1.GRPCRouteFilter{{
			Type:          gwapiv1.GRPCRouteFilterRequestMirror,
			RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{BackendRef: objRef("grpc-mirror", 9300)},
		}},
	}}}}
	assert.Equal(t, []gwapiv1.BackendObjectReference{objRef("grpc-mirror", 9300)}, MirrorBackendRefs(grpc))

	assert.Empty(t, MirrorBackendRefs(httpRouteToSvc("r", "ns", "gw", "app", nil)), "no mirror filter")
}

func TestExtractBackendRefs_IncludesMirrorTargets(t *testing.T) {
	refs := ExtractBackendRefs(httpRouteWithMirror("r", "ns", "gw", "mirror"))
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, string(ref.Name))
	}
	assert.Equal(t, []string{"app", "mirror"}, names)
}

func TestEnqueueRequestsForRouteEvent_Update_MirrorChanged(t *testing.T) {
	h := NewEnqueueRequestsForRouteEvent(testLogger(), serviceTestClient())
	q := &fakeQueue[reconcile.Request]{}
	h.Update(context.Background(), event.TypedUpdateEvent[client.Object]{
		ObjectOld: httpRouteWithMirror("r", "ns", "gw", "mirror-old"),
		ObjectNew: httpRouteWithMirror("r", "ns", "gw", "mirror-new"),
	}, q)
	assert.Equal(t, []reconcile.Request{gatewayRequest("ns", "gw")}, q.items)
}

func TestGatewayKeysForService_MirrorTarget(t *testing.T) {
	c := serviceTestClient(httpRouteWithMirror("r", "default", "gw", "mirror"))
	keys, err := gatewayKeysForService(context.Background(), c, testLogger(), "default", "mirror")
	require.NoError(t, err)
	assert.Equal(t, []string{"default/gw"}, keys)
}
