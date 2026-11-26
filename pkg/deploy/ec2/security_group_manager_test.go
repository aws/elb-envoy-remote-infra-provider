// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ec2

import (
	"context"
	"errors"
	"fmt"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	ec2model "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
	"github.com/aws/smithy-go"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func Test_defaultSecurityGroupManager_Create(t *testing.T) {
	type createSGWithContextCall struct {
		resp *ec2sdk.CreateSecurityGroupOutput
		err  error
	}
	type reconcileIngressCall struct {
		sgID string
		err  error
	}

	type fields struct {
		createSGWithContextCalls []createSGWithContextCall
		reconcileIngressCalls    []reconcileIngressCall
	}
	type args struct {
		spec ec2model.SecurityGroupSpec
	}

	port80 := int32(80)

	tests := []struct {
		name       string
		fields     fields
		args       args
		wantStatus ec2model.SecurityGroupStatus
		wantErr    error
		verify     func(t *testing.T, input *ec2sdk.CreateSecurityGroupInput)
	}{
		{
			name: "passes correct fields to CreateSecurityGroup",
			fields: fields{
				createSGWithContextCalls: []createSGWithContextCall{
					{resp: &ec2sdk.CreateSecurityGroupOutput{GroupId: awssdk.String("sg-12345")}},
				},
				reconcileIngressCalls: []reconcileIngressCall{
					{sgID: "sg-12345"},
				},
			},
			args: args{
				spec: ec2model.SecurityGroupSpec{
					GroupName:   "my-sg",
					Description: "my security group",
					Tags:        map[string]string{"env": "test"},
				},
			},
			wantStatus: ec2model.SecurityGroupStatus{GroupID: "sg-12345"},
			verify: func(t *testing.T, input *ec2sdk.CreateSecurityGroupInput) {
				assert.Equal(t, awssdk.String("vpc-abc123"), input.VpcId)
				assert.Equal(t, awssdk.String("my-sg"), input.GroupName)
				assert.Equal(t, awssdk.String("my security group"), input.Description)
				assert.Len(t, input.TagSpecifications, 1)
				assert.Equal(t, ec2types.ResourceTypeSecurityGroup, input.TagSpecifications[0].ResourceType)
			},
		},
		{
			name: "includes tracking provider tags",
			fields: fields{
				createSGWithContextCalls: []createSGWithContextCall{
					{resp: &ec2sdk.CreateSecurityGroupOutput{GroupId: awssdk.String("sg-12345")}},
				},
				reconcileIngressCalls: []reconcileIngressCall{
					{sgID: "sg-12345"},
				},
			},
			args: args{
				spec: ec2model.SecurityGroupSpec{
					GroupName:   "my-sg",
					Description: "desc",
					Tags:        map[string]string{"custom-key": "custom-value"},
				},
			},
			wantStatus: ec2model.SecurityGroupStatus{GroupID: "sg-12345"},
			verify: func(t *testing.T, input *ec2sdk.CreateSecurityGroupInput) {
				tagMap := make(map[string]string)
				for _, tag := range input.TagSpecifications[0].Tags {
					tagMap[awssdk.ToString(tag.Key)] = awssdk.ToString(tag.Value)
				}
				assert.Equal(t, "custom-value", tagMap["custom-key"])
				assert.Equal(t, "elb-envoy-remote-infra-provider", tagMap[tracking.TagKeyManagedBy])
			},
		},
		{
			name: "reconciles ingress with correct SG ID",
			fields: fields{
				createSGWithContextCalls: []createSGWithContextCall{
					{resp: &ec2sdk.CreateSecurityGroupOutput{GroupId: awssdk.String("sg-99999")}},
				},
				reconcileIngressCalls: []reconcileIngressCall{
					{sgID: "sg-99999"},
				},
			},
			args: args{
				spec: ec2model.SecurityGroupSpec{
					GroupName:   "my-sg",
					Description: "desc",
					Ingress: []ec2model.IPPermission{
						{
							IPProtocol: "tcp",
							FromPort:   &port80,
							ToPort:     &port80,
							IPRanges:   []ec2model.IPRange{{CIDRIP: "10.0.0.0/8", Description: "internal"}},
						},
					},
				},
			},
			wantStatus: ec2model.SecurityGroupStatus{GroupID: "sg-99999"},
		},
		{
			name: "returns error when EC2 CreateSecurityGroup fails",
			fields: fields{
				createSGWithContextCalls: []createSGWithContextCall{
					{err: errors.New("ec2 api error")},
				},
			},
			args: args{
				spec: ec2model.SecurityGroupSpec{
					GroupName:   "my-sg",
					Description: "desc",
				},
			},
			wantErr: errors.New("ec2 api error"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mgr, ec2Client, _, sgReconciler := newTestManager(ctrl, "vpc-abc123")

			for _, call := range tt.fields.createSGWithContextCalls {
				ec2Client.EXPECT().CreateSecurityGroupWithContext(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, input *ec2sdk.CreateSecurityGroupInput) (*ec2sdk.CreateSecurityGroupOutput, error) {
						if tt.verify != nil {
							tt.verify(t, input)
						}
						return call.resp, call.err
					})
			}
			for _, call := range tt.fields.reconcileIngressCalls {
				sgReconciler.EXPECT().ReconcileIngress(gomock.Any(), call.sgID, gomock.Any()).Return(call.err)
			}

			got, err := mgr.Create(context.Background(), newTestSG(t, tt.args.spec))
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantStatus, got)
			}
		})
	}
}

