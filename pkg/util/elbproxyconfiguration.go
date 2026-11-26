// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"os"
	"sort"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

const (
	// GatewayAPIGroup is the Gateway API group name.
	GatewayAPIGroup = gwapiv1.GroupName
	// KindGateway is the Gateway resource kind.
	KindGateway = "Gateway"
	// KindGatewayClass is the GatewayClass resource kind.
	KindGatewayClass = "GatewayClass"
	// owningGatewayClassLabel is the IR metadata label envoy-gateway sets for merged fleets.
	// It is the authoritative signal that a proxy belongs to a merged gateway fleet.
	owningGatewayClassLabel = "gateway.envoyproxy.io/owning-gatewayclass"
	// envoyGatewayNamespaceEnvVar is the env var that overrides the default envoy-gateway namespace.
	envoyGatewayNamespaceEnvVar = "ENVOY_GATEWAY_NAMESPACE"
	// defaultEnvoyGatewayNamespace is the default namespace for envoy-gateway resources.
	defaultEnvoyGatewayNamespace = "envoy-gateway-system"
)

// ParseProxyInfraName parses a proxy infra key into (namespace, name).
// Non-merged keys are "namespace/name"; merged keys are bare GatewayClass names.
// For merged keys, namespace is the Envoy Gateway installation namespace
// (ENVOY_GATEWAY_NAMESPACE, defaults to "envoy-gateway-system").
func ParseProxyInfraName(key string) (namespace, name string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return resolveEnvoyGatewayNamespace(), key
}

// IsMergedGatewayKey reports whether key is a merged-gateway key (bare GatewayClass name, no "/").
// String-only heuristic — use IsMergedGatewayProxyInfra when the full IR is available.
func IsMergedGatewayKey(key string) bool {
	return !strings.Contains(key, "/")
}

// IsMergedGatewayProxyInfra reports whether infra belongs to a merged-gateway fleet.
// Checks the owning-gatewayclass metadata label first; falls back to IsMergedGatewayKey.
func IsMergedGatewayProxyInfra(infra *ir.Infra) bool {
	if infra == nil || infra.Proxy == nil {
		return false
	}
	proxyInfra := infra.Proxy
	if proxyInfra.Metadata != nil {
		if gwClass, ok := proxyInfra.Metadata.Labels[owningGatewayClassLabel]; ok && gwClass != "" {
			return true
		}
	}
	return IsMergedGatewayKey(proxyInfra.Name)
}

// resolveEnvoyGatewayNamespace returns the Envoy Gateway installation namespace.
// Reads ENVOY_GATEWAY_NAMESPACE (injected by the Helm chart to match the actual
// install namespace) and falls back to "envoy-gateway-system" if unset.
func resolveEnvoyGatewayNamespace() string {
	if ns := os.Getenv(envoyGatewayNamespaceEnvVar); ns != "" {
		return ns
	}
	return defaultEnvoyGatewayNamespace
}

// IsGatewayManagedByEnvoyGateway checks if a Gateway is managed by the Envoy Gateway controller.
func IsGatewayManagedByEnvoyGateway(ctx context.Context, c client.Client, gateway *gwapiv1.Gateway) bool {
	if gateway == nil {
		return false
	}
	gatewayClass := &gwapiv1.GatewayClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: string(gateway.Spec.GatewayClassName)}, gatewayClass); err != nil {
		return false
	}
	return string(gatewayClass.Spec.ControllerName) == egv1a1.GatewayControllerName
}

// ResolveEffectiveProxy returns the ELBProxyConfiguration that attaches to the given Gateway.
// Gateway-scoped targetRefs (same namespace) take precedence over GatewayClass-scoped ones.
// Ties within a tier break by (creationTimestamp, namespace, name) ascending.
func ResolveEffectiveProxy(
	proxies []*elbproxyv1alpha1.ELBProxyConfiguration,
	gatewayNamespace, gatewayName, gatewayClassName string,
	controllerNamespace string,
) *elbproxyv1alpha1.ELBProxyConfiguration {
	winner, _ := ResolveEffectiveProxyWithLosers(proxies, gatewayNamespace, gatewayName, gatewayClassName, controllerNamespace)
	return winner
}

