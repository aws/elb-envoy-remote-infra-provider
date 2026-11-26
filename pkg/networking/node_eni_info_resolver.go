// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"fmt"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	defaultNodeENIInfoCacheTTL = 10 * time.Minute
)

// ENIInfo contains the resolved ENI identity and its attached security groups.
type ENIInfo struct {
	// NetworkInterfaceID is the ENI's ID.
	NetworkInterfaceID string
	// SecurityGroups are the SG IDs attached to this ENI.
	SecurityGroups []string
}

// NodeENIInfoResolver resolves the primary ENI and its node SG for each k8s node.
//
//go:generate mockgen -copyright_file=../../hack/boilerplate.mockgen.txt -destination node_eni_info_resolver_mock.go -package networking github.com/aws/elb-envoy-remote-infra-provider/pkg/networking NodeENIInfoResolver
type NodeENIInfoResolver interface {
	// Resolve returns the node SG ID for each node, keyed by NamespacedName.
	// It discovers the node SG by finding the primary ENI (DeviceIndex==0) and
	// filtering its SGs by the kubernetes.io/cluster/<clusterName> tag
	Resolve(ctx context.Context, nodes []*corev1.Node) (map[types.NamespacedName]string, error)
}

// NewDefaultNodeENIInfoResolver constructs a NodeENIInfoResolver.
func NewDefaultNodeENIInfoResolver(nodeInfoProvider NodeInfoProvider, sgManager SecurityGroupManager, clusterName string, logger logr.Logger) NodeENIInfoResolver {
	return &defaultNodeENIInfoResolver{
		nodeInfoProvider:      nodeInfoProvider,
		sgManager:             sgManager,
		clusterName:           clusterName,
		logger:                logger,
		nodeENIInfoCache:      cache.NewExpiring(),
		nodeENIInfoCacheMutex: sync.RWMutex{},
		nodeENIInfoCacheTTL:   defaultNodeENIInfoCacheTTL,
	}
}

var _ NodeENIInfoResolver = &defaultNodeENIInfoResolver{}

type defaultNodeENIInfoResolver struct {
	nodeInfoProvider NodeInfoProvider
	sgManager        SecurityGroupManager
	clusterName      string
	logger           logr.Logger

	nodeENIInfoCache      *cache.Expiring
	nodeENIInfoCacheMutex sync.RWMutex
	nodeENIInfoCacheTTL   time.Duration
}

// nodeENIInfoCacheKey is keyed by both node name and UID so a replaced node (same name, new UID)
type nodeENIInfoCacheKey struct {
	nodeKey types.NamespacedName
	nodeUID types.UID
}

func (r *defaultNodeENIInfoResolver) Resolve(ctx context.Context, nodes []*corev1.Node) (map[types.NamespacedName]string, error) {
	sgIDByNodeKey := r.fetchFromCache(nodes)
	missing := r.computeMissingNodes(nodes, sgIDByNodeKey)

	if len(missing) > 0 {
		resolved, err := r.resolveViaInstanceID(ctx, missing)
		if err != nil {
			return nil, err
		}
		r.saveToCache(missing, resolved)
		for nodeKey, sgID := range resolved {
			sgIDByNodeKey[nodeKey] = sgID
		}
		missing = r.computeMissingNodes(missing, sgIDByNodeKey)
	}

	if len(missing) > 0 {
		unresolved := make([]types.NamespacedName, 0, len(missing))
		for _, node := range missing {
			unresolved = append(unresolved, types.NamespacedName{Name: node.Name})
		}
		return nil, fmt.Errorf("cannot resolve node SG for nodes: %v", unresolved)
	}
	return sgIDByNodeKey, nil
}

func (r *defaultNodeENIInfoResolver) resolveViaInstanceID(ctx context.Context, nodes []*corev1.Node) (map[types.NamespacedName]string, error) {
	nodeInstanceByNodeKey, err := r.nodeInfoProvider.FetchNodeInstances(ctx, nodes)
	if err != nil {
		return nil, err
	}

	sgIDByNodeKey := make(map[types.NamespacedName]string, len(nodeInstanceByNodeKey))
	for nodeKey, instance := range nodeInstanceByNodeKey {
		primaryENI, err := findInstancePrimaryENI(instance.NetworkInterfaces)
		if err != nil {
			return nil, fmt.Errorf("node %v: %w", nodeKey, err)
		}
		eniInfo := buildENIInfoViaInstanceENI(primaryENI)
		sgID, err := r.resolveNodeSGFromENI(ctx, eniInfo)
		if err != nil {
			return nil, fmt.Errorf("node %v: %w", nodeKey, err)
		}
		sgIDByNodeKey[nodeKey] = sgID
	}
	return sgIDByNodeKey, nil
}

