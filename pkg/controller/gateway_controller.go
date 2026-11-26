// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/controller/eventhandlers"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/builder"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

const (
	infraController = "elbproxy-infra-controller"

	// ELBProxyConfigurationFinalizer is added to ELBProxyConfiguration objects to ensure cleanup
	// before deletion. Exported so tests can reference it directly.
	ELBProxyConfigurationFinalizer = "gateway.elbproxy.aws/finalizer"

	// Maximum number of concurrently running reconcile loops
	maxConcurrentReconciles = 3

	// finalizerAddRequeueDelay is how long to wait before re-reconciling a
	// ELBProxyConfiguration after its finalizer has just been added.
	finalizerAddRequeueDelay = 15 * time.Second

	// finalizerAddRequeueReason explains why a reconcile is requeued after the
	// ELBProxyConfiguration finalizer is added.
	finalizerAddRequeueReason = "ELBProxyConfiguration finalizer added"
)

// GatewayReconciler is the single controller that reconciles all gateway keys.
// It watches ELBProxyConfiguration CRDs, all route types, and EndpointSlices via the
// eventhandlers package. Cert file changes are fed in via an out-of-band channel
// backed by a source.Channel watch source.
type GatewayReconciler struct {
	client     client.Client
	log        logging.Logger
	cache      *infrastore.Cache
	reconciler *reconciler.Reconciler
	routeRec   *RouteReconciler
	recorder   record.EventRecorder
}

// GatewayEventCh is the channel type used to push gateway key events from
// out-of-band triggers into the controller queue. Currently used by:
//   - cert watcher: re-enqueues all gateways when TLS certs rotate on disk
//   - gRPC server: enqueues a gateway key on CreateOrUpdateProxyInfra
type GatewayEventCh = chan<- event.TypedGenericEvent[*gatewayKeyEvent]

// EnqueueGatewayKey pushes a gateway key into the controller queue.
// Exported so the gRPC server can enqueue keys without accessing the
// unexported gatewayKeyEvent type. Non-blocking: drops if the channel
// is full since the key will already be pending.
func EnqueueGatewayKey(ch GatewayEventCh, key string) {
	select {
	case ch <- event.TypedGenericEvent[*gatewayKeyEvent]{Object: &gatewayKeyEvent{key: key}}:
	default:
	}
}

// gatewayKeyEvent carries a gateway key through the source.Channel pipeline.
type gatewayKeyEvent struct {
	key string
}

func (g *gatewayKeyEvent) GetNamespace() string        { return "" }
func (g *gatewayKeyEvent) GetName() string             { return g.key }
func (g *gatewayKeyEvent) DeepCopyObject() interface{} { return &gatewayKeyEvent{key: g.key} }

// RegisterGatewayController registers the unified gateway controller.
// Returns:
//   - RouteReconciler for SyncGateway cold-cache warm-up (used by gRPC server)
//   - GatewayReconciler for SyncXDSHosts cold-cache warm-up (used by gRPC server)
//   - GatewayEventCh for the gRPC server to enqueue gateway keys asynchronously
func RegisterGatewayController(
	ctx context.Context,
	mgr manager.Manager,
	c *infrastore.Cache,
	rec *reconciler.Reconciler,
	log logging.Logger,
) (*RouteReconciler, *GatewayReconciler, GatewayEventCh, error) {
	gatewayKeyCh := make(chan event.TypedGenericEvent[*gatewayKeyEvent], 100)

	routeRec := &RouteReconciler{
		client:     mgr.GetClient(),
		log:        log.WithName(routeController),
		cache:      c,
		reconciler: rec,
	}

	gatewayRec := &GatewayReconciler{
		client:     mgr.GetClient(),
		log:        log.WithName(infraController),
		cache:      c,
		reconciler: rec,
		routeRec:   routeRec,
		recorder:   mgr.GetEventRecorderFor("elbproxy-gateway-extension"),
	}

	if err := gatewayRec.setupWithManager(ctx, mgr, gatewayKeyCh, c, log); err != nil {
		return nil, nil, nil, err
	}

	return routeRec, gatewayRec, gatewayKeyCh, nil
}

