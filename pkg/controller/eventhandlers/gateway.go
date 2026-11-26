// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"maps"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// EnqueueRequestsForGatewayEvent enqueues a Gateway when its labels change, because
// policy targetSelectors select Gateways by label. Other Gateway changes arrive through
// Envoy Gateway's infra updates.
//
// Update filter: skips the enqueue when the labels are unchanged.
type EnqueueRequestsForGatewayEvent struct {
	log logging.Logger
}

var _ handler.TypedEventHandler[*gwapiv1.Gateway, reconcile.Request] = &EnqueueRequestsForGatewayEvent{}

// NewEnqueueRequestsForGatewayEvent returns a new handler for Gateway events.
func NewEnqueueRequestsForGatewayEvent(log logging.Logger) *EnqueueRequestsForGatewayEvent {
	return &EnqueueRequestsForGatewayEvent{log: log}
}

// Create is a no-op: Envoy Gateway's infra update covers a new Gateway.
func (h *EnqueueRequestsForGatewayEvent) Create(
	_ context.Context,
	_ event.TypedCreateEvent[*gwapiv1.Gateway],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}

// Update enqueues the Gateway when its labels changed.
func (h *EnqueueRequestsForGatewayEvent) Update(
	_ context.Context,
	e event.TypedUpdateEvent[*gwapiv1.Gateway],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if maps.Equal(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) {
		return
	}
	key := e.ObjectNew.Namespace + "/" + e.ObjectNew.Name
	h.log.V(1).Info("enqueuing gateway key for label change", "gateway", key)
	q.Add(requestFromGatewayKey(key))
}

// Delete is a no-op: Envoy Gateway's infra delete covers a deleted Gateway.
func (h *EnqueueRequestsForGatewayEvent) Delete(
	_ context.Context,
	_ event.TypedDeleteEvent[*gwapiv1.Gateway],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}

// Generic is a no-op.
func (h *EnqueueRequestsForGatewayEvent) Generic(
	_ context.Context,
	_ event.TypedGenericEvent[*gwapiv1.Gateway],
	_ workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
}
