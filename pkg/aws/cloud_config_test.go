// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
)

func TestResolveVpcID(t *testing.T) {
	tests := []struct {
		name       string
		cfg        CloudConfig
		nodeName   string
		setupEC2   func(*services.MockEC2)
		wantVpcID  string
		wantErrMsg string
	}{
		{
			name:      "explicit vpc id skips all lookups",
			cfg:       CloudConfig{VpcID: "vpc-explicit"},
			wantVpcID: "vpc-explicit",
		},
		{
			name: "vpc tags lookup success",
			cfg:  CloudConfig{VpcTags: map[string]string{"env": "prod"}},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeVpcsAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Vpc{
					{VpcId: aws.String("vpc-from-tags")},
				}, nil)
			},
			wantVpcID: "vpc-from-tags",
		},
		{
			name: "vpc tags lookup - no vpc found",
			cfg:  CloudConfig{VpcTags: map[string]string{"env": "prod"}},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeVpcsAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Vpc{}, nil)
			},
			wantErrMsg: "no VPC found",
		},
		{
			name: "vpc tags lookup - multiple vpcs",
			cfg:  CloudConfig{VpcTags: map[string]string{"env": "prod"}},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeVpcsAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Vpc{
					{VpcId: aws.String("vpc-1")},
					{VpcId: aws.String("vpc-2")},
				}, nil)
			},
			wantErrMsg: "multiple VPCs found",
		},
		{
			name: "vpc tags lookup - ec2 error",
			cfg:  CloudConfig{VpcTags: map[string]string{"env": "prod"}},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeVpcsAsList(gomock.Any(), gomock.Any()).Return(nil, errors.New("ec2 error"))
			},
			wantErrMsg: "failed to fetch VPC ID with tags",
		},
		{
			name: "vpc tags lookup - nil vpc id",
			cfg:  CloudConfig{VpcTags: map[string]string{"env": "prod"}},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeVpcsAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Vpc{
					{VpcId: nil},
				}, nil)
			},
			wantErrMsg: "has nil VpcId",
		},
		{
			name:     "nodename instance lookup success",
			cfg:      CloudConfig{},
			nodeName: "i-0abc123",
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Instance{
					{VpcId: aws.String("vpc-from-instance")},
				}, nil)
			},
			wantVpcID: "vpc-from-instance",
		},
		{
			name:       "nodename empty",
			cfg:        CloudConfig{},
			nodeName:   "",
			wantErrMsg: "not an instance ID",
		},
		{
			name:       "nodename not an instance id",
			cfg:        CloudConfig{},
			nodeName:   "ip-10-0-0-1.ec2.internal",
			wantErrMsg: "not an instance ID",
		},
		{
			name:     "nodename describe instances error",
			cfg:      CloudConfig{},
			nodeName: "i-0abc123",
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return(nil, errors.New("api error"))
			},
			wantErrMsg: "failed to describe instance",
		},
		{
			name:     "nodename describe returns zero instances",
			cfg:      CloudConfig{},
			nodeName: "i-0abc123",
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Instance{}, nil)
			},
			wantErrMsg: "expected 1 instance",
		},
		{
			name:     "nodename describe returns multiple instances",
			cfg:      CloudConfig{},
			nodeName: "i-0abc123",
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Instance{
					{VpcId: aws.String("vpc-1")},
					{VpcId: aws.String("vpc-2")},
				}, nil)
			},
			wantErrMsg: "expected 1 instance",
		},
		{
			name:     "nodename instance has nil vpc id",
			cfg:      CloudConfig{},
			nodeName: "i-0abc123",
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return([]ec2types.Instance{
					{VpcId: nil},
				}, nil)
			},
			wantErrMsg: "has no VPC ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockEC2 := services.NewMockEC2(ctrl)
			if tt.setupEC2 != nil {
				tt.setupEC2(mockEC2)
			}

			// Inject a getenv that returns the table's nodeName for NODENAME
			// and empty for anything else. Avoids touching process env state.
			getenv := func(k string) string {
				if k == "NODENAME" {
					return tt.nodeName
				}
				return ""
			}

			err := tt.cfg.ResolveVpcID(context.Background(), mockEC2, getenv, logr.Discard())

			if tt.wantErrMsg != "" {
				assert.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantVpcID, tt.cfg.VpcID)
		})
	}
}

func TestCloudConfig_BindFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantCfg CloudConfig
	}{
		{
			name: "defaults when no flags provided",
			args: nil,
			wantCfg: CloudConfig{
				Region:      "",
				VpcID:       "",
				MaxAttempts: DefaultMaxAttempts,
			},
		},
		{
			name: "parses all flags",
			args: []string{
				"--aws-region=us-east-1",
				"--aws-vpc-id=vpc-abc",
				"--aws-vpc-tags=env=prod,team=platform",
				"--aws-max-attempts=5",
				"--aws-api-endpoints=EC2=https://ec2.local",
			},
			wantCfg: CloudConfig{
				Region:       "us-east-1",
				VpcID:        "vpc-abc",
				VpcTags:      map[string]string{"env": "prod", "team": "platform"},
				MaxAttempts:  5,
				AWSEndpoints: map[string]string{"EC2": "https://ec2.local"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg CloudConfig
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			cfg.BindFlags(fs)
			require.NoError(t, fs.Parse(tt.args))
			assert.Equal(t, tt.wantCfg, cfg)
		})
	}
}
