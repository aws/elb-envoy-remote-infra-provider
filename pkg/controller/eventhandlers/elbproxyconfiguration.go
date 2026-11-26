// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

// EnqueueRequestForELBProxyConfigurationEvent implements handler.TypedEventHandler for ELBProxyConfiguration
// resources. It enqueues two types of items:
//
//  1. The proxy itself, as an ELBProxyConfigurationRequest — so handleProxyUpdate/handleProxyDelete
//     always runs for finalizer management, cache population, and tiebreak status sync.
//
//  2. The impacted gateway keys (fan-out) — so controller-runtime's queue deduplication
//     can collapse concurrent ELBProxyConfiguration + gateway key events for the same gateway
//     into a single reconcile, preventing the PriorRequestNotCompletedException race.
//
// The keyedMutex in ReconcileKey handles any residual overlap (e.g. cold-start where
// the proxy item and a gateway key item both call ReconcileKey concurrently).
//
// Update filter: skips the enqueue when neither spec nor deletionTimestamp has changed.
type EnqueueRequestForELBProxyConfigurationEvent struct {
	log                 logging.Logger
	cache               *infrastore.Cache
	controllerNamespace string
}

var _ handler.TypedEventHandler[*elbproxyv1alpha1.ELBProxyConfiguration, reconcile.Request] = &EnqueueRequestForELBProxyConfigurationEvent{}

// NewEnqueueRequestForELBProxyConfigurationEvent returns a new handler for ELBProxyConfiguration events.
// controllerNamespace is the namespace where the Envoy Gateway controller runs —
// GatewayClass-scoped fan-out is only applied for proxies in this namespace.
func NewEnqueueRequestForELBProxyConfigurationEvent(log logging.Logger, cache *infrastore.Cache, controllerNamespace string) *EnqueueRequestForELBProxyConfigurationEvent {
	return &EnqueueRequestForELBProxyConfigurationEvent{log: log, cache: cache, controllerNamespace: controllerNamespace}
}

// Create enqueues the proxy itself (for lifecycle) and all impacted gateway keys (for reconciliation).
func (h *EnqueueRequestForELBProxyConfigurationEvent) Create(
	_ context.Context,
	e event.TypedCreateEvent[*elbproxyv1alpha1.ELBProxyConfiguration],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.cache.SetELBProxyConfiguration(elbproxyutil.ELBProxyConfigurationKey(e.Object), e.Object)
	q.Add(requestForObject(e.Object))
	h.enqueueGatewayKeys(e.Object, q)
}

// Update enqueues the proxy and impacted gateways. The cache is always updated
// so that the in-memory ELBProxyConfiguration stays fresh. The reconcile is enqueued
// unconditionally to support periodic resync (SyncPeriod) which delivers
// synthetic Update events with ObjectOld == ObjectNew for drift correction.
func (h *EnqueueRequestForELBProxyConfigurationEvent) Update(
	_ context.Context,
	e event.TypedUpdateEvent[*elbproxyv1alpha1.ELBProxyConfiguration],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.cache.SetELBProxyConfiguration(elbproxyutil.ELBProxyConfigurationKey(e.ObjectNew), e.ObjectNew)
	q.Add(requestForObject(e.ObjectNew))
	// Fan out using both old and new to catch gateways losing the proxy (old)
	// and gateways gaining it (new). Deduplicate across both.
	seen := h.impactedGatewayKeys(e.ObjectOld)
	for k := range h.impactedGatewayKeys(e.ObjectNew) {
		seen[k] = struct{}{}
	}
	for key := range seen {
		q.Add(gatewayKeyToRequest(key))
	}
}

// Delete enqueues the proxy itself and all impacted gateway keys.
func (h *EnqueueRequestForELBProxyConfigurationEvent) Delete(
	_ context.Context,
	e event.TypedDeleteEvent[*elbproxyv1alpha1.ELBProxyConfiguration],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	q.Add(requestForObject(e.Object))
	h.enqueueGatewayKeys(e.Object, q)
}

