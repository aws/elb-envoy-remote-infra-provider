// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/controller/eventhandlers"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

const routeController = "elbproxy-route-controller"

const (
	// gatewayClassMembersRequeueDelay is how long to wait before retrying after
	// failing to register a merged gateway's class members.
	gatewayClassMembersRequeueDelay = 15 * time.Second

	// gatewayClassMembersRequeueReason explains why a reconcile is requeued after
	// failing to register a merged gateway's class members.
	gatewayClassMembersRequeueReason = "failed to register gateway class members"
)

type RouteReconciler struct {
	client     client.Client
	log        logging.Logger
	cache      *infrastore.Cache
	reconciler *reconciler.Reconciler
}

// updateBackendPorts refreshes the backend port cache for a single gateway key.
// It is called by GatewayReconciler.Reconcile on every gateway key event so
// backend ports stay in sync with route changes.
// Returns (requeue=true, nil) if a transient API error requires a retry.
func (r *RouteReconciler) updateBackendPorts(ctx context.Context, infraKey string) error {
	// Scan all namespaces, for merged and non-merged gateways alike: a route can attach to
	// a Gateway in another namespace, and a merged gateway's members can be in any namespace.
	affectedGatewayKeys, portsByGateway, err := r.collectPortsByGateway(ctx, "")
	if err != nil {
		return err
	}

	var ports []ir.PortProtocol

	if _, isIndividual := affectedGatewayKeys[infraKey]; isIndividual {
		ports = portsByGateway[infraKey]
	} else if elbproxyutil.IsMergedGatewayKey(infraKey) {
		// Refresh the members on every update: Gateways can join or leave the class at
		// any time.
		if err := r.registerGatewayClassMembers(ctx, infraKey); err != nil {
			r.log.Error(err, "failed to register gateway class members", "class", infraKey)
			return reconciler.NewRequeueNeededAfter(gatewayClassMembersRequeueReason, gatewayClassMembersRequeueDelay)
		}
		classMembers := r.cache.GatewaysInClass(infraKey)
		if len(classMembers) > 0 {
			memberSet := make(map[string]struct{}, len(classMembers))
			for _, m := range classMembers {
				memberSet[m] = struct{}{}
			}
			relevant := false
			for k := range affectedGatewayKeys {
				if _, ok := memberSet[k]; ok {
					relevant = true
					break
				}
			}
			if relevant {
				memberPorts := make(map[string][]ir.PortProtocol, len(classMembers))
				for _, m := range classMembers {
					if p, ok := portsByGateway[m]; ok {
						memberPorts[m] = p
					}
				}
				ports = aggregatePortsForMergedKey(memberPorts)
			}
		}
	}

	// The scan succeeded, so no ports means no route or policy needs one any more. Store an
	// empty slice so the ports of removed sources get revoked. nil would delete the entry
	// instead, which defers the gateway's reconcile until its ports are synced again.
	if ports == nil {
		ports = []ir.PortProtocol{}
	}
	r.cache.SetBackendPorts(infraKey, ports)
	return nil
}

// aggregatePortsForMergedKey returns the union of all ports across the given
// gateway keys. Used for merged gateways where the infra key is the GatewayClass
// name and routes reference individual gateways.
func aggregatePortsForMergedKey(portsByGateway map[string][]ir.PortProtocol) []ir.PortProtocol {
	seen := make(map[ir.PortProtocol]struct{})
	var result []ir.PortProtocol
	for _, ports := range portsByGateway {
		for _, p := range ports {
			if _, exists := seen[p]; !exists {
				seen[p] = struct{}{}
				result = append(result, p)
			}
		}
	}
	return result
}

