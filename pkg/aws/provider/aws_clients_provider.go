// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/endpoints"
)

type defaultAWSClientsProvider struct {
	ec2Client *ec2.Client
	rgtClient *resourcegroupstaggingapi.Client
}

func NewDefaultAWSClientsProvider(cfg aws.Config, endpointsResolver *endpoints.Resolver) AWSClientsProvider {
	ec2CustomEndpoint := endpointsResolver.EndpointFor(ec2.ServiceID)
	ec2Client := ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		if ec2CustomEndpoint != nil {
			o.BaseEndpoint = ec2CustomEndpoint
		}
	})
	rgtCustomEndpoint := endpointsResolver.EndpointFor(resourcegroupstaggingapi.ServiceID)
	rgtClient := resourcegroupstaggingapi.NewFromConfig(cfg, func(o *resourcegroupstaggingapi.Options) {
		if rgtCustomEndpoint != nil {
			o.BaseEndpoint = rgtCustomEndpoint
		}
	})
	return &defaultAWSClientsProvider{ec2Client: ec2Client, rgtClient: rgtClient}
}

func (p *defaultAWSClientsProvider) GetEC2Client(_ context.Context, _ string) (*ec2.Client, error) {
	return p.ec2Client, nil
}

func (p *defaultAWSClientsProvider) GetRGTClient(_ context.Context, _ string) (*resourcegroupstaggingapi.Client, error) {
	return p.rgtClient, nil
}

var _ AWSClientsProvider = (*defaultAWSClientsProvider)(nil)
