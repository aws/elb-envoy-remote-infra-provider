// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	elbproxyir "github.com/aws/elb-envoy-remote-infra-provider/pkg/elbproxyir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

const (
	elbproxyInfraReconciler = "elbproxy-infra-reconciler"
)

// InfraManager is the interface the reconciler uses to create, update, and delete
// proxy infrastructure.
type InfraManager interface {
	CreateOrUpdateProxyInfra(ctx context.Context, infra *elbproxyir.ELBProxyInfra) (*elbproxyaws.ProxyDetails, error)
	DeleteProxyInfra(ctx context.Context, infra *ir.Infra) error
	// ReconcileNodeSGRules recomputes node SG ingress rules from the current union of
	// active backend ports. Called after a gateway is deleted to revoke ports that
	// are no longer needed by any remaining gateway. gatewaysActive reports whether any
	// gateway remains, which keeps the xDS port open even with no backend ports.
	ReconcileNodeSGRules(ctx context.Context, allBackendPorts []ir.PortProtocol, gatewaysActive bool) error
}

// Reconciler drives proxy reconciliation using the infrastore cache.
// Triggered by InfraIR changes, ELBProxyConfiguration CRD changes, xDS host changes,
// and route (backend port) changes.
//
// Normally the event handler fans out ELBProxyConfiguration events to gateway keys at
// enqueue time, so controller-runtime's queue deduplication prevents the same
// gateway from being reconciled concurrently from two triggers.
//
// One exception: during the GatewayClass cold-start fallback the proxy is
// enqueued directly (gatewaysByClass was empty), so ReconcileForELBProxyConfiguration
// and a concurrent gateway key event can both call ReconcileKey for the same
// key simultaneously. keyMu serializes those calls — the second finds the
// config already up to date and skips the ELBProxy API call.
type Reconciler struct {
	logger              logging.Logger
	infra               InfraManager
	cache               *infrastore.Cache
	k8sClient           client.Client
	controllerNamespace string
	keyMu               keyedMutex
}

// New returns a new Reconciler.
func New(logger logging.Logger, infra InfraManager, c *infrastore.Cache, cli client.Client, controllerNamespace string) *Reconciler {
	return &Reconciler{
		logger:              logger.WithName(elbproxyInfraReconciler),
		infra:               infra,
		cache:               c,
		k8sClient:           cli,
		controllerNamespace: controllerNamespace,
	}
}

// keyedMutex provides per-key mutual exclusion using a sync.Map of *sync.Mutex.
// Only one goroutine may hold the lock for a given key at a time; all others block.
type keyedMutex struct {
	m sync.Map
}

