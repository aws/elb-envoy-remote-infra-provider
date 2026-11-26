// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"

	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/provider"
)

const (
	ec2CreateSecurityGroup           = "CreateSecurityGroup"
	ec2DescribeSecurityGroups        = "DescribeSecurityGroups"
	ec2DeleteSecurityGroup           = "DeleteSecurityGroup"
	ec2AuthorizeSecurityGroupIngress = "AuthorizeSecurityGroupIngress"
	ec2RevokeSecurityGroupIngress    = "RevokeSecurityGroupIngress"
	ec2CreateTags                    = "CreateTags"
	ec2DeleteTags                    = "DeleteTags"
	ec2DescribeSubnets               = "DescribeSubnets"
	ec2DescribeRouteTables           = "DescribeRouteTables"
	ec2DescribeAvailabilityZones     = "DescribeAvailabilityZones"
	ec2DescribeInstances             = "DescribeInstances"
	ec2DescribeVpcs                  = "DescribeVpcs"
)

//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination ec2_mock.go -package services github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services EC2
type EC2 interface {
	CreateSecurityGroupWithContext(ctx context.Context, input *ec2sdk.CreateSecurityGroupInput) (*ec2sdk.CreateSecurityGroupOutput, error)
	DescribeSecurityGroupsAsList(ctx context.Context, input *ec2sdk.DescribeSecurityGroupsInput) ([]ec2types.SecurityGroup, error)
	DeleteSecurityGroupWithContext(ctx context.Context, input *ec2sdk.DeleteSecurityGroupInput) (*ec2sdk.DeleteSecurityGroupOutput, error)
	AuthorizeSecurityGroupIngressWithContext(ctx context.Context, input *ec2sdk.AuthorizeSecurityGroupIngressInput) (*ec2sdk.AuthorizeSecurityGroupIngressOutput, error)
	RevokeSecurityGroupIngressWithContext(ctx context.Context, input *ec2sdk.RevokeSecurityGroupIngressInput) (*ec2sdk.RevokeSecurityGroupIngressOutput, error)
	CreateTagsWithContext(ctx context.Context, input *ec2sdk.CreateTagsInput) (*ec2sdk.CreateTagsOutput, error)
	DeleteTagsWithContext(ctx context.Context, input *ec2sdk.DeleteTagsInput) (*ec2sdk.DeleteTagsOutput, error)
	DescribeSubnetsAsList(ctx context.Context, input *ec2sdk.DescribeSubnetsInput) ([]ec2types.Subnet, error)
	DescribeRouteTablesAsList(ctx context.Context, input *ec2sdk.DescribeRouteTablesInput) ([]ec2types.RouteTable, error)
	DescribeAvailabilityZonesAsList(ctx context.Context, input *ec2sdk.DescribeAvailabilityZonesInput) ([]ec2types.AvailabilityZone, error)
	DescribeInstancesAsList(ctx context.Context, input *ec2sdk.DescribeInstancesInput) ([]ec2types.Instance, error)
	DescribeVpcsAsList(ctx context.Context, input *ec2sdk.DescribeVpcsInput) ([]ec2types.Vpc, error)
}

type ec2Client struct {
	awsClientsProvider provider.AWSClientsProvider
}

func NewEC2(p provider.AWSClientsProvider) EC2 {
	return &ec2Client{awsClientsProvider: p}
}

func (c *ec2Client) CreateSecurityGroupWithContext(ctx context.Context, input *ec2sdk.CreateSecurityGroupInput) (*ec2sdk.CreateSecurityGroupOutput, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2CreateSecurityGroup)
	if err != nil {
		return nil, err
	}
	return client.CreateSecurityGroup(ctx, input)
}

func (c *ec2Client) DescribeSecurityGroupsAsList(ctx context.Context, input *ec2sdk.DescribeSecurityGroupsInput) ([]ec2types.SecurityGroup, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DescribeSecurityGroups)
	if err != nil {
		return nil, err
	}
	var result []ec2types.SecurityGroup
	paginator := ec2sdk.NewDescribeSecurityGroupsPaginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, page.SecurityGroups...)
	}
	return result, nil
}

