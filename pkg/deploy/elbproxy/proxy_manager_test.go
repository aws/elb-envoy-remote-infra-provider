// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"
	"testing"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

func newProxyManager(t *testing.T) (*defaultProxyManager, *elbproxyaws.MockELBProxyClient, *MockTaggingManager) {
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })
	mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
	mockTaggingMgr := NewMockTaggingManager(ctrl)
	mockAttrReconciler := NewMockProxyAttributeReconciler(ctrl)
	mockNetworkReconciler := NewMockProxyNetworkConfigReconciler(ctrl)
	mockConfigReconciler := NewMockProxyConfigurationReconciler(ctrl)
	tp := tracking.NewProvider("test-cluster")
	return NewDefaultProxyManager(mockClient, mockTaggingMgr, tp, mockAttrReconciler, mockNetworkReconciler, mockConfigReconciler, nil, testLogger()), mockClient, mockTaggingMgr
}

func newResProxy(t *testing.T, stack core.Stack, id, name string) *elbproxymodel.ProxyResource {
	res, err := elbproxymodel.NewProxyResource(stack, id)
	assert.NoError(t, err)
	res.Name = name
	return res
}

func Test_defaultProxyManager_Create(t *testing.T) {
	tests := []struct {
		name      string
		resProxy  *elbproxymodel.ProxyResource
		setupMock func(t *testing.T, client *elbproxyaws.MockELBProxyClient)
		wantArn   string
		wantDns   string
		wantErr   string
	}{
		{
			name: "creates proxy with tags",
			setupMock: func(t *testing.T, client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().CreateProxy(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input elbproxyaws.CreateProxyInput) (*elbproxyaws.ProxyDetails, error) {
						assert.NotEmpty(t, input.Tags)
						assert.Equal(t, "test-cluster", input.Tags[tracking.TagKeyCluster])
						assert.Equal(t, "default/my-gw", input.Tags[tracking.TagKeyStack])
						assert.Equal(t, tracking.TagValueManagedBy, input.Tags[tracking.TagKeyManagedBy])
						return &elbproxyaws.ProxyDetails{
							Arn: "arn:proxy-1", DnsName: "proxy-1.dns", Name: "my-proxy",
						}, nil
					})
			},
			wantArn: "arn:proxy-1",
			wantDns: "proxy-1.dns",
		},
		{
			name:    "nil proxy returns error",
			wantErr: "resProxy is nil",
		},
		{
			name: "API error propagates",
			setupMock: func(t *testing.T, client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().CreateProxy(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("api error"))
			},
			wantErr: "failed to create proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, mockClient, _ := newProxyManager(t)
			stack := newTestStack("default", "my-gw")

			var resProxy *elbproxymodel.ProxyResource
			if tt.wantErr != "resProxy is nil" {
				resProxy = newResProxy(t, stack, "my-proxy", "my-proxy")
				resProxy.ProxyType = "envoy"
				resProxy.RoleArn = "arn:aws:iam::123456789012:role/test"
				resProxy.ProxyVersion = "1.0.0"
			}
			if tt.setupMock != nil {
				tt.setupMock(t, mockClient)
			}

			status, sdkProxy, err := mgr.Create(context.Background(), resProxy)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantArn, status.Arn)
				assert.Equal(t, tt.wantDns, status.DnsName)
				assert.Equal(t, tt.wantArn, sdkProxy.ProxyDetails.Arn)
				assert.NotEmpty(t, sdkProxy.Tags)
			}
		})
	}
}

func Test_defaultProxyManager_Create_RoleArn(t *testing.T) {
	mgr, mockClient, _ := newProxyManager(t)
	resProxy := newResProxy(t, newTestStack("default", "my-gw"), "my-proxy", "my-proxy")
	resProxy.ProxyType = "envoy"
	resProxy.RoleArn = "arn:aws:iam::123456789012:role/proxy-code"
	resProxy.ProxyVersion = "1.37"

	mockClient.EXPECT().CreateProxy(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, input elbproxyaws.CreateProxyInput) (*elbproxyaws.ProxyDetails, error) {
			assert.Equal(t, "arn:aws:iam::123456789012:role/proxy-code", input.RoleArn)
			return &elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", Name: "my-proxy"}, nil
		})

	_, _, err := mgr.Create(context.Background(), resProxy)
	assert.NoError(t, err)
}

