// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ProxyNetworkConfigReconciler reconciles proxy network configuration to match the desired state.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination proxy_network_config_reconciler_mock.go -package elbproxy github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy ProxyNetworkConfigReconciler
type ProxyNetworkConfigReconciler interface {
	Reconcile(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) (*elbproxyaws.ProxyDetails, error)
}

// NewDefaultProxyNetworkConfigReconciler constructs a new defaultProxyNetworkConfigReconciler.
func NewDefaultProxyNetworkConfigReconciler(client elbproxyaws.ELBProxyClient, logger logging.Logger) *defaultProxyNetworkConfigReconciler {
	return &defaultProxyNetworkConfigReconciler{
		client: client,
		logger: logger,
	}
}

var _ ProxyNetworkConfigReconciler = &defaultProxyNetworkConfigReconciler{}

type defaultProxyNetworkConfigReconciler struct {
	client elbproxyaws.ELBProxyClient
	logger logging.Logger
}

func (r *defaultProxyNetworkConfigReconciler) Reconcile(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) (*elbproxyaws.ProxyDetails, error) {
	desiredNetwork := resProxy.NetworkConfiguration
	if len(resProxy.SecurityGroupTokens) > 0 {
		resolved, err := resolveSecurityGroupTokens(ctx, resProxy.SecurityGroupTokens)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve security group tokens: %w", err)
		}
		desiredNetwork.VpcConfiguration.SecurityGroups = resolved
	}

	return r.updateNetworkConfiguration(ctx, resProxy, desiredNetwork, sdkProxy)
}

// updateNetworkConfiguration sends one UpdateProxyNetworkConfiguration when the subnets
// or security groups differ from the live proxy.
func (r *defaultProxyNetworkConfigReconciler) updateNetworkConfiguration(ctx context.Context, resProxy *elbproxymodel.ProxyResource, desired elbproxyaws.NetworkUserConfiguration, sdkProxy ProxyWithTags) (*elbproxyaws.ProxyDetails, error) {
	live := sdkProxy.ProxyDetails.NetworkConfiguration.VpcConfiguration

	currentSubnetIDs := sets.New[string]()
	for _, az := range live.AvailabilityZones {
		currentSubnetIDs.Insert(az.SubnetId)
	}
	desiredSubnetIDs := sets.New[string]()
	for _, s := range desired.VpcConfiguration.Subnets {
		desiredSubnetIDs.Insert(s.SubnetId)
	}

	// When IR is absent (e.g. sidecar restarted, EG hasn't replayed yet), the frontend
	// SG build is skipped so the desired set is empty. Keep the live SGs: a proxy must
	// always have at least one SG.
	manageSGs := resProxy.Stack().IRPresent() && len(desired.VpcConfiguration.SecurityGroups) > 0
	currentSGIDs := sets.New[string]()
	for _, sg := range live.SecurityGroups {
		currentSGIDs.Insert(sg.SecurityGroupId)
	}
	desiredSGIDs := sets.New[string]()
	for _, sg := range desired.VpcConfiguration.SecurityGroups {
		desiredSGIDs.Insert(sg.SecurityGroupId)
	}

	subnetsChanged := !desiredSubnetIDs.Equal(currentSubnetIDs)
	sgsChanged := manageSGs && !desiredSGIDs.Equal(currentSGIDs)
	if !subnetsChanged && !sgsChanged {
		return nil, nil
	}

	// Omitted SGs leave the live ones in place.
	vpcConfig := elbproxyaws.VpcUserConfiguration{Subnets: desired.VpcConfiguration.Subnets}
	if manageSGs {
		vpcConfig.SecurityGroups = desired.VpcConfiguration.SecurityGroups
	}
	r.logger.Info("modifying proxy network configuration",
		"stackID", resProxy.Stack().StackID(),
		"resourceID", resProxy.ID(),
		"arn", sdkProxy.ProxyDetails.Arn,
		"subnets", fmt.Sprintf("%v => %v", sets.List(currentSubnetIDs), sets.List(desiredSubnetIDs)),
		"securityGroups", fmt.Sprintf("%v => %v", sets.List(currentSGIDs), sets.List(desiredSGIDs)),
		"sendSecurityGroups", manageSGs)
	details, err := r.client.UpdateProxyNetworkConfiguration(ctx, elbproxyaws.UpdateProxyNetworkConfigurationInput{
		ProxyIdentifier:  sdkProxy.ProxyDetails.Arn,
		VpcConfiguration: vpcConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update proxy network configuration: %w", err)
	}
	r.logger.Info("modified proxy network configuration",
		"stackID", resProxy.Stack().StackID(),
		"resourceID", resProxy.ID(),
		"arn", sdkProxy.ProxyDetails.Arn)
	return details, nil
}
