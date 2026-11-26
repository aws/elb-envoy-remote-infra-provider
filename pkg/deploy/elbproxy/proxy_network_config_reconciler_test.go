// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"
	"testing"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

func Test_proxyNetworkConfigReconciler_Reconcile(t *testing.T) {
	type updateProxyNetworkConfigurationCall struct {
		req  elbproxyaws.UpdateProxyNetworkConfigurationInput
		resp *elbproxyaws.ProxyDetails
		err  error
	}
	type fields struct {
		updateProxyNetworkConfigurationCalls []updateProxyNetworkConfigurationCall
	}

	tests := []struct {
		name           string
		desiredNetwork elbproxyaws.NetworkUserConfiguration
		currentNetwork elbproxyaws.NetworkConfiguration
		fields         fields
		wantDetails    bool
		wantErr        string
	}{
		{
			name: "subnets and security groups match results in no update",
			desiredNetwork: elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{
					Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}, {SubnetId: "subnet-2"}},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-1"}},
				},
			},
			currentNetwork: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{
						{SubnetId: "subnet-1", ZoneId: "us-east-1a"},
						{SubnetId: "subnet-2", ZoneId: "us-east-1b"},
					},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-1"}},
				},
			},
		},
		{
			name: "desired subnets differ from current triggers subnet update",
			desiredNetwork: elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{
					Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-new-1"}, {SubnetId: "subnet-new-2"}},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-1"}},
				},
			},
			currentNetwork: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{
						{SubnetId: "subnet-old-1", ZoneId: "us-east-1a"},
					},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-1"}},
				},
			},
			fields: fields{
				updateProxyNetworkConfigurationCalls: []updateProxyNetworkConfigurationCall{
					{
						req: elbproxyaws.UpdateProxyNetworkConfigurationInput{
							ProxyIdentifier: "arn:proxy-1",
							VpcConfiguration: elbproxyaws.VpcUserConfiguration{
								Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-new-1"}, {SubnetId: "subnet-new-2"}},
								SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-1"}},
							},
						},
						resp: &elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"},
					},
				},
			},
			wantDetails: true,
		},
		{
			name: "desired security groups differ from current triggers security group update",
			desiredNetwork: elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{
					Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-new-1"}, {SecurityGroupId: "sg-new-2"}},
				},
			},
			currentNetwork: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{
						{SubnetId: "subnet-1", ZoneId: "us-east-1a"},
					},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-old-1"}},
				},
			},
			fields: fields{
				updateProxyNetworkConfigurationCalls: []updateProxyNetworkConfigurationCall{
					{
						req: elbproxyaws.UpdateProxyNetworkConfigurationInput{
							ProxyIdentifier: "arn:proxy-1",
							VpcConfiguration: elbproxyaws.VpcUserConfiguration{
								SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-new-1"}, {SecurityGroupId: "sg-new-2"}},
								Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}},
							},
						},
						resp: &elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", DnsName: "updated.dns"},
					},
				},
			},
			wantDetails: true,
		},
		{
			name: "both subnets and security groups differ sends one update with both",
			desiredNetwork: elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{
					Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-new"}},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-new"}},
				},
			},
			currentNetwork: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{
						{SubnetId: "subnet-old", ZoneId: "us-east-1a"},
					},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-old"}},
				},
			},
			fields: fields{
				updateProxyNetworkConfigurationCalls: []updateProxyNetworkConfigurationCall{
					{
						req: elbproxyaws.UpdateProxyNetworkConfigurationInput{
							ProxyIdentifier: "arn:proxy-1",
							VpcConfiguration: elbproxyaws.VpcUserConfiguration{
								Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-new"}},
								SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-new"}},
							},
						},
						resp: &elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"},
					},
				},
			},
			wantDetails: true,
		},
		{
			name: "UpdateProxyNetworkConfiguration returns error on subnet update and error is propagated",
			desiredNetwork: elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{
					Subnets: []elbproxyaws.SubnetDetails{{SubnetId: "subnet-new"}},
				},
			},
			currentNetwork: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{
						{SubnetId: "subnet-old", ZoneId: "us-east-1a"},
					},
				},
			},
			fields: fields{
				updateProxyNetworkConfigurationCalls: []updateProxyNetworkConfigurationCall{
					{
						req: elbproxyaws.UpdateProxyNetworkConfigurationInput{
							ProxyIdentifier: "arn:proxy-1",
							VpcConfiguration: elbproxyaws.VpcUserConfiguration{
								Subnets: []elbproxyaws.SubnetDetails{{SubnetId: "subnet-new"}},
							},
						},
						err: fmt.Errorf("subnet update failed"),
					},
				},
			},
			wantErr: "failed to update proxy network configuration",
		},
		{
			name: "UpdateProxyNetworkConfiguration returns error on security group update and error is propagated",
			desiredNetwork: elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{
					Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-new"}},
				},
			},
			currentNetwork: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{
						{SubnetId: "subnet-1", ZoneId: "us-east-1a"},
					},
					SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-old"}},
				},
			},
			fields: fields{
				updateProxyNetworkConfigurationCalls: []updateProxyNetworkConfigurationCall{
					{
						req: elbproxyaws.UpdateProxyNetworkConfigurationInput{
							ProxyIdentifier: "arn:proxy-1",
							VpcConfiguration: elbproxyaws.VpcUserConfiguration{
								SecurityGroups: []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-new"}},
								Subnets:        []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}},
							},
						},
						err: fmt.Errorf("sg update failed"),
					},
				},
			},
			wantErr: "failed to update proxy network configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)

			for _, call := range tt.fields.updateProxyNetworkConfigurationCalls {
				mockClient.EXPECT().UpdateProxyNetworkConfiguration(gomock.Any(), call.req).
					Return(call.resp, call.err)
			}

			reconciler := NewDefaultProxyNetworkConfigReconciler(mockClient, testLogger())

			stack := core.NewDefaultStack(core.StackID{Namespace: "default", Name: "my-gw"})
			resProxy, err := elbproxymodel.NewProxyResource(stack, "proxy")
			assert.NoError(t, err)
			resProxy.NetworkConfiguration = tt.desiredNetwork

			sdkProxy := ProxyWithTags{
				ProxyDetails: elbproxyaws.ProxyDetails{
					Arn:                  "arn:proxy-1",
					NetworkConfiguration: tt.currentNetwork,
				},
			}

			details, err := reconciler.Reconcile(context.Background(), resProxy, sdkProxy)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
				if tt.wantDetails {
					assert.NotNil(t, details)
				} else {
					assert.Nil(t, details)
				}
			}
		})
	}
}

