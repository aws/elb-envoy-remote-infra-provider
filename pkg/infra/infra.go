// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"errors"
	"fmt"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy"
	ec2deploy "github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	kubedeploy "github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/kubernetes"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	elbproxyir "github.com/aws/elb-envoy-remote-infra-provider/pkg/elbproxyir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/builder"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	elbproxyutil "github.com/aws/elb-envoy-remote-infra-provider/pkg/util"
	"k8s.io/apimachinery/pkg/labels"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Infra struct {
	logger                 logging.Logger
	elbProxyClient         elbproxyaws.ELBProxyClient
	k8sClient              client.Client
	subnetResolver         networking.SubnetsResolver
	trackingProvider       tracking.Provider
	stackDeployer          deploy.StackDeployer
	backendSGProvider      networking.BackendSGProvider
	sgReconciler           networking.SecurityGroupReconciler
	elbProxyTaggingManager elbproxy.TaggingManager
	ec2TaggingManager      ec2deploy.TaggingManager
	nodeSGResolver         networking.NodeSGResolver
	tagHelper              builder.TagHelper
	controllerNamespace    string
	clusterName            string
}

func NewInfra(ctx context.Context, cloudCfg elbproxyaws.CloudConfig, clusterName string, cli client.Client, logger logging.Logger, externalManagedTags []string, defaultTags map[string]string, pendingDeletionChecker networking.PendingDeletionChecker) (*Infra, error) {

	cloud, err := elbproxyaws.NewCloud(ctx, cloudCfg, logger.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS cloud: %w", err)
	}

	if clusterName == "" {
		return nil, fmt.Errorf("cluster name must be set via --cluster-name flag or CLUSTER_NAME env var")
	}

	vpcID := cloud.VpcID()
	ec2Client := cloud.EC2()
	elbProxyClient := cloud.ELBProxy()

	controllerNamespace := config.EnvoyGatewayNamespace()

	trackingProvider := tracking.NewProvider(clusterName)
	networkingSGManager := networking.NewDefaultSecurityGroupManager(ec2Client, logger.Logger)
	ec2TaggingManager := ec2deploy.NewDefaultTaggingManager(ec2Client, networkingSGManager, vpcID, logger)
	networkingSGReconciler := networking.NewDefaultSecurityGroupReconciler(networkingSGManager, logger.Logger)
	sgManager := ec2deploy.NewSecurityGroupManager(ec2Client, ec2TaggingManager, trackingProvider, networkingSGReconciler, vpcID, logger)
	backendSGProvider := networking.NewBackendSGProvider(clusterName, "", vpcID, ec2Client, cli, trackingProvider, logger,
		config.GatewayControllerName, controllerNamespace, pendingDeletionChecker)
	elbProxyTaggingManager := elbproxy.NewDefaultTaggingManager(elbProxyClient, cloud.RGT(), logger)
	proxyManager := elbproxy.NewDefaultProxyManager(elbProxyClient, elbProxyTaggingManager, trackingProvider,
		elbproxy.NewDefaultProxyAttributeReconciler(elbProxyClient, logger),
		elbproxy.NewDefaultProxyNetworkConfigReconciler(elbProxyClient, logger),
		elbproxy.NewDefaultProxyConfigurationReconciler(elbProxyClient, logger),
		externalManagedTags,
		logger)
	serviceManager := kubedeploy.NewDefaultServiceManager(cli, trackingProvider, logger)
	azInfoProvider := networking.NewDefaultAZInfoProvider(cloud.EC2(), logger.WithName("az-info-provider").Logger)
	subnetResolver := networking.NewDefaultSubnetsResolver(azInfoProvider, cloud.EC2(), vpcID, clusterName,
		true, // clusterTagCheckEnabled, for elbproxy all default to true, if ever need it configurable, we could be exposed via controller flag
		true, // discoveryByReachabilityEnabled, for elbproxy all default to true, if ever need it configurable, we could be exposed via controller flag
		logger.WithName("subnets-resolver").Logger)

	nodeInfoProvider := networking.NewDefaultNodeInfoProvider(ec2Client, logger.Logger)
	nodeENIResolver := networking.NewDefaultNodeENIInfoResolver(nodeInfoProvider, networkingSGManager, clusterName, logger.Logger)
	nodeSGResolver := networking.NewDefaultNodeSGResolver(nodeENIResolver, networkingSGManager, clusterName, vpcID, cli, logger.Logger)

	stackDeployer := deploy.NewStackDeployer(
		elbProxyClient,
		cli,
		proxyManager,
		serviceManager,
		trackingProvider,
		ec2TaggingManager,
		elbProxyTaggingManager,
		sgManager,
		logger,
	)

	tagHelper := builder.NewTagHelper(builder.TagHelperConfig{
		ExternalManagedTags:               sets.New(externalManagedTags...),
		DefaultTags:                       defaultTags,
		AdditionalTagsOverrideDefaultTags: true, // user tags take precedence over default tags
	})

	return &Infra{
		logger:                 logger,
		elbProxyClient:         elbProxyClient,
		k8sClient:              cli,
		stackDeployer:          stackDeployer,
		backendSGProvider:      backendSGProvider,
		sgReconciler:           networkingSGReconciler,
		elbProxyTaggingManager: elbProxyTaggingManager,
		ec2TaggingManager:      ec2TaggingManager,
		nodeSGResolver:         nodeSGResolver,
		subnetResolver:         subnetResolver,
		trackingProvider:       trackingProvider,
		tagHelper:              tagHelper,
		controllerNamespace:    controllerNamespace,
		clusterName:            clusterName,
	}, nil
}

