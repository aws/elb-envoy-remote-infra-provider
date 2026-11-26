// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"reflect"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// Envoy Gateway policies can point the proxy at backends that no route references:
// ext_authz services (SecurityPolicy) and ext_proc services (EnvoyExtensionPolicy).
// The managed proxy runs outside the cluster, so the target ports of those backends
// must be opened on the node security group, the same way route backend ports are.

const (
	kindGateway     = "Gateway"
	kindListenerSet = "ListenerSet"
	kindHTTPRoute   = "HTTPRoute"
	kindGRPCRoute   = "GRPCRoute"
)

// PolicyTypes returns the Envoy Gateway policy types whose backends the proxy calls.
func PolicyTypes() []client.Object {
	return []client.Object{
		&egv1a1.SecurityPolicy{},
		&egv1a1.EnvoyExtensionPolicy{},
	}
}

// PolicyTargetRefs returns the targetRefs of a SecurityPolicy or EnvoyExtensionPolicy,
// including the deprecated single targetRef. It returns nil for any other object.
// See GatewayKeysForTargetSelectors for targetSelectors.
func PolicyTargetRefs(obj client.Object) []gwapiv1.LocalPolicyTargetReferenceWithSectionName {
	switch p := obj.(type) {
	case *egv1a1.SecurityPolicy:
		return p.Spec.GetTargetRefs()
	case *egv1a1.EnvoyExtensionPolicy:
		return p.Spec.GetTargetRefs()
	}
	return nil
}

// PolicyBackendRefs returns the backends a policy sends traffic to: the ext_authz gRPC
// and HTTP services of a SecurityPolicy, and the ext_proc services of an
// EnvoyExtensionPolicy. Both backendRefs and the deprecated single backendRef are
// included. It returns nil for any other object.
func PolicyBackendRefs(obj client.Object) []gwapiv1.BackendObjectReference {
	var refs []gwapiv1.BackendObjectReference
	switch p := obj.(type) {
	case *egv1a1.SecurityPolicy:
		if extAuth := p.Spec.ExtAuth; extAuth != nil {
			if extAuth.GRPC != nil {
				refs = append(refs, backendClusterRefs(&extAuth.GRPC.BackendCluster)...)
			}
			if extAuth.HTTP != nil {
				refs = append(refs, backendClusterRefs(&extAuth.HTTP.BackendCluster)...)
			}
		}
	case *egv1a1.EnvoyExtensionPolicy:
		for i := range p.Spec.ExtProc {
			refs = append(refs, backendClusterRefs(&p.Spec.ExtProc[i].BackendCluster)...)
		}
	}
	return refs
}

func backendClusterRefs(bc *egv1a1.BackendCluster) []gwapiv1.BackendObjectReference {
	var refs []gwapiv1.BackendObjectReference
	if bc.BackendRef != nil {
		refs = append(refs, *bc.BackendRef)
	}
	for _, ref := range bc.BackendRefs {
		refs = append(refs, ref.BackendObjectReference)
	}
	return refs
}

// RouteParentsFunc returns the parentRefs of the route of the given kind
// (HTTPRoute or GRPCRoute), namespace and name, or nil if there is no such route.
type RouteParentsFunc func(kind, namespace, name string) []gwapiv1.ParentReference

// GatewayKeysForPolicyTargets returns the deduplicated gateway keys ("namespace/name")
// that a policy in policyNamespace applies to through its targetRefs:
//   - Gateway: that Gateway.
//   - ListenerSet: its Gateway, looked up with listenerSetParent.
//   - HTTPRoute, GRPCRoute: the Gateways in its parentRefs, looked up with routeParents.
//
// targetRefs are always in the policy's namespace. Other kinds are ignored.
func GatewayKeysForPolicyTargets(policyNamespace string, refs []gwapiv1.LocalPolicyTargetReferenceWithSectionName, routeParents RouteParentsFunc, listenerSetParent ListenerSetParentFunc) []string {
	seen := make(map[string]struct{})
	var keys []string
	addKey := func(key string) {
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	for _, ref := range refs {
		if string(ref.Group) != gwapiv1.GroupName {
			continue
		}
		switch string(ref.Kind) {
		case kindGateway:
			addKey(policyNamespace + "/" + string(ref.Name))
		case kindListenerSet:
			if listenerSetParent == nil {
				continue
			}
			if key := listenerSetParent(policyNamespace, string(ref.Name)); key != "" {
				addKey(key)
			}
		case kindHTTPRoute, kindGRPCRoute:
			parents := routeParents(string(ref.Kind), policyNamespace, string(ref.Name))
			for _, key := range GatewayKeysForParentRefs(policyNamespace, parents, listenerSetParent) {
				addKey(key)
			}
		}
	}
	return keys
}

// routeParentsFromClient returns a RouteParentsFunc that reads routes through c.
func routeParentsFromClient(ctx context.Context, c client.Client, log logging.Logger) RouteParentsFunc {
	return func(kind, namespace, name string) []gwapiv1.ParentReference {
		var route client.Object
		switch kind {
		case kindHTTPRoute:
			route = &gwapiv1.HTTPRoute{}
		case kindGRPCRoute:
			route = &gwapiv1.GRPCRoute{}
		default:
			return nil
		}
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, route); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "failed to get policy target route", "kind", kind, "namespace", namespace, "name", name)
			}
			return nil
		}
		return ExtractParentRefs(route)
	}
}

