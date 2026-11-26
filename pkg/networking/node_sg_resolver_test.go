// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"errors"
	"testing"

	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func nodeWithReadyStatus(name string, ready bool) corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: status},
			},
		},
	}
}

// nodeWithComputeType returns a Ready node labelled with the given
// eks.amazonaws.com/compute-type value (empty => label absent).
func nodeWithComputeType(name, computeType string) corev1.Node {
	n := nodeWithReadyStatus(name, true)
	if computeType != "" {
		n.Labels = map[string]string{labelComputeType: computeType}
	}
	return n
}

func TestResolveNodeSGIDs(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []corev1.Node
		setupENI   func(*MockNodeENIInfoResolver)
		wantSGIDs  []string
		wantErrMsg string
	}{
		{
			name:  "single ready node resolved to one sg",
			nodes: []corev1.Node{nodeWithReadyStatus("node-1", true)},
			setupENI: func(m *MockNodeENIInfoResolver) {
				m.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(map[types.NamespacedName]string{
					{Name: "node-1"}: "sg-aaa",
				}, nil)
			},
			wantSGIDs: []string{"sg-aaa"},
		},
		{
			name: "multiple nodes with duplicate sg ids are deduplicated",
			nodes: []corev1.Node{
				nodeWithReadyStatus("node-1", true),
				nodeWithReadyStatus("node-2", true),
				nodeWithReadyStatus("node-3", true),
			},
			setupENI: func(m *MockNodeENIInfoResolver) {
				m.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(map[types.NamespacedName]string{
					{Name: "node-1"}: "sg-aaa",
					{Name: "node-2"}: "sg-aaa",
					{Name: "node-3"}: "sg-bbb",
				}, nil)
			},
			wantSGIDs: []string{"sg-aaa", "sg-bbb"},
		},
		{
			name: "not-ready nodes are excluded",
			nodes: []corev1.Node{
				nodeWithReadyStatus("node-ready", true),
				nodeWithReadyStatus("node-not-ready", false),
			},
			setupENI: func(m *MockNodeENIInfoResolver) {
				m.EXPECT().Resolve(gomock.Any(), gomock.Len(1)).Return(map[types.NamespacedName]string{
					{Name: "node-ready"}: "sg-aaa",
				}, nil)
			},
			wantSGIDs: []string{"sg-aaa"},
		},
		{
			name:  "no ready nodes returns error",
			nodes: []corev1.Node{nodeWithReadyStatus("node-1", false)},
			setupENI: func(m *MockNodeENIInfoResolver) {
				m.EXPECT().Resolve(gomock.Any(), gomock.Len(0)).Return(map[types.NamespacedName]string{}, nil)
			},
			wantErrMsg: "no node security groups discovered",
		},
		{
			name:  "eni resolver error is propagated",
			nodes: []corev1.Node{nodeWithReadyStatus("node-1", true)},
			setupENI: func(m *MockNodeENIInfoResolver) {
				m.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, errors.New("eni resolve failed"))
			},
			wantErrMsg: "failed to resolve node SGs",
		},
		{
			name: "non-EC2 compute types are excluded from sg resolution",
			nodes: []corev1.Node{
				nodeWithComputeType("node-ec2-explicit", "ec2"),
				nodeWithComputeType("node-ec2-unlabelled", ""), // older nodes without the label
				nodeWithComputeType("node-hybrid", "hybrid"),
				nodeWithComputeType("node-fargate", "fargate"),
				nodeWithComputeType("node-future", "someFutureType"),
			},
			setupENI: func(m *MockNodeENIInfoResolver) {
				// Only the two EC2 nodes should be passed — Len(2).
				m.EXPECT().Resolve(gomock.Any(), gomock.Len(2)).Return(map[types.NamespacedName]string{
					{Name: "node-ec2-explicit"}:   "sg-aaa",
					{Name: "node-ec2-unlabelled"}: "sg-aaa",
				}, nil)
			},
			wantSGIDs: []string{"sg-aaa"},
		},
		{
			name:  "eni resolver returns empty map",
			nodes: []corev1.Node{nodeWithReadyStatus("node-1", true)},
			setupENI: func(m *MockNodeENIInfoResolver) {
				m.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(map[types.NamespacedName]string{}, nil)
			},
			wantErrMsg: "no node security groups discovered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			nodeList := &corev1.NodeList{Items: tt.nodes}
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithLists(nodeList).Build()

			mockENI := NewMockNodeENIInfoResolver(ctrl)
			if tt.setupENI != nil {
				tt.setupENI(mockENI)
			}

			resolver := NewDefaultNodeSGResolver(mockENI, nil, "", "", k8sClient, log.Log.WithName("test"))
			got, err := resolver.ResolveNodeSGIDs(context.Background())

			if tt.wantErrMsg != "" {
				assert.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantSGIDs, got)
		})
	}
}