func Test_defaultProxyManager_Update(t *testing.T) {
	tests := []struct {
		name     string
		setupMgr func(t *testing.T) *defaultProxyManager
		wantArn  string
		wantDns  string
		wantErr  string
	}{
		{
			name: "reconciles tags and returns status",
			setupMgr: func(t *testing.T) *defaultProxyManager {
				ctrl := gomock.NewController(t)
				t.Cleanup(func() { ctrl.Finish() })
				mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
				mockTaggingMgr := NewMockTaggingManager(ctrl)
				mockAttrReconciler := NewMockProxyAttributeReconciler(ctrl)
				mockNetworkReconciler := NewMockProxyNetworkConfigReconciler(ctrl)
				mockTaggingMgr.EXPECT().ReconcileTags(gomock.Any(), "arn:proxy-1", gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, _ string, desiredTags map[string]string, _ ...ReconcileTagsOption) error {
						assert.Equal(t, "test-cluster", desiredTags[tracking.TagKeyCluster])
						assert.Equal(t, "default/my-gw", desiredTags[tracking.TagKeyStack])
						return nil
					})
				mockAttrReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
				mockNetworkReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
				tp := tracking.NewProvider("test-cluster")
				mockConfigReconciler := NewMockProxyConfigurationReconciler(ctrl)
				mockConfigReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
				return NewDefaultProxyManager(mockClient, mockTaggingMgr, tp, mockAttrReconciler, mockNetworkReconciler, mockConfigReconciler, nil, testLogger())
			},
			wantArn: "arn:proxy-1",
			wantDns: "proxy-1.dns",
		},
		{
			name: "tag reconcile error propagates",
			setupMgr: func(t *testing.T) *defaultProxyManager {
				ctrl := gomock.NewController(t)
				t.Cleanup(func() { ctrl.Finish() })
				mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
				mockTaggingMgr := NewMockTaggingManager(ctrl)
				mockAttrReconciler := NewMockProxyAttributeReconciler(ctrl)
				mockNetworkReconciler := NewMockProxyNetworkConfigReconciler(ctrl)
				mockTaggingMgr.EXPECT().ReconcileTags(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("reconcile failed"))
				tp := tracking.NewProvider("test-cluster")
				return NewDefaultProxyManager(mockClient, mockTaggingMgr, tp, mockAttrReconciler, mockNetworkReconciler, NewMockProxyConfigurationReconciler(ctrl), nil, testLogger())
			},
			wantErr: "failed to reconcile tags",
		},
		{
			name: "attribute reconciler error propagates",
			setupMgr: func(t *testing.T) *defaultProxyManager {
				ctrl := gomock.NewController(t)
				t.Cleanup(func() { ctrl.Finish() })
				mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
				mockTaggingMgr := NewMockTaggingManager(ctrl)
				mockAttrReconciler := NewMockProxyAttributeReconciler(ctrl)
				mockNetworkReconciler := NewMockProxyNetworkConfigReconciler(ctrl)
				mockTaggingMgr.EXPECT().ReconcileTags(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
				mockAttrReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("attr reconcile failed"))
				tp := tracking.NewProvider("test-cluster")
				return NewDefaultProxyManager(mockClient, mockTaggingMgr, tp, mockAttrReconciler, mockNetworkReconciler, NewMockProxyConfigurationReconciler(ctrl), nil, testLogger())
			},
			wantErr: "failed to reconcile attributes",
		},
		{
			name: "network reconciler error propagates",
			setupMgr: func(t *testing.T) *defaultProxyManager {
				ctrl := gomock.NewController(t)
				t.Cleanup(func() { ctrl.Finish() })
				mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
				mockTaggingMgr := NewMockTaggingManager(ctrl)
				mockAttrReconciler := NewMockProxyAttributeReconciler(ctrl)
				mockNetworkReconciler := NewMockProxyNetworkConfigReconciler(ctrl)
				mockTaggingMgr.EXPECT().ReconcileTags(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
				mockAttrReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
				mockNetworkReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("network reconcile failed"))
				tp := tracking.NewProvider("test-cluster")
				return NewDefaultProxyManager(mockClient, mockTaggingMgr, tp, mockAttrReconciler, mockNetworkReconciler, NewMockProxyConfigurationReconciler(ctrl), nil, testLogger())
			},
			wantErr: "failed to reconcile network configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := tt.setupMgr(t)
			stack := newTestStack("default", "my-gw")
			resProxy := newResProxy(t, stack, "my-proxy", "my-proxy")
			sdkProxy := ProxyWithTags{
				ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", DnsName: "proxy-1.dns"},
				Tags:         map[string]string{"old-key": "old-value"},
			}

			status, err := mgr.Update(context.Background(), resProxy, sdkProxy)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantArn, status.Arn)
				assert.Equal(t, tt.wantDns, status.DnsName)
			}
		})
	}
}