func (c *ec2Client) DeleteSecurityGroupWithContext(ctx context.Context, input *ec2sdk.DeleteSecurityGroupInput) (*ec2sdk.DeleteSecurityGroupOutput, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DeleteSecurityGroup)
	if err != nil {
		return nil, err
	}
	return client.DeleteSecurityGroup(ctx, input)
}

func (c *ec2Client) AuthorizeSecurityGroupIngressWithContext(ctx context.Context, input *ec2sdk.AuthorizeSecurityGroupIngressInput) (*ec2sdk.AuthorizeSecurityGroupIngressOutput, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2AuthorizeSecurityGroupIngress)
	if err != nil {
		return nil, err
	}
	return client.AuthorizeSecurityGroupIngress(ctx, input)
}

func (c *ec2Client) RevokeSecurityGroupIngressWithContext(ctx context.Context, input *ec2sdk.RevokeSecurityGroupIngressInput) (*ec2sdk.RevokeSecurityGroupIngressOutput, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2RevokeSecurityGroupIngress)
	if err != nil {
		return nil, err
	}
	return client.RevokeSecurityGroupIngress(ctx, input)
}

func (c *ec2Client) CreateTagsWithContext(ctx context.Context, input *ec2sdk.CreateTagsInput) (*ec2sdk.CreateTagsOutput, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2CreateTags)
	if err != nil {
		return nil, err
	}
	return client.CreateTags(ctx, input)
}

func (c *ec2Client) DeleteTagsWithContext(ctx context.Context, input *ec2sdk.DeleteTagsInput) (*ec2sdk.DeleteTagsOutput, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DeleteTags)
	if err != nil {
		return nil, err
	}
	return client.DeleteTags(ctx, input)
}

func (c *ec2Client) DescribeSubnetsAsList(ctx context.Context, input *ec2sdk.DescribeSubnetsInput) ([]ec2types.Subnet, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DescribeSubnets)
	if err != nil {
		return nil, err
	}
	var result []ec2types.Subnet
	paginator := ec2sdk.NewDescribeSubnetsPaginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Subnets...)
	}
	return result, nil
}

func (c *ec2Client) DescribeRouteTablesAsList(ctx context.Context, input *ec2sdk.DescribeRouteTablesInput) ([]ec2types.RouteTable, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DescribeRouteTables)
	if err != nil {
		return nil, err
	}
	var result []ec2types.RouteTable
	paginator := ec2sdk.NewDescribeRouteTablesPaginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, page.RouteTables...)
	}
	return result, nil
}

func (c *ec2Client) DescribeAvailabilityZonesAsList(ctx context.Context, input *ec2sdk.DescribeAvailabilityZonesInput) ([]ec2types.AvailabilityZone, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DescribeAvailabilityZones)
	if err != nil {
		return nil, err
	}
	output, err := client.DescribeAvailabilityZones(ctx, input)
	if err != nil {
		return nil, err
	}
	return output.AvailabilityZones, nil
}

func (c *ec2Client) DescribeInstancesAsList(ctx context.Context, input *ec2sdk.DescribeInstancesInput) ([]ec2types.Instance, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DescribeInstances)
	if err != nil {
		return nil, err
	}
	var result []ec2types.Instance
	paginator := ec2sdk.NewDescribeInstancesPaginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Reservations {
			result = append(result, r.Instances...)
		}
	}
	return result, nil
}

func (c *ec2Client) DescribeVpcsAsList(ctx context.Context, input *ec2sdk.DescribeVpcsInput) ([]ec2types.Vpc, error) {
	client, err := c.awsClientsProvider.GetEC2Client(ctx, ec2DescribeVpcs)
	if err != nil {
		return nil, err
	}
	var result []ec2types.Vpc
	paginator := ec2sdk.NewDescribeVpcsPaginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Vpcs...)
	}
	return result, nil
}

var _ EC2 = (*ec2Client)(nil)
