// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"errors"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/cache"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func newTestSGManager(ctrl *gomock.Controller) (*defaultSecurityGroupManager, *services.MockEC2) {
	ec2Client := services.NewMockEC2(ctrl)
	mgr := &defaultSecurityGroupManager{
		ec2Client:      ec2Client,
		logger:         log.Log.WithName("test"),
		sgInfoCache:    cache.NewExpiring(),
		sgInfoCacheTTL: 10 * time.Minute,
	}
	return mgr, ec2Client
}

func sdkSG(id string, tags map[string]string) ec2types.SecurityGroup {
	var sdkTags []ec2types.Tag
	for k, v := range tags {
		sdkTags = append(sdkTags, ec2types.Tag{Key: awssdk.String(k), Value: awssdk.String(v)})
	}
	return ec2types.SecurityGroup{GroupId: awssdk.String(id), Tags: sdkTags}
}

func Test_defaultSecurityGroupManager_FetchSGInfosByID(t *testing.T) {
	type describeSGsCall struct {
		resp []ec2types.SecurityGroup
		err  error
	}
	type fields struct {
		describeSGsCalls []describeSGsCall
		preCacheSGs      map[string]SecurityGroupInfo
	}
	type args struct {
		sgIDs []string
		opts  []FetchSGInfoOption
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantIDs []string
		wantErr error
	}{
		{
			name: "fetches from AWS when cache empty",
			fields: fields{
				describeSGsCalls: []describeSGsCall{
					{resp: []ec2types.SecurityGroup{sdkSG("sg-aaa", nil)}},
				},
			},
			args:    args{sgIDs: []string{"sg-aaa"}},
			wantIDs: []string{"sg-aaa"},
		},
		{
			name: "returns from cache when available",
			fields: fields{
				preCacheSGs: map[string]SecurityGroupInfo{
					"sg-cached": {SecurityGroupID: "sg-cached"},
				},
			},
			args:    args{sgIDs: []string{"sg-cached"}},
			wantIDs: []string{"sg-cached"},
		},
		{
			name: "partial cache hit fetches remaining from AWS",
			fields: fields{
				preCacheSGs: map[string]SecurityGroupInfo{
					"sg-cached": {SecurityGroupID: "sg-cached"},
				},
				describeSGsCalls: []describeSGsCall{
					{resp: []ec2types.SecurityGroup{sdkSG("sg-new", nil)}},
				},
			},
			args:    args{sgIDs: []string{"sg-cached", "sg-new"}},
			wantIDs: []string{"sg-cached", "sg-new"},
		},
		{
			name: "WithReloadIgnoringCache bypasses cache",
			fields: fields{
				preCacheSGs: map[string]SecurityGroupInfo{
					"sg-aaa": {SecurityGroupID: "sg-aaa"},
				},
				describeSGsCalls: []describeSGsCall{
					{resp: []ec2types.SecurityGroup{sdkSG("sg-aaa", map[string]string{"refreshed": "true"})}},
				},
			},
			args:    args{sgIDs: []string{"sg-aaa"}, opts: []FetchSGInfoOption{WithReloadIgnoringCache()}},
			wantIDs: []string{"sg-aaa"},
		},
		{
			name: "returns error when AWS fails",
			fields: fields{
				describeSGsCalls: []describeSGsCall{
					{err: errors.New("aws error")},
				},
			},
			args:    args{sgIDs: []string{"sg-aaa"}},
			wantErr: errors.New("aws error"),
		},
		{
			name: "returns error when SG not found",
			fields: fields{
				describeSGsCalls: []describeSGsCall{
					{resp: nil},
				},
			},
			args:    args{sgIDs: []string{"sg-missing"}},
			wantErr: errors.New("couldn't fetch SecurityGroupInfos: [sg-missing]"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mgr, ec2Client := newTestSGManager(ctrl)
			for sgID, sgInfo := range tt.fields.preCacheSGs {
				mgr.sgInfoCache.Set(sgID, sgInfo, mgr.sgInfoCacheTTL)
			}
			for _, call := range tt.fields.describeSGsCalls {
				ec2Client.EXPECT().DescribeSecurityGroupsAsList(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			got, err := mgr.FetchSGInfosByID(context.Background(), tt.args.sgIDs, tt.args.opts...)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				for _, id := range tt.wantIDs {
					assert.Contains(t, got, id)
				}
			}
		})
	}
}

func Test_defaultSecurityGroupManager_FetchSGInfosByRequest(t *testing.T) {
	type describeSGsCall struct {
		resp []ec2types.SecurityGroup
		err  error
	}
	type fields struct {
		describeSGsCalls []describeSGsCall
	}
	tests := []struct {
		name    string
		fields  fields
		wantIDs []string
		wantErr error
	}{
		{
			name: "fetches and caches results",
			fields: fields{
				describeSGsCalls: []describeSGsCall{
					{resp: []ec2types.SecurityGroup{sdkSG("sg-aaa", nil), sdkSG("sg-bbb", nil)}},
				},
			},
			wantIDs: []string{"sg-aaa", "sg-bbb"},
		},
		{
			name: "returns error when AWS fails",
			fields: fields{
				describeSGsCalls: []describeSGsCall{
					{err: errors.New("describe failed")},
				},
			},
			wantErr: errors.New("describe failed"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mgr, ec2Client := newTestSGManager(ctrl)
			for _, call := range tt.fields.describeSGsCalls {
				ec2Client.EXPECT().DescribeSecurityGroupsAsList(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			req := &ec2sdk.DescribeSecurityGroupsInput{GroupIds: []string{"sg-aaa"}}
			got, err := mgr.FetchSGInfosByRequest(context.Background(), req)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				for _, id := range tt.wantIDs {
					assert.Contains(t, got, id)
				}
			}
		})
	}
}

func Test_defaultSecurityGroupManager_AuthorizeSGIngress(t *testing.T) {
	type authorizeSGIngressCall struct {
		resp *ec2sdk.AuthorizeSecurityGroupIngressOutput
		err  error
	}
	type fields struct {
		authorizeCalls []authorizeSGIngressCall
	}
	type args struct {
		sgID        string
		permissions []IPPermissionInfo
	}
	port80 := int32(80)
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr error
	}{
		{
			name: "authorizes ingress and clears cache",
			fields: fields{
				authorizeCalls: []authorizeSGIngressCall{
					{resp: &ec2sdk.AuthorizeSecurityGroupIngressOutput{}},
				},
			},
			args: args{
				sgID: "sg-aaa",
				permissions: []IPPermissionInfo{
					NewCIDRIPPermission("tcp", &port80, &port80, "10.0.0.0/8", NewIPPermissionLabelsForRawDescription("internal")),
				},
			},
		},
		{
			name: "returns error when EC2 fails",
			fields: fields{
				authorizeCalls: []authorizeSGIngressCall{
					{err: errors.New("authorize failed")},
				},
			},
			args: args{
				sgID: "sg-aaa",
				permissions: []IPPermissionInfo{
					NewCIDRIPPermission("tcp", &port80, &port80, "10.0.0.0/8", NewIPPermissionLabelsForRawDescription("internal")),
				},
			},
			wantErr: errors.New("authorize failed"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mgr, ec2Client := newTestSGManager(ctrl)
			mgr.sgInfoCache.Set(tt.args.sgID, SecurityGroupInfo{SecurityGroupID: tt.args.sgID}, mgr.sgInfoCacheTTL)
			for _, call := range tt.fields.authorizeCalls {
				ec2Client.EXPECT().AuthorizeSecurityGroupIngressWithContext(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			err := mgr.AuthorizeSGIngress(context.Background(), tt.args.sgID, tt.args.permissions)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				_, exists := mgr.sgInfoCache.Get(tt.args.sgID)
				assert.False(t, exists, "cache should be cleared after authorize")
			}
		})
	}
}

func Test_defaultSecurityGroupManager_RevokeSGIngress(t *testing.T) {
	type revokeSGIngressCall struct {
		resp *ec2sdk.RevokeSecurityGroupIngressOutput
		err  error
	}
	type fields struct {
		revokeCalls []revokeSGIngressCall
	}
	type args struct {
		sgID        string
		permissions []IPPermissionInfo
	}
	port443 := int32(443)
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr error
	}{
		{
			name: "revokes ingress and clears cache",
			fields: fields{
				revokeCalls: []revokeSGIngressCall{
					{resp: &ec2sdk.RevokeSecurityGroupIngressOutput{}},
				},
			},
			args: args{
				sgID: "sg-aaa",
				permissions: []IPPermissionInfo{
					NewCIDRIPPermission("tcp", &port443, &port443, "0.0.0.0/0", NewIPPermissionLabelsForRawDescription("public")),
				},
			},
		},
		{
			name: "returns error when EC2 fails",
			fields: fields{
				revokeCalls: []revokeSGIngressCall{
					{err: errors.New("revoke failed")},
				},
			},
			args: args{
				sgID: "sg-aaa",
				permissions: []IPPermissionInfo{
					NewCIDRIPPermission("tcp", &port443, &port443, "0.0.0.0/0", NewIPPermissionLabelsForRawDescription("public")),
				},
			},
			wantErr: errors.New("revoke failed"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mgr, ec2Client := newTestSGManager(ctrl)
			mgr.sgInfoCache.Set(tt.args.sgID, SecurityGroupInfo{SecurityGroupID: tt.args.sgID}, mgr.sgInfoCacheTTL)
			for _, call := range tt.fields.revokeCalls {
				ec2Client.EXPECT().RevokeSecurityGroupIngressWithContext(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			err := mgr.RevokeSGIngress(context.Background(), tt.args.sgID, tt.args.permissions)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				_, exists := mgr.sgInfoCache.Get(tt.args.sgID)
				assert.False(t, exists, "cache should be cleared after revoke")
			}
		})
	}
}

func Test_buildSDKIPPermissions(t *testing.T) {
	port80 := int32(80)
	tests := []struct {
		name        string
		permissions []IPPermissionInfo
		wantLen     int
	}{
		{
			name:        "nil input returns nil",
			permissions: nil,
			wantLen:     0,
		},
		{
			name:        "empty input returns nil",
			permissions: []IPPermissionInfo{},
			wantLen:     0,
		},
		{
			name: "converts permissions to SDK format",
			permissions: []IPPermissionInfo{
				NewCIDRIPPermission("tcp", &port80, &port80, "10.0.0.0/8", NewIPPermissionLabelsForRawDescription("internal")),
				NewCIDRIPPermission("tcp", &port80, &port80, "172.16.0.0/12", NewIPPermissionLabelsForRawDescription("private")),
			},
			wantLen: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSDKIPPermissions(tt.permissions)
			if tt.wantLen == 0 {
				assert.Nil(t, got)
			} else {
				assert.Len(t, got, tt.wantLen)
			}
		})
	}
}
