// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
)

// ProxyManager is responsible for create/update/delete ELBProxy proxy resources.
//
//go:generate mockgen -copyright_file=../../../hack/boilerplate.mockgen.txt -destination proxy_manager_mock.go -package elbproxy github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy ProxyManager
type ProxyManager interface {
	Create(ctx context.Context, resProxy *elbproxymodel.ProxyResource) (elbproxymodel.ProxyResourceStatus, ProxyWithTags, error)
	Update(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) (elbproxymodel.ProxyResourceStatus, error)
	Delete(ctx context.Context, sdkProxy ProxyWithTags) error
}

func NewDefaultProxyManager(
	client elbproxyaws.ELBProxyClient,
	taggingManager TaggingManager,
	trackingProvider tracking.Provider,
	attributeReconciler ProxyAttributeReconciler,
	networkConfigReconciler ProxyNetworkConfigReconciler,
	configReconciler ProxyConfigurationReconciler,
	externalManagedTags []string,
	logger logging.Logger,
) *defaultProxyManager {
	return &defaultProxyManager{
		client:                  client,
		taggingManager:          taggingManager,
		trackingProvider:        trackingProvider,
		attributeReconciler:     attributeReconciler,
		networkConfigReconciler: networkConfigReconciler,
		configReconciler:        configReconciler,
		externalManagedTags:     externalManagedTags,
		logger:                  logger,
	}
}

type defaultProxyManager struct {
	client                  elbproxyaws.ELBProxyClient
	taggingManager          TaggingManager
	trackingProvider        tracking.Provider
	attributeReconciler     ProxyAttributeReconciler
	networkConfigReconciler ProxyNetworkConfigReconciler
	configReconciler        ProxyConfigurationReconciler
	externalManagedTags     []string
	logger                  logging.Logger
}

func (m *defaultProxyManager) Create(ctx context.Context, resProxy *elbproxymodel.ProxyResource) (elbproxymodel.ProxyResourceStatus, ProxyWithTags, error) {
	if resProxy == nil {
		return elbproxymodel.ProxyResourceStatus{}, ProxyWithTags{}, fmt.Errorf("resProxy is nil")
	}

	tags := m.trackingProvider.ResourceTags(resProxy.Stack(), resProxy, resProxy.Tags)
	input, err := m.buildCreateProxyInput(ctx, resProxy, tags)
	if err != nil {
		return elbproxymodel.ProxyResourceStatus{}, ProxyWithTags{}, fmt.Errorf("failed to build create proxy input: %w", err)
	}

	m.logger.Info("creating proxy",
		"name", input.Name,
		"type", input.Type,
		"proxyVersion", input.ProxyVersion,
		"roleArn", input.RoleArn,
		"subnets", input.NetworkConfiguration.VpcConfiguration.Subnets,
		"securityGroups", input.NetworkConfiguration.VpcConfiguration.SecurityGroups,
		"ipv4Addressability", input.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv4Addressability,
		"ipv6Addressability", input.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv6Addressability,
		"bootstrapBytes", len(input.Configuration.BootstrapFile),
		"archiveBytes", len(input.Configuration.ArchiveBytes),
	)
	proxyDetails, err := m.client.CreateProxy(ctx, input)
	if err != nil {
		return elbproxymodel.ProxyResourceStatus{}, ProxyWithTags{}, fmt.Errorf("failed to create proxy: %w", err)
	}

	m.logger.Info("proxy created",
		"arn", proxyDetails.Arn,
		"dnsName", proxyDetails.DnsName,
		"status", proxyDetails.Status.Code)

	sdkProxy := ProxyWithTags{
		ProxyDetails: *proxyDetails,
		Tags:         tags,
	}
	return elbproxymodel.ProxyResourceStatus{
		Arn:     proxyDetails.Arn,
		DnsName: proxyDetails.DnsName,
	}, sdkProxy, nil
}

