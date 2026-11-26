// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

// ResolveEffectiveProxy returns the ELBProxyConfiguration attached to the given gateway key, or nil.
func (r *Reconciler) ResolveEffectiveProxy(ctx context.Context, key string) (*elbproxyv1alpha1.ELBProxyConfiguration, error) {
	winner, _, err := r.resolveEffectiveProxyWithLosers(ctx, key)
	return winner, err
}

// resolveEffectiveProxyWithLosers returns the winning ELBProxyConfiguration and any same-tier losers.
// Losers are used to write Accepted=False / Conflicted status.
func (r *Reconciler) resolveEffectiveProxyWithLosers(ctx context.Context, key string) (*elbproxyv1alpha1.ELBProxyConfiguration, []*elbproxyv1alpha1.ELBProxyConfiguration, error) {
	ns, name := elbproxyutil.ParseProxyInfraName(key)
	className, err := r.gatewayClassFor(ctx, key, ns, name)
	if err != nil {
		return nil, nil, err
	}
	// For merged gateways (bare GatewayClass key, no namespace/name), pass an empty
	// namespace so that ResolveEffectiveProxyWithLosers skips Gateway-scoped targetRefs.
	resolveNS := ns
	if elbproxyutil.IsMergedGatewayKey(key) {
		resolveNS = ""
	}
	winner, losers := elbproxyutil.ResolveEffectiveProxyWithLosers(r.cache.AllELBProxyConfigurations(), resolveNS, name, className, r.controllerNamespace)
	return winner, losers, nil
}

// impactedGatewayKeys returns the union of:
//   - gateways currently attached to the proxy (handles drops),
//   - gateways named by Gateway-scoped targetRefs (handles adds),
//   - gateways in classes named by GatewayClass-scoped targetRefs (handles class fanout).
func (r *Reconciler) impactedGatewayKeys(proxyKey string) []string {
	seen := make(map[string]struct{})
	add := func(key string) {
		if key != "" {
			seen[key] = struct{}{}
		}
	}

	for _, key := range r.cache.GatewaysForEffectiveProxy(proxyKey) {
		add(key)
	}
	if proxy, ok := r.cache.GetELBProxyConfiguration(proxyKey); ok {
		for _, key := range elbproxyutil.GatewaysReferencedByTargetRefs(proxy) {
			add(key)
		}
		for _, className := range elbproxyutil.ClassesReferencedByTargetRefs(proxy, r.controllerNamespace) {
			for _, key := range r.cache.GatewaysInClass(className) {
				add(key)
			}
		}
	}

	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	return out
}

// gatewayClassFor returns the GatewayClass name for a gateway.
// Consults the cache first; on a miss fetches the Gateway from the API and caches the result.
//
// For merged gateways (bare GatewayClass key, no "namespace/name") the cache is
// always populated by the time infra arrives via gRPC, so a cache miss here means
// the reconcile was triggered before the first infra push. In that case the k8s
// lookup will return NotFound (no Gateway object exists with the class name) and
// we return "" — the caller will skip the reconcile and retry when infra arrives.
func (r *Reconciler) gatewayClassFor(ctx context.Context, key, namespace, name string) (string, error) {
	if className, ok := r.cache.GetGatewayClass(key); ok {
		return className, nil
	}

	// Merged gateway keys are bare GatewayClass names — no corresponding Gateway
	// object exists in the API. The class is set in the cache when infra arrives
	// via gRPC (CreateOrUpdateProxyInfra). A miss here means we were called too
	// early; log and return empty so the caller skips gracefully.
	if elbproxyutil.IsMergedGatewayKey(key) {
		r.logger.Info("merged gateway class not yet cached, skipping — will retry when infra arrives", "key", key)
		return "", nil
	}

	gw := &gwapiv1.Gateway{}
	if err := r.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, gw); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get gateway %s: %w", key, err)
	}
	className := string(gw.Spec.GatewayClassName)
	r.cache.SetGatewayClass(key, className)
	return className, nil
}

// tiebreakResults tracks which ELBProxyConfigurations won or lost a same-tier conflict
// during a reconcile pass. Used to write Accepted status conditions.
// A proxy can appear in both won and lost if it won for one gateway and lost for another.
type tiebreakResults struct {
	proxies map[string]*elbproxyv1alpha1.ELBProxyConfiguration
	won     map[string]bool
	lost    map[string]bool
}

func newTiebreakResults() *tiebreakResults {
	return &tiebreakResults{
		proxies: make(map[string]*elbproxyv1alpha1.ELBProxyConfiguration),
		won:     make(map[string]bool),
		lost:    make(map[string]bool),
	}
}

// observeGatewayResolution records the tiebreak winner and losers from one gateway's resolution.
func (t *tiebreakResults) observeGatewayResolution(winner *elbproxyv1alpha1.ELBProxyConfiguration, losers []*elbproxyv1alpha1.ELBProxyConfiguration) {
	if winner != nil {
		key := elbproxyutil.ELBProxyConfigurationKey(winner)
		t.proxies[key] = winner
		t.won[key] = true
	}
	for _, loser := range losers {
		if loser == nil {
			continue
		}
		key := elbproxyutil.ELBProxyConfigurationKey(loser)
		t.proxies[key] = loser
		t.lost[key] = true
	}
}

// syncELBProxyConfigurationStatuses writes Accepted=True on tiebreak winners and Accepted=False/Conflicted on losers.
func (r *Reconciler) syncELBProxyConfigurationStatuses(ctx context.Context, tiebreaks *tiebreakResults) error {
	if len(tiebreaks.proxies) == 0 {
		return nil
	}
	var errs []error
	for proxyKey, proxy := range tiebreaks.proxies {
		var cond metav1.Condition
		switch {
		case tiebreaks.won[proxyKey]:
			cond = metav1.Condition{
				Type:    elbproxyutil.ELBProxyConfigurationAcceptedConditionType,
				Status:  metav1.ConditionTrue,
				Reason:  elbproxyutil.ELBProxyConfigurationReasonAccepted,
				Message: "ELBProxyConfiguration is attached to at least one target",
			}
		case tiebreaks.lost[proxyKey]:
			cond = metav1.Condition{
				Type:    elbproxyutil.ELBProxyConfigurationAcceptedConditionType,
				Status:  metav1.ConditionFalse,
				Reason:  elbproxyutil.ELBProxyConfigurationReasonConflicted,
				Message: "Another ELBProxyConfiguration targeting the same resource outranks this one in tiebreak",
			}
		default:
			// observeGatewayResolution always sets won or lost when adding to proxies,
			// so this branch is unreachable in practice.
			continue
		}
		if err := r.patchAcceptedCondition(ctx, proxyKey, proxy, cond); err != nil {
			r.logger.Error(err, "failed to patch ELBProxyConfiguration status", "proxy", proxyKey)
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("status sync errors: %w", errors.Join(errs...))
	}
	return nil
}

// patchAcceptedCondition patches the Accepted condition on an ELBProxyConfiguration status subresource.
// No-ops if the condition is already up to date.
func (r *Reconciler) patchAcceptedCondition(ctx context.Context, proxyKey string, proxy *elbproxyv1alpha1.ELBProxyConfiguration, cond metav1.Condition) error {
	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(proxy), func(live *elbproxyv1alpha1.ELBProxyConfiguration) {
		c := cond
		c.ObservedGeneration = live.Generation
		meta.SetStatusCondition(&live.Status.Conditions, c)
	}); err != nil {
		return fmt.Errorf("patch ELBProxyConfiguration %s: %w", proxyKey, err)
	}
	return nil
}
