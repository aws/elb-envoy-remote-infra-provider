// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elbproxy"
	elbproxytypes "github.com/aws/aws-sdk-go-v2/service/elbproxy/types"
)

// ELBProxyClient defines the interface for interacting with the ELBProxy API.
//
//go:generate mockgen -copyright_file=../../hack/boilerplate.mockgen.txt -destination elbproxy_client_mock.go -package aws github.com/aws/elb-envoy-remote-infra-provider/pkg/aws ELBProxyClient
type ELBProxyClient interface {
	CreateProxy(ctx context.Context, input CreateProxyInput) (*ProxyDetails, error)
	DeleteProxy(ctx context.Context, proxyIdentifier string) error
	GetProxy(ctx context.Context, proxyIdentifier string) (*ProxyDetails, error)
	TagResource(ctx context.Context, input TagResourceInput) error
	UntagResource(ctx context.Context, input UntagResourceInput) error
	ListTagsForResource(ctx context.Context, input ListTagsForResourceInput) (*ListTagsForResourceOutput, error)
	GetProxyAttributes(ctx context.Context, input GetProxyAttributesInput) (*GetProxyAttributesOutput, error)
	UpdateProxyAttributes(ctx context.Context, input UpdateProxyAttributesInput) (*UpdateProxyAttributesOutput, error)
	UpdateProxyNetworkConfiguration(ctx context.Context, input UpdateProxyNetworkConfigurationInput) (*ProxyDetails, error)
	UpdateProxyConfiguration(ctx context.Context, input UpdateProxyConfigurationInput) error
}

type elbProxyClient struct {
	inner *elbproxy.Client
}

func (c *elbProxyClient) CreateProxy(ctx context.Context, input CreateProxyInput) (*ProxyDetails, error) {
	req := &elbproxy.CreateProxyInput{
		Name:         &input.Name,
		Type:         elbproxytypes.ProxyTypeEnvoy,
		ProxyVersion: &input.ProxyVersion,
		Configuration: new(elbproxytypes.ProxyConfiguration{
			ConfigurationArchive: new(elbproxytypes.ConfigurationArchive{
				ConfigurationBytes: input.Configuration.ArchiveBytes,
			}),
			ConfigurationFile: new(elbproxytypes.ConfigurationFile{
				ConfigurationBytes: input.Configuration.BootstrapFile,
			}),
		}),
		NetworkConfiguration: new(elbproxytypes.ProxyNetworkUserConfiguration{
			IpConfiguration: new(elbproxytypes.ProxyIpConfiguration{
				IpAddressingConfiguration: new(elbproxytypes.ProxyIpAddressingConfiguration{
					Ipv4Addressability: elbproxytypes.ProxyIpv4Addressability(input.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv4Addressability),
					Ipv6Addressability: elbproxytypes.ProxyIpv6Addressability(input.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration.Ipv6Addressability),
				}),
			}),
			VpcConfiguration: new(elbproxytypes.ProxyVpcUserConfiguration{
				Subnets:        toAPISubnetDetails(input.NetworkConfiguration.VpcConfiguration.Subnets),
				SecurityGroups: toAPISecurityGroupDetails(input.NetworkConfiguration.VpcConfiguration.SecurityGroups),
			}),
		}),
	}
	req.RoleArn = &input.RoleArn
	if len(input.Attributes) > 0 {
		attrs := make([]elbproxytypes.Attribute, len(input.Attributes))
		for i, a := range input.Attributes {
			attrs[i] = elbproxytypes.Attribute{Key: new(a.Key), Value: new(a.Value)}
		}
		req.Attributes = attrs
	}
	if len(input.Tags) > 0 {
		req.Tags = input.Tags
	}
	if input.CustomerKmsKeyArn != nil {
		req.CustomerKmsKeyArn = input.CustomerKmsKeyArn
	}

	resp, err := c.inner.CreateProxy(ctx, req)
	if err != nil {
		return nil, err
	}
	return toProxyDetails_create(*resp), nil
}

func (c *elbProxyClient) DeleteProxy(ctx context.Context, proxyIdentifier string) error {
	_, err := c.inner.DeleteProxy(ctx, &elbproxy.DeleteProxyInput{
		ProxyIdentifier: &proxyIdentifier,
	})
	return err
}

func (c *elbProxyClient) GetProxy(ctx context.Context, proxyIdentifier string) (*ProxyDetails, error) {
	resp, err := c.inner.GetProxy(ctx, &elbproxy.GetProxyInput{
		ProxyIdentifier: &proxyIdentifier,
	})
	if err != nil {
		return nil, err
	}
	return toProxyDetails_get(resp), nil
}

func (c *elbProxyClient) TagResource(ctx context.Context, input TagResourceInput) error {
	_, err := c.inner.TagResource(ctx, &elbproxy.TagResourceInput{
		ResourceArn: &input.ResourceArn,
		Tags:        input.Tags,
	})
	return err
}

