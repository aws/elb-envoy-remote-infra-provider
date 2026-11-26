// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package eventhandlers

import (
	"context"
	"fmt"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// PolicyTargetSelectors returns the targetSelectors of a SecurityPolicy or
// EnvoyExtensionPolicy. It returns nil for any other object.
func PolicyTargetSelectors(obj client.Object) []egv1a1.TargetSelector {
	switch p := obj.(type) {
	case *egv1a1.SecurityPolicy:
		return p.Spec.TargetSelectors
	case *egv1a1.EnvoyExtensionPolicy:
		return p.Spec.TargetSelectors
	}
	return nil
}

// GatewayKeysForTargetSelectors returns the deduplicated gateway keys ("namespace/name")
// that a policy applies to through its targetSelectors. Selected targets map to Gateways
// as in GatewayKeysForPolicyTargets. It follows Envoy Gateway's rules:
//   - namespaces.from: Same (the default) searches the policy's namespace, All every
//     namespace, and Selector the namespaces whose labels match.
//   - A target in another namespace needs a ReferenceGrant there for the policy's kind.
//   - An invalid label selector matches nothing.
func GatewayKeysForTargetSelectors(ctx context.Context, c client.Reader, policy client.Object, listenerSetParent ListenerSetParentFunc) ([]string, error) {
	selectors := PolicyTargetSelectors(policy)
	if len(selectors) == 0 {
		return nil, nil
	}
	r := &selectorResolver{
		ctx:               ctx,
		c:                 c,
		policy:            policy,
		listenerSetParent: listenerSetParent,
		grants:            make(map[string][]gwapiv1.ReferenceGrant),
	}
	var keys []string
	seen := make(map[string]struct{})
	for _, sel := range selectors {
		selKeys, err := r.gatewayKeys(sel)
		if err != nil {
			return nil, err
		}
		for _, key := range selKeys {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}

// selectorResolver resolves the targetSelectors of one policy, caching ReferenceGrants
// per namespace.
type selectorResolver struct {
	ctx               context.Context
	c                 client.Reader
	policy            client.Object
	listenerSetParent ListenerSetParentFunc
	grants            map[string][]gwapiv1.ReferenceGrant
}

// gatewayKeys returns the gateway keys for the targets one selector matches.
func (r *selectorResolver) gatewayKeys(sel egv1a1.TargetSelector) ([]string, error) {
	if string(ptr.Deref(sel.Group, gwapiv1.GroupName)) != gwapiv1.GroupName {
		return nil, nil
	}
	var list client.ObjectList
	switch string(sel.Kind) {
	case kindGateway:
		list = &gwapiv1.GatewayList{}
	case kindListenerSet:
		list = &gwapiv1.ListenerSetList{}
	case kindHTTPRoute:
		list = &gwapiv1.HTTPRouteList{}
	case kindGRPCRoute:
		list = &gwapiv1.GRPCRouteList{}
	default:
		return nil, nil
	}
	labelSelector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels:      sel.MatchLabels,
		MatchExpressions: sel.MatchExpressions,
	})
	if err != nil {
		return nil, nil
	}
	inScope, opts, err := r.namespaceScope(sel.Namespaces)
	if err != nil || inScope == nil {
		return nil, err
	}
	opts = append(opts, client.MatchingLabelsSelector{Selector: labelSelector})
	if err := r.c.List(r.ctx, list, opts...); err != nil {
		return nil, fmt.Errorf("failed to list %ss for targetSelectors: %w", sel.Kind, err)
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s list: %w", sel.Kind, err)
	}
	var keys []string
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok || !inScope(obj.GetNamespace()) {
			continue
		}
		permitted, err := r.referencePermitted(string(sel.Kind), obj)
		if err != nil {
			return nil, err
		}
		if !permitted {
			continue
		}
		switch target := obj.(type) {
		case *gwapiv1.Gateway:
			keys = append(keys, target.Namespace+"/"+target.Name)
		case *gwapiv1.ListenerSet:
			if key := ListenerSetGatewayKey(target); key != "" {
				keys = append(keys, key)
			}
		default:
			keys = append(keys, GatewayKeysForParentRefs(obj.GetNamespace(), ExtractParentRefs(obj), r.listenerSetParent)...)
		}
	}
	return keys, nil
}

