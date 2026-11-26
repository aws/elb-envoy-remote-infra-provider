// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

// gatewayRefFromKey maps a gateway key to its GatewayReference. A merged key (no "/") is a GatewayClass.
func gatewayRefFromKey(key string) elbproxyv1alpha1.GatewayReference {
	if elbproxyutil.IsMergedGatewayKey(key) {
		return elbproxyv1alpha1.GatewayReference{
			Kind: elbproxyutil.KindGatewayClass,
			Name: key,
		}
	}
	ns, name := elbproxyutil.ParseProxyInfraName(key)
	return elbproxyv1alpha1.GatewayReference{
		Kind:      elbproxyutil.KindGateway,
		Namespace: ns,
		Name:      name,
	}
}

// programmedConditionFor maps a proxy status code to the Programmed condition.
// ok is false for transient codes, which keep the existing condition.
func programmedConditionFor(status elbproxyaws.ProxyStatus) (metav1.Condition, bool) {
	switch status.Code {
	case elbproxyaws.ProxyStatusActive:
		return metav1.Condition{
			Type:    elbproxyutil.ELBProxyConfigurationProgrammedConditionType,
			Status:  metav1.ConditionTrue,
			Reason:  elbproxyutil.ELBProxyConfigurationReasonProgrammed,
			Message: "Proxy is active",
		}, true
	case elbproxyaws.ProxyStatusActionRequired:
		return metav1.Condition{
			Type:    elbproxyutil.ELBProxyConfigurationProgrammedConditionType,
			Status:  metav1.ConditionFalse,
			Reason:  elbproxyutil.ELBProxyConfigurationReasonActionRequired,
			Message: formatProxyStatusReasons(status.Reasons),
		}, true
	case elbproxyaws.ProxyStatusCreateFailed:
		return metav1.Condition{
			Type:    elbproxyutil.ELBProxyConfigurationProgrammedConditionType,
			Status:  metav1.ConditionFalse,
			Reason:  elbproxyutil.ELBProxyConfigurationReasonCreateFailed,
			Message: "Proxy failed to create",
		}, true
	case elbproxyaws.ProxyStatusCreateInProgress:
		return metav1.Condition{
			Type:    elbproxyutil.ELBProxyConfigurationProgrammedConditionType,
			Status:  metav1.ConditionFalse,
			Reason:  elbproxyutil.ELBProxyConfigurationReasonPending,
			Message: "Proxy is being created",
		}, true
	default:
		return metav1.Condition{}, false
	}
}

const (
	// statusPatchAttempts bounds patchStatus retries on a resourceVersion conflict.
	statusPatchAttempts = 5
	// maxProxyStatusEntries matches the MaxItems on ELBProxyConfigurationStatus.Proxies.
	maxProxyStatusEntries = 32
)