func (c *elbProxyClient) UntagResource(ctx context.Context, input UntagResourceInput) error {
	_, err := c.inner.UntagResource(ctx, &elbproxy.UntagResourceInput{
		ResourceArn: &input.ResourceArn,
		TagKeys:     input.TagKeys,
	})
	return err
}

func (c *elbProxyClient) ListTagsForResource(ctx context.Context, input ListTagsForResourceInput) (*ListTagsForResourceOutput, error) {
	resp, err := c.inner.ListTagsForResource(ctx, &elbproxy.ListTagsForResourceInput{
		ResourceArn: &input.ResourceArn,
	})
	if err != nil {
		return nil, err
	}
	return &ListTagsForResourceOutput{
		Tags: resp.Tags,
	}, nil
}

func (c *elbProxyClient) GetProxyAttributes(ctx context.Context, input GetProxyAttributesInput) (*GetProxyAttributesOutput, error) {
	resp, err := c.inner.GetProxyAttributes(ctx, &elbproxy.GetProxyAttributesInput{
		ProxyIdentifier: &input.ProxyIdentifier,
	})
	if err != nil {
		return nil, err
	}
	return &GetProxyAttributesOutput{
		Attributes: toAttributeDetailsList(resp.Attributes),
	}, nil
}

func (c *elbProxyClient) UpdateProxyAttributes(ctx context.Context, input UpdateProxyAttributesInput) (*UpdateProxyAttributesOutput, error) {
	attrs := make([]elbproxytypes.Attribute, len(input.Attributes))
	for i, a := range input.Attributes {
		attrs[i] = elbproxytypes.Attribute{Key: new(a.Key), Value: new(a.Value)}
	}
	resp, err := c.inner.UpdateProxyAttributes(ctx, &elbproxy.UpdateProxyAttributesInput{
		ProxyIdentifier: &input.ProxyIdentifier,
		Attributes:      attrs,
	})
	if err != nil {
		return nil, err
	}
	return &UpdateProxyAttributesOutput{
		Attributes: toAttributeDetailsList(resp.Attributes),
	}, nil
}

func (c *elbProxyClient) UpdateProxyNetworkConfiguration(ctx context.Context, input UpdateProxyNetworkConfigurationInput) (*ProxyDetails, error) {
	req := &elbproxy.UpdateProxyNetworkConfigurationInput{
		ProxyIdentifier: &input.ProxyIdentifier,
		VpcConfiguration: &elbproxytypes.UpdateProxyVpcConfiguration{
			Subnets:        toAPISubnetDetails(input.VpcConfiguration.Subnets),
			SecurityGroups: toAPISecurityGroupDetails(input.VpcConfiguration.SecurityGroups),
		},
	}
	resp, err := c.inner.UpdateProxyNetworkConfiguration(ctx, req)
	if err != nil {
		return nil, err
	}
	return toProxyDetails_updateNetwork(resp), nil
}

func (c *elbProxyClient) UpdateProxyConfiguration(ctx context.Context, input UpdateProxyConfigurationInput) error {
	req := &elbproxy.UpdateProxyConfigurationInput{
		ProxyIdentifier: &input.ProxyIdentifier,
	}
	if len(input.ArchiveBytes) > 0 {
		req.ConfigurationArchive = &elbproxytypes.ConfigurationArchive{
			ConfigurationBytes: input.ArchiveBytes,
		}
	}
	if len(input.BootstrapFile) > 0 {
		req.ConfigurationFile = &elbproxytypes.ConfigurationFile{
			ConfigurationBytes: input.BootstrapFile,
		}
	}
	// A nil CustomerKmsKeyArn is left off the request.
	req.CustomerKmsKeyArn = input.CustomerKmsKeyArn
	_, err := c.inner.UpdateProxyConfiguration(ctx, req)
	return err
}

// Example usage.
/*
	client := elbproxyaws.NewELBProxyClientFromConfig(awsCfg, nil)
	res, err := client.GetProxy(context.Background(), proxyArn)
	if err != nil {
		return error
	}
*/

// NewELBProxyClientFromConfig creates an ELBProxyClient from a pre-configured aws.Config.
// This ensures the client inherits region, retry, user-agent, and other middleware
// configured by AWSConfigGenerator.
// customEndpoint optionally overrides the service endpoint (from the endpoints resolver);
// pass nil to use the SDK default.
func NewELBProxyClientFromConfig(cfg aws.Config, customEndpoint *string) ELBProxyClient {
	sdkClient := elbproxy.NewFromConfig(cfg, func(o *elbproxy.Options) {
		if customEndpoint != nil {
			o.BaseEndpoint = customEndpoint
		}
	})
	return &elbProxyClient{inner: sdkClient}
}
