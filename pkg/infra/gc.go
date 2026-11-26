// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/controller"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
)

// GarbageCollector discovers and cleans up leaked AWS resources (proxies, security
// groups) that no longer have a corresponding Kubernetes Gateway. Designed to run
// once at startup after leader election to recover from in-memory pendingDeletion
// state lost during pod crashes.
//
// Two resource types are scanned independently:
//   - Proxies: discovered via the ELBProxy API. If the proxy exists but its Gateway
//     is gone, the key is re-enqueued for full teardown (DeleteProxy + SG delete).
//   - Security Groups: discovered via EC2 tags. Handles the case where DeleteProxy
//     already succeeded but the pod crashed before the SG could be deleted (ENI
//     still attached at the time). On restart, no proxy exists to discover via
//     ListProxies, but the orphaned SG is still tagged.
type GarbageCollector struct {
	infra        *Infra
	cache        *infrastore.Cache
	k8sClient    client.Client
	gatewayKeyCh controller.GatewayEventCh
}

// NewGarbageCollector creates a GarbageCollector.
func NewGarbageCollector(infra *Infra, cache *infrastore.Cache, k8sClient client.Client, gatewayKeyCh controller.GatewayEventCh) *GarbageCollector {
	return &GarbageCollector{
		infra:        infra,
		cache:        cache,
		k8sClient:    k8sClient,
		gatewayKeyCh: gatewayKeyCh,
	}
}

// RunPeriodic runs GC once immediately, then repeats at the given interval until
// the context is cancelled. This ensures orphaned resources are eventually cleaned
// up even if the initial sweep fails due to transient API errors.
func (gc *GarbageCollector) RunPeriodic(ctx context.Context, interval time.Duration) {
	gc.Run(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gc.Run(ctx)
		}
	}
}

// Run performs a single GC sweep. It discovers all controller-owned resources via
// tags, identifies orphaned keys (no corresponding Gateway in k8s), and enqueues
// them for teardown via the existing pendingDeletion → reconcile path.
func (gc *GarbageCollector) Run(ctx context.Context) {
	gc.infra.logger.Info("running resource garbage collection")

	orphanedKeys, err := gc.discoverOrphanedKeys(ctx)
	if err != nil {
		gc.infra.logger.Error(err, "gc: discovery failed")
		return
	}

	if len(orphanedKeys) == 0 {
		gc.infra.logger.Info("gc complete, no orphans found")
		return
	}

	gc.infra.logger.Info("gc found orphaned resources",
		"count", len(orphanedKeys),
		"keys", fmt.Sprintf("%v", orphanedKeys))

	for _, key := range orphanedKeys {
		gc.cache.MarkPendingDeletion(key)
		controller.EnqueueGatewayKey(gc.gatewayKeyCh, key)
		gc.infra.logger.Info("gc: enqueued orphaned key for teardown", "key", key)
	}
}

// discoverOrphanedKeys finds gateway keys that have AWS resources but no corresponding
// Gateway in Kubernetes. Scans both proxies and security groups to cover all crash scenarios.
func (gc *GarbageCollector) discoverOrphanedKeys(ctx context.Context) ([]string, error) {
	orphanedSet := make(map[string]struct{})

	// 1. Discover orphaned proxies.
	proxyKeys, err := gc.discoverOrphanedProxyKeys(ctx)
	if err != nil {
		gc.infra.logger.Error(err, "gc: failed to discover orphaned proxies")
	} else {
		for _, k := range proxyKeys {
			orphanedSet[k] = struct{}{}
		}
	}

	// 2. Discover orphaned SGs (proxy already deleted, SG left behind).
	sgKeys, err := gc.discoverOrphanedSGKeys(ctx)
	if err != nil {
		gc.infra.logger.Error(err, "gc: failed to discover orphaned security groups")
	} else {
		for _, k := range sgKeys {
			orphanedSet[k] = struct{}{}
		}
	}

	orphaned := make([]string, 0, len(orphanedSet))
	for k := range orphanedSet {
		orphaned = append(orphaned, k)
	}
	return orphaned, nil
}

// clusterFilter returns a tag filter that matches all resources owned by this
// controller in this cluster. Uses the same pattern as DiscoverManagedKeys.
func (gc *GarbageCollector) clusterFilter() tracking.TagFilter {
	clusterTags := gc.infra.trackingProvider.BuildClusterTags("")
	return tracking.TagFilter{
		tracking.TagKeyCluster:   {clusterTags[tracking.TagKeyCluster]},
		tracking.TagKeyManagedBy: {tracking.TagValueManagedBy},
	}
}

func (gc *GarbageCollector) discoverOrphanedProxyKeys(ctx context.Context) ([]string, error) {
	proxies, err := gc.infra.elbProxyTaggingManager.ListProxies(ctx, gc.clusterFilter())
	if err != nil {
		return nil, fmt.Errorf("failed to list managed proxies: %w", err)
	}

	var orphaned []string
	for _, p := range proxies {
		key := p.Tags[tracking.TagKeyStack]
		if key == "" {
			continue
		}
		if gc.gatewayExistsForProxy(ctx, key, p.Tags) {
			// Gateway exists but cache doesn't know about it — EG hasn't pushed
			// IR yet (startup race). Don't GC it.
			continue
		}
		orphaned = append(orphaned, key)
	}
	return orphaned, nil
}

func (gc *GarbageCollector) discoverOrphanedSGKeys(ctx context.Context) ([]string, error) {
	sgs, err := gc.infra.ec2TaggingManager.ListSecurityGroups(ctx, gc.clusterFilter())
	if err != nil {
		return nil, fmt.Errorf("failed to list managed security groups: %w", err)
	}

	var orphaned []string
	for _, sg := range sgs {
		key := sg.Tags[tracking.TagKeyStack]
		if key == "" {
			continue
		}
		if gc.gatewayExistsForProxy(ctx, key, sg.Tags) {
			continue
		}
		orphaned = append(orphaned, key)
	}
	return orphaned, nil
}

// gatewayExists checks whether the owner of a given key still exists in Kubernetes.
// The key is a stack ID ("namespace/name") from the AWS tag.
//   - Per-gateway stacks: checks if the Gateway ns/name exists.
//   - Merged stacks (identified by TagKeyDeployMode=merged): checks if any Gateway
//     in the cluster references the GatewayClass named after the "name" portion.
func (gc *GarbageCollector) gatewayExistsForProxy(ctx context.Context, key string, tags map[string]string) bool {
	ns, name := elbproxyutil.ParseProxyInfraName(key)

	// Check if this is a merged stack via the explicit tag.
	if tags[tracking.TagKeyDeployMode] == tracking.TagValueDeployModeMerged {
		gwList := &gwapiv1.GatewayList{}
		if err := gc.k8sClient.List(ctx, gwList); err != nil {
			return true // On error, assume exists — don't GC.
		}
		for i := range gwList.Items {
			if string(gwList.Items[i].Spec.GatewayClassName) == name {
				return true
			}
		}
		return false
	}

	// Per-gateway: does a Gateway with this exact ns/name exist?
	gw := &gwapiv1.Gateway{}
	if err := gc.k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, gw); err != nil {
		return false
	}
	return true
}
