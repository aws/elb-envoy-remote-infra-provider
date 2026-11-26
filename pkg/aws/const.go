// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

const (
	flagAWSRegion       = "aws-region"
	flagAWSAPIEndpoints = "aws-api-endpoints"
	flagAWSVpcID        = "aws-vpc-id"
	flagAWSVpcTags      = "aws-vpc-tags"
	flagAWSMaxAttempts  = "aws-max-attempts"
	defaultVpcID        = ""
	defaultRegion       = ""

	// DefaultMaxAttempts is the default total number of attempts (initial + retries) for AWS API calls.
	DefaultMaxAttempts = 10

	// nodeNameEnvVar is the env var the kubelet/downward API sets to the
	// node's EC2 instance ID ("i-..."), used as a last-resort VPC fallback.
	nodeNameEnvVar = "NODENAME"
)
