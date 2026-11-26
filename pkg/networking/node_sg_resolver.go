// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// labelComputeType is the EKS node label that identifies the compute type.
	labelComputeType = "eks.amazonaws.com/compute-type"
	// computeTypeEC2 identifies standard EC2-backed nodes.
	computeTypeEC2 = "ec2"
	// computeTypeAuto identifies EKS Auto Mode nodes, which are also EC2-backed
	// and have an EC2 instance that can be introspected for SG resolution.
	computeTypeAuto = "auto"
)

// NodeSGResolver discovers node security group IDs for this cluster.
//
//go:generate mockgen -copyright_file=../../hack/boilerplate.mockgen.txt -destination node_sg_resolver_mock.go -package networking github.com/aws/elb-envoy-remote-infra-provider/pkg/networking NodeSGResolver
type NodeSGResolver interface {
	// ResolveNodeSGIDs returns the deduplicated set of node SG IDs by inspecting
	// the primary ENI of each Ready EC2 node.
	ResolveNodeSGIDs(ctx context.Context) ([]string, error)

	// FindNodeSGsByClusterTag discovers node SGs by querying AWS for SGs tagged
	// kubernetes.io/cluster/<clusterName>=owned.
	FindNodeSGsByClusterTag(ctx context.Context) ([]string, error)
}

// NewDefaultNodeSGResolver constructs a NodeSGResolver.
func NewDefaultNodeSGResolver(eniResolver NodeENIInfoResolver, sgManager SecurityGroupManager, clusterName string, vpcID string, k8sClient client.Client, logger logr.Logger) NodeSGResolver {
	return &defaultNodeSGResolver{
		eniResolver: eniResolver,
		sgManager:   sgManager,
		clusterName: clusterName,
		vpcID:       vpcID,
		k8sClient:   k8sClient,
		logger:      logger,
	}
}

type defaultNodeSGResolver struct {
	eniResolver NodeENIInfoResolver
	sgManager   SecurityGroupManager
	clusterName string
	vpcID       string
	k8sClient   client.Client
	logger      logr.Logger
}

func (r *defaultNodeSGResolver) ResolveNodeSGIDs(ctx context.Context) ([]string, error) {
	nodes, err := r.listReadyNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list ready nodes: %w", err)
	}

	var nodePtrs []*corev1.Node
	for i := range nodes {
		if ct := nodes[i].Labels[labelComputeType]; ct != "" && ct != computeTypeEC2 && ct != computeTypeAuto {
			r.logger.V(1).Info("skipping non-EC2 node for SG resolution",
				"node", nodes[i].Name, "computeType", ct)
			continue
		}
		nodePtrs = append(nodePtrs, &nodes[i])
	}

	sgIDByNodeKey, err := r.eniResolver.Resolve(ctx, nodePtrs)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve node SGs: %w", err)
	}

	sgIDs := sets.NewString()
	for _, sgID := range sgIDByNodeKey {
		sgIDs.Insert(sgID)
	}

	if sgIDs.Len() == 0 {
		return nil, fmt.Errorf("no node security groups discovered (ready nodes: %d, EC2 nodes considered: %d)", len(nodes), len(nodePtrs))
	}
	return sgIDs.List(), nil
}

// FindNodeSGsByClusterTag discovers node SGs via the kubernetes.io/cluster/<clusterName> tag.
// Queries both "owned" and "shared" values and scopes to the cluster's VPC
func (r *defaultNodeSGResolver) FindNodeSGsByClusterTag(ctx context.Context) ([]string, error) {
	clusterTagKey := fmt.Sprintf("kubernetes.io/cluster/%s", r.clusterName)
	sgInfosByID, err := r.sgManager.FetchSGInfosByRequest(ctx, &ec2sdk.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{
				Name:   awssdk.String("tag:" + clusterTagKey),
				Values: []string{"owned", "shared"},
			},
			{
				Name:   awssdk.String("vpc-id"),
				Values: []string{r.vpcID},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe node SGs by cluster tag: %w", err)
	}
	sgIDs := make([]string, 0, len(sgInfosByID))
	for sgID := range sgInfosByID {
		sgIDs = append(sgIDs, sgID)
	}
	return sgIDs, nil
}

func (r *defaultNodeSGResolver) listReadyNodes(ctx context.Context) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := r.k8sClient.List(ctx, nodeList); err != nil {
		return nil, err
	}
	var ready []corev1.Node
	for _, node := range nodeList.Items {
		if isNodeReady(node) {
			ready = append(ready, node)
		}
	}
	return ready, nil
}

func isNodeReady(node corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
