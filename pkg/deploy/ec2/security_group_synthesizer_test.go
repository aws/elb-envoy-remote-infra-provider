// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ec2

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"

	ec2model "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

func Test_defaultSecurityGroupSynthesizer_Synthesize(t *testing.T) {
	type listSGsCall struct {
		resp []networking.SecurityGroupInfo
		err  error
	}
	type createCall struct {
		resp ec2model.SecurityGroupStatus
		err  error
	}
	type updateCall struct {
		resp ec2model.SecurityGroupStatus
		err  error
	}
	type fields struct {
		listSGsCalls []listSGsCall
		createCalls  []createCall
		updateCalls  []updateCall
	}

	resourceIDTagKey := tracking.TagKeyResourceType

	tests := []struct {
		name       string
		fields     fields
		setupStack func(t *testing.T, stack core.Stack)
		wantErr    error
	}{
		{
			name: "creates new SG when no existing SDK SGs",
			fields: fields{
				listSGsCalls: []listSGsCall{{resp: nil}},
				createCalls:  []createCall{{resp: ec2model.SecurityGroupStatus{GroupID: "sg-new"}}},
			},
			setupStack: func(t *testing.T, stack core.Stack) {
				newTestSecurityGroup(t, stack, "test-sg", ec2model.SecurityGroupSpec{GroupName: "my-sg", Description: "desc"})
			},
		},
		{
			name: "updates existing SG when matched by resource ID tag",
			fields: fields{
				listSGsCalls: []listSGsCall{{resp: []networking.SecurityGroupInfo{
					{SecurityGroupID: "sg-existing", Tags: map[string]string{resourceIDTagKey: "test-sg"}},
				}}},
				updateCalls: []updateCall{{resp: ec2model.SecurityGroupStatus{GroupID: "sg-existing"}}},
			},
			setupStack: func(t *testing.T, stack core.Stack) {
				newTestSecurityGroup(t, stack, "test-sg", ec2model.SecurityGroupSpec{GroupName: "my-sg", Description: "desc"})
			},
		},
		{
			name: "returns error when ListSecurityGroups fails",
			fields: fields{
				listSGsCalls: []listSGsCall{{err: errors.New("list failed")}},
			},
			setupStack: func(t *testing.T, stack core.Stack) {
				newTestSecurityGroup(t, stack, "test-sg", ec2model.SecurityGroupSpec{GroupName: "my-sg", Description: "desc"})
			},
			wantErr: errors.New("list failed"),
		},
		{
			name: "returns error when Create fails",
			fields: fields{
				listSGsCalls: []listSGsCall{{resp: nil}},
				createCalls:  []createCall{{err: errors.New("create failed")}},
			},
			setupStack: func(t *testing.T, stack core.Stack) {
				newTestSecurityGroup(t, stack, "test-sg", ec2model.SecurityGroupSpec{GroupName: "my-sg", Description: "desc"})
			},
			wantErr: errors.New("create failed"),
		},
		{
			name: "returns error when Update fails",
			fields: fields{
				listSGsCalls: []listSGsCall{{resp: []networking.SecurityGroupInfo{
					{SecurityGroupID: "sg-existing", Tags: map[string]string{resourceIDTagKey: "test-sg"}},
				}}},
				updateCalls: []updateCall{{err: errors.New("update failed")}},
			},
			setupStack: func(t *testing.T, stack core.Stack) {
				newTestSecurityGroup(t, stack, "test-sg", ec2model.SecurityGroupSpec{GroupName: "my-sg", Description: "desc"})
			},
			wantErr: errors.New("update failed"),
		},
		{
			name: "no-op when stack has no SG resources and no SDK SGs",
			fields: fields{
				listSGsCalls: []listSGsCall{{resp: nil}},
			},
			setupStack: func(t *testing.T, stack core.Stack) {},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			taggingMgr := NewMockTaggingManager(ctrl)
			sgMgr := NewMockSecurityGroupManager(ctrl)
			tp := tracking.NewProvider("test-cluster")
			stack := newTestStack()
			tt.setupStack(t, stack)

			for _, call := range tt.fields.listSGsCalls {
				taggingMgr.EXPECT().ListSecurityGroups(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			for _, call := range tt.fields.createCalls {
				sgMgr.EXPECT().Create(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			for _, call := range tt.fields.updateCalls {
				sgMgr.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}

			synth := NewSecurityGroupSynthesizer(tp, taggingMgr, sgMgr, newTestLogger(), stack)
			err := synth.Synthesize(context.Background())
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_defaultSecurityGroupSynthesizer_PostSynthesize(t *testing.T) {
	type deleteCall struct {
		sgID string
		err  error
	}
	type fields struct {
		unmatchedSDKSGs []networking.SecurityGroupInfo
		deleteCalls     []deleteCall
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "deletes unmatched SDK SGs",
			fields: fields{
				unmatchedSDKSGs: []networking.SecurityGroupInfo{
					{SecurityGroupID: "sg-orphan-1"},
					{SecurityGroupID: "sg-orphan-2"},
				},
				deleteCalls: []deleteCall{
					{sgID: "sg-orphan-1"},
					{sgID: "sg-orphan-2"},
				},
			},
		},
		{
			name:   "no-op when no unmatched SDK SGs",
			fields: fields{unmatchedSDKSGs: nil},
		},
		{
			name: "returns error on delete failure to trigger requeue",
			fields: fields{
				unmatchedSDKSGs: []networking.SecurityGroupInfo{
					{SecurityGroupID: "sg-fail"},
				},
				deleteCalls: []deleteCall{
					{sgID: "sg-fail", err: errors.New("delete failed")},
				},
			},
			wantErr: true,
		},
		{
			name: "continues deleting remaining SGs when one fails and returns aggregate error",
			fields: fields{
				unmatchedSDKSGs: []networking.SecurityGroupInfo{
					{SecurityGroupID: "sg-ok-1"},
					{SecurityGroupID: "sg-fail"},
					{SecurityGroupID: "sg-ok-2"},
				},
				deleteCalls: []deleteCall{
					{sgID: "sg-ok-1"},
					{sgID: "sg-fail", err: errors.New("delete failed")},
					{sgID: "sg-ok-2"},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			sgMgr := NewMockSecurityGroupManager(ctrl)
			for _, call := range tt.fields.deleteCalls {
				sgMgr.EXPECT().Delete(gomock.Any(), networking.SecurityGroupInfo{SecurityGroupID: call.sgID}).Return(call.err)
			}

			synth := &defaultSecurityGroupSynthesizer{
				sgManager:       sgMgr,
				logger:          newTestLogger(),
				unmatchedSDKSGs: tt.fields.unmatchedSDKSGs,
			}
			err := synth.PostSynthesize(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_matchResAndSDKSecurityGroups(t *testing.T) {
	resourceIDTagKey := tracking.TagKeyResourceType

	type args struct {
		resSGs           []*ec2model.SecurityGroup
		sdkSGs           []networking.SecurityGroupInfo
		resourceIDTagKey string
	}
	tests := []struct {
		name             string
		args             args
		wantMatchedCount int
		wantUnmatchedRes int
		wantUnmatchedSDK int
		wantErr          error
	}{
		{
			name: "all matched",
			args: args{
				resSGs: func() []*ec2model.SecurityGroup {
					stack := newTestStack()
					return []*ec2model.SecurityGroup{newTestSecurityGroup(t, stack, "sg-a", ec2model.SecurityGroupSpec{GroupName: "a"})}
				}(),
				sdkSGs:           []networking.SecurityGroupInfo{{SecurityGroupID: "sg-111", Tags: map[string]string{resourceIDTagKey: "sg-a"}}},
				resourceIDTagKey: resourceIDTagKey,
			},
			wantMatchedCount: 1,
		},
		{
			name: "unmatched desired resource",
			args: args{
				resSGs: func() []*ec2model.SecurityGroup {
					stack := newTestStack()
					return []*ec2model.SecurityGroup{newTestSecurityGroup(t, stack, "sg-a", ec2model.SecurityGroupSpec{GroupName: "a"})}
				}(),
				resourceIDTagKey: resourceIDTagKey,
			},
			wantUnmatchedRes: 1,
		},
		{
			name: "unmatched SDK SG",
			args: args{
				sdkSGs:           []networking.SecurityGroupInfo{{SecurityGroupID: "sg-111", Tags: map[string]string{resourceIDTagKey: "sg-orphan"}}},
				resourceIDTagKey: resourceIDTagKey,
			},
			wantUnmatchedSDK: 1,
		},
		{
			name: "duplicate SDK SGs for same resource ID - first matches, rest unmatched",
			args: args{
				resSGs: func() []*ec2model.SecurityGroup {
					stack := newTestStack()
					return []*ec2model.SecurityGroup{newTestSecurityGroup(t, stack, "sg-a", ec2model.SecurityGroupSpec{GroupName: "a"})}
				}(),
				sdkSGs: []networking.SecurityGroupInfo{
					{SecurityGroupID: "sg-111", Tags: map[string]string{resourceIDTagKey: "sg-a"}},
					{SecurityGroupID: "sg-222", Tags: map[string]string{resourceIDTagKey: "sg-a"}},
				},
				resourceIDTagKey: resourceIDTagKey,
			},
			wantMatchedCount: 1,
			wantUnmatchedSDK: 1,
		},
		{
			name: "error when SDK SG missing resource ID tag",
			args: args{
				sdkSGs:           []networking.SecurityGroupInfo{{SecurityGroupID: "sg-no-tag", Tags: map[string]string{}}},
				resourceIDTagKey: resourceIDTagKey,
			},
			wantErr: errors.New("unexpected securityGroup with no resourceID: sg-no-tag"),
		},
		{
			name: "empty inputs",
			args: args{resourceIDTagKey: resourceIDTagKey},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, unmatchedRes, unmatchedSDK, err := matchResAndSDKSecurityGroups(
				tt.args.resSGs, tt.args.sdkSGs, tt.args.resourceIDTagKey,
			)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				assert.Len(t, matched, tt.wantMatchedCount)
				assert.Len(t, unmatchedRes, tt.wantUnmatchedRes)
				assert.Len(t, unmatchedSDK, tt.wantUnmatchedSDK)
			}
		})
	}
}

func newTestStack() core.Stack {
	return core.NewDefaultStack(core.StackID{Namespace: "ns", Name: "gw"})
}

func newTestSecurityGroup(t *testing.T, stack core.Stack, id string, spec ec2model.SecurityGroupSpec) *ec2model.SecurityGroup {
	t.Helper()
	sg, err := ec2model.NewSecurityGroup(stack, id, spec)
	if err != nil {
		t.Fatalf("mustNewSecurityGroup: %v", err)
	}
	return sg
}

// Test_defaultSecurityGroupSynthesizer_PreservesWhenIRAbsent asserts that with an
// IR-absent stack the synthesizer is a complete no-op: it must not list, create,
// update, or queue any SG for deletion. The mocks have no expectations, so any AWS
// call fails the test. This enforces the never-revert invariant for the frontend SG.
func Test_defaultSecurityGroupSynthesizer_PreservesWhenIRAbsent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	taggingMgr := NewMockTaggingManager(ctrl)
	sgMgr := NewMockSecurityGroupManager(ctrl)
	tp := tracking.NewProvider("test-cluster")

	// IR-absent stack.
	stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: "ns", Name: "gw"}, false)

	synth := NewSecurityGroupSynthesizer(tp, taggingMgr, sgMgr, newTestLogger(), stack)
	err := synth.Synthesize(context.Background())
	assert.NoError(t, err)

	// PostSynthesize must also be a no-op (no unmatched SGs were queued).
	err = synth.PostSynthesize(context.Background())
	assert.NoError(t, err)
}