func Test_defaultProxyManager_Delete(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(client *elbproxyaws.MockELBProxyClient)
		wantErr   string
	}{
		{
			name: "deletes proxy successfully",
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().DeleteProxy(gomock.Any(), "arn:proxy-1").Return(nil)
			},
		},
		{
			name: "API error propagates",
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().DeleteProxy(gomock.Any(), "arn:proxy-1").Return(fmt.Errorf("delete failed"))
			},
			wantErr: "failed to delete proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, mockClient, _ := newProxyManager(t)

			sdkProxy := ProxyWithTags{
				ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", Name: "my-proxy"},
			}

			tt.setupMock(mockClient)

			err := mgr.Delete(context.Background(), sdkProxy)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_defaultProxyManager_UserTags(t *testing.T) {
	tests := []struct {
		name          string
		userTags      map[string]string
		wantUserTags  map[string]string
		wantAbsentKey string
	}{
		{
			name:         "user tags merged with system tags on create",
			userTags:     map[string]string{"team": "platform", "env": "prod"},
			wantUserTags: map[string]string{"team": "platform", "env": "prod"},
		},
		{
			name:          "nil user tags — only system tags present",
			userTags:      nil,
			wantAbsentKey: "team",
		},
		{
			name:         "single user tag",
			userTags:     map[string]string{"cost-center": "12345"},
			wantUserTags: map[string]string{"cost-center": "12345"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, mockClient, _ := newProxyManager(t)
			stack := newTestStack("default", "my-gw")
			resProxy := newResProxy(t, stack, "my-proxy", "my-proxy")
			resProxy.Tags = tt.userTags

			mockClient.EXPECT().CreateProxy(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, input elbproxyaws.CreateProxyInput) (*elbproxyaws.ProxyDetails, error) {
					// System tags always present.
					assert.Equal(t, "test-cluster", input.Tags[tracking.TagKeyCluster])
					assert.Equal(t, tracking.TagValueManagedBy, input.Tags[tracking.TagKeyManagedBy])
					// User tags merged in.
					for k, v := range tt.wantUserTags {
						assert.Equal(t, v, input.Tags[k], "expected user tag %s=%s", k, v)
					}
					// Absent keys must not appear.
					if tt.wantAbsentKey != "" {
						_, found := input.Tags[tt.wantAbsentKey]
						assert.False(t, found, "key %s should not be present", tt.wantAbsentKey)
					}
					return &elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", DnsName: "proxy-1.dns", Name: "my-proxy"}, nil
				})

			_, _, err := mgr.Create(context.Background(), resProxy)
			assert.NoError(t, err)
		})
	}
}