func (i *Infra) CreateOrUpdateProxyInfra(ctx context.Context, elbProxyInfra *elbproxyir.ELBProxyInfra) (*elbproxyaws.ProxyDetails, error) {
	if elbProxyInfra == nil {
		return nil, errors.New("elbproxyInfra is nil")
	}
	if elbProxyInfra.Infra == nil {
		return nil, errors.New("elbproxyInfra.Infra is nil")
	}
	if elbProxyInfra.ELBProxyConfiguration == nil {
		return nil, errors.New("elbproxyInfra.ELBProxyConfiguration is nil")
	}

	infra := elbProxyInfra.Infra
	elbProxyConfig := elbProxyInfra.ELBProxyConfiguration
	backendTargetPorts := elbProxyInfra.BackendTargetPorts

	// Model build
	proxyInfra := infra.GetProxyInfra()
	namespace, name := elbproxyutil.ParseProxyInfraName(proxyInfra.Name)
	stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: namespace, Name: name}, elbProxyInfra.IRPresent)
	b := builder.NewProxyBuilder(i.logger, i.k8sClient, i.backendSGProvider, i.sgReconciler, i.subnetResolver, i.trackingProvider, i.nodeSGResolver, i.elbProxyTaggingManager, i.tagHelper, i.controllerNamespace, i.clusterName)
	if err := b.Build(ctx, infra, elbProxyConfig, stack, backendTargetPorts, elbProxyInfra.AllBackendTargetPorts, elbProxyInfra.XDSHosts); err != nil {
		return nil, fmt.Errorf("failed to build stack: %w", err)
	}

	i.logger.Info("deploying stack",
		"gatewayKey", proxyInfra.Name,
		"isMergedGateway", elbproxyutil.IsMergedGatewayProxyInfra(infra),
		"namespace", namespace,
		"name", name,
		"elbProxyConfig", fmt.Sprintf("%s/%s", elbProxyConfig.Namespace, elbProxyConfig.Name),
		"xdsHosts", elbProxyInfra.XDSHosts,
		"backendTargetPorts", backendTargetPorts,
		"allBackendTargetPorts", elbProxyInfra.AllBackendTargetPorts,
		"stackID", stack.StackID().String(),
	)

	// Deploy stack
	result, err := i.stackDeployer.Deploy(ctx, stack)
	if err != nil {
		// A provisioning requeue still carries the new proxy's details, for status.
		var proxyDetails *elbproxyaws.ProxyDetails
		if result != nil {
			proxyDetails = result.ProxyDetails
		}
		return proxyDetails, fmt.Errorf("failed to deploy stack: %w", err)
	}

	// Release backend SG if no backend target ports (all routes removed).
	if len(backendTargetPorts) == 0 {
		if err := i.backendSGProvider.Release(ctx, networking.ResourceTypeGateway,
			[]ktypes.NamespacedName{{Namespace: namespace, Name: name}}); err != nil {
			i.logger.Error(err, "failed to release backend SG", "gateway", proxyInfra.Name)
		}
	}

	if result == nil || result.ProxyDetails == nil {
		return nil, fmt.Errorf("deploy returned no proxy details for gateway %s", proxyInfra.Name)
	}

	return result.ProxyDetails, nil
}

