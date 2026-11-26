// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"context"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	ec2deploy "github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	kubedeploy "github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/kubernetes"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeployResult holds the results from deploying a stack.
type DeployResult struct {
	ProxyDetails *elbproxyaws.ProxyDetails
}

type ResourceSynthesizer interface {
	Synthesize(ctx context.Context) error
	PostSynthesize(ctx context.Context) error
}

type StackDeployer interface {
	Deploy(ctx context.Context, stack core.Stack) (*DeployResult, error)
}

type defaultStackDeployer struct {
	client                 elbproxyaws.ELBProxyClient
	k8sClient              client.Client
	proxyManager           elbproxy.ProxyManager
	serviceManager         kubedeploy.ServiceManager
	trackingProvider       tracking.Provider
	ec2TaggingManager      ec2deploy.TaggingManager
	elbProxyTaggingManager elbproxy.TaggingManager
	sgManager              ec2deploy.SecurityGroupManager
	logger                 logging.Logger
}

func NewStackDeployer(
	client elbproxyaws.ELBProxyClient,
	k8sClient client.Client,
	proxyManager elbproxy.ProxyManager,
	serviceManager kubedeploy.ServiceManager,
	trackingProvider tracking.Provider,
	ec2TaggingManager ec2deploy.TaggingManager,
	elbProxyTaggingManager elbproxy.TaggingManager,
	sgManager ec2deploy.SecurityGroupManager,
	logger logging.Logger,
) StackDeployer {
	return &defaultStackDeployer{
		client:                 client,
		k8sClient:              k8sClient,
		proxyManager:           proxyManager,
		serviceManager:         serviceManager,
		trackingProvider:       trackingProvider,
		ec2TaggingManager:      ec2TaggingManager,
		elbProxyTaggingManager: elbProxyTaggingManager,
		sgManager:              sgManager,
		logger:                 logger,
	}
}

func (d *defaultStackDeployer) Deploy(ctx context.Context, stack core.Stack) (*DeployResult, error) {
	sgSynthesizer := ec2deploy.NewSecurityGroupSynthesizer(
		d.trackingProvider,
		d.ec2TaggingManager,
		d.sgManager,
		d.logger,
		stack,
	)
	proxySynthesizer := elbproxy.NewProxySynthesizer(d.trackingProvider, d.elbProxyTaggingManager, d.proxyManager, d.logger, stack)
	serviceSynthesizer := kubedeploy.NewServiceSynthesizer(d.k8sClient, d.trackingProvider, d.serviceManager, d.logger, stack)

	synthesizers := []ResourceSynthesizer{
		sgSynthesizer,
		proxySynthesizer,
		serviceSynthesizer,
	}

	for _, synthesizer := range synthesizers {
		if err := synthesizer.Synthesize(ctx); err != nil {
			// Keep the observed proxy, e.g. a new proxy still provisioning, for status.
			return &DeployResult{ProxyDetails: proxySynthesizer.GetProxyDetails()}, err
		}
	}

	for i := len(synthesizers) - 1; i >= 0; i-- {
		if err := synthesizers[i].PostSynthesize(ctx); err != nil {
			return nil, err
		}
	}

	d.logger.Info("stack deployed successfully", "stackId", stack.StackID())

	return &DeployResult{
		ProxyDetails: proxySynthesizer.GetProxyDetails(),
	}, nil
}