func (km *keyedMutex) lock(key string) func() {
	v, _ := km.m.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ReconcileKey reconciles a single gateway key against the current cache state.
// A per-key lock serializes concurrent callers — the second call blocks until
// the first completes, then finds the config already up to date and skips the
// ELBProxy API call.
//
// A key is reconciled when known (via IR push or tag discovery). When IR is
// absent, only IR-free facets (archive, tags, subnets) reconcile.
//
// Returns the proxy DNS name on success (empty string if no infra was deployed).
func (r *Reconciler) ReconcileKey(ctx context.Context, key string) (string, error) {
	unlock := r.keyMu.lock(key)
	defer unlock()

	// If the key is pending deletion, run the teardown path.
	if r.cache.IsPendingDeletion(key) {
		r.logger.Info("key is pending deletion, running teardown", "key", key)
		infra := newKeyOnlyInfra(key)
		if err := r.infra.DeleteProxyInfra(ctx, infra); err != nil {
			r.patchDeletingStatus(ctx, key, err)
			// Requeue after 30s to allow ENI detachment / dependency resolution.
			// This avoids hammering the AWS API on every immediate requeue.
			return "", NewRequeueNeededAfter(
				fmt.Sprintf("teardown incomplete for %s: %v", key, err),
				30*time.Second,
			)
		}
		// Remove the status entry before forgetting the key, so a failure is retried.
		if err := r.updateGatewayProxyStatus(ctx, key, removeProxyStatusEntry); err != nil {
			return "", NewRequeueNeededAfter(fmt.Sprintf("remove proxy status for %s: %v", key, err), 30*time.Second)
		}
		if !r.cache.ForgetKeyIfPendingDeletion(key) {
			// Gateway was re-applied during teardown; reconcile it again to recreate.
			r.logger.Info("teardown complete but key was re-applied, requeueing", "key", key)
			return "", NewRequeueNeededAfter(fmt.Sprintf("%s re-applied during teardown", key), time.Second)
		}
		r.logger.Info("teardown complete, key forgotten", "key", key)
		return "", nil
	}

	wireInfra, _ := r.cache.GetInfra(key)
	if wireInfra == nil && !r.cache.IsKnownKey(key) {
		r.logger.V(1).Info("key is unknown and has no infra, skipping", "key", key)
		return "", nil
	}

	dnsName, err := r.reconcile(ctx, key, wireInfra)
	if err != nil && !IsRequeueError(err) {
		return "", err
	}

	// After a successful deploy (or in-progress requeue), ensure the effective
	// ELBProxyConfiguration has Accepted=True. This covers the cold-start race where a
	// GatewayClass-scoped ELBProxyConfiguration was created before EG pushed IR:
	// ReconcileForELBProxyConfiguration ran with 0 gateways and never wrote status.
	// syncELBProxyConfigurationStatuses is a no-op if the condition is already up-to-date.
	if proxyKey, ok := r.cache.GetEffectiveProxy(key); ok && proxyKey != "" {
		if proxy, found := r.cache.GetELBProxyConfiguration(proxyKey); found {
			// Build a minimal tiebreak result marking this proxy as the winner
			// (no losers) so syncELBProxyConfigurationStatuses writes Accepted=True
			// through the same path used by ReconcileForELBProxyConfiguration.
			tiebreaks := newTiebreakResults()
			tiebreaks.observeGatewayResolution(proxy, nil)
			if patchErr := r.syncELBProxyConfigurationStatuses(ctx, tiebreaks); patchErr != nil {
				r.logger.Error(patchErr, "failed to sync ELBProxyConfiguration status after deploy", "proxy", proxyKey)
			}
		}
	}

	return dnsName, err
}

// ReconcileAll reconciles all gateway keys in the cache.
func (r *Reconciler) ReconcileAll(ctx context.Context) error {
	keys := r.cache.AllInfraKeys()
	r.logger.Info("reconciling all gateways", "count", len(keys))
	var errs []error
	for _, key := range keys {
		if _, err := r.ReconcileKey(ctx, key); err != nil {
			if !IsRequeueError(err) {
				r.logger.Error(err, "failed to reconcile gateway", "key", key)
			}
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("reconcile errors: %w", errors.Join(errs...))
	}
	return nil
}

// ReconcileForELBProxyConfiguration reconciles gateways affected by an ELBProxyConfiguration change.
// Uses the index to find impacted gateways (currently-attached + targetRef fanout)
// and skips gateways whose resolution is unaffected by this proxy.
func (r *Reconciler) ReconcileForELBProxyConfiguration(ctx context.Context, proxyKey string) error {
	impacted := r.impactedGatewayKeys(proxyKey)
	r.logger.Info("reconciling gateways for ELBProxyConfiguration change", "proxy", proxyKey, "count", len(impacted))

	tiebreaks := newTiebreakResults()
	var errs []error

	for _, key := range impacted {
		prev, _ := r.cache.GetEffectiveProxy(key)
		winner, losers, err := r.resolveEffectiveProxyWithLosers(ctx, key)
		if err != nil {
			r.logger.Error(err, "failed to resolve ELBProxyConfiguration", "key", key)
			errs = append(errs, err)
			continue
		}
		tiebreaks.observeGatewayResolution(winner, losers)

		current := elbproxyutil.ELBProxyConfigurationKey(winner)
		if prev != proxyKey && current != proxyKey {
			// This gateway's effective proxy didn't change relative to the trigger — skip.
			continue
		}
		if _, err := r.ReconcileKey(ctx, key); err != nil {
			if !IsRequeueError(err) {
				r.logger.Error(err, "failed to reconcile gateway", "key", key)
			}
			errs = append(errs, err)
		}
	}

	if err := r.syncELBProxyConfigurationStatuses(ctx, tiebreaks); err != nil {
		r.logger.Error(err, "failed to sync ELBProxyConfiguration status", "proxy", proxyKey)
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("reconcile errors for proxy %s: %w", proxyKey, errors.Join(errs...))
	}
	return nil
}

// Delete removes proxy infrastructure for a gateway.
func (r *Reconciler) Delete(ctx context.Context, infra *ir.Infra) error {
	if infra == nil {
		return fmt.Errorf("infra is nil")
	}
	return r.infra.DeleteProxyInfra(ctx, infra)
}

// ReconcileNodeSGRules recomputes node SG rules from the current union of all active
// backend ports. Called after MarkPendingDeletion to revoke ports no longer needed.
// AllBackendPorts() only returns ports for gateways still in infraByKey, so the deleted
// gateway's ports are excluded. The xDS port stays open while any known gateway remains:
// AllKnownKeys() includes gateways seeded from AWS discovery after a restart, before their
// IR arrives, and MarkPendingDeletion removes the deleted gateway from it.
func (r *Reconciler) ReconcileNodeSGRules(ctx context.Context) error {
	return r.infra.ReconcileNodeSGRules(ctx, r.cache.AllBackendPorts(), len(r.cache.AllKnownKeys()) > 0)
}

// ELBProxyConfiguration returns the cached ELBProxyConfiguration for a given key ("namespace/name").
func (r *Reconciler) ELBProxyConfiguration(key string) (*elbproxyv1alpha1.ELBProxyConfiguration, bool) {
	return r.cache.GetELBProxyConfiguration(key)
}

func (r *Reconciler) reconcile(ctx context.Context, key string, wireInfra *ir.Infra) (string, error) {
	irPresent := wireInfra != nil
	r.logger.Info("reconciling gateway", "key", key, "irPresent", irPresent)

	elbProxyConfig, err := r.ResolveEffectiveProxy(ctx, key)
	if err != nil {
		return "", fmt.Errorf("resolve ELBProxyConfiguration for %s: %w", key, err)
	}
	proxyKey := elbproxyutil.ELBProxyConfigurationKey(elbProxyConfig)
	// The Gateway moved off its previous configuration: drop its entry there before
	// updating the index, so a failure is retried.
	if prev, ok := r.cache.GetEffectiveProxy(key); ok && prev != proxyKey {
		if err := r.removeDetachedProxyStatus(ctx, prev, key); err != nil {
			return "", fmt.Errorf("remove proxy status from %s for %s: %w", prev, key, err)
		}
	}
	// Update the reverse index for targeted fanout.
	r.cache.SetEffectiveProxy(key, proxyKey)

	if elbProxyConfig == nil {
		r.logger.Info("ELBProxyConfiguration not resolved, skipping reconcile", "key", key)
		return "", nil
	}

	backendPorts := r.cache.GetBackendPorts(key)
	if irPresent && backendPorts == nil {
		// Backend ports not yet populated. Only gate IR-present path — IR-absent
		// preserves SGs anyway, so missing ports can't cause unwanted revokes.
		r.logger.Info("backend ports not yet available, deferring reconcile to route watcher", "key", key)
		return "", nil
	}

	if !irPresent {
		// Synthesize minimal Infra so proxy name/stack ID resolve correctly.
		wireInfra = newKeyOnlyInfra(key)
	}

	effective := &elbproxyir.ELBProxyInfra{
		Infra:                 wireInfra,
		ELBProxyConfiguration: elbProxyConfig,
		BackendTargetPorts:    backendPorts,
		AllBackendTargetPorts: r.cache.AllBackendPorts(),
		XDSHosts:              r.cache.XDSHosts(),
		IRPresent:             irPresent,
	}

	result, err := r.infra.CreateOrUpdateProxyInfra(ctx, effective)
	if err != nil {
		// A proxy still being created returns a requeue: report Pending, with its ARN if known.
		if IsRequeueAfterReason(err, RequeueReasonProxyProvisioning) {
			pending := result
			if pending == nil {
				pending = &elbproxyaws.ProxyDetails{
					Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusCreateInProgress},
				}
			}
			if patchErr := r.patchProxyHealth(ctx, elbProxyConfig, key, pending); patchErr != nil {
				r.logger.Error(patchErr, "failed to patch proxy health status", "key", key)
			}
		}
		r.patchDeletingStatus(ctx, key, err)
		return "", fmt.Errorf("CreateOrUpdateProxyInfra failed for %s: %w", key, err)
	}
	if result == nil {
		return "", nil
	}

	// Best-effort: a status patch failure doesn't fail the reconcile.
	if patchErr := r.patchProxyHealth(ctx, elbProxyConfig, key, result); patchErr != nil {
		r.logger.Error(patchErr, "failed to patch proxy health status", "key", key)
	}

	return result.DnsName, nil
}

// newKeyOnlyInfra builds a minimal Infra so proxy name/stack ID resolve correctly
// in IR-absent mode. No listeners or config are set.
func newKeyOnlyInfra(key string) *ir.Infra {
	return ir.New(&pb.Infra{
		Proxy: &pb.ProxyInfra{
			Name: key,
		},
	})
}