// resolveNodeSGFromENI selects the node security group from an ENI:
// if the ENI has a single SG, use it directly.
// If multiple, find the one tagged kubernetes.io/cluster/<clusterName>.
func (r *defaultNodeENIInfoResolver) resolveNodeSGFromENI(ctx context.Context, eniInfo ENIInfo) (string, error) {
	if len(eniInfo.SecurityGroups) == 1 {
		return eniInfo.SecurityGroups[0], nil
	}

	sgInfoByID, err := r.sgManager.FetchSGInfosByID(ctx, eniInfo.SecurityGroups)
	if err != nil {
		return "", err
	}

	clusterTagKey := fmt.Sprintf("kubernetes.io/cluster/%s", r.clusterName)
	matched := sets.NewString()
	for sgID, sgInfo := range sgInfoByID {
		if _, ok := sgInfo.Tags[clusterTagKey]; ok {
			matched.Insert(sgID)
		}
	}

	if matched.Len() != 1 {
		return "", fmt.Errorf("expected exactly one SG tagged %q on ENI %s, got: %v",
			clusterTagKey, eniInfo.NetworkInterfaceID, matched.List())
	}
	sgID, _ := matched.PopAny()
	return sgID, nil
}

// findInstancePrimaryENI returns the ENI with DeviceIndex == 0.
func findInstancePrimaryENI(enis []ec2types.InstanceNetworkInterface) (ec2types.InstanceNetworkInterface, error) {
	for _, eni := range enis {
		if eni.Attachment != nil && awssdk.ToInt32(eni.Attachment.DeviceIndex) == 0 {
			return eni, nil
		}
	}
	return ec2types.InstanceNetworkInterface{}, fmt.Errorf("no ENI with DeviceIndex 0 found; instance may have non-standard ENI configuration")
}

// buildENIInfoViaInstanceENI extracts ENIInfo from an instance's network interface.
func buildENIInfoViaInstanceENI(eni ec2types.InstanceNetworkInterface) ENIInfo {
	sgIDs := make([]string, 0, len(eni.Groups))
	for _, g := range eni.Groups {
		if g.GroupId != nil {
			sgIDs = append(sgIDs, awssdk.ToString(g.GroupId))
		}
	}
	return ENIInfo{
		NetworkInterfaceID: awssdk.ToString(eni.NetworkInterfaceId),
		SecurityGroups:     sgIDs,
	}
}

func (r *defaultNodeENIInfoResolver) fetchFromCache(nodes []*corev1.Node) map[types.NamespacedName]string {
	r.nodeENIInfoCacheMutex.RLock()
	defer r.nodeENIInfoCacheMutex.RUnlock()

	result := make(map[types.NamespacedName]string)
	for _, node := range nodes {
		cacheKey := nodeENIInfoCacheKey{nodeKey: types.NamespacedName{Name: node.Name}, nodeUID: node.UID}
		if raw, exists := r.nodeENIInfoCache.Get(cacheKey); exists {
			result[types.NamespacedName{Name: node.Name}] = raw.(string)
		}
	}
	return result
}

func (r *defaultNodeENIInfoResolver) saveToCache(nodes []*corev1.Node, sgIDByNodeKey map[types.NamespacedName]string) {
	r.nodeENIInfoCacheMutex.Lock()
	defer r.nodeENIInfoCacheMutex.Unlock()

	for _, node := range nodes {
		nodeKey := types.NamespacedName{Name: node.Name}
		if sgID, exists := sgIDByNodeKey[nodeKey]; exists {
			cacheKey := nodeENIInfoCacheKey{nodeKey: nodeKey, nodeUID: node.UID}
			r.nodeENIInfoCache.Set(cacheKey, sgID, r.nodeENIInfoCacheTTL)
		}
	}
}

func (r *defaultNodeENIInfoResolver) computeMissingNodes(nodes []*corev1.Node, resolved map[types.NamespacedName]string) []*corev1.Node {
	var missing []*corev1.Node
	for _, node := range nodes {
		if _, ok := resolved[types.NamespacedName{Name: node.Name}]; !ok {
			missing = append(missing, node)
		}
	}
	return missing
}