// setupWithManager creates the controller, registers field indexers, and sets up
// all watch sources.
func (r *GatewayReconciler) setupWithManager(ctx context.Context, mgr manager.Manager, gatewayKeyCh chan event.TypedGenericEvent[*gatewayKeyEvent], c *infrastore.Cache, log logging.Logger) error {
	if err := r.setupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return err
	}

	ctrl, err := controller.New(infraController, mgr, controller.Options{
		Reconciler:              r,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	})
	if err != nil {
		return err
	}

	return r.setupWatches(ctx, ctrl, mgr, gatewayKeyCh, c, log)
}

// setupIndexes registers field indexers needed by the event handlers.
// Must run before the informer cache starts.
func (r *GatewayReconciler) setupIndexes(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	if err := eventhandlers.RegisterRouteBackendIndexers(ctx, fieldIndexer); err != nil {
		return err
	}
	return eventhandlers.RegisterPolicyBackendIndexers(ctx, fieldIndexer)
}

// setupWatches registers all watch sources on the controller, including the
// cert file watcher which feeds into the same gatewayKeyCh as other out-of-band triggers.
func (r *GatewayReconciler) setupWatches(ctx context.Context, ctrl controller.Controller, mgr manager.Manager, gatewayKeyCh chan event.TypedGenericEvent[*gatewayKeyEvent], c *infrastore.Cache, log logging.Logger) error {
	loggerPrefix := log.WithName(infraController)

	// ELBProxyConfiguration: fan out to impacted gateway keys at enqueue time.
	if err := ctrl.Watch(source.Kind(mgr.GetCache(), &elbproxyv1alpha1.ELBProxyConfiguration{},
		eventhandlers.NewEnqueueRequestForELBProxyConfigurationEvent(loggerPrefix, c, config.EnvoyGatewayNamespace()),
	)); err != nil {
		return err
	}

	// The watches below need the Gateway API v1.6 standard channel CRDs (TCPRoute,
	// TLSRoute, UDPRoute and ListenerSet at v1). Unlike Envoy Gateway, the controller
	// doesn't skip kinds whose CRD is missing.

	// Route types: map each route to the gateway keys in its parentRefs.
	routeHandler := eventhandlers.NewEnqueueRequestsForRouteEvent(loggerPrefix, mgr.GetClient())
	for _, routeType := range []client.Object{
		&gwapiv1.HTTPRoute{},
		&gwapiv1.GRPCRoute{},
		&gwapiv1.TCPRoute{},
		&gwapiv1.TLSRoute{},
		&gwapiv1.UDPRoute{},
	} {
		if err := ctrl.Watch(source.Kind(mgr.GetCache(), routeType, routeHandler)); err != nil {
			return err
		}
	}

	// Gateways: policy targetSelectors select Gateways by label.
	if err := ctrl.Watch(source.Kind(mgr.GetCache(), &gwapiv1.Gateway{},
		eventhandlers.NewEnqueueRequestsForGatewayEvent(loggerPrefix),
	)); err != nil {
		return err
	}

	// ListenerSets: their routes and policies count for the Gateway they are attached to.
	if err := ctrl.Watch(source.Kind(mgr.GetCache(), &gwapiv1.ListenerSet{},
		eventhandlers.NewEnqueueRequestsForListenerSetEvent(loggerPrefix),
	)); err != nil {
		return err
	}

	// Namespaces and ReferenceGrants: they decide which targets in other namespaces
	// policy targetSelectors select.
	if err := ctrl.Watch(source.Kind(mgr.GetCache(), &corev1.Namespace{},
		eventhandlers.NewEnqueueRequestsForNamespaceEvent(loggerPrefix, mgr.GetClient(), r.cache),
	)); err != nil {
		return err
	}
	if err := ctrl.Watch(source.Kind(mgr.GetCache(), &gwapiv1.ReferenceGrant{},
		eventhandlers.NewEnqueueRequestsForReferenceGrantEvent(loggerPrefix, mgr.GetClient(), r.cache),
	)); err != nil {
		return err
	}

	// Policies with backends the proxy calls (SecurityPolicy ext_authz,
	// EnvoyExtensionPolicy ext_proc): map each policy to the gateway keys it applies
	// to, so the backends' target ports are opened on the node SG like route backends.
	policyHandler := eventhandlers.NewEnqueueRequestsForPolicyEvent(loggerPrefix, mgr.GetClient())
	for _, policyType := range eventhandlers.PolicyTypes() {
		if err := ctrl.Watch(source.Kind(mgr.GetCache(), policyType, policyHandler)); err != nil {
			return err
		}
	}

	// EndpointSlice: when xDS pod IPs change, enqueue all cached gateway keys.
	if err := ctrl.Watch(source.Kind(mgr.GetCache(), &discoveryv1.EndpointSlice{},
		eventhandlers.NewEnqueueRequestsForEndpointEvent(loggerPrefix, mgr.GetClient(), r.cache),
		predicate.NewTypedPredicateFuncs(func(obj *discoveryv1.EndpointSlice) bool {
			labels := obj.GetLabels()
			return labels[discoveryv1.LabelServiceName] == config.EnvoyGatewayServiceName &&
				obj.GetNamespace() == config.EnvoyGatewayNamespace()
		}),
	)); err != nil {
		return err
	}

	// Backend Services: when a Service's targetPort changes, re-resolve backend
	// ports and reconcile the affected gateways' node SG rules. This covers Services
	// referenced by routes and by policies (ext_authz, ext_proc).
	if err := ctrl.Watch(source.Kind(mgr.GetCache(), &corev1.Service{},
		eventhandlers.NewEnqueueRequestsForServiceEvent(loggerPrefix, mgr.GetClient()),
	)); err != nil {
		return err
	}

	// Out-of-band channel: cert watcher pushes all gateway keys here when TLS
	// certs rotate on disk, triggering a reconcile to push updated certs to proxies.
	if err := ctrl.Watch(source.Channel(gatewayKeyCh,
		handler.TypedEnqueueRequestsFromMapFunc(
			func(_ context.Context, e *gatewayKeyEvent) []reconcile.Request {
				return []reconcile.Request{requestFromGatewayKey(e.key)}
			},
		),
	)); err != nil {
		return err
	}

	// Cert watcher: re-enqueue all gateways when TLS certs rotate.
	certPaths := builder.DefaultCertPaths()
	go func() {
		if err := StartCertWatcher(ctx, []string{
			certPaths.TLSCert,
			certPaths.TLSKey,
			certPaths.CACert,
		}, gatewayKeyCh, c, log); err != nil {
			log.Error(err, "cert watcher exited")
		}
	}()
	return nil
}

