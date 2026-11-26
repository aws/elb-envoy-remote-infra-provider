// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ec2

import (
	"context"
	"errors"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	ec2model "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	"github.com/aws/smithy-go"
)

// SecurityGroupManager is responsible for create/update/delete SecurityGroup resources.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination security_group_manager_mock.go -package ec2 github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/ec2 SecurityGroupManager
type SecurityGroupManager interface {
	Create(ctx context.Context, resSG *ec2model.SecurityGroup) (ec2model.SecurityGroupStatus, error)

	Update(ctx context.Context, resSG *ec2model.SecurityGroup, sdkSG networking.SecurityGroupInfo) (ec2model.SecurityGroupStatus, error)

	Delete(ctx context.Context, sdkSG networking.SecurityGroupInfo) error
}

type defaultSecurityGroupManager struct {
	ec2Client              services.EC2
	taggingManager         TaggingManager
	trackingProvider       tracking.Provider
	networkingSGReconciler networking.SecurityGroupReconciler
	vpcID                  string
	logger                 logging.Logger
}

// NewSecurityGroupManager creates a new SecurityGroupManager.
func NewSecurityGroupManager(
	ec2Client services.EC2,
	taggingManager TaggingManager,
	trackingProvider tracking.Provider,
	networkingSGReconciler networking.SecurityGroupReconciler,
	vpcID string,
	logger logging.Logger,
) SecurityGroupManager {
	return &defaultSecurityGroupManager{
		ec2Client:              ec2Client,
		taggingManager:         taggingManager,
		trackingProvider:       trackingProvider,
		networkingSGReconciler: networkingSGReconciler,
		vpcID:                  vpcID,
		logger:                 logger,
	}
}

func (m *defaultSecurityGroupManager) Create(
	ctx context.Context,
	resSG *ec2model.SecurityGroup,
) (ec2model.SecurityGroupStatus, error) {
	sgTags := m.trackingProvider.ResourceTags(resSG.Stack(), resSG, resSG.Spec.Tags)
	sdkTags := convertTagsToSDKTags(sgTags)
	permissionInfos, err := buildIPPermissionInfos(resSG.Spec.Ingress)
	if err != nil {
		return ec2model.SecurityGroupStatus{}, err
	}

	req := &ec2sdk.CreateSecurityGroupInput{
		VpcId:       awssdk.String(m.vpcID),
		GroupName:   awssdk.String(resSG.Spec.GroupName),
		Description: awssdk.String(resSG.Spec.Description),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags:         sdkTags,
			},
		},
	}
	m.logger.Info("creating securityGroup", "resourceID", resSG.ID())
	resp, err := m.ec2Client.CreateSecurityGroupWithContext(ctx, req)
	if err != nil {
		return ec2model.SecurityGroupStatus{}, err
	}
	sgID := awssdk.ToString(resp.GroupId)
	m.logger.Info("created securityGroup", "resourceID", resSG.ID(), "securityGroupID", sgID)

	if err := m.networkingSGReconciler.ReconcileIngress(ctx, sgID, permissionInfos); err != nil {
		return ec2model.SecurityGroupStatus{}, err
	}

	return ec2model.SecurityGroupStatus{GroupID: sgID}, nil
}

func (m *defaultSecurityGroupManager) Update(
	ctx context.Context,
	resSG *ec2model.SecurityGroup,
	sdkSG networking.SecurityGroupInfo,
) (ec2model.SecurityGroupStatus, error) {

	permissionInfos, err := buildIPPermissionInfos(resSG.Spec.Ingress)
	if err != nil {
		return ec2model.SecurityGroupStatus{}, err
	}

	if err := m.updateSDKSecurityGroupGroupWithTags(ctx, resSG, sdkSG); err != nil {
		return ec2model.SecurityGroupStatus{}, err
	}
	if err := m.networkingSGReconciler.ReconcileIngress(ctx, sdkSG.SecurityGroupID, permissionInfos); err != nil {
		return ec2model.SecurityGroupStatus{}, err
	}
	return ec2model.SecurityGroupStatus{GroupID: sdkSG.SecurityGroupID}, nil
}

func (m *defaultSecurityGroupManager) Delete(ctx context.Context, sdkSG networking.SecurityGroupInfo) error {
	req := &ec2sdk.DeleteSecurityGroupInput{
		GroupId: awssdk.String(sdkSG.SecurityGroupID),
	}
	m.logger.Info("deleting securityGroup", "securityGroupID", sdkSG.SecurityGroupID)

	_, err := m.ec2Client.DeleteSecurityGroupWithContext(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to delete securityGroup: %w", err)
	}
	m.logger.Info("deleted securityGroup", "securityGroupID", sdkSG.SecurityGroupID)
	return nil
}

func (m *defaultSecurityGroupManager) updateSDKSecurityGroupGroupWithTags(ctx context.Context, resSG *ec2model.SecurityGroup, sdkSG networking.SecurityGroupInfo) error {
	desiredSGTags := m.trackingProvider.ResourceTags(resSG.Stack(), resSG, resSG.Spec.Tags)
	return m.taggingManager.ReconcileTags(ctx, sdkSG.SecurityGroupID, desiredSGTags,
		WithCurrentTags(sdkSG.Tags),
	)
}

func buildIPPermissionInfos(permissions []ec2model.IPPermission) ([]networking.IPPermissionInfo, error) {
	permissionInfos := make([]networking.IPPermissionInfo, 0, len(permissions))
	for _, permission := range permissions {
		permissionInfo, err := buildIPPermissionInfo(permission)
		if err != nil {
			return nil, err
		}
		permissionInfos = append(permissionInfos, permissionInfo)
	}
	return permissionInfos, nil
}

func buildIPPermissionInfo(permission ec2model.IPPermission) (networking.IPPermissionInfo, error) {
	protocol := permission.IPProtocol
	if len(permission.IPRanges) == 1 {
		labels := networking.NewIPPermissionLabelsForRawDescription(permission.IPRanges[0].Description)
		return networking.NewCIDRIPPermission(protocol, permission.FromPort, permission.ToPort, permission.IPRanges[0].CIDRIP, labels), nil
	}
	if len(permission.IPv6Range) == 1 {
		labels := networking.NewIPPermissionLabelsForRawDescription(permission.IPv6Range[0].Description)
		return networking.NewCIDRv6IPPermission(protocol, permission.FromPort, permission.ToPort, permission.IPv6Range[0].CIDRIPv6, labels), nil
	}
	if len(permission.UserIDGroupPairs) == 1 {
		labels := networking.NewIPPermissionLabelsForRawDescription(permission.UserIDGroupPairs[0].Description)
		return networking.NewGroupIDIPPermission(protocol, permission.FromPort, permission.ToPort, permission.UserIDGroupPairs[0].GroupID, labels), nil
	}
	if len(permission.PrefixLists) == 1 {
		labels := networking.NewIPPermissionLabelsForRawDescription(permission.PrefixLists[0].Description)
		return networking.NewPrefixListIDPermission(protocol, permission.FromPort, permission.ToPort, permission.PrefixLists[0].ListID, labels), nil
	}
	return networking.IPPermissionInfo{}, errors.New("invalid ipPermission")
}

func isSecurityGroupDependencyViolationError(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "DependencyViolation"
	}
	return false
}