// SyncBackendPorts populates backend ports for a proxy key by listing all existing
// routes. Called on first infra arrival when the route controller cache is cold —
// on pod restart, pre-existing routes don't fire change events so backend ports
// are never populated until a route changes.
//
// For non-merged gateways the proxy key equals the individual gateway key
// ("namespace/name") and the lookup is direct.
//
// For merged gateways Proxy.Name is the GatewayClass name (no "/"). Routes
// reference individual gateways as parentRefs, so portsByGateway is keyed by
// "namespace/gateway-name", not the merged proxy key. We aggregate the union of
// all ports across all individual gateways and store it under the merged key.
func (r *RouteReconciler) SyncBackendPorts(ctx context.Context, infra *ir.Infra) error {
	if infra == nil || infra.Proxy == nil {
		return fmt.Errorf("SyncBackendPorts: infra or proxy is nil")
	}
	key := infra.Proxy.Name

	// Scan all namespaces: a route can attach to a Gateway in another namespace, and a
	// merged gateway's members can be in any namespace.
	_, portsByGateway, err := r.collectPortsByGateway(ctx, "")
	if err != nil {
		return fmt.Errorf("route sync for %s failed: %w", key, err)
	}

	var ports []ir.PortProtocol
	if direct, ok := portsByGateway[key]; ok {
		// Non-merged: direct hit.
		ports = direct
	} else {
		// Merged: register individual gateways into the class index, then aggregate
		// only their ports. This ensures Reconcile() can filter correctly later.
		if err := r.registerGatewayClassMembers(ctx, key); err != nil {
			r.log.Error(err, "failed to register gateway class members", "class", key)
		}
		memberPorts := make(map[string][]ir.PortProtocol)
		for _, m := range r.cache.GatewaysInClass(key) {
			if p, ok := portsByGateway[m]; ok {
				memberPorts[m] = p
			}
		}
		ports = aggregatePortsForMergedKey(memberPorts)
	}
	if ports == nil {
		ports = []ir.PortProtocol{}
	}
	r.cache.SetBackendPorts(key, ports)
	r.log.Info("synced backend ports for gateway", "gateway", key, "ports", ports)
	return nil
}

// registerGatewayClassMembers sets the cache's members of the merged class mergedKey to
// the Gateways of that class, removing former members. The class key itself, which
// server.go adds, is kept.
func (r *RouteReconciler) registerGatewayClassMembers(ctx context.Context, mergedKey string) error {
	gwList := &gwapiv1.GatewayList{}
	if err := r.client.List(ctx, gwList); err != nil {
		return fmt.Errorf("list gateways: %w", err)
	}
	members := make(map[string]struct{})
	for i := range gwList.Items {
		gw := &gwList.Items[i]
		if string(gw.Spec.GatewayClassName) != mergedKey {
			continue
		}
		memberKey := gw.Namespace + "/" + gw.Name
		members[memberKey] = struct{}{}
		r.cache.SetGatewayClass(memberKey, mergedKey)
	}
	for _, key := range r.cache.GatewaysInClass(mergedKey) {
		if _, ok := members[key]; !ok && !elbproxyutil.IsMergedGatewayKey(key) {
			r.cache.SetGatewayClass(key, "")
		}
	}
	return nil
}