func TestIsNodeReady(t *testing.T) {
	tests := []struct {
		name string
		node corev1.Node
		want bool
	}{
		{
			name: "node with ready condition true",
			node: nodeWithReadyStatus("n", true),
			want: true,
		},
		{
			name: "node with ready condition false",
			node: nodeWithReadyStatus("n", false),
			want: false,
		},
		{
			name: "node with no conditions",
			node: corev1.Node{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isNodeReady(tt.node))
		})
	}
}

func TestFindNodeSGsByClusterTag(t *testing.T) {
	tests := []struct {
		name        string
		clusterName string
		vpcID       string
		sgInfos     map[string]SecurityGroupInfo
		fetchErr    error
		wantSGIDs   []string
		wantErrMsg  string
	}{
		{
			name:        "returns sg ids tagged with cluster, scoped to vpc",
			clusterName: "my-cluster",
			vpcID:       "vpc-123",
			sgInfos: map[string]SecurityGroupInfo{
				"sg-node-1": {SecurityGroupID: "sg-node-1"},
				"sg-node-2": {SecurityGroupID: "sg-node-2"},
			},
			wantSGIDs: []string{"sg-node-1", "sg-node-2"},
		},
		{
			name:        "no matching sgs returns empty slice",
			clusterName: "my-cluster",
			vpcID:       "vpc-123",
			sgInfos:     map[string]SecurityGroupInfo{},
			wantSGIDs:   []string{},
		},
		{
			name:        "describe error is propagated",
			clusterName: "my-cluster",
			vpcID:       "vpc-123",
			fetchErr:    errors.New("throttled"),
			wantErrMsg:  "failed to describe node SGs by cluster tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSGMgr := NewMockSecurityGroupManager(ctrl)
			mockSGMgr.EXPECT().
				FetchSGInfosByRequest(gomock.Any(), gomock.AssignableToTypeOf(&ec2sdk.DescribeSecurityGroupsInput{})).
				DoAndReturn(func(_ context.Context, in *ec2sdk.DescribeSecurityGroupsInput) (map[string]SecurityGroupInfo, error) {
					// Verify both owned/shared values and vpc-id filter are present.
					assert.Len(t, in.Filters, 2)
					var tagFilter, vpcFilter *ec2types.Filter
					for i := range in.Filters {
						if in.Filters[i].Name != nil && *in.Filters[i].Name == "vpc-id" {
							vpcFilter = &in.Filters[i]
						} else {
							tagFilter = &in.Filters[i]
						}
					}
					require.NotNil(t, tagFilter, "cluster tag filter must be present")
					assert.ElementsMatch(t, []string{"owned", "shared"}, tagFilter.Values)
					require.NotNil(t, vpcFilter, "vpc-id filter must be present")
					assert.Equal(t, []string{tt.vpcID}, vpcFilter.Values)
					return tt.sgInfos, tt.fetchErr
				})

			r := &defaultNodeSGResolver{
				sgManager:   mockSGMgr,
				clusterName: tt.clusterName,
				vpcID:       tt.vpcID,
				logger:      log.Log.WithName("test"),
			}

			got, err := r.FindNodeSGsByClusterTag(context.Background())

			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantSGIDs, got)
		})
	}
}
