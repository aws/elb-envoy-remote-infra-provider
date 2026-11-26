// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"maps"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

// EnqueueRequestsForRouteEvent implements handler.TypedEventHandler[client.Object, reconcile.Request]
// for all route types (HTTPRoute, GRPCRoute, TCPRoute, TLSRoute, UDPRoute). It maps each route
// event to reconcile.Requests for the gateway keys referenced in the route's parentRefs.
//
// Update filter: skips the enqueue when parentRefs, backendRefs and labels are unchanged.
type EnqueueRequestsForRouteEvent struct {
	log    logging.Logger
	client client.Client
}

var _ handler.TypedEventHandler[client.Object, reconcile.Request] = &EnqueueRequestsForRouteEvent{}

// NewEnqueueRequestsForRouteEvent returns a new handler for route events. It reads
// ListenerSets through c.
func NewEnqueueRequestsForRouteEvent(log logging.Logger, c client.Client) *EnqueueRequestsForRouteEvent {
	return &EnqueueRequestsForRouteEvent{log: log, client: c}
}

// Create enqueues gateway keys from the route's parentRefs unconditionally.
func (h *EnqueueRequestsForRouteEvent) Create(
	ctx context.Context,
	e event.TypedCreateEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	for _, req := range h.requestsForRoute(ctx, e.Object) {
		q.Add(req)
	}
}

// Update enqueues gateway keys only when parentRefs, backendRefs or labels have changed.
func (h *EnqueueRequestsForRouteEvent) Update(
	ctx context.Context,
	e event.TypedUpdateEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if routeUnchanged(e.ObjectOld, e.ObjectNew) {
		h.log.V(1).Info("skipping route update: parentRefs, backendRefs and labels unchanged",
			"namespace", e.ObjectNew.GetNamespace(), "name", e.ObjectNew.GetName())
		return
	}
	// Enqueue for both old and new parentRefs so gateways that were removed from
	// parentRefs also get a reconcile pass.
	seen := make(map[string]struct{})
	for _, req := range h.requestsForRoute(ctx, e.ObjectOld) {
		key := req.Namespace + "/" + req.Name
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			q.Add(req)
		}
	}
	for _, req := range h.requestsForRoute(ctx, e.ObjectNew) {
		key := req.Namespace + "/" + req.Name
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			q.Add(req)
		}
	}
}

// Delete enqueues gateway keys from the route's parentRefs unconditionally.
func (h *EnqueueRequestsForRouteEvent) Delete(
	ctx context.Context,
	e event.TypedDeleteEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	for _, req := range h.requestsForRoute(ctx, e.Object) {
		q.Add(req)
	}
}

// Generic enqueues gateway keys from the route's parentRefs unconditionally.
func (h *EnqueueRequestsForRouteEvent) Generic(
	ctx context.Context,
	e event.TypedGenericEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	for _, req := range h.requestsForRoute(ctx, e.Object) {
		q.Add(req)
	}
}

// requestsForRoute returns one reconcile.Request per gateway key in the route's parentRefs.
func (h *EnqueueRequestsForRouteEvent) requestsForRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	refs := ExtractParentRefs(obj)
	keys := GatewayKeysForParentRefs(obj.GetNamespace(), refs, listenerSetParentFromClient(ctx, h.client, h.log))
	reqs := make([]reconcile.Request, 0, len(keys))
	for _, k := range keys {
		reqs = append(reqs, requestFromGatewayKey(k))
	}
	return reqs
}

// routeUnchanged returns true when parentRefs, backendRefs and labels are identical
// between old and new. Labels matter because policy targetSelectors select routes by them.
func routeUnchanged(old, new client.Object) bool {
	return reflect.DeepEqual(ExtractParentRefs(old), ExtractParentRefs(new)) &&
		reflect.DeepEqual(ExtractBackendRefs(old), ExtractBackendRefs(new)) &&
		maps.Equal(old.GetLabels(), new.GetLabels())
}

// ExtractParentRefs extracts ParentReferences from any supported route type.
func ExtractParentRefs(obj client.Object) []gwapiv1.ParentReference {
	switch r := obj.(type) {
	case *gwapiv1.HTTPRoute:
		return r.Spec.ParentRefs
	case *gwapiv1.GRPCRoute:
		return r.Spec.ParentRefs
	case *gwapiv1.TCPRoute:
		return r.Spec.ParentRefs
	case *gwapiv1.TLSRoute:
		return r.Spec.ParentRefs
	case *gwapiv1.UDPRoute:
		return r.Spec.ParentRefs
	}
	return nil
}