func (i *Infra) DeleteProxyInfra(ctx context.Context, infra *ir.Infra) error {
	if infra == nil {
		return errors.New("infra ir is nil")
	}

	if infra.Proxy == nil {
		return errors.New("proxy ir is nil")
	}

	proxyName := infra.Proxy.Name
	i.logger.Info("deleting ELBProxy proxy infrastructure", "proxy", proxyName)

	namespace, name := elbproxyutil.ParseProxyInfraName(proxyName)
	stackID := core.StackID{Namespace: namespace, Name: name}

	var errs []error

	// 1. Deploy an empty stack — all existing SDK resources become unmatched and are deleted.
	emptyStack := core.NewDefaultStack(stackID)
	if _, err := i.stackDeployer.Deploy(ctx, emptyStack); err != nil {
		// Proxy still being deleted: hold off on frontend and backend SG cleanup until it is gone.
		if reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonWaitingForProxyDeletion) {
			i.logger.Info("waiting for proxy deletion before deleting security groups", "proxy", proxyName)
			return err
		}
		i.logger.Error(err, "failed to deploy empty stack for teardown", "proxy", proxyName)
		errs = append(errs, fmt.Errorf("failed to deploy empty stack for teardown: %w", err))
	}

	// 2. Node SG ingress rules for this gateway were already revoked by the gRPC
	// DeleteProxyInfra handler (server.go) before enqueuing this teardown.

	// 3. Release backend security group if no longer needed.
	// Backend SG is shared/cluster-scoped — its deletion failing should not block
	// per-gateway teardown. It will be retried on the next gateway create/delete cycle.
	if err := i.backendSGProvider.Release(ctx, networking.ResourceTypeGateway,
		[]ktypes.NamespacedName{{Namespace: namespace, Name: name}}); err != nil {
		i.logger.Error(err, "failed to release backend SG", "proxy", proxyName)
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("failed to clean up resources for proxy %s: %w", proxyName, err)
	}

	i.logger.Info("deleted ELBProxy proxy infrastructure", "proxy", proxyName)
	return nil
}

// ReconcileNodeSGRules recomputes node SG ingress rules from the given union of active
// backend ports. Revokes ports no longer needed after a gateway deletion. gatewaysActive keeps the
// control-plane ports (xDS and the Wasm module server) open while any gateway remains, even one
// with no backend ports.
func (i *Infra) ReconcileNodeSGRules(ctx context.Context, allBackendPorts []ir.PortProtocol, gatewaysActive bool) error {
	backendSGID, err := i.backendSGProvider.Get(ctx, networking.ResourceTypeGateway, nil)
	if err != nil {
		return fmt.Errorf("failed to get backend SG: %w", err)
	}

	netPorts := make([]networking.PortProtocol, 0, len(allBackendPorts))
	for _, p := range allBackendPorts {
		netPorts = append(netPorts, networking.PortProtocol{Protocol: p.Protocol, Port: int32(p.Port)})
	}
	nodeSGPorts := networking.CollectNodeSGPorts(netPorts, gatewaysActive, networking.DefaultXDSServerPort, networking.DefaultWasmServerPort)
	clusterLabels := networking.BuildClusterRuleLabels(i.controllerNamespace)
	nodeSGPerms := networking.BuildNodeSGPermissions(nodeSGPorts, backendSGID, clusterLabels)

	nodeSGIDs, err := i.nodeSGResolver.FindNodeSGsByClusterTag(ctx)
	if err != nil {
		return fmt.Errorf("failed to find node SG IDs: %w", err)
	}

	var errs []error
	for _, sgID := range nodeSGIDs {
		if err := i.sgReconciler.ReconcileIngress(ctx, sgID, nodeSGPerms,
			networking.WithPermissionSelector(labels.SelectorFromSet(clusterLabels))); err != nil {
			errs = append(errs, fmt.Errorf("failed to reconcile node SG %s: %w", sgID, err))
		}
	}
	return errors.Join(errs...)
}

func (i *Infra) CreateOrUpdateRateLimitInfra(ctx context.Context) error {
	// TODO: Implement rate limit infrastructure if needed
	return nil
}

// DiscoverManagedKeys lists proxies by cluster tag and returns their gateway keys.
// TODO: In multi-tenant deployments, this discovers all proxies in the cluster
// (including other tenants'). The reconciler bails out early for foreign keys
// (no ELBProxyConfiguration found), so it's safe but wasteful. Fix by adding
// TagKeyControllerNS to proxy tags at creation time and filtering here.
func (i *Infra) DiscoverManagedKeys(ctx context.Context) ([]string, error) {
	clusterTags := i.trackingProvider.BuildClusterTags("")
	filter := tracking.TagFilter{
		tracking.TagKeyCluster: {clusterTags[tracking.TagKeyCluster]},
	}

	proxies, err := i.elbProxyTaggingManager.ListProxies(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list proxies for key discovery: %w", err)
	}

	var keys []string
	for _, p := range proxies {
		stackTag, ok := p.Tags[tracking.TagKeyStack]
		if !ok || stackTag == "" {
			continue
		}
		keys = append(keys, stackTag)
	}
	i.logger.Info("discovered managed proxy keys from AWS", "count", len(keys))
	return keys, nil
}

func (i *Infra) DeleteRateLimitInfra(ctx context.Context) error {
	// TODO: Implement rate limit infrastructure deletion if needed
	return nil
}