// Reconcile is the single reconcile entry point for all gateway keys.
//
// Most queue items are gateway keys ("ns/name" or bare GatewayClass name)
// produced by the event handlers after fan-out. The ELBProxyConfiguration branch
// handles items for the proxy itself, which eventhandlers.ELBProxyConfigurationRequest
// marks so they can't be mistaken for a gateway key with the same namespace and name.
// The proxy is enqueued directly in two cases:
//
//  1. Lifecycle management (finalizer add/remove). The proxy is always enqueued
//     on create/update/delete so handleProxyUpdate/handleProxyDelete can run,
//     regardless of whether any gateway exists yet.
//
//  2. GatewayClass-scoped cold start. When a class-scoped proxy arrives before
//     any gateway in that class, the gatewaysByClass index is empty and the
//     event handler cannot resolve any gateway keys. The proxy is enqueued
//     directly; once gateways arrive and populate the index, subsequent events
//     fan out to gateway keys as normal.
//
// Gateway-scoped proxies (targetRef Kind=Gateway) do not hit case 2 because
// the event handler resolves the gateway key directly from the proxy spec.
func (r *GatewayReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	return reconciler.HandleReconcileError(r.wrappedReconcile(ctx, req), r.log)
}

func (r *GatewayReconciler) wrappedReconcile(ctx context.Context, req reconcile.Request) error {
	// ELBProxyConfiguration items (cold-start fallback or lifecycle path). These are marked, rather
	// than detected by looking the object up: an ELBProxyConfiguration can have the same namespace
	// and name as a Gateway, and treating that Gateway's events as proxy events would skip the
	// backend port refresh below.
	if proxyName, ok := eventhandlers.ELBProxyConfigurationFromRequest(req); ok {
		proxy := &elbproxyv1alpha1.ELBProxyConfiguration{}
		if err := r.client.Get(ctx, proxyName, proxy); err != nil {
			// Not found means it was deleted after it was enqueued. Its Delete event also
			// enqueued the gateway keys it affected, so there's nothing left to do here.
			return client.IgnoreNotFound(err)
		}
		return r.reconcileELBProxyConfiguration(ctx, proxy)
	}

	key := gatewayKeyFromRequest(req)

	// Refresh backend ports from the routes and policies in all namespaces. This is the
	// incremental update path.
	if err := r.routeRec.updateBackendPorts(ctx, key); err != nil {
		return err
	}

	// If this individual gateway key is a member of a merged class, also update
	// the merged class key's backend ports and enqueue it for reconciliation.
	className, ok, err := r.gatewayClassOfMember(ctx, req, key)
	if err != nil {
		return err
	}
	if ok {
		if err := r.routeRec.updateBackendPorts(ctx, className); err != nil {
			return err
		}
		if _, err := r.reconciler.ReconcileKey(ctx, className); err != nil {
			if !reconciler.IsRequeueError(err) {
				r.emitGatewayEvent(ctx, key, corev1.EventTypeWarning, GatewayEventReasonFailedDeployModel,
					fmt.Sprintf("Failed deploy model due to %v", err))
			} else if reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonPreviousProxyDeleting) {
				r.emitGatewayEvent(ctx, key, corev1.EventTypeNormal, GatewayEventReasonWaitingForProxyDeletion,
					"Waiting for the previous proxy to finish deleting before creating a new one")
			}
			return err
		}
	}

	if _, err := r.reconciler.ReconcileKey(ctx, key); err != nil {
		if !reconciler.IsRequeueError(err) {
			r.emitGatewayEvent(ctx, key, corev1.EventTypeWarning, GatewayEventReasonFailedDeployModel,
				fmt.Sprintf("Failed deploy model due to %v", err))
		} else if reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonPreviousProxyDeleting) {
			r.emitGatewayEvent(ctx, key, corev1.EventTypeNormal, GatewayEventReasonWaitingForProxyDeletion,
				"Waiting for the previous proxy to finish deleting before creating a new one")
		}
		return err
	}
	r.emitGatewayEvent(ctx, key, corev1.EventTypeNormal, GatewayEventReasonSuccessfullyReconciled,
		"Successfully reconciled proxy infrastructure")
	return nil
}

