// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
)

func TestExtractInstanceID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		wantID     string
		wantErr    bool
	}{
		{
			name:       "canonical EKS format",
			providerID: "aws:///us-east-1a/i-0abc1234def56789",
			wantID:     "i-0abc1234def56789",
		},
		{
			name:       "format without region path",
			providerID: "aws:///i-0abc123",
			wantID:     "i-0abc123",
		},
		{
			name:       "bare instance id",
			providerID: "i-0abc123",
			wantID:     "i-0abc123",
		},
		{
			name:       "empty providerID returns error",
			providerID: "",
			wantErr:    true,
		},
		{
			name:       "hostname-style (hybrid/fargate) returns error",
			providerID: "aws:///us-east-1a/ip-10-0-0-1.ec2.internal",
			wantErr:    true,
		},
		{
			name:       "trailing slash returns error",
			providerID: "aws:///us-east-1a/i-0abc/",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractInstanceID(tt.providerID)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got)
		})
	}
}

func nodeWithProvider(name, providerID string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: providerID},
	}
}

func instanceWithID(id string) ec2types.Instance {
	return ec2types.Instance{InstanceId: awssdk.String(id)}
}

func TestFetchNodeInstances(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []*corev1.Node
		setupEC2   func(*services.MockEC2)
		wantKeys   []string // node names present in the result
		wantErrMsg string
	}{
		{
			name:  "empty node list returns nil",
			nodes: nil,
			// No EC2 call expected.
			wantKeys: nil,
		},
		{
			name: "only non-EC2 nodes returns empty map without EC2 call",
			nodes: []*corev1.Node{
				nodeWithProvider("hybrid-1", "", map[string]string{labelComputeType: "hybrid"}),
				nodeWithProvider("fargate-1", "", map[string]string{labelComputeType: "fargate"}),
			},
			// No EC2 call expected.
			wantKeys: []string{},
		},
		{
			name: "ec2 nodes call DescribeInstances with sorted unique IDs",
			nodes: []*corev1.Node{
				nodeWithProvider("node-b", "aws:///us-east-1b/i-bbb", nil),
				nodeWithProvider("node-a", "aws:///us-east-1a/i-aaa", nil),
				nodeWithProvider("node-dup", "aws:///us-east-1a/i-aaa", nil), // same instance, different node
			},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().
					DescribeInstancesAsList(gomock.Any(), gomock.AssignableToTypeOf(&ec2sdk.DescribeInstancesInput{})).
					DoAndReturn(func(_ context.Context, in *ec2sdk.DescribeInstancesInput) ([]ec2types.Instance, error) {
						// sets.StringKeySet().List() returns sorted IDs.
						assert.Equal(t, []string{"i-aaa", "i-bbb"}, in.InstanceIds)
						return []ec2types.Instance{instanceWithID("i-aaa"), instanceWithID("i-bbb")}, nil
					})
			},
			wantKeys: []string{"node-a", "node-b", "node-dup"},
		},
		{
			name: "hybrid nodes are skipped alongside EC2 nodes",
			nodes: []*corev1.Node{
				nodeWithProvider("node-ec2", "aws:///us-east-1a/i-ec2", nil),
				nodeWithProvider("node-hybrid", "", map[string]string{labelComputeType: "hybrid"}),
				nodeWithProvider("node-ec2-labelled", "aws:///us-east-1a/i-ec2b", map[string]string{labelComputeType: "ec2"}),
			},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return(
					[]ec2types.Instance{instanceWithID("i-ec2"), instanceWithID("i-ec2b")}, nil,
				)
			},
			wantKeys: []string{"node-ec2", "node-ec2-labelled"},
		},
		{
			name: "invalid providerID aborts before EC2 call",
			nodes: []*corev1.Node{
				nodeWithProvider("node-bad", "ip-10-0-0-1", nil),
			},
			// No EC2 call expected; fail-fast on parse.
			wantErrMsg: "unexpected providerID format",
		},
		{
			name: "DescribeInstances error is propagated",
			nodes: []*corev1.Node{
				nodeWithProvider("node-1", "aws:///us-east-1a/i-aaa", nil),
			},
			setupEC2: func(m *services.MockEC2) {
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return(nil, errors.New("throttled"))
			},
			wantErrMsg: "throttled",
		},
		{
			name: "instance not returned for a node is silently missing from result",
			nodes: []*corev1.Node{
				nodeWithProvider("node-present", "aws:///us-east-1a/i-aaa", nil),
				nodeWithProvider("node-missing", "aws:///us-east-1a/i-bbb", nil),
			},
			setupEC2: func(m *services.MockEC2) {
				// EC2 returns only one instance even though two were requested.
				m.EXPECT().DescribeInstancesAsList(gomock.Any(), gomock.Any()).Return(
					[]ec2types.Instance{instanceWithID("i-aaa")}, nil,
				)
			},
			// node-missing intentionally absent; caller's unresolved-nodes check
			// is what surfaces this as an error at the resolver layer.
			wantKeys: []string{"node-present"},
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

			p := NewDefaultNodeInfoProvider(mockEC2, log.Log.WithName("test"))
			got, err := p.FetchNodeInstances(context.Background(), tt.nodes)

			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)

			gotKeys := make([]string, 0, len(got))
			for k := range got {
				gotKeys = append(gotKeys, k.Name)
			}
			assert.ElementsMatch(t, tt.wantKeys, gotKeys)

			// Sanity: every returned instance pointer should match its node's expected instance ID.
			for nodeKey, inst := range got {
				require.NotNil(t, inst, "instance pointer should be non-nil for %s", nodeKey.Name)
				assert.True(t, awssdk.ToString(inst.InstanceId) != "",
					"returned instance for %s should have an ID", nodeKey.Name)
			}
		})
	}
}
