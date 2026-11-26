// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/endpoints"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/provider"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/go-logr/logr"
)

// Cloud holds AWS service clients used by the ELBProxy controller.
type Cloud struct {
	cfg      CloudConfig
	ec2      services.EC2
	rgt      services.RGT
	elbProxy ELBProxyClient
}

// NewCloud constructs a new Cloud from the given config.
// ctx is used for SDK default-config loading and for region/VPC resolution.
// Cancel ctx to abort a slow NewCloud startup.
func NewCloud(ctx context.Context, cfg CloudConfig, logger logr.Logger) (*Cloud, error) {
	endpointsResolver := endpoints.NewResolver(cfg.AWSEndpoints)

	awsCfg, err := NewAWSConfigGenerator(cfg).GenerateAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate AWS config: %w", err)
	}

	if awsCfg.Region == "" {
		return nil, fmt.Errorf("region not resolved; specify --aws-region or set AWS_REGION / AWS_DEFAULT_REGION")
	}
	cfg.Region = awsCfg.Region

	awsClientsProvider := provider.NewDefaultAWSClientsProvider(awsCfg, endpointsResolver)
	ec2Service := services.NewEC2(awsClientsProvider)

	if err := cfg.ResolveVpcID(ctx, ec2Service, os.Getenv, logger); err != nil {
		return nil, fmt.Errorf("failed to get VPC ID: %w", err)
	}

	// Endpoint override is optional. When --aws-api-endpoints does not carry an
	// entry for the ELBProxy service, elbProxyEndpoint is nil and the
	// SDK's default (prod) endpoint is used. To target a non-prod environment
	// (e.g. gamma), pass --aws-api-endpoints "ELB Proxy=<url>".
	//
	// TODO: route this client through AWSClientsProvider (add GetELBProxyClient)
	// so all endpoint resolution lives in one place.
	elbProxyEndpoint := endpointsResolver.EndpointFor(elbproxy.ServiceID)
	return &Cloud{
		cfg:      cfg,
		ec2:      ec2Service,
		rgt:      services.NewRGT(awsClientsProvider),
		elbProxy: NewELBProxyClientFromConfig(awsCfg, elbProxyEndpoint),
	}, nil
}

func (c *Cloud) EC2() services.EC2 {
	return c.ec2
}

// RGT returns the Resource Groups Tagging API client.
func (c *Cloud) RGT() services.RGT {
	return c.rgt
}

func (c *Cloud) ELBProxy() ELBProxyClient {
	return c.elbProxy
}

func (c *Cloud) Region() string {
	return c.cfg.Region
}

func (c *Cloud) VpcID() string {
	return c.cfg.VpcID
}