func (r *GatewayReconciler) reconcileELBProxyConfiguration(ctx context.Context, proxy *elbproxyv1alpha1.ELBProxyConfiguration) error {
	if proxy.DeletionTimestamp == nil || proxy.DeletionTimestamp.IsZero() {
		return r.handleProxyUpdate(ctx, proxy)
	}
	return r.handleProxyDelete(ctx, proxy)
}

func (r *GatewayReconciler) handleProxyUpdate(ctx context.Context, proxy *elbproxyv1alpha1.ELBProxyConfiguration) error {
	if !controllerutil.ContainsFinalizer(proxy, ELBProxyConfigurationFinalizer) {
		controllerutil.AddFinalizer(proxy, ELBProxyConfigurationFinalizer)
		if err := r.client.Update(ctx, proxy); err != nil {
			return err
		}
		// Requeue so the next reconcile runs with the finalizer already present
		// rather than relying on the Update event to re-enqueue.
		return reconciler.NewRequeueNeededAfter(finalizerAddRequeueReason, finalizerAddRequeueDelay)
	}

	key := elbproxyutil.ELBProxyConfigurationKey(proxy)
	r.cache.SetELBProxyConfiguration(key, proxy)

	// Reconcile the InvalidTargetRef condition. A proxy is misconfigured when it
	// has a GatewayClass-scoped targetRef but lives outside the controller
	// namespace: such refs are silently ignored during resolution, so the proxy
	// can never take effect. Set Accepted=False/InvalidTargetRef in that case and
	// clear a previously-set InvalidTargetRef condition once the proxy is fixed.
	misconfigured := proxy.Namespace != config.EnvoyGatewayNamespace() && r.hasGatewayClassTargetRef(proxy)
	if err := r.reconcileInvalidTargetRefCondition(ctx, proxy, misconfigured); err != nil {
		r.log.Error(err, "failed to reconcile InvalidTargetRef status condition", "proxy", key)
		return err
	}

	// ReconcileForELBProxyConfiguration fans out to all impacted gateway keys. It is
	// called here (rather than relying solely on the event-handler fan-out) for
	// two reasons:
	//   a) GatewayClass cold-start: when the proxy arrived before any gateway in
	//      the class, the event handler enqueued the proxy directly because
	//      gatewaysByClass was empty. ReconcileForELBProxyConfiguration does the fan-out
	//      now that the proxy is cached.
	//   b) Tiebreak and status: resolves which proxy wins per gateway and writes
	//      Accepted conditions on all impacted ELBProxyConfigurations.
	// The keyedMutex in ReconcileKey serializes this path against any concurrent
	// gateway key items that the event-handler fan-out may have already enqueued.
	if err := r.reconciler.ReconcileForELBProxyConfiguration(ctx, key); err != nil {
		if !reconciler.IsRequeueError(err) {
			r.log.Error(err, "reconcile failed after ELBProxyConfiguration change", "key", key)
		}
		return err
	}
	return nil
}

