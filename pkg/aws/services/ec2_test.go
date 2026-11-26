// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/stretchr/testify/assert"
)

type mockAWSClientsProvider struct {
	ec2Client *ec2.Client
	rgtClient *resourcegroupstaggingapi.Client
	err       error
}

func (m *mockAWSClientsProvider) GetEC2Client(_ context.Context, _ string) (*ec2.Client, error) {
	return m.ec2Client, m.err
}

func (m *mockAWSClientsProvider) GetRGTClient(_ context.Context, _ string) (*resourcegroupstaggingapi.Client, error) {
	return m.rgtClient, m.err
}

func TestNewEC2(t *testing.T) {
	provider := &mockAWSClientsProvider{}
	client := NewEC2(provider)
	assert.NotNil(t, client)
}

func TestCreateSecurityGroupWithContext_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.CreateSecurityGroupWithContext(context.Background(), &ec2.CreateSecurityGroupInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDescribeSecurityGroupsAsList_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DescribeSecurityGroupsAsList(context.Background(), &ec2.DescribeSecurityGroupsInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDeleteSecurityGroupWithContext_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DeleteSecurityGroupWithContext(context.Background(), &ec2.DeleteSecurityGroupInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestAuthorizeSecurityGroupIngressWithContext_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.AuthorizeSecurityGroupIngressWithContext(context.Background(), &ec2.AuthorizeSecurityGroupIngressInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestRevokeSecurityGroupIngressWithContext_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.RevokeSecurityGroupIngressWithContext(context.Background(), &ec2.RevokeSecurityGroupIngressInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestCreateTagsWithContext_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.CreateTagsWithContext(context.Background(), &ec2.CreateTagsInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDeleteTagsWithContext_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DeleteTagsWithContext(context.Background(), &ec2.DeleteTagsInput{})
	assert.ErrorIs(t, err, providerErr)
}

type operationCapturingProvider struct {
	capturedOperation string
	ec2Client         *ec2.Client
	rgtClient         *resourcegroupstaggingapi.Client
}

func (m *operationCapturingProvider) GetEC2Client(_ context.Context, operationName string) (*ec2.Client, error) {
	m.capturedOperation = operationName
	return m.ec2Client, nil
}

func (m *operationCapturingProvider) GetRGTClient(_ context.Context, operationName string) (*resourcegroupstaggingapi.Client, error) {
	m.capturedOperation = operationName
	return m.rgtClient, nil
}

func TestCreateSecurityGroupWithContext_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.CreateSecurityGroupWithContext(context.Background(), &ec2.CreateSecurityGroupInput{})
	assert.Equal(t, ec2CreateSecurityGroup, provider.capturedOperation)
}

func TestDescribeSecurityGroupsAsList_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DescribeSecurityGroupsAsList(context.Background(), &ec2.DescribeSecurityGroupsInput{})
	assert.Equal(t, ec2DescribeSecurityGroups, provider.capturedOperation)
}

func TestDeleteSecurityGroupWithContext_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DeleteSecurityGroupWithContext(context.Background(), &ec2.DeleteSecurityGroupInput{})
	assert.Equal(t, ec2DeleteSecurityGroup, provider.capturedOperation)
}

func TestAuthorizeSecurityGroupIngressWithContext_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.AuthorizeSecurityGroupIngressWithContext(context.Background(), &ec2.AuthorizeSecurityGroupIngressInput{})
	assert.Equal(t, ec2AuthorizeSecurityGroupIngress, provider.capturedOperation)
}

func TestRevokeSecurityGroupIngressWithContext_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.RevokeSecurityGroupIngressWithContext(context.Background(), &ec2.RevokeSecurityGroupIngressInput{})
	assert.Equal(t, ec2RevokeSecurityGroupIngress, provider.capturedOperation)
}

func TestCreateTagsWithContext_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.CreateTagsWithContext(context.Background(), &ec2.CreateTagsInput{})
	assert.Equal(t, ec2CreateTags, provider.capturedOperation)
}

func TestDeleteTagsWithContext_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DeleteTagsWithContext(context.Background(), &ec2.DeleteTagsInput{})
	assert.Equal(t, ec2DeleteTags, provider.capturedOperation)
}

func TestDescribeSubnetsAsList_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DescribeSubnetsAsList(context.Background(), &ec2.DescribeSubnetsInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDescribeRouteTablesAsList_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DescribeRouteTablesAsList(context.Background(), &ec2.DescribeRouteTablesInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDescribeAvailabilityZonesAsList_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DescribeAvailabilityZonesAsList(context.Background(), &ec2.DescribeAvailabilityZonesInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDescribeSubnetsAsList_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DescribeSubnetsAsList(context.Background(), &ec2.DescribeSubnetsInput{})
	assert.Equal(t, ec2DescribeSubnets, provider.capturedOperation)
}

func TestDescribeRouteTablesAsList_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DescribeRouteTablesAsList(context.Background(), &ec2.DescribeRouteTablesInput{})
	assert.Equal(t, ec2DescribeRouteTables, provider.capturedOperation)
}

func TestDescribeAvailabilityZonesAsList_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DescribeAvailabilityZonesAsList(context.Background(), &ec2.DescribeAvailabilityZonesInput{})
	assert.Equal(t, ec2DescribeAvailabilityZones, provider.capturedOperation)
}

func TestDescribeInstancesAsList_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DescribeInstancesAsList(context.Background(), &ec2.DescribeInstancesInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDescribeVpcsAsList_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockAWSClientsProvider{err: providerErr}
	client := NewEC2(provider)

	_, err := client.DescribeVpcsAsList(context.Background(), &ec2.DescribeVpcsInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestDescribeInstancesAsList_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DescribeInstancesAsList(context.Background(), &ec2.DescribeInstancesInput{})
	assert.Equal(t, ec2DescribeInstances, provider.capturedOperation)
}

func TestDescribeVpcsAsList_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{ec2Client: ec2.New(ec2.Options{})}
	client := NewEC2(provider)

	_, _ = client.DescribeVpcsAsList(context.Background(), &ec2.DescribeVpcsInput{})
	assert.Equal(t, ec2DescribeVpcs, provider.capturedOperation)
}