// patchStatus applies mutate to a fresh read of the ELBProxyConfiguration and patches its status
// with a resourceVersion check, retrying on conflict. Gateways sharing one configuration
// reconcile concurrently, and a merge patch replaces whole status lists.
func (r *Reconciler) patchStatus(ctx context.Context, key client.ObjectKey, mutate func(live *elbproxyv1alpha1.ELBProxyConfiguration)) error {
	patch := func() error {
		live := &elbproxyv1alpha1.ELBProxyConfiguration{}
		if err := r.k8sClient.Get(ctx, key, live); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("get ELBProxyConfiguration %s: %w", key, err)
		}
		base := live.DeepCopy()
		mutate(live)
		if apiequality.Semantic.DeepEqual(base.Status, live.Status) {
			return nil
		}
		err := r.k8sClient.Status().Patch(ctx, live, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	var err error
	for range statusPatchAttempts {
		if err = patch(); !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}

// patchProxyHealth upserts the Gateway's entry and recomputes the aggregate Programmed condition.
func (r *Reconciler) patchProxyHealth(ctx context.Context, proxy *elbproxyv1alpha1.ELBProxyConfiguration, key string, details *elbproxyaws.ProxyDetails) error {
	programmed, programmedKnown := programmedConditionFor(details.Status)
	ref := gatewayRefFromKey(key)
	return r.patchStatus(ctx, client.ObjectKeyFromObject(proxy), func(live *elbproxyv1alpha1.ELBProxyConfiguration) {
		entry := findProxyStatusEntry(live.Status.Proxies, ref)
		if entry == nil {
			if !programmedKnown {
				// Transient code and no entry yet: wait for a settled code.
				return
			}
			if len(live.Status.Proxies) >= maxProxyStatusEntries {
				// The API server would reject the whole patch past the CRD limit.
				r.logger.Info("status.proxies is full, not reporting this Gateway", "key", key, "limit", maxProxyStatusEntries)
				return
			}
			live.Status.Proxies = append(live.Status.Proxies, elbproxyv1alpha1.ProxyStatusEntry{GatewayRef: ref})
			entry = &live.Status.Proxies[len(live.Status.Proxies)-1]
		}
		if details.Arn != "" {
			entry.ProxyArn = details.Arn
		}
		if programmedKnown {
			programmed.ObservedGeneration = live.Generation
			meta.SetStatusCondition(&entry.Conditions, programmed)
		}
		meta.SetStatusCondition(&live.Status.Conditions, aggregateProgrammedCondition(live.Status.Proxies, live.Generation))
	})
}

// patchDeletingStatus sets Deleting while waiting for a proxy to be gone (teardown or replacement).
func (r *Reconciler) patchDeletingStatus(ctx context.Context, key string, err error) {
	var requeue *RequeueNeededAfter
	if !errors.As(err, &requeue) {
		return
	}
	var message string
	switch requeue.Reason() {
	case RequeueReasonWaitingForProxyDeletion:
		message = "Proxy is being deleted"
	case RequeueReasonPreviousProxyDeleting:
		message = "Replacing the proxy: deleting the old proxy"
	default:
		return
	}
	cond := metav1.Condition{
		Type:    elbproxyutil.ELBProxyConfigurationProgrammedConditionType,
		Status:  metav1.ConditionFalse,
		Reason:  elbproxyutil.ELBProxyConfigurationReasonDeleting,
		Message: message,
	}
	setCond := func(entries []elbproxyv1alpha1.ProxyStatusEntry, i int, generation int64) []elbproxyv1alpha1.ProxyStatusEntry {
		c := cond
		c.ObservedGeneration = generation
		meta.SetStatusCondition(&entries[i].Conditions, c)
		return entries
	}
	if err := r.updateGatewayProxyStatus(ctx, key, setCond); err != nil {
		r.logger.Error(err, "failed to patch proxy teardown status", "key", key)
	}
}

// removeProxyStatusEntry drops entry i; used once a Gateway's proxy is gone.
func removeProxyStatusEntry(entries []elbproxyv1alpha1.ProxyStatusEntry, i int, _ int64) []elbproxyv1alpha1.ProxyStatusEntry {
	return append(entries[:i], entries[i+1:]...)
}

// updateGatewayProxyStatus applies update to the Gateway's entry on every ELBProxyConfiguration.
// Entries are found by Gateway ref because teardown has no cache mapping.
func (r *Reconciler) updateGatewayProxyStatus(ctx context.Context, key string,
	update func(entries []elbproxyv1alpha1.ProxyStatusEntry, i int, generation int64) []elbproxyv1alpha1.ProxyStatusEntry) error {
	list := &elbproxyv1alpha1.ELBProxyConfigurationList{}
	if err := r.k8sClient.List(ctx, list); err != nil {
		return fmt.Errorf("list ELBProxyConfigurations: %w", err)
	}
	ref := gatewayRefFromKey(key)
	hasEntry := func(e elbproxyv1alpha1.ProxyStatusEntry) bool { return e.GatewayRef == ref }
	var errs []error
	for idx := range list.Items {
		if !slices.ContainsFunc(list.Items[idx].Status.Proxies, hasEntry) {
			continue
		}
		objKey := client.ObjectKeyFromObject(&list.Items[idx])
		err := r.patchStatus(ctx, objKey, func(live *elbproxyv1alpha1.ELBProxyConfiguration) {
			updateProxyStatusEntry(live, ref, update)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("patch ELBProxyConfiguration %s status: %w", objKey, err))
		}
	}
	return errors.Join(errs...)
}

// removeDetachedProxyStatus drops the Gateway's entry from proxyKey, the ELBProxyConfiguration
// it no longer resolves to.
func (r *Reconciler) removeDetachedProxyStatus(ctx context.Context, proxyKey, key string) error {
	ns, name, _ := strings.Cut(proxyKey, "/")
	ref := gatewayRefFromKey(key)
	return r.patchStatus(ctx, client.ObjectKey{Namespace: ns, Name: name}, func(live *elbproxyv1alpha1.ELBProxyConfiguration) {
		updateProxyStatusEntry(live, ref, removeProxyStatusEntry)
	})
}

// updateProxyStatusEntry applies update to ref's entry, if any, and recomputes the aggregate.
func updateProxyStatusEntry(live *elbproxyv1alpha1.ELBProxyConfiguration, ref elbproxyv1alpha1.GatewayReference,
	update func(entries []elbproxyv1alpha1.ProxyStatusEntry, i int, generation int64) []elbproxyv1alpha1.ProxyStatusEntry) {
	i := slices.IndexFunc(live.Status.Proxies, func(e elbproxyv1alpha1.ProxyStatusEntry) bool { return e.GatewayRef == ref })
	if i < 0 {
		return
	}
	live.Status.Proxies = update(live.Status.Proxies, i, live.Generation)
	if len(live.Status.Proxies) == 0 {
		meta.RemoveStatusCondition(&live.Status.Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType)
	} else {
		meta.SetStatusCondition(&live.Status.Conditions, aggregateProgrammedCondition(live.Status.Proxies, live.Generation))
	}
}

// findProxyStatusEntry returns a pointer to the entry matching ref, or nil.
func findProxyStatusEntry(entries []elbproxyv1alpha1.ProxyStatusEntry, ref elbproxyv1alpha1.GatewayReference) *elbproxyv1alpha1.ProxyStatusEntry {
	for i := range entries {
		if entries[i].GatewayRef == ref {
			return &entries[i]
		}
	}
	return nil
}

// aggregateProgrammedCondition is True when at least one entry's proxy is active.
func aggregateProgrammedCondition(entries []elbproxyv1alpha1.ProxyStatusEntry, generation int64) metav1.Condition {
	total := len(entries)
	programmed := 0
	for i := range entries {
		if c := meta.FindStatusCondition(entries[i].Conditions, elbproxyutil.ELBProxyConfigurationProgrammedConditionType); c != nil && c.Status == metav1.ConditionTrue {
			programmed++
		}
	}
	if programmed > 0 {
		return metav1.Condition{
			Type:               elbproxyutil.ELBProxyConfigurationProgrammedConditionType,
			Status:             metav1.ConditionTrue,
			Reason:             elbproxyutil.ELBProxyConfigurationReasonProgrammed,
			ObservedGeneration: generation,
			Message:            fmt.Sprintf("%d of %d backing proxies are active", programmed, total),
		}
	}
	return metav1.Condition{
		Type:               elbproxyutil.ELBProxyConfigurationProgrammedConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             elbproxyutil.ELBProxyConfigurationReasonPending,
		ObservedGeneration: generation,
		Message:            fmt.Sprintf("0 of %d backing proxies are active", total),
	}
}

// formatProxyStatusReasons renders reasons as "<code>: <message> [<target>]", joined by "; ".
func formatProxyStatusReasons(reasons []elbproxyaws.ProxyStatusReason) string {
	if len(reasons) == 0 {
		return "Proxy requires action"
	}
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		s := r.Code
		if r.Message != "" {
			s += ": " + r.Message
		}
		if r.Target != "" {
			s += " [" + r.Target + "]"
		}
		parts = append(parts, s)
	}
	return "Proxy requires action: " + strings.Join(parts, "; ")
}