func (r *GatewayReconciler) handleProxyDelete(ctx context.Context, proxy *elbproxyv1alpha1.ELBProxyConfiguration) error {
	if !controllerutil.ContainsFinalizer(proxy, ELBProxyConfigurationFinalizer) {
		return nil
	}

	target := elbproxyutil.ELBProxyConfigurationKey(proxy)
	for _, key := range r.cache.AllInfraKeys() {
		effective, err := r.reconciler.ResolveEffectiveProxy(ctx, key)
		if err != nil {
			return err
		}
		if effective != nil && elbproxyutil.ELBProxyConfigurationKey(effective) == target {
			return fmt.Errorf("ELBProxyConfiguration %s is still in use by gateway %s", target, key)
		}
	}

	controllerutil.RemoveFinalizer(proxy, ELBProxyConfigurationFinalizer)
	if err := r.client.Update(ctx, proxy); err != nil {
		return err
	}
	r.cache.DeleteELBProxyConfiguration(target)
	r.log.Info("deleted ELBProxyConfiguration", "key", target)
	return nil
}

// hasGatewayClassTargetRef reports whether the proxy has at least one
// GatewayClass-scoped targetRef.
func (r *GatewayReconciler) hasGatewayClassTargetRef(proxy *elbproxyv1alpha1.ELBProxyConfiguration) bool {
	for _, ref := range proxy.Spec.TargetRefs {
		if string(ref.Group) == elbproxyutil.GatewayAPIGroup && string(ref.Kind) == elbproxyutil.KindGatewayClass {
			return true
		}
	}
	return false
}

// reconcileInvalidTargetRefCondition sets Accepted=False/InvalidTargetRef when the
// proxy is misconfigured (GatewayClass-scoped targetRef from outside the controller
// namespace), and clears a previously-set InvalidTargetRef condition once the proxy
// is fixed. Clearing only removes an InvalidTargetRef-reason condition so it does not
// interfere with the Accepted/Conflicted conditions written by the tiebreak resolver.
func (r *GatewayReconciler) reconcileInvalidTargetRefCondition(ctx context.Context, proxy *elbproxyv1alpha1.ELBProxyConfiguration, misconfigured bool) error {
	live := &elbproxyv1alpha1.ELBProxyConfiguration{}
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(proxy), live); err != nil {
		return fmt.Errorf("get ELBProxyConfiguration %s/%s: %w", proxy.Namespace, proxy.Name, err)
	}

	existing := meta.FindStatusCondition(live.Status.Conditions, elbproxyutil.ELBProxyConfigurationAcceptedConditionType)

	if !misconfigured {
		// Only clear if the current condition is the one we set. Leave
		// Accepted/Conflicted conditions from the tiebreak resolver untouched.
		if existing == nil || existing.Reason != elbproxyutil.ELBProxyConfigurationReasonInvalidTargetRef {
			return nil
		}
		base := live.DeepCopy()
		meta.RemoveStatusCondition(&live.Status.Conditions, elbproxyutil.ELBProxyConfigurationAcceptedConditionType)
		return r.client.Status().Patch(ctx, live, client.MergeFrom(base))
	}

	cond := metav1.Condition{
		Type:               elbproxyutil.ELBProxyConfigurationAcceptedConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             elbproxyutil.ELBProxyConfigurationReasonInvalidTargetRef,
		ObservedGeneration: live.Generation,
		Message: fmt.Sprintf(
			"ELBProxyConfiguration has GatewayClass-scoped targetRef but is in namespace %q; "+
				"GatewayClass attachments are only honored from the controller namespace %q",
			proxy.Namespace, config.EnvoyGatewayNamespace()),
	}

	if existing != nil &&
		existing.Status == cond.Status &&
		existing.Reason == cond.Reason &&
		existing.Message == cond.Message &&
		existing.ObservedGeneration == cond.ObservedGeneration {
		return nil
	}

	base := live.DeepCopy()
	meta.SetStatusCondition(&live.Status.Conditions, cond)
	return r.client.Status().Patch(ctx, live, client.MergeFrom(base))
}

