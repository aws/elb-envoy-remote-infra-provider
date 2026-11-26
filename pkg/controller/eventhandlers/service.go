// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"fmt"
	"reflect"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

// EnqueueRequestsForServiceEvent maps a backend Service event to reconcile.Requests
// for every gateway whose routes reference that Service via a backendRef, or that a
// policy referencing that Service (ext_authz, ext_proc) applies to.
//
// This closes the gap where a route watch alone misses a backend Service's targetPort
// being edited (the route object is unchanged, so the route watch never fires). On such
// a change the backend target ports the controller opens on the node security group
// would otherwise stay stale until an unrelated event triggered a reconcile.
//
// Update filter: skips the enqueue when the Service's ports (port, targetPort, name,
// protocol) are unchanged, since only those affect backend target port resolution.
type EnqueueRequestsForServiceEvent struct {
	log    logging.Logger
	client client.Client
}

var _ handler.TypedEventHandler[*corev1.Service, reconcile.Request] = &EnqueueRequestsForServiceEvent{}

// NewEnqueueRequestsForServiceEvent returns a new handler for backend Service events.
func NewEnqueueRequestsForServiceEvent(log logging.Logger, c client.Client) *EnqueueRequestsForServiceEvent {
	return &EnqueueRequestsForServiceEvent{log: log, client: c}
}

// Create enqueues gateways whose routes reference the Service.
func (h *EnqueueRequestsForServiceEvent) Create(
	ctx context.Context,
	e event.TypedCreateEvent[*corev1.Service],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueueForService(ctx, e.Object.Namespace, e.Object.Name, q)
}

// Update enqueues only when the Service's ports changed.
func (h *EnqueueRequestsForServiceEvent) Update(
	ctx context.Context,
	e event.TypedUpdateEvent[*corev1.Service],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if servicePortsEqual(e.ObjectOld.Spec.Ports, e.ObjectNew.Spec.Ports) {
		h.log.V(1).Info("skipping Service update: ports unchanged",
			"namespace", e.ObjectNew.Namespace, "name", e.ObjectNew.Name)
		return
	}
	h.enqueueForService(ctx, e.ObjectNew.Namespace, e.ObjectNew.Name, q)
}

// Delete enqueues gateways whose routes reference the Service.
func (h *EnqueueRequestsForServiceEvent) Delete(
	ctx context.Context,
	e event.TypedDeleteEvent[*corev1.Service],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueueForService(ctx, e.Object.Namespace, e.Object.Name, q)
}

// Generic enqueues gateways whose routes reference the Service.
func (h *EnqueueRequestsForServiceEvent) Generic(
	ctx context.Context,
	e event.TypedGenericEvent[*corev1.Service],
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	h.enqueueForService(ctx, e.Object.Namespace, e.Object.Name, q)
}

func (h *EnqueueRequestsForServiceEvent) enqueueForService(
	ctx context.Context,
	svcNamespace, svcName string,
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	keys, err := gatewayKeysForService(ctx, h.client, h.log, svcNamespace, svcName)
	if err != nil {
		h.log.Error(err, "failed to map Service to gateway keys", "namespace", svcNamespace, "service", svcName)
		return
	}
	for _, key := range keys {
		h.log.V(1).Info("enqueuing gateway key for backend Service change",
			"service", svcNamespace+"/"+svcName, "gateway", key)
		q.Add(requestFromGatewayKey(key))
	}
}

// IndexKeyBackendServiceRefName is the field-indexer key under which each route
// type is indexed by the "namespace/name" of every backend Service it references.
// Because a backendRef may carry an explicit (cross-namespace) namespace, a single
// route can be indexed under multiple "ns/name" keys — the route's own namespace
// for namespace-less refs, plus each explicit ref namespace.
const IndexKeyBackendServiceRefName = "spec.backendRefs.serviceRef.name"

// routeTypesForIndex are the route list/object pairs that carry backendRefs and
// must be indexed for the Service → routes lookup.
func routeTypesForIndex() []client.Object {
	return []client.Object{
		&gwapiv1.HTTPRoute{},
		&gwapiv1.GRPCRoute{},
		&gwapiv1.TCPRoute{},
		&gwapiv1.TLSRoute{},
		&gwapiv1.UDPRoute{},
	}
}

// indexFuncBackendServiceRefName extracts the "namespace/name" keys of the core
// Services referenced by a route's backendRefs. A backendRef without a namespace
// defaults to the route's namespace; explicit namespaces are honored so the index
// covers cross-namespace backendRefs.
func indexFuncBackendServiceRefName(obj client.Object) []string {
	routeRefs := ExtractBackendRefs(obj)
	refs := make([]gwapiv1.BackendObjectReference, 0, len(routeRefs))
	for _, ref := range routeRefs {
		refs = append(refs, ref.BackendObjectReference)
	}
	return serviceRefKeys(obj.GetNamespace(), refs)
}

// indexFuncPolicyBackendServiceRefName extracts the "namespace/name" keys of the core
// Services a SecurityPolicy or EnvoyExtensionPolicy sends traffic to (see
// PolicyBackendRefs). A backendRef without a namespace defaults to the policy's namespace.
func indexFuncPolicyBackendServiceRefName(obj client.Object) []string {
	return serviceRefKeys(obj.GetNamespace(), PolicyBackendRefs(obj))
}