// Test_proxyNetworkConfigReconciler_PreservesSGsWhenIRAbsent asserts the
// never-revert invariant: when IR is absent (e.g. sidecar restart before EG
// replays), the reconciler must NOT call UpdateProxyNetworkConfiguration to
// clear the proxy's live SGs. The mock has no expectation, so any SG update
// call fails the test.
func Test_proxyNetworkConfigReconciler_PreservesSGsWhenIRAbsent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
	// No UpdateProxyNetworkConfiguration expectation: calling it would fail the test.

	reconciler := NewDefaultProxyNetworkConfigReconciler(mockClient, testLogger())

	stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: "default", Name: "my-gw"}, false)
	resProxy, err := elbproxymodel.NewProxyResource(stack, "proxy")
	assert.NoError(t, err)
	// Desired network has matching subnets (so subnet path is a no-op) and NO SGs
	// (as expected when IR is absent and SG build is skipped).
	resProxy.NetworkConfiguration = elbproxyaws.NetworkUserConfiguration{
		VpcConfiguration: elbproxyaws.VpcUserConfiguration{
			Subnets: []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}},
		},
	}

	sdkProxy := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{
			Arn: "arn:proxy-1",
			NetworkConfiguration: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{{SubnetId: "subnet-1", ZoneId: "us-east-1a"}},
					SecurityGroups:    []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-live"}},
				},
			},
		},
	}

	details, err := reconciler.Reconcile(context.Background(), resProxy, sdkProxy)
	assert.NoError(t, err)
	assert.Nil(t, details)
}

