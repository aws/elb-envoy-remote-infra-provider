// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"fmt"
	"slices"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// EnqueueRequestsForEndpointEvent implements handler.TypedEventHandler for the
// EndpointSlices of the Envoy Gateway Service. On any event it recomputes the xDS hosts
// from all of the Service's slices, since one Service can have several, and when they
// changed enqueues every gateway key the controller manages (cache.AllKnownKeys), so each
// proxy gets an updated xds-endpoints.json, including proxies whose IR was lost on a
// sidecar restart (the xDS endpoints live in the IR-free config archive).
//
// If no address is ready, the last known hosts are kept rather than pushing an empty list.
type EnqueueRequestsForEndpointEvent struct {
	log    logging.Logger
	client client.Client
	cache  *infrastore.Cache
}

var _ handler.TypedEventHandler[*discoveryv1.EndpointSlice, reconcile.Request] = &EnqueueRequestsForEndpointEvent{}

// NewEnqueueRequestsForEndpointEvent returns a new handler for EndpointSlice events. It
// lists the Envoy Gateway Service's EndpointSlices through c.
func NewEnqueueRequestsForEndpointEvent(log logging.Logger, c client.Client, cache *infrastore.Cache) *EnqueueRequestsForEndpointEvent {
	return &EnqueueRequestsForEndpointEvent{log: log, client: c, cache: cache}
}

// Create updates the xDS hosts and enqueues all gateway keys if they changed.
func (h *EnqueueRequestsForEndpointEvent) Create(
	ctx context.Context,
	_ event.TypedCreateEvent[*discoveryv1.EndpointSlice],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.sync(ctx, q)
}

// Update updates the xDS hosts and enqueues all gateway keys if they changed.
func (h *EnqueueRequestsForEndpointEvent) Update(
	ctx context.Context,
	_ event.TypedUpdateEvent[*discoveryv1.EndpointSlice],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.sync(ctx, q)
}

// Delete updates the xDS hosts from the remaining slices and enqueues all gateway keys
// if they changed.
func (h *EnqueueRequestsForEndpointEvent) Delete(
	ctx context.Context,
	_ event.TypedDeleteEvent[*discoveryv1.EndpointSlice],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.sync(ctx, q)
}

// Generic updates the xDS hosts and enqueues all gateway keys if they changed.
func (h *EnqueueRequestsForEndpointEvent) Generic(
	ctx context.Context,
	_ event.TypedGenericEvent[*discoveryv1.EndpointSlice],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.sync(ctx, q)
}

func (h *EnqueueRequestsForEndpointEvent) sync(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	hosts, err := ListXDSHosts(ctx, h.client)
	if err != nil {
		h.log.Error(err, "failed to list xDS EndpointSlices")
		return
	}
	if len(hosts) == 0 {
		h.log.Info("no ready xDS endpoints, keeping the last known hosts")
		return
	}
	if !h.cache.SetXDSHosts(hosts) {
		h.log.V(1).Info("skipping EndpointSlice event: ready xDS addresses unchanged")
		return
	}
	for _, req := range allGatewayRequests(h.cache) {
		q.Add(req)
	}
}

// ListXDSHosts returns the sorted, deduplicated ready addresses of all EndpointSlices of
// the Envoy Gateway Service.
func ListXDSHosts(ctx context.Context, c client.Reader) ([]string, error) {
	sliceList := &discoveryv1.EndpointSliceList{}
	if err := c.List(ctx, sliceList,
		client.InNamespace(config.EnvoyGatewayNamespace()),
		client.MatchingLabels{discoveryv1.LabelServiceName: config.EnvoyGatewayServiceName},
	); err != nil {
		return nil, fmt.Errorf("failed to list EndpointSlices for xDS hosts: %w", err)
	}
	var addrs []string
	for i := range sliceList.Items {
		addrs = append(addrs, readyAddresses(&sliceList.Items[i])...)
	}
	slices.Sort(addrs)
	return slices.Compact(addrs), nil
}

// allGatewayRequests returns a reconcile.Request for every managed gateway key.
// Uses AllKnownKeys so changes reach proxies even without IR.
func allGatewayRequests(cache *infrastore.Cache) []reconcile.Request {
	keys := cache.AllKnownKeys()
	reqs := make([]reconcile.Request, 0, len(keys))
	for _, k := range keys {
		reqs = append(reqs, requestFromGatewayKey(k))
	}
	return reqs
}

// readyAddresses returns a sorted slice of addresses from ready endpoints in the slice.
func readyAddresses(slice *discoveryv1.EndpointSlice) []string {
	var addrs []string
	for _, ep := range slice.Endpoints {
		if ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
			continue
		}
		addrs = append(addrs, ep.Addresses...)
	}
	slices.Sort(addrs)
	return addrs
}