// namespaceScope returns a function that reports whether a namespace is in scope, and
// list options that limit a List to that scope. The function is nil if nothing is in scope.
func (r *selectorResolver) namespaceScope(namespaces *egv1a1.TargetSelectorNamespaces) (func(string) bool, []client.ListOption, error) {
	policyNamespace := r.policy.GetNamespace()
	from := egv1a1.TargetNamespaceFromSame
	if namespaces != nil && namespaces.From != "" {
		from = namespaces.From
	}
	switch from {
	case egv1a1.TargetNamespaceFromSame:
		return func(ns string) bool { return ns == policyNamespace }, []client.ListOption{client.InNamespace(policyNamespace)}, nil
	case egv1a1.TargetNamespaceFromAll:
		return func(string) bool { return true }, nil, nil
	case egv1a1.TargetNamespaceFromSelector:
		if namespaces.Selector == nil {
			return nil, nil, nil
		}
		nsSelector, err := metav1.LabelSelectorAsSelector(namespaces.Selector)
		if err != nil {
			return nil, nil, nil
		}
		nsList := &corev1.NamespaceList{}
		if err := r.c.List(r.ctx, nsList, client.MatchingLabelsSelector{Selector: nsSelector}); err != nil {
			return nil, nil, fmt.Errorf("failed to list namespaces for targetSelectors: %w", err)
		}
		matched := make(map[string]struct{}, len(nsList.Items))
		for _, ns := range nsList.Items {
			matched[ns.Name] = struct{}{}
		}
		return func(ns string) bool { _, ok := matched[ns]; return ok }, nil, nil
	}
	return nil, nil, nil
}

// referencePermitted reports whether the policy may target obj: always in its own
// namespace, elsewhere only if a ReferenceGrant allows it.
func (r *selectorResolver) referencePermitted(targetKind string, obj client.Object) (bool, error) {
	targetNamespace := obj.GetNamespace()
	if targetNamespace == r.policy.GetNamespace() {
		return true, nil
	}
	grants, ok := r.grants[targetNamespace]
	if !ok {
		list := &gwapiv1.ReferenceGrantList{}
		if err := r.c.List(r.ctx, list, client.InNamespace(targetNamespace)); err != nil {
			return false, fmt.Errorf("failed to list ReferenceGrants in %s: %w", targetNamespace, err)
		}
		grants = list.Items
		r.grants[targetNamespace] = grants
	}
	policyKind := policyKindOf(r.policy)
	for i := range grants {
		if grantAllows(&grants[i], policyKind, r.policy.GetNamespace(), targetKind, obj.GetName()) {
			return true, nil
		}
	}
	return false, nil
}

// grantAllows reports whether grant lets a policyKind in policyNamespace reference the
// targetKind named targetName.
func grantAllows(grant *gwapiv1.ReferenceGrant, policyKind, policyNamespace, targetKind, targetName string) bool {
	fromAllowed := false
	for _, from := range grant.Spec.From {
		if string(from.Group) == egv1a1.GroupName && string(from.Kind) == policyKind &&
			string(from.Namespace) == policyNamespace {
			fromAllowed = true
			break
		}
	}
	if !fromAllowed {
		return false
	}
	for _, to := range grant.Spec.To {
		if string(to.Group) == gwapiv1.GroupName && string(to.Kind) == targetKind &&
			(to.Name == nil || *to.Name == "" || string(*to.Name) == targetName) {
			return true
		}
	}
	return false
}

// policyKindOf returns the kind of a SecurityPolicy or EnvoyExtensionPolicy, from its Go
// type, since cached objects have no TypeMeta.
func policyKindOf(obj client.Object) string {
	switch obj.(type) {
	case *egv1a1.SecurityPolicy:
		return egv1a1.KindSecurityPolicy
	case *egv1a1.EnvoyExtensionPolicy:
		return egv1a1.KindEnvoyExtensionPolicy
	}
	return ""
}
