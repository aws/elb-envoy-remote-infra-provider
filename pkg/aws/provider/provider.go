// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
)

type AWSClientsProvider interface {
	GetEC2Client(ctx context.Context, operationName string) (*ec2.Client, error)
	GetRGTClient(ctx context.Context, operationName string) (*resourcegroupstaggingapi.Client, error)
}
