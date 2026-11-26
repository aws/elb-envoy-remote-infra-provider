// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
)

// CloudConfig holds configuration for the AWS cloud client.
// Region and VpcID are auto-detected when running on an EKS node:
//   - Region: from AWS_REGION or AWS_DEFAULT_REGION env vars. --aws-region flag takes precedence.
//   - VpcID: from --aws-vpc-tags tag lookup, then EC2 DescribeInstances using the NODENAME env var.
//
// Flags:
//
//	--aws-region        AWS region. Auto-detected from AWS_REGION or AWS_DEFAULT_REGION env vars.
//	--aws-vpc-id        VPC ID for resources. Auto-detected via --aws-vpc-tags or NODENAME instance lookup if not set.
//	--aws-vpc-tags      Find VPC by tags instead of explicit ID, e.g. env=prod,team=platform.
//	--aws-max-attempts  Total attempts including initial request (default 10). SDK default (3) applies when 0.
//	--aws-api-endpoints Custom endpoint overrides, useful for LocalStack or VPC endpoints.
type CloudConfig struct {
	// AWS Region for the kubernetes cluster.
	Region string

	// VpcID for the resources. If empty, it will be resolved automatically.
	VpcID string

	// VpcTags are used to look up the VPC by tags when VpcID is not set.
	VpcTags map[string]string

	// MaxAttempts is the total number of attempts (initial request + retries) for AWS API calls.
	// Maps directly to retry.StandardOptions.MaxAttempts. SDK default (3) is used when set to 0.
	MaxAttempts int

	// Custom AWS endpoint overrides, format: serviceID=URL.
	AWSEndpoints map[string]string

	// TODO: add per-operation throttle config for AWS API calls.
}

// BindFlags registers CloudConfig fields as CLI flags.
func (cfg *CloudConfig) BindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&cfg.Region, flagAWSRegion, defaultRegion, "AWS Region for the kubernetes cluster")
	fs.StringVar(&cfg.VpcID, flagAWSVpcID, defaultVpcID, "AWS VpcID for the resources")
	fs.StringToStringVar(&cfg.VpcTags, flagAWSVpcTags, nil, "AWS VPC tags for VPC lookup, format: key1=val1,key2=val2")
	fs.IntVar(&cfg.MaxAttempts, flagAWSMaxAttempts, DefaultMaxAttempts, "Total number of attempts (initial + retries) for AWS API calls. SDK default (3) applies when set to 0.")
	fs.StringToStringVar(&cfg.AWSEndpoints, flagAWSAPIEndpoints, nil, "Custom AWS endpoint overrides, format: serviceID1=URL1,serviceID2=URL2")
}

// ResolveVpcID resolves and sets cfg.VpcID using the explicit flag, VPC tags, or NODENAME instance lookup fallback.
// getenv is injected so tests don't depend on process env state.
func (cfg *CloudConfig) ResolveVpcID(ctx context.Context, ec2Service services.EC2, getenv func(string) string, logger logr.Logger) error {
	if cfg.VpcID != "" {
		logger.V(1).Info("using explicit VPC ID", "vpc", cfg.VpcID)
		return nil
	}

	var (
		vpcID string
		err   error
	)

	if len(cfg.VpcTags) > 0 {
		logger.V(1).Info("resolving VPC ID from tags", "tags", cfg.VpcTags)
		vpcID, err = inferVPCIDFromTags(ctx, ec2Service, cfg.VpcTags)
	} else {
		vpcID, err = inferVPCIDFromInstance(ctx, ec2Service, getenv)
	}

	if err != nil {
		return err
	}
	if vpcID == "" {
		// Guard against silent propagation of empty VPC IDs to downstream
		// consumers (security group manager, subnet resolver, etc.).
		return fmt.Errorf("resolved VPC ID is empty; set --aws-vpc-id or --aws-vpc-tags")
	}
	cfg.VpcID = vpcID
	return nil
}

// inferVPCIDFromInstance looks up the VPC ID using the NODENAME env var (instance ID).
func inferVPCIDFromInstance(ctx context.Context, ec2Service services.EC2, getenv func(string) string) (string, error) {
	nodeName := getenv(nodeNameEnvVar)
	if !strings.HasPrefix(nodeName, "i-") {
		return "", fmt.Errorf("NODENAME %q is not an instance ID; set --aws-vpc-id or --aws-vpc-tags", nodeName)
	}

	instances, err := ec2Service.DescribeInstancesAsList(ctx, &ec2.DescribeInstancesInput{
		InstanceIds:             []string{nodeName},
		IncludeManagedResources: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe instance %q: %w", nodeName, err)
	}
	if len(instances) != 1 {
		return "", fmt.Errorf("expected 1 instance for %q, got %d", nodeName, len(instances))
	}
	if instances[0].VpcId == nil {
		return "", fmt.Errorf("instance %q has no VPC ID", nodeName)
	}
	return *instances[0].VpcId, nil
}

// inferVPCIDFromTags looks up the VPC ID by matching all provided tags.
func inferVPCIDFromTags(ctx context.Context, ec2Service services.EC2, vpcTags map[string]string) (string, error) {
	var filters []ec2types.Filter
	for key, value := range vpcTags {
		filters = append(filters, ec2types.Filter{
			Name:   aws.String("tag:" + key),
			Values: []string{value},
		})
	}
	vpcs, err := ec2Service.DescribeVpcsAsList(ctx, &ec2.DescribeVpcsInput{
		Filters: filters,
	})
	if err != nil {
		return "", fmt.Errorf("failed to fetch VPC ID with tags: %w", err)
	}
	if len(vpcs) == 0 {
		return "", fmt.Errorf("no VPC found with tags %v", vpcTags)
	}
	if len(vpcs) > 1 {
		return "", fmt.Errorf("multiple VPCs found with tags %v", vpcTags)
	}
	if vpcs[0].VpcId == nil {
		return "", fmt.Errorf("VPC matched by tags %v has nil VpcId", vpcTags)
	}
	return *vpcs[0].VpcId, nil
}