// collectPortsByGateway lists all route types in the namespace, extracts Service backend
// ports from backendRefs, and groups them by gateway key ("namespace/name").
//
// Besides route backendRefs it collects the other Services the proxy sends traffic to:
// RequestMirror targets on HTTPRoutes and GRPCRoutes, and the ext_authz (SecurityPolicy)
// and ext_proc (EnvoyExtensionPolicy) backends of policies in the namespace. A policy's
// ports go to the Gateways it targets, directly or through a targeted route's parentRefs.
func (r *RouteReconciler) collectPortsByGateway(ctx context.Context, namespace string) (map[string]struct{}, map[string][]ir.PortProtocol, error) {
	result := make(map[string][]ir.PortProtocol)
	seen := make(map[string]map[ir.PortProtocol]bool)
	affectedKeys := make(map[string]struct{})

	add := func(gatewayKey string, pp ir.PortProtocol) {
		affectedKeys[gatewayKey] = struct{}{}
		if seen[gatewayKey] == nil {
			seen[gatewayKey] = make(map[ir.PortProtocol]bool)
		}
		if !seen[gatewayKey][pp] {
			seen[gatewayKey][pp] = true
			result[gatewayKey] = append(result[gatewayKey], pp)
		}
	}

	addObjectRefs := func(defaultNamespace string, keys []string, refs []gwapiv1.BackendObjectReference, protocol string) {
		for _, key := range keys {
			affectedKeys[key] = struct{}{}
		}
		for _, ref := range refs {
			if !isServiceBackendRef(ref.Group, ref.Kind) || ref.Port == nil {
				continue
			}
			svcNamespace := defaultNamespace
			if ref.Namespace != nil {
				svcNamespace = string(*ref.Namespace)
			}
			targetPort, err := r.resolveTargetPort(ctx, svcNamespace, string(ref.Name), int32(*ref.Port))
			if err != nil {
				r.log.V(1).Info("cannot resolve target port, using service port",
					"service", svcNamespace+"/"+string(ref.Name), "servicePort", int32(*ref.Port), "error", err)
				targetPort = int32(*ref.Port)
			}
			for _, key := range keys {
				add(key, ir.PortProtocol{Protocol: protocol, Port: targetPort})
			}
		}
	}

	addBackendRefs := func(routeNamespace string, keys []string, refs []gwapiv1.BackendRef, protocol string) {
		objRefs := make([]gwapiv1.BackendObjectReference, 0, len(refs))
		for _, ref := range refs {
			objRefs = append(objRefs, ref.BackendObjectReference)
		}
		addObjectRefs(routeNamespace, keys, objRefs, protocol)
	}

	// Routes and policies on a ListenerSet count for its Gateway. Routes can name a
	// ListenerSet in another namespace, so list them all.
	listenerSets := &gwapiv1.ListenerSetList{}
	if err := r.client.List(ctx, listenerSets); err != nil {
		return nil, nil, fmt.Errorf("failed to list ListenerSets: %w", err)
	}
	listenerSetParent := eventhandlers.ListenerSetParentsFromList(listenerSets.Items)

	// parentRefs of the HTTPRoutes and GRPCRoutes in scope, keyed by "kind/namespace/name",
	// to resolve policies that target routes.
	routeParents := make(map[string][]gwapiv1.ParentReference)

	httpRoutes := &gwapiv1.HTTPRouteList{}
	if err := r.client.List(ctx, httpRoutes, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list HTTPRoutes: %w", err)
	}
	for i := range httpRoutes.Items {
		route := &httpRoutes.Items[i]
		routeParents["HTTPRoute/"+route.Namespace+"/"+route.Name] = route.Spec.ParentRefs
		keys := eventhandlers.GatewayKeysForParentRefs(route.Namespace, eventhandlers.ExtractParentRefs(route), listenerSetParent)
		for _, rule := range route.Spec.Rules {
			refs := make([]gwapiv1.BackendRef, 0, len(rule.BackendRefs))
			for _, r := range rule.BackendRefs {
				refs = append(refs, r.BackendRef)
			}
			addBackendRefs(route.Namespace, keys, refs, "tcp")
		}
		addObjectRefs(route.Namespace, keys, eventhandlers.MirrorBackendRefs(route), "tcp")
	}

	grpcRoutes := &gwapiv1.GRPCRouteList{}
	if err := r.client.List(ctx, grpcRoutes, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list GRPCRoutes: %w", err)
	}
	for i := range grpcRoutes.Items {
		route := &grpcRoutes.Items[i]
		routeParents["GRPCRoute/"+route.Namespace+"/"+route.Name] = route.Spec.ParentRefs
		keys := eventhandlers.GatewayKeysForParentRefs(route.Namespace, eventhandlers.ExtractParentRefs(route), listenerSetParent)
		for _, rule := range route.Spec.Rules {
			refs := make([]gwapiv1.BackendRef, 0, len(rule.BackendRefs))
			for _, r := range rule.BackendRefs {
				refs = append(refs, r.BackendRef)
			}
			addBackendRefs(route.Namespace, keys, refs, "tcp")
		}
		addObjectRefs(route.Namespace, keys, eventhandlers.MirrorBackendRefs(route), "tcp")
	}

	tcpRoutes := &gwapiv1.TCPRouteList{}
	if err := r.client.List(ctx, tcpRoutes, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list TCPRoutes: %w", err)
	}
	for i := range tcpRoutes.Items {
		route := &tcpRoutes.Items[i]
		keys := eventhandlers.GatewayKeysForParentRefs(route.Namespace, eventhandlers.ExtractParentRefs(route), listenerSetParent)
		for _, rule := range route.Spec.Rules {
			addBackendRefs(route.Namespace, keys, rule.BackendRefs, "tcp")
		}
	}

	tlsRoutes := &gwapiv1.TLSRouteList{}
	if err := r.client.List(ctx, tlsRoutes, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list TLSRoutes: %w", err)
	}
	for i := range tlsRoutes.Items {
		route := &tlsRoutes.Items[i]
		keys := eventhandlers.GatewayKeysForParentRefs(route.Namespace, eventhandlers.ExtractParentRefs(route), listenerSetParent)
		for _, rule := range route.Spec.Rules {
			addBackendRefs(route.Namespace, keys, rule.BackendRefs, "tcp")
		}
	}

	udpRoutes := &gwapiv1.UDPRouteList{}
	if err := r.client.List(ctx, udpRoutes, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list UDPRoutes: %w", err)
	}
	for i := range udpRoutes.Items {
		route := &udpRoutes.Items[i]
		keys := eventhandlers.GatewayKeysForParentRefs(route.Namespace, eventhandlers.ExtractParentRefs(route), listenerSetParent)
		for _, rule := range route.Spec.Rules {
			addBackendRefs(route.Namespace, keys, rule.BackendRefs, "udp")
		}
	}

	// Policy backends (ext_authz, ext_proc). targetRefs stay in the policy's namespace,
	// so the routes above cover them. targetSelectors can reach other namespaces and are
	// resolved through the client.
	lookupRouteParents := func(kind, ns, name string) []gwapiv1.ParentReference {
		return routeParents[kind+"/"+ns+"/"+name]
	}
	addPolicy := func(policy client.Object) error {
		refs := eventhandlers.PolicyBackendRefs(policy)
		if len(refs) == 0 {
			return nil
		}
		keys := eventhandlers.GatewayKeysForPolicyTargets(policy.GetNamespace(), eventhandlers.PolicyTargetRefs(policy), lookupRouteParents, listenerSetParent)
		selected, err := eventhandlers.GatewayKeysForTargetSelectors(ctx, r.client, policy, listenerSetParent)
		if err != nil {
			return fmt.Errorf("failed to resolve targetSelectors of policy %s/%s: %w", policy.GetNamespace(), policy.GetName(), err)
		}
		addObjectRefs(policy.GetNamespace(), append(keys, selected...), refs, "tcp")
		return nil
	}

	securityPolicies := &egv1a1.SecurityPolicyList{}
	if err := r.client.List(ctx, securityPolicies, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list SecurityPolicies: %w", err)
	}
	for i := range securityPolicies.Items {
		if err := addPolicy(&securityPolicies.Items[i]); err != nil {
			return nil, nil, err
		}
	}

	extensionPolicies := &egv1a1.EnvoyExtensionPolicyList{}
	if err := r.client.List(ctx, extensionPolicies, client.InNamespace(namespace)); err != nil {
		return nil, nil, fmt.Errorf("failed to list EnvoyExtensionPolicies: %w", err)
	}
	for i := range extensionPolicies.Items {
		if err := addPolicy(&extensionPolicies.Items[i]); err != nil {
			return nil, nil, err
		}
	}

	return affectedKeys, result, nil
}

