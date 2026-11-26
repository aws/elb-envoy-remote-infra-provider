// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"errors"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/cache"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func newTestNode(name, uid string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)},
	}
}

// sdkENI builds an InstanceNetworkInterface with the given deviceIndex, ENI ID, and SG IDs.
func sdkENI(deviceIndex int32, eniID string, sgIDs ...string) ec2types.InstanceNetworkInterface {
	groups := make([]ec2types.GroupIdentifier, 0, len(sgIDs))
	for _, id := range sgIDs {
		groups = append(groups, ec2types.GroupIdentifier{GroupId: awssdk.String(id)})
	}
	return ec2types.InstanceNetworkInterface{
		NetworkInterfaceId: awssdk.String(eniID),
		Attachment:         &ec2types.InstanceNetworkInterfaceAttachment{DeviceIndex: awssdk.Int32(deviceIndex)},
		Groups:             groups,
	}
}

// sdkInstance builds an Instance with a primary ENI carrying the given SG IDs.
func sdkInstance(instanceID string, sgIDs ...string) *ec2types.Instance {
	return &ec2types.Instance{
		InstanceId:        awssdk.String(instanceID),
		NetworkInterfaces: []ec2types.InstanceNetworkInterface{sdkENI(0, "eni-"+instanceID, sgIDs...)},
	}
}

// newENIResolver constructs a defaultNodeENIInfoResolver wired with fresh mocks.
func newENIResolver(ctrl *gomock.Controller, clusterName string) (*defaultNodeENIInfoResolver, *MockNodeInfoProvider, *MockSecurityGroupManager) {
	provider := NewMockNodeInfoProvider(ctrl)
	sgMgr := NewMockSecurityGroupManager(ctrl)
	return &defaultNodeENIInfoResolver{
		nodeInfoProvider:    provider,
		sgManager:           sgMgr,
		clusterName:         clusterName,
		logger:              log.Log.WithName("test"),
		nodeENIInfoCache:    cache.NewExpiring(),
		nodeENIInfoCacheTTL: 1 * time.Minute,
	}, provider, sgMgr
}

func TestFindInstancePrimaryENI(t *testing.T) {
	tests := []struct {
		name    string
		enis    []ec2types.InstanceNetworkInterface
		wantENI string
		wantErr bool
	}{
		{
			name:    "primary ENI found among multiple",
			enis:    []ec2types.InstanceNetworkInterface{sdkENI(1, "eni-secondary"), sdkENI(0, "eni-primary")},
			wantENI: "eni-primary",
		},
		{
			name:    "only primary ENI",
			enis:    []ec2types.InstanceNetworkInterface{sdkENI(0, "eni-primary")},
			wantENI: "eni-primary",
		},
		{
			name:    "no primary ENI returns error",
			enis:    []ec2types.InstanceNetworkInterface{sdkENI(1, "eni-1"), sdkENI(2, "eni-2")},
			wantErr: true,
		},
		{
			name:    "empty ENI list returns error",
			enis:    nil,
			wantErr: true,
		},
		{
			name: "ENI with nil attachment is skipped",
			enis: []ec2types.InstanceNetworkInterface{
				{NetworkInterfaceId: awssdk.String("eni-unattached")},
				sdkENI(0, "eni-primary"),
			},
			wantENI: "eni-primary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findInstancePrimaryENI(tt.enis)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantENI, awssdk.ToString(got.NetworkInterfaceId))
		})
	}
}

func TestBuildENIInfoViaInstanceENI(t *testing.T) {
	tests := []struct {
		name    string
		eni     ec2types.InstanceNetworkInterface
		wantID  string
		wantSGs []string
	}{
		{
			name:    "single SG",
			eni:     sdkENI(0, "eni-1", "sg-a"),
			wantID:  "eni-1",
			wantSGs: []string{"sg-a"},
		},
		{
			name:    "multiple SGs preserved in order",
			eni:     sdkENI(0, "eni-1", "sg-a", "sg-b", "sg-c"),
			wantID:  "eni-1",
			wantSGs: []string{"sg-a", "sg-b", "sg-c"},
		},
		{
			name: "nil GroupId is silently dropped",
			eni: ec2types.InstanceNetworkInterface{
				NetworkInterfaceId: awssdk.String("eni-1"),
				Groups: []ec2types.GroupIdentifier{
					{GroupId: awssdk.String("sg-a")},
					{GroupId: nil},
					{GroupId: awssdk.String("sg-b")},
				},
			},
			wantID:  "eni-1",
			wantSGs: []string{"sg-a", "sg-b"},
		},
		{
			name:    "no SGs returns empty slice",
			eni:     sdkENI(0, "eni-1"),
			wantID:  "eni-1",
			wantSGs: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildENIInfoViaInstanceENI(tt.eni)
			assert.Equal(t, tt.wantID, got.NetworkInterfaceID)
			assert.Equal(t, tt.wantSGs, got.SecurityGroups)
		})
	}
}