// ExtractBackendRefs extracts BackendRefs from any supported route type, including
// the targets of RequestMirror filters on HTTPRoutes and GRPCRoutes.
func ExtractBackendRefs(obj client.Object) []gwapiv1.BackendRef {
	switch r := obj.(type) {
	case *gwapiv1.HTTPRoute:
		var refs []gwapiv1.BackendRef
		for _, rule := range r.Spec.Rules {
			for _, br := range rule.BackendRefs {
				refs = append(refs, br.BackendRef)
			}
		}
		for _, mirror := range MirrorBackendRefs(obj) {
			refs = append(refs, gwapiv1.BackendRef{BackendObjectReference: mirror})
		}
		return refs
	case *gwapiv1.GRPCRoute:
		var refs []gwapiv1.BackendRef
		for _, rule := range r.Spec.Rules {
			for _, br := range rule.BackendRefs {
				refs = append(refs, br.BackendRef)
			}
		}
		for _, mirror := range MirrorBackendRefs(obj) {
			refs = append(refs, gwapiv1.BackendRef{BackendObjectReference: mirror})
		}
		return refs
	case *gwapiv1.TCPRoute:
		var refs []gwapiv1.BackendRef
		for _, rule := range r.Spec.Rules {
			refs = append(refs, rule.BackendRefs...)
		}
		return refs
	case *gwapiv1.TLSRoute:
		var refs []gwapiv1.BackendRef
		for _, rule := range r.Spec.Rules {
			refs = append(refs, rule.BackendRefs...)
		}
		return refs
	case *gwapiv1.UDPRoute:
		var refs []gwapiv1.BackendRef
		for _, rule := range r.Spec.Rules {
			refs = append(refs, rule.BackendRefs...)
		}
		return refs
	}
	return nil
}

// MirrorBackendRefs returns the targets of the RequestMirror filters on an HTTPRoute
// or GRPCRoute, both on rules and on individual backendRefs. The proxy sends mirrored
// traffic to these backends, so their ports must be reachable like other backends.
func MirrorBackendRefs(obj client.Object) []gwapiv1.BackendObjectReference {
	var refs []gwapiv1.BackendObjectReference
	switch r := obj.(type) {
	case *gwapiv1.HTTPRoute:
		for _, rule := range r.Spec.Rules {
			refs = append(refs, httpMirrorRefs(rule.Filters)...)
			for _, br := range rule.BackendRefs {
				refs = append(refs, httpMirrorRefs(br.Filters)...)
			}
		}
	case *gwapiv1.GRPCRoute:
		for _, rule := range r.Spec.Rules {
			refs = append(refs, grpcMirrorRefs(rule.Filters)...)
			for _, br := range rule.BackendRefs {
				refs = append(refs, grpcMirrorRefs(br.Filters)...)
			}
		}
	}
	return refs
}

func httpMirrorRefs(filters []gwapiv1.HTTPRouteFilter) []gwapiv1.BackendObjectReference {
	var refs []gwapiv1.BackendObjectReference
	for _, f := range filters {
		if f.RequestMirror != nil {
			refs = append(refs, f.RequestMirror.BackendRef)
		}
	}
	return refs
}

func grpcMirrorRefs(filters []gwapiv1.GRPCRouteFilter) []gwapiv1.BackendObjectReference {
	var refs []gwapiv1.BackendObjectReference
	for _, f := range filters {
		if f.RequestMirror != nil {
			refs = append(refs, f.RequestMirror.BackendRef)
		}
	}
	return refs
}

// GatewayKeysForParentRefs returns "namespace/name" strings for the Gateways in a route's
// parentRefs. A ListenerSet parentRef maps to its Gateway through listenerSetParent, and
// is skipped if listenerSetParent is nil.
func GatewayKeysForParentRefs(routeNamespace string, parentRefs []gwapiv1.ParentReference, listenerSetParent ListenerSetParentFunc) []string {
	var keys []string
	for _, ref := range parentRefs {
		if ref.Group != nil && string(*ref.Group) != gwapiv1.GroupName {
			continue
		}
		ns := routeNamespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		kind := kindGateway
		if ref.Kind != nil {
			kind = string(*ref.Kind)
		}
		switch kind {
		case kindGateway:
			keys = append(keys, ns+"/"+string(ref.Name))
		case kindListenerSet:
			if listenerSetParent == nil {
				continue
			}
			if key := listenerSetParent(ns, string(ref.Name)); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// requestFromGatewayKey converts a gateway key string to a reconcile.Request.
// Merged gateway keys (bare GatewayClass names without "/") are encoded with
// an empty namespace so the controller can distinguish them from namespaced keys.
func requestFromGatewayKey(key string) reconcile.Request {
	if elbproxyutil.IsMergedGatewayKey(key) {
		return reconcile.Request{NamespacedName: types.NamespacedName{Name: key}}
	}
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: parts[0], Name: parts[1]}}
	}
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: key}}
}