func (m *defaultProxyManager) Update(ctx context.Context, resProxy *elbproxymodel.ProxyResource, sdkProxy ProxyWithTags) (elbproxymodel.ProxyResourceStatus, error) {
	m.logger.Info("updating proxy", "proxy", resProxy.Name, "arn", sdkProxy.ProxyDetails.Arn)

	desiredTags := m.trackingProvider.ResourceTags(resProxy.Stack(), resProxy, resProxy.Tags)
	if err := m.taggingManager.ReconcileTags(ctx, sdkProxy.ProxyDetails.Arn, desiredTags,
		WithCurrentTags(sdkProxy.Tags),
		WithIgnoredTagKeys(m.externalManagedTags),
	); err != nil {
		return elbproxymodel.ProxyResourceStatus{}, fmt.Errorf("failed to reconcile tags for proxy %s: %w", sdkProxy.ProxyDetails.Arn, err)
	}

	if err := m.attributeReconciler.Reconcile(ctx, resProxy, sdkProxy); err != nil {
		return elbproxymodel.ProxyResourceStatus{}, fmt.Errorf("failed to reconcile attributes for proxy %s: %w", sdkProxy.ProxyDetails.Arn, err)
	}

	if _, err := m.networkConfigReconciler.Reconcile(ctx, resProxy, sdkProxy); err != nil {
		return elbproxymodel.ProxyResourceStatus{}, fmt.Errorf("failed to reconcile network configuration for proxy %s: %w", sdkProxy.ProxyDetails.Arn, err)
	}

	if err := m.configReconciler.Reconcile(ctx, resProxy, sdkProxy); err != nil {
		return elbproxymodel.ProxyResourceStatus{}, fmt.Errorf("failed to reconcile configuration for proxy %s: %w", sdkProxy.ProxyDetails.Arn, err)
	}

	return elbproxymodel.ProxyResourceStatus{
		Arn:     sdkProxy.ProxyDetails.Arn,
		DnsName: sdkProxy.ProxyDetails.DnsName,
	}, nil
}

func (m *defaultProxyManager) Delete(ctx context.Context, sdkProxy ProxyWithTags) error {
	m.logger.Info("deleting proxy", "name", sdkProxy.ProxyDetails.Name, "arn", sdkProxy.ProxyDetails.Arn)

	if err := m.client.DeleteProxy(ctx, sdkProxy.ProxyDetails.Arn); err != nil {
		return fmt.Errorf("failed to delete proxy %s: %w", sdkProxy.ProxyDetails.Arn, err)
	}

	return nil
}

func (m *defaultProxyManager) buildCreateProxyInput(ctx context.Context, resProxy *elbproxymodel.ProxyResource, tags map[string]string) (elbproxyaws.CreateProxyInput, error) {
	networkConfig := resProxy.NetworkConfiguration

	if len(resProxy.SecurityGroupTokens) > 0 {
		resolved, err := resolveSecurityGroupTokens(ctx, resProxy.SecurityGroupTokens)
		if err != nil {
			return elbproxyaws.CreateProxyInput{}, err
		}
		networkConfig.VpcConfiguration.SecurityGroups = resolved
	}

	return elbproxyaws.CreateProxyInput{
		Name:                 resProxy.Name,
		Type:                 resProxy.ProxyType,
		RoleArn:              resProxy.RoleArn,
		ProxyVersion:         resProxy.ProxyVersion,
		Attributes:           resProxy.Attributes,
		Configuration:        resProxy.Configuration,
		NetworkConfiguration: networkConfig,
		Tags:                 tags,
		CustomerKmsKeyArn:    resProxy.CustomerKmsKeyArn,
	}, nil
}

// resolveSecurityGroupTokens resolves security group tokens to concrete SG IDs
func resolveSecurityGroupTokens(ctx context.Context, tokens []core.StringToken) ([]elbproxyaws.SecurityGroupDetails, error) {
	var sgs []elbproxyaws.SecurityGroupDetails
	for _, token := range tokens {
		sgID, err := token.Resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve security group token: %w", err)
		}
		if sgID != "" {
			sgs = append(sgs, elbproxyaws.SecurityGroupDetails{SecurityGroupId: sgID})
		}
	}
	return sgs, nil
}