// gatewayKeysForPolicy returns the gateway keys a SecurityPolicy or EnvoyExtensionPolicy
// applies to through its targetRefs and targetSelectors, reading its targets through c.
func gatewayKeysForPolicy(ctx context.Context, c client.Client, log logging.Logger, obj client.Object) []string {
	listenerSetParent := listenerSetParentFromClient(ctx, c, log)
	keys := GatewayKeysForPolicyTargets(obj.GetNamespace(), PolicyTargetRefs(obj), routeParentsFromClient(ctx, c, log), listenerSetParent)
	selected, err := GatewayKeysForTargetSelectors(ctx, c, obj, listenerSetParent)
	if err != nil {
		log.Error(err, "failed to resolve policy targetSelectors", "namespace", obj.GetNamespace(), "name", obj.GetName())
	}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	for _, key := range selected {
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

// EnqueueRequestsForPolicyEvent maps a SecurityPolicy or EnvoyExtensionPolicy event to
// reconcile.Requests for the Gateways the policy applies to, so the target ports of its
// ext_authz and ext_proc backends are opened or released on the node security group.
//
// Update filter: skips the enqueue when the policy's targetRefs, targetSelectors and
// backend refs are unchanged.
type EnqueueRequestsForPolicyEvent struct {
	log    logging.Logger
	client client.Client
}

var _ handler.TypedEventHandler[client.Object, reconcile.Request] = &EnqueueRequestsForPolicyEvent{}

// NewEnqueueRequestsForPolicyEvent returns a new handler for policy events.
func NewEnqueueRequestsForPolicyEvent(log logging.Logger, c client.Client) *EnqueueRequestsForPolicyEvent {
	return &EnqueueRequestsForPolicyEvent{log: log, client: c}
}

// Create enqueues the Gateways the policy applies to.
func (h *EnqueueRequestsForPolicyEvent) Create(
	ctx context.Context,
	e event.TypedCreateEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueue(ctx, q, e.Object)
}

// Update enqueues the Gateways of both the old and the new policy when its targets or
// backends changed.
func (h *EnqueueRequestsForPolicyEvent) Update(
	ctx context.Context,
	e event.TypedUpdateEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if policyUnchanged(e.ObjectOld, e.ObjectNew) {
		h.log.V(1).Info("skipping policy update: targets and backend refs unchanged",
			"namespace", e.ObjectNew.GetNamespace(), "name", e.ObjectNew.GetName())
		return
	}
	h.enqueue(ctx, q, e.ObjectOld, e.ObjectNew)
}

// Delete enqueues the Gateways the policy applied to.
func (h *EnqueueRequestsForPolicyEvent) Delete(
	ctx context.Context,
	e event.TypedDeleteEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueue(ctx, q, e.Object)
}

// Generic enqueues the Gateways the policy applies to.
func (h *EnqueueRequestsForPolicyEvent) Generic(
	ctx context.Context,
	e event.TypedGenericEvent[client.Object],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueue(ctx, q, e.Object)
}

func (h *EnqueueRequestsForPolicyEvent) enqueue(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], objs ...client.Object) {
	seen := make(map[string]struct{})
	for _, obj := range objs {
		for _, key := range gatewayKeysForPolicy(ctx, h.client, h.log, obj) {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			h.log.V(1).Info("enqueuing gateway key for policy change",
				"policy", obj.GetNamespace()+"/"+obj.GetName(), "gateway", key)
			q.Add(requestFromGatewayKey(key))
		}
	}
}

// policyUnchanged reports whether two versions of a policy have the same targetRefs,
// targetSelectors and backend refs.
func policyUnchanged(old, new client.Object) bool {
	return reflect.DeepEqual(PolicyTargetRefs(old), PolicyTargetRefs(new)) &&
		reflect.DeepEqual(PolicyTargetSelectors(old), PolicyTargetSelectors(new)) &&
		reflect.DeepEqual(PolicyBackendRefs(old), PolicyBackendRefs(new))
}