// SyncXDSHosts seeds the xDS hosts cache from the Envoy Gateway Service's EndpointSlices.
// Called on first infra arrival after a pod restart, when no EndpointSlice event may
// fire because nothing changed.
func (r *GatewayReconciler) SyncXDSHosts(ctx context.Context) error {
	hosts, err := eventhandlers.ListXDSHosts(ctx, r.client)
	if err != nil {
		return err
	}
	r.cache.SetXDSHosts(hosts)
	return nil
}

// gatewayClassOfMember returns the merged GatewayClass an individual Gateway key belongs
// to. A Gateway can join a merged class after its members were registered, so on a cache
// miss it reads the Gateway and registers it if its class has a merged proxy.
func (r *GatewayReconciler) gatewayClassOfMember(ctx context.Context, req reconcile.Request, key string) (string, bool, error) {
	if elbproxyutil.IsMergedGatewayKey(key) {
		return "", false, nil
	}
	if className, ok := r.cache.GetGatewayClass(key); ok {
		return className, true, nil
	}
	gw := &gwapiv1.Gateway{}
	if err := r.client.Get(ctx, req.NamespacedName, gw); err != nil {
		return "", false, client.IgnoreNotFound(err)
	}
	className := string(gw.Spec.GatewayClassName)
	if _, hasInfra := r.cache.GetInfra(className); !hasInfra && !r.cache.IsKnownKey(className) {
		return "", false, nil
	}
	r.cache.SetGatewayClass(key, className)
	return className, true, nil
}

// gatewayKeyFromRequest converts a reconcile.Request to a gateway key string.
func gatewayKeyFromRequest(req reconcile.Request) string {
	if req.Namespace == "" {
		return req.Name
	}
	return req.Namespace + "/" + req.Name
}

// requestFromGatewayKey converts a gateway key string to a reconcile.Request.
func requestFromGatewayKey(key string) reconcile.Request {
	if elbproxyutil.IsMergedGatewayKey(key) {
		return reconcile.Request{NamespacedName: types.NamespacedName{Name: key}}
	}
	ns, name := elbproxyutil.ParseProxyInfraName(key)
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

// emitGatewayEvent emits a Kubernetes Event on the Gateway resource identified by key.
// Best-effort: logs and swallows errors if the Gateway cannot be fetched (e.g. deleted).
func (r *GatewayReconciler) emitGatewayEvent(ctx context.Context, key string, eventType, reason, message string) {
	// A merged key is a GatewayClass name, not a Gateway: emit on each member Gateway instead.
	if elbproxyutil.IsMergedGatewayKey(key) {
		for _, member := range r.cache.GatewaysInClass(key) {
			// The server seeds the class key into its own class; skip it, it is not a Gateway.
			if !elbproxyutil.IsMergedGatewayKey(member) {
				r.emitEventOnGateway(ctx, member, eventType, reason, message)
			}
		}
		return
	}
	r.emitEventOnGateway(ctx, key, eventType, reason, message)
}

// emitEventOnGateway emits an event on the Gateway named by a namespace/name key.
func (r *GatewayReconciler) emitEventOnGateway(ctx context.Context, key string, eventType, reason, message string) {
	ns, name := elbproxyutil.ParseProxyInfraName(key)
	gw := &gwapiv1.Gateway{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, gw); err != nil {
		r.log.V(1).Info("could not fetch gateway for event emission", "key", key, "error", err)
		return
	}
	r.recorder.Event(gw, eventType, reason, message)
}
