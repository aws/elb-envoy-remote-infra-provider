// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"maps"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// ListenerSetParentFunc returns the key ("namespace/name") of the Gateway the given
// ListenerSet is attached to, or "" if none. A ListenerSet's listeners run on its
// Gateway's proxy, so its routes' and policies' backend ports are opened for that Gateway.
type ListenerSetParentFunc func(namespace, name string) string

// ListenerSetGatewayKey returns the key of the Gateway a ListenerSet is attached to, or ""
// if it isn't attached. Envoy Gateway checks the Gateway's spec.allowedListeners and sets
// the ListenerSet's Accepted condition, so that condition decides.
func ListenerSetGatewayKey(ls *gwapiv1.ListenerSet) string {
	if !meta.IsStatusConditionTrue(ls.Status.Conditions, string(gwapiv1.ListenerSetConditionAccepted)) {
		return ""
	}
	parent := ls.Spec.ParentRef
	if string(ptr.Deref(parent.Group, gwapiv1.GroupName)) != gwapiv1.GroupName ||
		string(ptr.Deref(parent.Kind, kindGateway)) != kindGateway {
		return ""
	}
	namespace := ls.Namespace
	if parent.Namespace != nil {
		namespace = string(*parent.Namespace)
	}
	return namespace + "/" + string(parent.Name)
}

// ListenerSetParentsFromList returns a ListenerSetParentFunc that looks up listenerSets.
func ListenerSetParentsFromList(listenerSets []gwapiv1.ListenerSet) ListenerSetParentFunc {
	parents := make(map[string]string, len(listenerSets))
	for i := range listenerSets {
		if key := ListenerSetGatewayKey(&listenerSets[i]); key != "" {
			parents[listenerSets[i].Namespace+"/"+listenerSets[i].Name] = key
		}
	}
	return func(namespace, name string) string { return parents[namespace+"/"+name] }
}

// listenerSetParentFromClient returns a ListenerSetParentFunc that reads ListenerSets
// through c.
func listenerSetParentFromClient(ctx context.Context, c client.Reader, log logging.Logger) ListenerSetParentFunc {
	return func(namespace, name string) string {
		ls := &gwapiv1.ListenerSet{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, ls); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "failed to get ListenerSet", "namespace", namespace, "name", name)
			}
			return ""
		}
		return ListenerSetGatewayKey(ls)
	}
}

// EnqueueRequestsForListenerSetEvent maps a ListenerSet event to a reconcile.Request for
// the Gateway it is attached to.
//
// Update filter: skips the enqueue when the attached Gateway and labels are unchanged.
type EnqueueRequestsForListenerSetEvent struct {
	log logging.Logger
}

var _ handler.TypedEventHandler[*gwapiv1.ListenerSet, reconcile.Request] = &EnqueueRequestsForListenerSetEvent{}

// NewEnqueueRequestsForListenerSetEvent returns a new handler for ListenerSet events.
func NewEnqueueRequestsForListenerSetEvent(log logging.Logger) *EnqueueRequestsForListenerSetEvent {
	return &EnqueueRequestsForListenerSetEvent{log: log}
}

// Create is a no-op: a new ListenerSet isn't attached until Envoy Gateway accepts it,
// and that status update enqueues its Gateway.
func (h *EnqueueRequestsForListenerSetEvent) Create(
	_ context.Context,
	_ event.TypedCreateEvent[*gwapiv1.ListenerSet],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}

// Update enqueues the old and new attached Gateways when the attachment or labels changed.
func (h *EnqueueRequestsForListenerSetEvent) Update(
	_ context.Context,
	e event.TypedUpdateEvent[*gwapiv1.ListenerSet],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	oldKey, newKey := ListenerSetGatewayKey(e.ObjectOld), ListenerSetGatewayKey(e.ObjectNew)
	if oldKey == newKey && maps.Equal(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) {
		return
	}
	if oldKey != "" {
		h.enqueue(q, e.ObjectNew, oldKey)
	}
	if newKey != "" && newKey != oldKey {
		h.enqueue(q, e.ObjectNew, newKey)
	}
}

// Delete enqueues the Gateway the ListenerSet was attached to.
func (h *EnqueueRequestsForListenerSetEvent) Delete(
	_ context.Context,
	e event.TypedDeleteEvent[*gwapiv1.ListenerSet],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if key := ListenerSetGatewayKey(e.Object); key != "" {
		h.enqueue(q, e.Object, key)
	}
}

// Generic is a no-op.
func (h *EnqueueRequestsForListenerSetEvent) Generic(
	_ context.Context,
	_ event.TypedGenericEvent[*gwapiv1.ListenerSet],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}

func (h *EnqueueRequestsForListenerSetEvent) enqueue(q workqueue.TypedRateLimitingInterface[reconcile.Request], ls *gwapiv1.ListenerSet, key string) {
	h.log.V(1).Info("enqueuing gateway key for ListenerSet change",
		"listenerSet", ls.Namespace+"/"+ls.Name, "gateway", key)
	q.Add(requestFromGatewayKey(key))
}