func Test_defaultSecurityGroupManager_Update(t *testing.T) {
	type reconcileTagsCall struct {
		sgID string
		err  error
	}
	type reconcileIngressCall struct {
		sgID string
		err  error
	}

	type fields struct {
		reconcileTagsCalls    []reconcileTagsCall
		reconcileIngressCalls []reconcileIngressCall
	}
	type args struct {
		spec  ec2model.SecurityGroupSpec
		sdkSG networking.SecurityGroupInfo
	}

	port443 := int32(443)

	tests := []struct {
		name       string
		fields     fields
		args       args
		wantStatus ec2model.SecurityGroupStatus
		wantErr    error
	}{
		{
			name: "reconciles tags and ingress",
			fields: fields{
				reconcileTagsCalls:    []reconcileTagsCall{{sgID: "sg-existing"}},
				reconcileIngressCalls: []reconcileIngressCall{{sgID: "sg-existing"}},
			},
			args: args{
				spec: ec2model.SecurityGroupSpec{
					GroupName:   "my-sg",
					Description: "desc",
					Tags:        map[string]string{"env": "prod"},
				},
				sdkSG: networking.SecurityGroupInfo{
					SecurityGroupID: "sg-existing",
					Tags:            map[string]string{"old-tag": "old-value"},
				},
			},
			wantStatus: ec2model.SecurityGroupStatus{GroupID: "sg-existing"},
		},
		{
			name: "reconciles ingress with correct SG ID and permissions",
			fields: fields{
				reconcileTagsCalls:    []reconcileTagsCall{{sgID: "sg-update-test"}},
				reconcileIngressCalls: []reconcileIngressCall{{sgID: "sg-update-test"}},
			},
			args: args{
				spec: ec2model.SecurityGroupSpec{
					GroupName:   "my-sg",
					Description: "desc",
					Ingress: []ec2model.IPPermission{
						{
							IPProtocol: "tcp",
							FromPort:   &port443,
							ToPort:     &port443,
							IPRanges:   []ec2model.IPRange{{CIDRIP: "0.0.0.0/0", Description: "public"}},
						},
					},
				},
				sdkSG: networking.SecurityGroupInfo{SecurityGroupID: "sg-update-test"},
			},
			wantStatus: ec2model.SecurityGroupStatus{GroupID: "sg-update-test"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mgr, _, taggingMgr, sgReconciler := newTestManager(ctrl, "vpc-abc123")

			for _, call := range tt.fields.reconcileTagsCalls {
				taggingMgr.EXPECT().ReconcileTags(gomock.Any(), call.sgID, gomock.Any(), gomock.Any()).Return(call.err)
			}
			for _, call := range tt.fields.reconcileIngressCalls {
				sgReconciler.EXPECT().ReconcileIngress(gomock.Any(), call.sgID, gomock.Any()).Return(call.err)
			}

			got, err := mgr.Update(context.Background(), newTestSG(t, tt.args.spec), tt.args.sdkSG)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantStatus, got)
			}
		})
	}
}