// Generic enqueues the proxy itself and all impacted gateway keys.
func (h *EnqueueRequestForELBProxyConfigurationEvent) Generic(
	_ context.Context,
	e event.TypedGenericEvent[*elbproxyv1alpha1.ELBProxyConfiguration],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	q.Add(requestForObject(e.Object))
	h.enqueueGatewayKeys(e.Object, q)
}

// enqueueGatewayKeys resolves and enqueues all gateway keys impacted by a proxy event.
func (h *EnqueueRequestForELBProxyConfigurationEvent) enqueueGatewayKeys(
	proxy *elbproxyv1alpha1.ELBProxyConfiguration,
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	for key := range h.impactedGatewayKeys(proxy) {
		h.log.V(1).Info("enqueuing gateway key for ELBProxyConfiguration change",
			"proxy", elbproxyutil.ELBProxyConfigurationKey(proxy), "gateway", key)
		q.Add(gatewayKeyToRequest(key))
	}
}

// impactedGatewayKeys returns the set of gateway keys impacted by a proxy change.
// Uses three sources:
//  1. Currently-attached gateways (reverse index) — handles proxy drops/deletes.
//  2. Gateway-scoped targetRefs — handles new attachments.
//  3. GatewayClass-scoped targetRefs — handles class-level fan-out.
func (h *EnqueueRequestForELBProxyConfigurationEvent) impactedGatewayKeys(proxy *elbproxyv1alpha1.ELBProxyConfiguration) map[string]struct{} {
	if proxy == nil {
		return nil
	}
	seen := make(map[string]struct{})
	proxyKey := elbproxyutil.ELBProxyConfigurationKey(proxy)

	for _, key := range h.cache.GatewaysForEffectiveProxy(proxyKey) {
		seen[key] = struct{}{}
	}
	for _, key := range elbproxyutil.GatewaysReferencedByTargetRefs(proxy) {
		seen[key] = struct{}{}
	}
	for _, className := range elbproxyutil.ClassesReferencedByTargetRefs(proxy, h.controllerNamespace) {
		for _, key := range h.cache.GatewaysInClass(className) {
			seen[key] = struct{}{}
		}
	}
	return seen
}

// elbProxyConfigurationRequestPrefix marks a queue item as an ELBProxyConfiguration rather than a
// gateway key. The queue holds both as namespace/name pairs, and an ELBProxyConfiguration may share
// its namespace and name with the Gateway it targets. Kubernetes object names can't contain ':', so
// no gateway key's name starts with this prefix.
const elbProxyConfigurationRequestPrefix = "ELBProxyConfiguration:"

// requestForObject builds the queue item for an ELBProxyConfiguration itself.
func requestForObject(obj *elbproxyv1alpha1.ELBProxyConfiguration) reconcile.Request {
	return ELBProxyConfigurationRequest(types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()})
}

// ELBProxyConfigurationRequest returns the queue item for the ELBProxyConfiguration nn. It can't equal
// the item for a gateway key, even when the Gateway has the same namespace and name.
func ELBProxyConfigurationRequest(nn types.NamespacedName) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: nn.Namespace,
		Name:      elbProxyConfigurationRequestPrefix + nn.Name,
	}}
}

// ELBProxyConfigurationFromRequest returns the ELBProxyConfiguration a queue item is for. It returns
// false when the item is a gateway key.
func ELBProxyConfigurationFromRequest(req reconcile.Request) (types.NamespacedName, bool) {
	name, ok := strings.CutPrefix(req.Name, elbProxyConfigurationRequestPrefix)
	if !ok {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{Namespace: req.Namespace, Name: name}, true
}

// GatewayClassToRequest converts a GatewayClass name to a reconcile.Request
// for a merged gateway key.
func GatewayClassToRequest(className gwapiv1.ObjectName) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: string(className)}}
}

// gatewayKeyToRequest converts a gateway key string ("ns/name" or bare class name)
// to a reconcile.Request.
func gatewayKeyToRequest(key string) reconcile.Request {
	if elbproxyutil.IsMergedGatewayKey(key) {
		return reconcile.Request{NamespacedName: types.NamespacedName{Name: key}}
	}
	ns, name := elbproxyutil.ParseProxyInfraName(key)
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}
