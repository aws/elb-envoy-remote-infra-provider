// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"maps"
	"reflect"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// Namespace labels and ReferenceGrants decide which targets in other namespaces
// targetSelectors select. A change can drop a Gateway the policy no longer finds, so
// these handlers enqueue every known gateway key, but only if some policy selects in
// other namespaces.

// EnqueueRequestsForNamespaceEvent enqueues all gateway keys when a Namespace's labels
// change and a policy selects namespaces by label.
//
// Update filter: skips the enqueue when the labels are unchanged.
type EnqueueRequestsForNamespaceEvent struct {
	log    logging.Logger
	client client.Client
	cache  *infrastore.Cache
}

var _ handler.TypedEventHandler[*corev1.Namespace, reconcile.Request] = &EnqueueRequestsForNamespaceEvent{}

// NewEnqueueRequestsForNamespaceEvent returns a new handler for Namespace events.
func NewEnqueueRequestsForNamespaceEvent(log logging.Logger, c client.Client, cache *infrastore.Cache) *EnqueueRequestsForNamespaceEvent {
	return &EnqueueRequestsForNamespaceEvent{log: log, client: c, cache: cache}
}

// Create is a no-op: a new Namespace has no routes or Gateways yet.
func (h *EnqueueRequestsForNamespaceEvent) Create(
	_ context.Context,
	_ event.TypedCreateEvent[*corev1.Namespace],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}

// Update enqueues all gateway keys when the labels changed.
func (h *EnqueueRequestsForNamespaceEvent) Update(
	ctx context.Context,
	e event.TypedUpdateEvent[*corev1.Namespace],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if maps.Equal(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) {
		return
	}
	enqueueForSelectorScopeChange(ctx, h.client, h.cache, h.log, q, egv1a1.TargetNamespaceFromSelector)
}

// Delete is a no-op: deleting the objects in the Namespace enqueues their Gateways.
func (h *EnqueueRequestsForNamespaceEvent) Delete(
	_ context.Context,
	_ event.TypedDeleteEvent[*corev1.Namespace],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}

// Generic is a no-op.
func (h *EnqueueRequestsForNamespaceEvent) Generic(
	_ context.Context,
	_ event.TypedGenericEvent[*corev1.Namespace],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}

// EnqueueRequestsForReferenceGrantEvent enqueues all gateway keys when a ReferenceGrant
// changes and a policy selects targets in other namespaces.
//
// Update filter: skips the enqueue when the spec is unchanged.
type EnqueueRequestsForReferenceGrantEvent struct {
	log    logging.Logger
	client client.Client
	cache  *infrastore.Cache
}

var _ handler.TypedEventHandler[*gwapiv1.ReferenceGrant, reconcile.Request] = &EnqueueRequestsForReferenceGrantEvent{}

// NewEnqueueRequestsForReferenceGrantEvent returns a new handler for ReferenceGrant events.
func NewEnqueueRequestsForReferenceGrantEvent(log logging.Logger, c client.Client, cache *infrastore.Cache) *EnqueueRequestsForReferenceGrantEvent {
	return &EnqueueRequestsForReferenceGrantEvent{log: log, client: c, cache: cache}
}

// Create enqueues all gateway keys, except for the initial list at startup, when every
// Gateway is reconciled anyway.
func (h *EnqueueRequestsForReferenceGrantEvent) Create(
	ctx context.Context,
	e event.TypedCreateEvent[*gwapiv1.ReferenceGrant],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if e.IsInInitialList {
		return
	}
	h.enqueue(ctx, q)
}

// Update enqueues all gateway keys when the spec changed.
func (h *EnqueueRequestsForReferenceGrantEvent) Update(
	ctx context.Context,
	e event.TypedUpdateEvent[*gwapiv1.ReferenceGrant],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if reflect.DeepEqual(e.ObjectOld.Spec, e.ObjectNew.Spec) {
		return
	}
	h.enqueue(ctx, q)
}

// Delete enqueues all gateway keys.
func (h *EnqueueRequestsForReferenceGrantEvent) Delete(
	ctx context.Context,
	_ event.TypedDeleteEvent[*gwapiv1.ReferenceGrant],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueue(ctx, q)
}

// Generic enqueues all gateway keys.
func (h *EnqueueRequestsForReferenceGrantEvent) Generic(
	ctx context.Context,
	_ event.TypedGenericEvent[*gwapiv1.ReferenceGrant],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueue(ctx, q)
}

func (h *EnqueueRequestsForReferenceGrantEvent) enqueue(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	enqueueForSelectorScopeChange(ctx, h.client, h.cache, h.log, q,
		egv1a1.TargetNamespaceFromAll, egv1a1.TargetNamespaceFromSelector)
}

// enqueueForSelectorScopeChange enqueues every known gateway key if a policy has a
// targetSelector whose namespaces.from is one of froms. If the policies can't be listed,
// it enqueues anyway.
func enqueueForSelectorScopeChange(ctx context.Context, c client.Client, cache *infrastore.Cache, log logging.Logger,
	q workqueue.TypedRateLimitingInterface[reconcile.Request], froms ...egv1a1.TargetNamespaceFrom) {
	used, err := targetSelectorsUseNamespaces(ctx, c, froms)
	if err != nil {
		log.Error(err, "failed to list policies, enqueuing all gateway keys")
	} else if !used {
		return
	}
	for _, req := range allGatewayRequests(cache) {
		q.Add(req)
	}
}

// targetSelectorsUseNamespaces reports whether any policy has a targetSelector whose
// namespaces.from is one of froms.
func targetSelectorsUseNamespaces(ctx context.Context, c client.Client, froms []egv1a1.TargetNamespaceFrom) (bool, error) {
	securityPolicies := &egv1a1.SecurityPolicyList{}
	if err := c.List(ctx, securityPolicies); err != nil {
		return false, err
	}
	for i := range securityPolicies.Items {
		if selectorsUseNamespaces(securityPolicies.Items[i].Spec.TargetSelectors, froms) {
			return true, nil
		}
	}
	extensionPolicies := &egv1a1.EnvoyExtensionPolicyList{}
	if err := c.List(ctx, extensionPolicies); err != nil {
		return false, err
	}
	for i := range extensionPolicies.Items {
		if selectorsUseNamespaces(extensionPolicies.Items[i].Spec.TargetSelectors, froms) {
			return true, nil
		}
	}
	return false, nil
}

func selectorsUseNamespaces(selectors []egv1a1.TargetSelector, froms []egv1a1.TargetNamespaceFrom) bool {
	for _, sel := range selectors {
		if sel.Namespaces == nil {
			continue
		}
		for _, from := range froms {
			if sel.Namespaces.From == from {
				return true
			}
		}
	}
	return false
}