func Test_defaultProxyManager_UserTags_Update(t *testing.T) {
	tests := []struct {
		name          string
		userTags      map[string]string
		wantUserTags  map[string]string
		wantAbsentKey string
	}{
		{
			name:         "user tags merged with system tags on update",
			userTags:     map[string]string{"team": "platform", "env": "prod"},
			wantUserTags: map[string]string{"team": "platform", "env": "prod"},
		},
		{
			name:          "nil user tags — only system tags reconciled",
			userTags:      nil,
			wantAbsentKey: "team",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			t.Cleanup(func() { ctrl.Finish() })

			mockTaggingMgr := NewMockTaggingManager(ctrl)
			mockAttrReconciler := NewMockProxyAttributeReconciler(ctrl)
			mockNetworkReconciler := NewMockProxyNetworkConfigReconciler(ctrl)
			mockConfigReconciler := NewMockProxyConfigurationReconciler(ctrl)
			tp := tracking.NewProvider("test-cluster")

			mockTaggingMgr.EXPECT().ReconcileTags(gomock.Any(), "arn:proxy-1", gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, _ string, desiredTags map[string]string, _ ...ReconcileTagsOption) error {
					assert.Equal(t, "test-cluster", desiredTags[tracking.TagKeyCluster])
					for k, v := range tt.wantUserTags {
						assert.Equal(t, v, desiredTags[k], "expected user tag %s=%s", k, v)
					}
					if tt.wantAbsentKey != "" {
						_, found := desiredTags[tt.wantAbsentKey]
						assert.False(t, found, "key %s should not be present", tt.wantAbsentKey)
					}
					return nil
				})
			mockAttrReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			mockNetworkReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
			mockConfigReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

			mgr := NewDefaultProxyManager(elbproxyaws.NewMockELBProxyClient(ctrl), mockTaggingMgr, tp, mockAttrReconciler, mockNetworkReconciler, mockConfigReconciler, nil, testLogger())

			stack := newTestStack("default", "my-gw")
			resProxy := newResProxy(t, stack, "my-proxy", "my-proxy")
			resProxy.Tags = tt.userTags
			sdkProxy := ProxyWithTags{
				ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", DnsName: "proxy-1.dns"},
			}

			_, err := mgr.Update(context.Background(), resProxy, sdkProxy)
			assert.NoError(t, err)
		})
	}
}

func Test_defaultProxyManager_ExternalManagedTags(t *testing.T) {
	tests := []struct {
		name                string
		externalManagedTags []string
		currentTags         map[string]string
		wantIgnoredKeys     []string
	}{
		{
			name:                "external managed tags are ignored during reconciliation",
			externalManagedTags: []string{"CostCenter", "Team"},
			currentTags: map[string]string{
				"CostCenter": "12345",
				"Team":       "platform",
				"old-key":    "old-value",
			},
			wantIgnoredKeys: []string{"CostCenter", "Team"},
		},
		{
			name:                "nil external managed tags — no keys ignored",
			externalManagedTags: nil,
			currentTags:         map[string]string{"old-key": "old-value"},
			wantIgnoredKeys:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			t.Cleanup(func() { ctrl.Finish() })

			mockTaggingMgr := NewMockTaggingManager(ctrl)
			mockAttrReconciler := NewMockProxyAttributeReconciler(ctrl)
			mockNetworkReconciler := NewMockProxyNetworkConfigReconciler(ctrl)
			mockConfigReconciler := NewMockProxyConfigurationReconciler(ctrl)
			tp := tracking.NewProvider("test-cluster")

			mockTaggingMgr.EXPECT().ReconcileTags(gomock.Any(), "arn:proxy-1", gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, _ string, _ map[string]string, opts ...ReconcileTagsOption) error {
					reconcileOpts := ReconcileTagsOptions{}
					reconcileOpts.ApplyOptions(opts)
					assert.ElementsMatch(t, tt.wantIgnoredKeys, reconcileOpts.IgnoredTagKeys)
					return nil
				})
			mockAttrReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			mockNetworkReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
			mockConfigReconciler.EXPECT().Reconcile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

			mgr := NewDefaultProxyManager(elbproxyaws.NewMockELBProxyClient(ctrl), mockTaggingMgr, tp, mockAttrReconciler, mockNetworkReconciler, mockConfigReconciler, tt.externalManagedTags, testLogger())

			stack := newTestStack("default", "my-gw")
			resProxy := newResProxy(t, stack, "my-proxy", "my-proxy")
			sdkProxy := ProxyWithTags{
				ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", DnsName: "proxy-1.dns"},
				Tags:         tt.currentTags,
			}

			_, err := mgr.Update(context.Background(), resProxy, sdkProxy)
			assert.NoError(t, err)
		})
	}
}