// isServiceBackendRef returns true if the BackendRef points to a core Service.
func isServiceBackendRef(group *gwapiv1.Group, kind *gwapiv1.Kind) bool {
	if group != nil && string(*group) != "" && string(*group) != "core" {
		return false
	}
	if kind != nil && string(*kind) != "" && string(*kind) != "Service" {
		return false
	}
	return true
}

// resolveTargetPort looks up the Service and returns the numeric targetPort for
// the given servicePort. If the targetPort is a named port, it resolves the
// numeric value from the backend EndpointSlice. Falls back to the service port
// if the Service is not found or the port can't be resolved.
func (r *RouteReconciler) resolveTargetPort(ctx context.Context, namespace, name string, servicePort int32) (int32, error) {
	svc := &corev1.Service{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, svc); err != nil {
		return 0, fmt.Errorf("get Service %s/%s: %w", namespace, name, err)
	}
	for _, p := range svc.Spec.Ports {
		if p.Port == servicePort {
			if p.TargetPort.IntValue() > 0 {
				return int32(p.TargetPort.IntValue()), nil
			}
			// targetPort is either omitted (defaults to servicePort) or a named
			// port (e.g. "http"). If omitted, IntValue()==0 and String()=="",
			// so we return servicePort. If named, resolve from EndpointSlice.
			if p.TargetPort.String() == "" || p.TargetPort.String() == "0" {
				return servicePort, nil
			}
			// Named port — resolve from EndpointSlice.
			resolved, err := r.resolveNamedPortFromEndpointSlice(ctx, namespace, name, p.Name)
			if err != nil {
				r.log.V(1).Info("cannot resolve named targetPort from EndpointSlice, using service port",
					"service", namespace+"/"+name, "targetPort", p.TargetPort.String(), "error", err)
				return servicePort, nil
			}
			return resolved, nil
		}
	}
	return 0, fmt.Errorf("service port %d not found on Service %s/%s", servicePort, namespace, name)
}

// resolveNamedPortFromEndpointSlice lists EndpointSlices for the given Service
// and returns the numeric port matching the named port. This is a one-shot API
// call (no persistent watch) used only for the rare named-port case.
func (r *RouteReconciler) resolveNamedPortFromEndpointSlice(ctx context.Context, namespace, svcName, portName string) (int32, error) {
	sliceList := &discoveryv1.EndpointSliceList{}
	if err := r.client.List(ctx, sliceList,
		client.InNamespace(namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svcName},
	); err != nil {
		return 0, fmt.Errorf("list EndpointSlices for %s/%s: %w", namespace, svcName, err)
	}
	for i := range sliceList.Items {
		for _, ep := range sliceList.Items[i].Ports {
			if ep.Name != nil && *ep.Name == portName && ep.Port != nil {
				return *ep.Port, nil
			}
		}
	}
	return 0, fmt.Errorf("named port %q not found in EndpointSlices for %s/%s", portName, namespace, svcName)
}