// serviceRefKeys returns the deduplicated "namespace/name" keys of the core Services
// in refs. A ref without a namespace defaults to defaultNamespace.
func serviceRefKeys(defaultNamespace string, refs []gwapiv1.BackendObjectReference) []string {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	var keys []string
	for _, ref := range refs {
		// Only core Services (empty/"core" group, empty/"Service" kind).
		if ref.Group != nil && string(*ref.Group) != "" && string(*ref.Group) != "core" {
			continue
		}
		if ref.Kind != nil && string(*ref.Kind) != "" && string(*ref.Kind) != "Service" {
			continue
		}
		ns := defaultNamespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		key := ns + "/" + string(ref.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// RegisterRouteBackendIndexers registers the backend-Service field indexer on every
// route type, enabling gatewayKeysForService to do an indexed lookup instead of
// listing all routes per backend Service event. Must be called during controller
// setup, before the cache starts.
func RegisterRouteBackendIndexers(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	for _, routeType := range routeTypesForIndex() {
		if err := fieldIndexer.IndexField(ctx, routeType, IndexKeyBackendServiceRefName, indexFuncBackendServiceRefName); err != nil {
			return fmt.Errorf("failed to index %T by backend serviceRef: %w", routeType, err)
		}
	}
	return nil
}

// RegisterPolicyBackendIndexers registers the backend-Service field indexer on the
// SecurityPolicy and EnvoyExtensionPolicy types, so gatewayKeysForService also finds
// policies whose ext_authz or ext_proc backends reference a Service. Must be called
// during controller setup, before the cache starts.
func RegisterPolicyBackendIndexers(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	for _, policyType := range PolicyTypes() {
		if err := fieldIndexer.IndexField(ctx, policyType, IndexKeyBackendServiceRefName, indexFuncPolicyBackendServiceRefName); err != nil {
			return fmt.Errorf("failed to index %T by backend serviceRef: %w", policyType, err)
		}
	}
	return nil
}

// gatewayKeysForService returns the deduplicated gateway keys ("namespace/name") of
// routes whose backendRefs reference the given Service, and of the Gateways that
// policies referencing the Service apply to, via the backend-Service field indexers
// (see RegisterRouteBackendIndexers and RegisterPolicyBackendIndexers). A backendRef
// without a namespace defaults to the route's or policy's namespace.
func gatewayKeysForService(ctx context.Context, c client.Client, log logging.Logger, svcNamespace, svcName string) ([]string, error) {
	indexKey := svcNamespace + "/" + svcName
	listOpt := client.MatchingFields{IndexKeyBackendServiceRefName: indexKey}

	seen := make(map[string]struct{})
	var keys []string

	listenerSetParent := listenerSetParentFromClient(ctx, c, log)
	collect := func(routeNamespace string, parentRefs []gwapiv1.ParentReference) {
		for _, key := range GatewayKeysForParentRefs(routeNamespace, parentRefs, listenerSetParent) {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
		}
	}

	httpRoutes := &gwapiv1.HTTPRouteList{}
	if err := c.List(ctx, httpRoutes, listOpt); err != nil {
		return nil, err
	}
	for i := range httpRoutes.Items {
		collect(httpRoutes.Items[i].Namespace, httpRoutes.Items[i].Spec.ParentRefs)
	}

	grpcRoutes := &gwapiv1.GRPCRouteList{}
	if err := c.List(ctx, grpcRoutes, listOpt); err != nil {
		return nil, err
	}
	for i := range grpcRoutes.Items {
		collect(grpcRoutes.Items[i].Namespace, grpcRoutes.Items[i].Spec.ParentRefs)
	}

	tcpRoutes := &gwapiv1.TCPRouteList{}
	if err := c.List(ctx, tcpRoutes, listOpt); err != nil {
		return nil, err
	}
	for i := range tcpRoutes.Items {
		collect(tcpRoutes.Items[i].Namespace, tcpRoutes.Items[i].Spec.ParentRefs)
	}

	tlsRoutes := &gwapiv1.TLSRouteList{}
	if err := c.List(ctx, tlsRoutes, listOpt); err != nil {
		return nil, err
	}
	for i := range tlsRoutes.Items {
		collect(tlsRoutes.Items[i].Namespace, tlsRoutes.Items[i].Spec.ParentRefs)
	}

	udpRoutes := &gwapiv1.UDPRouteList{}
	if err := c.List(ctx, udpRoutes, listOpt); err != nil {
		return nil, err
	}
	for i := range udpRoutes.Items {
		collect(udpRoutes.Items[i].Namespace, udpRoutes.Items[i].Spec.ParentRefs)
	}

	collectPolicy := func(obj client.Object) {
		for _, key := range gatewayKeysForPolicy(ctx, c, log, obj) {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
		}
	}

	securityPolicies := &egv1a1.SecurityPolicyList{}
	if err := c.List(ctx, securityPolicies, listOpt); err != nil {
		return nil, err
	}
	for i := range securityPolicies.Items {
		collectPolicy(&securityPolicies.Items[i])
	}

	extensionPolicies := &egv1a1.EnvoyExtensionPolicyList{}
	if err := c.List(ctx, extensionPolicies, listOpt); err != nil {
		return nil, err
	}
	for i := range extensionPolicies.Items {
		collectPolicy(&extensionPolicies.Items[i])
	}

	return keys, nil
}

// servicePortsEqual reports whether two ServicePort slices are equal in the fields
// that affect backend target port resolution (name, port, targetPort, protocol),
// regardless of ordering.
func servicePortsEqual(a, b []corev1.ServicePort) bool {
	if len(a) != len(b) {
		return false
	}
	type key struct {
		name       string
		port       int32
		targetPort string
		protocol   corev1.Protocol
	}
	toSet := func(ports []corev1.ServicePort) map[key]int {
		m := make(map[key]int, len(ports))
		for _, p := range ports {
			m[key{p.Name, p.Port, p.TargetPort.String(), p.Protocol}]++
		}
		return m
	}
	return reflect.DeepEqual(toSet(a), toSet(b))
}