func TestResolveNodeSGFromENI(t *testing.T) {
	const clusterName = "my-cluster"
	clusterTag := "kubernetes.io/cluster/" + clusterName

	tests := []struct {
		name       string
		eniInfo    ENIInfo
		sgInfos    map[string]SecurityGroupInfo
		sgFetchErr error
		wantSGID   string
		wantErrMsg string
	}{
		{
			name:     "single SG fast path skips FetchSGInfosByID",
			eniInfo:  ENIInfo{NetworkInterfaceID: "eni-1", SecurityGroups: []string{"sg-only"}},
			wantSGID: "sg-only",
		},
		{
			name:    "multiple SGs, exactly one tagged with cluster tag",
			eniInfo: ENIInfo{NetworkInterfaceID: "eni-1", SecurityGroups: []string{"sg-a", "sg-b"}},
			sgInfos: map[string]SecurityGroupInfo{
				"sg-a": {SecurityGroupID: "sg-a", Tags: map[string]string{clusterTag: "owned"}},
				"sg-b": {SecurityGroupID: "sg-b", Tags: map[string]string{"other": "tag"}},
			},
			wantSGID: "sg-a",
		},
		{
			name:    "multiple SGs, none tagged returns error",
			eniInfo: ENIInfo{NetworkInterfaceID: "eni-1", SecurityGroups: []string{"sg-a", "sg-b"}},
			sgInfos: map[string]SecurityGroupInfo{
				"sg-a": {SecurityGroupID: "sg-a", Tags: map[string]string{"other": "tag"}},
				"sg-b": {SecurityGroupID: "sg-b", Tags: map[string]string{}},
			},
			wantErrMsg: "expected exactly one SG tagged",
		},
		{
			name:    "multiple SGs, more than one tagged returns error",
			eniInfo: ENIInfo{NetworkInterfaceID: "eni-1", SecurityGroups: []string{"sg-a", "sg-b"}},
			sgInfos: map[string]SecurityGroupInfo{
				"sg-a": {SecurityGroupID: "sg-a", Tags: map[string]string{clusterTag: "owned"}},
				"sg-b": {SecurityGroupID: "sg-b", Tags: map[string]string{clusterTag: "shared"}},
			},
			wantErrMsg: "expected exactly one SG tagged",
		},
		{
			name:       "sgManager error is propagated",
			eniInfo:    ENIInfo{NetworkInterfaceID: "eni-1", SecurityGroups: []string{"sg-a", "sg-b"}},
			sgFetchErr: errors.New("describe failed"),
			wantErrMsg: "describe failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSG := NewMockSecurityGroupManager(ctrl)
			if len(tt.eniInfo.SecurityGroups) > 1 {
				mockSG.EXPECT().
					FetchSGInfosByID(gomock.Any(), tt.eniInfo.SecurityGroups).
					Return(tt.sgInfos, tt.sgFetchErr)
			}

			r := &defaultNodeENIInfoResolver{
				sgManager:   mockSG,
				clusterName: clusterName,
				logger:      log.Log.WithName("test"),
			}

			got, err := r.resolveNodeSGFromENI(context.Background(), tt.eniInfo)
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSGID, got)
		})
	}
}

// --- cache/provider orchestration ---

