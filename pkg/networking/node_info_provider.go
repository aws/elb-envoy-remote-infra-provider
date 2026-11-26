// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
)

// NodeInfoProvider is responsible for providing EC2 instance info for k8s nodes.
//
//go:generate mockgen -copyright_file=../../hack/boilerplate.mockgen.txt -destination node_info_provider_mock.go -package networking github.com/aws/elb-envoy-remote-infra-provider/pkg/networking NodeInfoProvider
type NodeInfoProvider interface {
	// FetchNodeInstances returns EC2 instance data keyed by node NamespacedName.
	// Hybrid nodes (eks.amazonaws.com/compute-type=hybrid) are skipped — they have no EC2 instance.
	FetchNodeInstances(ctx context.Context, nodes []*corev1.Node) (map[types.NamespacedName]*ec2types.Instance, error)
}

// NewDefaultNodeInfoProvider constructs a new defaultNodeInfoProvider.
func NewDefaultNodeInfoProvider(ec2Client services.EC2, logger logr.Logger) NodeInfoProvider {
	return &defaultNodeInfoProvider{
		ec2Client: ec2Client,
		logger:    logger,
	}
}

var _ NodeInfoProvider = &defaultNodeInfoProvider{}

type defaultNodeInfoProvider struct {
	ec2Client services.EC2
	logger    logr.Logger
}

func (p *defaultNodeInfoProvider) FetchNodeInstances(ctx context.Context, nodes []*corev1.Node) (map[types.NamespacedName]*ec2types.Instance, error) {
	if len(nodes) == 0 {
		return nil, nil
	}

	nodeKeysByInstanceID := make(map[string][]types.NamespacedName, len(nodes))
	for _, node := range nodes {
		// Non-EC2 compute types (hybrid, fargate, etc.) have no EC2 instance
		// to describe. Kept as a defense-in-depth filter; NodeSGResolver
		// already filters these out before calling here.
		if ct := node.Labels[labelComputeType]; ct != "" && ct != computeTypeEC2 && ct != computeTypeAuto {
			continue
		}
		instanceID, err := extractInstanceID(node.Spec.ProviderID)
		if err != nil {
			return nil, err
		}
		nodeKey := types.NamespacedName{Name: node.Name}
		nodeKeysByInstanceID[instanceID] = append(nodeKeysByInstanceID[instanceID], nodeKey)
	}

	if len(nodeKeysByInstanceID) == 0 {
		return make(map[types.NamespacedName]*ec2types.Instance), nil
	}

	instanceIDs := sets.StringKeySet(nodeKeysByInstanceID).List()
	instances, err := p.ec2Client.DescribeInstancesAsList(ctx, &ec2sdk.DescribeInstancesInput{
		InstanceIds:             instanceIDs,
		IncludeManagedResources: awssdk.Bool(true),
	})
	if err != nil {
		return nil, err
	}

	nodeInstanceByNodeKey := make(map[types.NamespacedName]*ec2types.Instance, len(nodes))
	for i := range instances {
		instance := &instances[i]
		instanceID := awssdk.ToString(instance.InstanceId)
		for _, nodeKey := range nodeKeysByInstanceID[instanceID] {
			nodeInstanceByNodeKey[nodeKey] = instance
		}
	}
	return nodeInstanceByNodeKey, nil
}

// extractInstanceID parses the EC2 instance ID from a node's spec.providerID.
// Expected format: aws:///us-east-1a/i-0abc1234def56789
func extractInstanceID(providerID string) (string, error) {
	if providerID == "" {
		return "", fmt.Errorf("providerID is empty")
	}
	parts := strings.Split(providerID, "/")
	id := parts[len(parts)-1]
	if !strings.HasPrefix(id, "i-") {
		return "", fmt.Errorf("unexpected providerID format: %q", providerID)
	}
	return id, nil
}
