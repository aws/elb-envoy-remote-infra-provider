// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	rgtsdk "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/provider"
)

const (
	// ResourceTypeELBProxy is the Resource Groups Tagging API resource type of an ELB Proxy proxy.
	ResourceTypeELBProxy = "elasticloadbalancing:proxy"

	rgtGetResources = "GetResources"
)

// RGT wraps the Resource Groups Tagging API.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination rgt_mock.go -package services github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services RGT
type RGT interface {
	// GetResourcesAsList returns every resource that matches the input, following pagination.
	GetResourcesAsList(ctx context.Context, input *rgtsdk.GetResourcesInput) ([]rgttypes.ResourceTagMapping, error)
}

// NewRGT constructs a new RGT implementation.
func NewRGT(p provider.AWSClientsProvider) RGT {
	return &rgtClient{awsClientsProvider: p}
}

type rgtClient struct {
	awsClientsProvider provider.AWSClientsProvider
}

func (c *rgtClient) GetResourcesAsList(ctx context.Context, input *rgtsdk.GetResourcesInput) ([]rgttypes.ResourceTagMapping, error) {
	client, err := c.awsClientsProvider.GetRGTClient(ctx, rgtGetResources)
	if err != nil {
		return nil, err
	}
	var result []rgttypes.ResourceTagMapping
	paginator := rgtsdk.NewGetResourcesPaginator(client, input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.ResourceTagMappingList...)
	}
	return result, nil
}

// ParseRGTTags converts Resource Groups Tagging API tags into a map.
func ParseRGTTags(tags []rgttypes.Tag) map[string]string {
	result := make(map[string]string, len(tags))
	for _, tag := range tags {
		result[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return result
}