// ResolveEffectiveProxyWithLosers is like ResolveEffectiveProxy but also returns
// same-tier candidates that lost the tiebreak, so callers can write Conflicted status on them.
//
// For merged gateways (gatewayNamespace == "") only GatewayClass-scoped targetRefs are
// considered. Gateway-scoped targetRefs are ignored because a merged fleet is identified
// by its GatewayClass, not by any individual Gateway resource.
//
// controllerNamespace is the namespace where the Envoy Gateway controller runs.
// GatewayClass-scoped targetRefs are honored only when the ELBProxyConfiguration lives in
// controllerNamespace.
func ResolveEffectiveProxyWithLosers(
	proxies []*elbproxyv1alpha1.ELBProxyConfiguration,
	gatewayNamespace, gatewayName, gatewayClassName string,
	controllerNamespace string,
) (winner *elbproxyv1alpha1.ELBProxyConfiguration, losers []*elbproxyv1alpha1.ELBProxyConfiguration) {
	if len(proxies) == 0 {
		return nil, nil
	}
	isMerged := gatewayNamespace == ""
	var gatewayMatches, classMatches []*elbproxyv1alpha1.ELBProxyConfiguration
	for _, p := range proxies {
		if p == nil {
			continue
		}
		for _, ref := range p.Spec.TargetRefs {
			if string(ref.Group) != GatewayAPIGroup {
				continue
			}
			switch string(ref.Kind) {
			case KindGateway:
				// Gateway-scoped refs are not valid for merged gateways.
				if !isMerged && p.Namespace == gatewayNamespace && string(ref.Name) == gatewayName {
					gatewayMatches = append(gatewayMatches, p)
				}
			case KindGatewayClass:
				if p.Namespace != controllerNamespace {
					continue
				}
				if gatewayClassName != "" && string(ref.Name) == gatewayClassName {
					classMatches = append(classMatches, p)
				}
			}
		}
	}
	switch {
	case len(gatewayMatches) > 0:
		sortELBProxyConfigurations(gatewayMatches)
		return gatewayMatches[0], gatewayMatches[1:]
	case len(classMatches) > 0:
		sortELBProxyConfigurations(classMatches)
		return classMatches[0], classMatches[1:]
	}
	return nil, nil
}

// ClassReferencedByTargetRefs reports whether p targets the given GatewayClass.
func ClassReferencedByTargetRefs(p *elbproxyv1alpha1.ELBProxyConfiguration, className, controllerNamespace string) bool {
	if p == nil || className == "" {
		return false
	}
	if p.Namespace != controllerNamespace {
		return false
	}
	for _, ref := range p.Spec.TargetRefs {
		if string(ref.Group) == GatewayAPIGroup &&
			string(ref.Kind) == KindGatewayClass &&
			string(ref.Name) == className {
			return true
		}
	}
	return false
}

// ClassesReferencedByTargetRefs returns the GatewayClass names from the proxy's targetRefs.
func ClassesReferencedByTargetRefs(p *elbproxyv1alpha1.ELBProxyConfiguration, controllerNamespace string) []string {
	if p == nil {
		return nil
	}
	if p.Namespace != controllerNamespace {
		return nil
	}
	var out []string
	for _, ref := range p.Spec.TargetRefs {
		if string(ref.Group) == GatewayAPIGroup && string(ref.Kind) == KindGatewayClass {
			out = append(out, string(ref.Name))
		}
	}
	return out
}

// GatewaysReferencedByTargetRefs returns "namespace/name" keys for Gateways in the proxy's targetRefs.
// Gateway refs resolve in the ELBProxyConfiguration's own namespace.
func GatewaysReferencedByTargetRefs(p *elbproxyv1alpha1.ELBProxyConfiguration) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, ref := range p.Spec.TargetRefs {
		if string(ref.Group) == GatewayAPIGroup && string(ref.Kind) == KindGateway {
			out = append(out, p.Namespace+"/"+string(ref.Name))
		}
	}
	return out
}

// sortELBProxyConfigurations sorts by (creationTimestamp, namespace, name) ascending.
func sortELBProxyConfigurations(matches []*elbproxyv1alpha1.ELBProxyConfiguration) {
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
}

// ELBProxyConfigurationKey returns the "namespace/name" key for an ELBProxyConfiguration, or empty string if nil.
func ELBProxyConfigurationKey(p *elbproxyv1alpha1.ELBProxyConfiguration) string {
	if p == nil {
		return ""
	}
	return p.Namespace + "/" + p.Name
}