func Test_defaultSecurityGroupManager_Delete(t *testing.T) {
	type deleteSGWithContextCall struct {
		resp *ec2sdk.DeleteSecurityGroupOutput
		err  error
	}

	type fields struct {
		deleteSGWithContextCalls []deleteSGWithContextCall
	}
	type args struct {
		sdkSG networking.SecurityGroupInfo
	}

	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr error
		verify  func(t *testing.T, input *ec2sdk.DeleteSecurityGroupInput)
	}{
		{
			name: "passes correct group ID and succeeds",
			fields: fields{
				deleteSGWithContextCalls: []deleteSGWithContextCall{
					{resp: &ec2sdk.DeleteSecurityGroupOutput{}},
				},
			},
			args: args{
				sdkSG: networking.SecurityGroupInfo{SecurityGroupID: "sg-to-delete"},
			},
			verify: func(t *testing.T, input *ec2sdk.DeleteSecurityGroupInput) {
				assert.Equal(t, awssdk.String("sg-to-delete"), input.GroupId)
			},
		},
		{
			name: "returns error on persistent failure",
			fields: fields{
				deleteSGWithContextCalls: []deleteSGWithContextCall{
					{err: errors.New("permanent error")},
				},
			},
			args: args{
				sdkSG: networking.SecurityGroupInfo{SecurityGroupID: "sg-fail"},
			},
			wantErr: fmt.Errorf("failed to delete securityGroup: %w", errors.New("permanent error")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mgr, ec2Client, _, _ := newTestManager(ctrl, "vpc-abc123")

			for _, call := range tt.fields.deleteSGWithContextCalls {
				ec2Client.EXPECT().DeleteSecurityGroupWithContext(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, input *ec2sdk.DeleteSecurityGroupInput) (*ec2sdk.DeleteSecurityGroupOutput, error) {
						if tt.verify != nil {
							tt.verify(t, input)
						}
						return call.resp, call.err
					}).AnyTimes()
			}

			err := mgr.Delete(context.Background(), tt.args.sdkSG)
			if tt.wantErr != nil {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "failed to delete securityGroup")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_isSecurityGroupDependencyViolationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "is DependencyViolation error",
			err:  &smithy.GenericAPIError{Code: "DependencyViolation", Message: "some message"},
			want: true,
		},
		{
			name: "wraps DependencyViolation error",
			err:  fmt.Errorf("wrapped: %w", &smithy.GenericAPIError{Code: "DependencyViolation", Message: "some message"}),
			want: true,
		},
		{
			name: "isn't DependencyViolation error",
			err:  errors.New("some other error"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSecurityGroupDependencyViolationError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func newTestManager(ctrl *gomock.Controller, vpcID string) (*defaultSecurityGroupManager, *services.MockEC2, *MockTaggingManager, *networking.MockSecurityGroupReconciler) {
	ec2Client := services.NewMockEC2(ctrl)
	taggingMgr := NewMockTaggingManager(ctrl)
	sgReconciler := networking.NewMockSecurityGroupReconciler(ctrl)

	mgr := &defaultSecurityGroupManager{
		ec2Client:              ec2Client,
		taggingManager:         taggingMgr,
		trackingProvider:       tracking.NewProvider("test-cluster"),
		networkingSGReconciler: sgReconciler,
		vpcID:                  vpcID,
		logger:                 newTestLogger(),
	}
	return mgr, ec2Client, taggingMgr, sgReconciler
}

func newTestLogger() logging.Logger {
	return logging.Logger{Logger: logr.New(&log.NullLogSink{})}
}

func newTestSG(t *testing.T, spec ec2model.SecurityGroupSpec) *ec2model.SecurityGroup {
	t.Helper()
	stack := core.NewDefaultStack(core.StackID{Namespace: "ns", Name: "gw"})
	sg, err := ec2model.NewSecurityGroup(stack, "test-sg", spec)
	if err != nil {
		t.Fatalf("newTestSG: %v", err)
	}
	return sg
}