// With IR absent the desired SGs are empty, so a subnet change must send only the
// subnets, which keeps the live security groups.
func Test_proxyNetworkConfigReconciler_SubnetChangeOmitsSGsWhenIRAbsent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
	mockClient.EXPECT().UpdateProxyNetworkConfiguration(gomock.Any(), elbproxyaws.UpdateProxyNetworkConfigurationInput{
		ProxyIdentifier: "arn:proxy-1",
		VpcConfiguration: elbproxyaws.VpcUserConfiguration{
			Subnets: []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}, {SubnetId: "subnet-2"}},
		},
	}).Return(&elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"}, nil)

	reconciler := NewDefaultProxyNetworkConfigReconciler(mockClient, testLogger())

	stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: "default", Name: "my-gw"}, false)
	resProxy, err := elbproxymodel.NewProxyResource(stack, "proxy")
	assert.NoError(t, err)
	resProxy.NetworkConfiguration = elbproxyaws.NetworkUserConfiguration{
		VpcConfiguration: elbproxyaws.VpcUserConfiguration{
			Subnets: []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}, {SubnetId: "subnet-2"}},
		},
	}

	sdkProxy := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{
			Arn: "arn:proxy-1",
			NetworkConfiguration: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{{SubnetId: "subnet-1", ZoneId: "us-east-1a"}},
					SecurityGroups:    []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-live"}},
				},
			},
		},
	}

	_, err = reconciler.Reconcile(context.Background(), resProxy, sdkProxy)
	assert.NoError(t, err)
}

// With IR present but every security group token resolving to "", the desired SG list
// is empty. The live SGs are kept: no call when subnets match, one subnets-only call
// when they differ, and never a second call.
func Test_proxyNetworkConfigReconciler_EmptyResolvedSGsWithIRPresent(t *testing.T) {
	live := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{
			Arn: "arn:proxy-1",
			NetworkConfiguration: elbproxyaws.NetworkConfiguration{
				VpcConfiguration: elbproxyaws.VpcConfiguration{
					AvailabilityZones: []elbproxyaws.SubnetAndZone{{SubnetId: "subnet-1", ZoneId: "us-east-1a"}},
					SecurityGroups:    []elbproxyaws.SecurityGroupDetails{{SecurityGroupId: "sg-live"}},
				},
			},
		},
	}
	tests := []struct {
		name       string
		subnets    []elbproxyaws.SubnetDetails
		wantUpdate bool
	}{
		{name: "subnets match -> no call", subnets: []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}}},
		{name: "subnets differ -> one subnets-only call", subnets: []elbproxyaws.SubnetDetails{{SubnetId: "subnet-1"}, {SubnetId: "subnet-2"}}, wantUpdate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
			if tt.wantUpdate {
				mockClient.EXPECT().UpdateProxyNetworkConfiguration(gomock.Any(), elbproxyaws.UpdateProxyNetworkConfigurationInput{
					ProxyIdentifier:  "arn:proxy-1",
					VpcConfiguration: elbproxyaws.VpcUserConfiguration{Subnets: tt.subnets},
				}).Return(&elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"}, nil).Times(1)
			}

			stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: "default", Name: "my-gw"}, true)
			resProxy, err := elbproxymodel.NewProxyResource(stack, "proxy")
			assert.NoError(t, err)
			resProxy.NetworkConfiguration = elbproxyaws.NetworkUserConfiguration{
				VpcConfiguration: elbproxyaws.VpcUserConfiguration{Subnets: tt.subnets},
			}
			resProxy.SecurityGroupTokens = []core.StringToken{core.LiteralStringToken("")}

			_, err = NewDefaultProxyNetworkConfigReconciler(mockClient, testLogger()).Reconcile(context.Background(), resProxy, live)
			assert.NoError(t, err)
		})
	}
}