func TestResolve(t *testing.T) {
	tests := []struct {
		name          string
		nodes         []*corev1.Node
		primeCache    func(r *defaultNodeENIInfoResolver)
		setupProvider func(*MockNodeInfoProvider)
		wantResult    map[types.NamespacedName]string
		wantErrMsg    string
	}{
		{
			name:  "all nodes cached; no provider call",
			nodes: []*corev1.Node{newTestNode("node-1", "uid-1"), newTestNode("node-2", "uid-2")},
			primeCache: func(r *defaultNodeENIInfoResolver) {
				r.nodeENIInfoCache.Set(nodeENIInfoCacheKey{nodeKey: types.NamespacedName{Name: "node-1"}, nodeUID: "uid-1"}, "sg-aaa", r.nodeENIInfoCacheTTL)
				r.nodeENIInfoCache.Set(nodeENIInfoCacheKey{nodeKey: types.NamespacedName{Name: "node-2"}, nodeUID: "uid-2"}, "sg-bbb", r.nodeENIInfoCacheTTL)
			},
			wantResult: map[types.NamespacedName]string{
				{Name: "node-1"}: "sg-aaa",
				{Name: "node-2"}: "sg-bbb",
			},
		},
		{
			name:  "all cache misses; provider called",
			nodes: []*corev1.Node{newTestNode("node-1", "uid-1")},
			setupProvider: func(p *MockNodeInfoProvider) {
				p.EXPECT().FetchNodeInstances(gomock.Any(), gomock.Len(1)).Return(
					map[types.NamespacedName]*ec2types.Instance{{Name: "node-1"}: sdkInstance("i-1", "sg-only")}, nil,
				)
			},
			wantResult: map[types.NamespacedName]string{{Name: "node-1"}: "sg-only"},
		},
		{
			name:  "mixed: cache hit for one, live for the other",
			nodes: []*corev1.Node{newTestNode("node-cached", "uid-c"), newTestNode("node-fresh", "uid-f")},
			primeCache: func(r *defaultNodeENIInfoResolver) {
				r.nodeENIInfoCache.Set(nodeENIInfoCacheKey{nodeKey: types.NamespacedName{Name: "node-cached"}, nodeUID: "uid-c"}, "sg-cached", r.nodeENIInfoCacheTTL)
			},
			setupProvider: func(p *MockNodeInfoProvider) {
				p.EXPECT().FetchNodeInstances(gomock.Any(), gomock.Len(1)).Return(
					map[types.NamespacedName]*ec2types.Instance{{Name: "node-fresh"}: sdkInstance("i-f", "sg-fresh")}, nil,
				)
			},
			wantResult: map[types.NamespacedName]string{
				{Name: "node-cached"}: "sg-cached",
				{Name: "node-fresh"}:  "sg-fresh",
			},
		},
		{
			name:  "replaced node (same name, new UID) bypasses stale cache",
			nodes: []*corev1.Node{newTestNode("node-1", "uid-new")},
			primeCache: func(r *defaultNodeENIInfoResolver) {
				r.nodeENIInfoCache.Set(nodeENIInfoCacheKey{nodeKey: types.NamespacedName{Name: "node-1"}, nodeUID: "uid-old"}, "sg-old", r.nodeENIInfoCacheTTL)
			},
			setupProvider: func(p *MockNodeInfoProvider) {
				p.EXPECT().FetchNodeInstances(gomock.Any(), gomock.Len(1)).Return(
					map[types.NamespacedName]*ec2types.Instance{{Name: "node-1"}: sdkInstance("i-1", "sg-new")}, nil,
				)
			},
			wantResult: map[types.NamespacedName]string{{Name: "node-1"}: "sg-new"},
		},
		{
			name:  "provider returns partial result; unresolved nodes error",
			nodes: []*corev1.Node{newTestNode("node-1", "uid-1"), newTestNode("node-2", "uid-2")},
			setupProvider: func(p *MockNodeInfoProvider) {
				p.EXPECT().FetchNodeInstances(gomock.Any(), gomock.Any()).Return(
					map[types.NamespacedName]*ec2types.Instance{{Name: "node-1"}: sdkInstance("i-1", "sg-a")}, nil,
				)
			},
			wantErrMsg: "cannot resolve node SG for nodes",
		},
		{
			name:  "provider error is propagated",
			nodes: []*corev1.Node{newTestNode("node-1", "uid-1")},
			setupProvider: func(p *MockNodeInfoProvider) {
				p.EXPECT().FetchNodeInstances(gomock.Any(), gomock.Any()).Return(nil, errors.New("describe failed"))
			},
			wantErrMsg: "describe failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			r, provider, _ := newENIResolver(ctrl, "c")
			if tt.primeCache != nil {
				tt.primeCache(r)
			}
			if tt.setupProvider != nil {
				tt.setupProvider(provider)
			}

			got, err := r.Resolve(context.Background(), tt.nodes)
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantResult, got)
		})
	}
}
