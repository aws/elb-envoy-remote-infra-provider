// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/reconciler"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStack(namespace, name string) core.Stack {
	return core.NewDefaultStack(core.StackID{Namespace: namespace, Name: name})
}

func Test_proxySynthesizer_Synthesize(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")

	tests := []struct {
		name         string
		setupStack   func(stack core.Stack) *elbproxymodel.ProxyResource
		setupMock    func(resProxy *elbproxymodel.ProxyResource, taggingMgr *MockTaggingManager, proxyMgr *MockProxyManager)
		wantProxyArn string
		wantErr      bool
	}{
		{
			name: "creates new proxy when none exist",
			setupStack: func(stack core.Stack) *elbproxymodel.ProxyResource {
				res, _ := elbproxymodel.NewProxyResource(stack, "my-proxy")
				res.Name = "my-proxy"
				res.ProxyType = "envoy"
				return res
			},
			setupMock: func(resProxy *elbproxymodel.ProxyResource, taggingMgr *MockTaggingManager, proxyMgr *MockProxyManager) {
				taggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return(nil, nil)
				proxyMgr.EXPECT().Create(gomock.Any(), resProxy).Return(
					elbproxymodel.ProxyResourceStatus{Arn: "arn:proxy-1", DnsName: "proxy-1.dns"},
					ProxyWithTags{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", Name: "my-proxy"}, Tags: map[string]string{}},
					nil,
				)
			},
			wantProxyArn: "arn:proxy-1",
		},
		{
			name: "updates existing proxy matched by tags",
			setupStack: func(stack core.Stack) *elbproxymodel.ProxyResource {
				res, _ := elbproxymodel.NewProxyResource(stack, "my-proxy")
				res.Name = "my-proxy"
				return res
			},
			setupMock: func(resProxy *elbproxymodel.ProxyResource, taggingMgr *MockTaggingManager, proxyMgr *MockProxyManager) {
				sdkProxy := ProxyWithTags{
					ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", Name: "my-proxy"},
					Tags: map[string]string{
						tracking.TagKeyCluster:      "test-cluster",
						tracking.TagKeyStack:        "default/my-gw",
						tracking.TagKeyManagedBy:    tracking.TagValueManagedBy,
						tracking.TagKeyResourceType: "my-proxy",
					},
				}
				taggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{sdkProxy}, nil)
				proxyMgr.EXPECT().Update(gomock.Any(), resProxy, sdkProxy).Return(
					elbproxymodel.ProxyResourceStatus{Arn: "arn:proxy-1", DnsName: "proxy-1.dns"}, nil)
			},
			wantProxyArn: "arn:proxy-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockTaggingMgr := NewMockTaggingManager(ctrl)
			mockProxyMgr := NewMockProxyManager(ctrl)
			stack := newTestStack("default", "my-gw")

			resProxy := tt.setupStack(stack)
			tt.setupMock(resProxy, mockTaggingMgr, mockProxyMgr)

			s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)
			err := s.Synthesize(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, s.GetProxyDetails())
				assert.Equal(t, tt.wantProxyArn, s.GetProxyDetails().Arn)
			}
		})
	}
}

func Test_proxySynthesizer_PostSynthesize(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")

	tests := []struct {
		name      string
		setupMock func(taggingMgr *MockTaggingManager, proxyMgr *MockProxyManager)
	}{
		{
			name: "deletes unmatched proxies",
			setupMock: func(taggingMgr *MockTaggingManager, proxyMgr *MockProxyManager) {
				unmatchedProxy := ProxyWithTags{
					ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:orphan", Name: "orphan"},
					Tags: map[string]string{
						tracking.TagKeyCluster:      "test-cluster",
						tracking.TagKeyStack:        "default/my-gw",
						tracking.TagKeyManagedBy:    tracking.TagValueManagedBy,
						tracking.TagKeyResourceType: "orphan-id",
					},
				}
				taggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{unmatchedProxy}, nil)
				proxyMgr.EXPECT().Delete(gomock.Any(), unmatchedProxy).Return(nil)
			},
		},
		{
			name: "continues on delete error",
			setupMock: func(taggingMgr *MockTaggingManager, proxyMgr *MockProxyManager) {
				proxy1 := ProxyWithTags{
					ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:orphan-1", Name: "orphan-1"},
					Tags:         map[string]string{tracking.TagKeyResourceType: "orphan-1"},
				}
				proxy2 := ProxyWithTags{
					ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:orphan-2", Name: "orphan-2"},
					Tags:         map[string]string{tracking.TagKeyResourceType: "orphan-2"},
				}
				taggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{proxy1, proxy2}, nil)
				proxyMgr.EXPECT().Delete(gomock.Any(), proxy1).Return(fmt.Errorf("delete failed"))
				proxyMgr.EXPECT().Delete(gomock.Any(), proxy2).Return(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockTaggingMgr := NewMockTaggingManager(ctrl)
			mockProxyMgr := NewMockProxyManager(ctrl)
			stack := newTestStack("default", "my-gw")

			tt.setupMock(mockTaggingMgr, mockProxyMgr)

			s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)
			err := s.Synthesize(context.Background())
			assert.NoError(t, err)

			// Empty stack = teardown: proxies are still listed, so SG cleanup is held.
			err = s.PostSynthesize(context.Background())
			assert.True(t, reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonWaitingForProxyDeletion))
		})
	}
}

// Test_proxySynthesizer_Synthesize_DeletingDuplicate covers the pass after a surplus duplicate was
// deleted: the active primary keeps being updated (no wait), even when the deleting duplicate is
// listed first, and the duplicate is not deleted again.
func Test_proxySynthesizer_Synthesize_DeletingDuplicate(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockTaggingMgr := NewMockTaggingManager(ctrl)
	mockProxyMgr := NewMockProxyManager(ctrl) // no Create or Delete expected

	stack := newTestStack("default", "my-gw")
	resProxy, _ := elbproxymodel.NewProxyResource(stack, "my-proxy")
	resProxy.Name = "my-proxy"
	deletingDup := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:dup", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusDeleteInProgress}},
		Tags:         map[string]string{tracking.TagKeyResourceType: "my-proxy"},
	}
	primary := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:primary", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}},
		Tags:         map[string]string{tracking.TagKeyResourceType: "my-proxy"},
	}
	mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{deletingDup, primary}, nil)
	mockProxyMgr.EXPECT().Update(gomock.Any(), resProxy, primary).Return(
		elbproxymodel.ProxyResourceStatus{Arn: "arn:primary"}, nil)

	s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)
	require.NoError(t, s.Synthesize(context.Background()))
	assert.Equal(t, "arn:primary", s.GetProxyDetails().Arn)
	assert.NoError(t, s.PostSynthesize(context.Background()))
}

// Test_proxySynthesizer_PostSynthesize_HoldsSGCleanup covers teardown waiting for the proxy:
// a deleting proxy is not re-deleted, and an empty list lets SG cleanup proceed. A normal
// reconcile deleting a surplus duplicate does not hold.
func Test_proxySynthesizer_PostSynthesize_HoldsSGCleanup(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")
	deleting := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:old", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusDeleteInProgress}},
		Tags:         map[string]string{tracking.TagKeyResourceType: "my-proxy"},
	}

	t.Run("teardown with deleting proxy skips DeleteProxy and holds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl) // no Delete expected
		mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{deleting}, nil)

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), newTestStack("default", "my-gw"))
		require.NoError(t, s.Synthesize(context.Background()))
		err := s.PostSynthesize(context.Background())
		var requeueErr *reconciler.RequeueNeededAfter
		require.ErrorAs(t, err, &requeueErr)
		assert.Equal(t, reconciler.RequeueReasonWaitingForProxyDeletion, requeueErr.Reason())
		assert.Equal(t, 30*time.Second, requeueErr.Duration())
	})

	t.Run("teardown with no proxy left proceeds", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl)
		mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return(nil, nil)

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), newTestStack("default", "my-gw"))
		require.NoError(t, s.Synthesize(context.Background()))
		assert.NoError(t, s.PostSynthesize(context.Background()))
	})

	t.Run("normal reconcile deleting a surplus duplicate does not hold", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl)
		stack := newTestStack("default", "my-gw")
		resProxy, _ := elbproxymodel.NewProxyResource(stack, "my-proxy")
		resProxy.Name = "my-proxy"
		primary := ProxyWithTags{
			ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:a", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}},
			Tags:         map[string]string{tracking.TagKeyResourceType: "my-proxy"},
		}
		surplus := ProxyWithTags{
			ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:b", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}},
			Tags:         map[string]string{tracking.TagKeyResourceType: "my-proxy"},
		}
		mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{primary, surplus}, nil)
		mockProxyMgr.EXPECT().Update(gomock.Any(), resProxy, primary).Return(
			elbproxymodel.ProxyResourceStatus{Arn: "arn:a"}, nil)
		mockProxyMgr.EXPECT().Delete(gomock.Any(), surplus).Return(nil)

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)
		require.NoError(t, s.Synthesize(context.Background()))
		assert.NoError(t, s.PostSynthesize(context.Background()))
	})
}

// Test_proxySynthesizer_Synthesize_Requeue verifies that Synthesize emits a
// *reconciler.RequeueNeededAfter error when the proxy it created/updated is still
// being provisioned (status code "create-in-progress"), and that no requeue error
// is emitted once the proxy is active.
//
// The requeue is emitted from Synthesize (not PostSynthesize) on purpose: the stack
// deployer runs every synthesizer's Synthesize in order before any PostSynthesize, so
// returning the requeue from Synthesize aborts the pass before the downstream
// serviceSynthesizer creates the Kubernetes Service. Emitting it from PostSynthesize
// would be too late — the Service would already exist.
func Test_proxySynthesizer_Synthesize_Requeue(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")

	tests := []struct {
		name        string
		statusCode  string
		wantRequeue bool
	}{
		{
			name:        "create-in-progress proxy triggers requeue",
			statusCode:  elbproxyaws.ProxyStatusCreateInProgress,
			wantRequeue: true,
		},
		{
			name:        "active proxy does not requeue",
			statusCode:  elbproxyaws.ProxyStatusActive,
			wantRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockTaggingMgr := NewMockTaggingManager(ctrl)
			mockProxyMgr := NewMockProxyManager(ctrl)
			stack := newTestStack("default", "my-gw")

			resProxy, _ := elbproxymodel.NewProxyResource(stack, "my-proxy")
			resProxy.Name = "my-proxy"

			// No existing proxies — Synthesize takes the Create path and records the
			// returned ProxyDetails (including its status) on the synthesizer.
			mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return(nil, nil)
			mockProxyMgr.EXPECT().Create(gomock.Any(), resProxy).Return(
				elbproxymodel.ProxyResourceStatus{Arn: "arn:proxy-1", DnsName: "proxy-1.dns"},
				ProxyWithTags{
					ProxyDetails: elbproxyaws.ProxyDetails{
						Arn:    "arn:proxy-1",
						Name:   "my-proxy",
						Status: elbproxyaws.ProxyStatus{Code: tt.statusCode},
					},
					Tags: map[string]string{},
				},
				nil,
			)

			s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)

			err := s.Synthesize(context.Background())
			if tt.wantRequeue {
				require.Error(t, err)
				var requeueErr *reconciler.RequeueNeededAfter
				require.ErrorAs(t, err, &requeueErr,
					"Synthesize must emit *reconciler.RequeueNeededAfter while provisioning")
				assert.Equal(t, 2*time.Minute, requeueErr.Duration())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Test_proxySynthesizer_Synthesize_DeleteInProgress verifies a re-applied Gateway does not reuse
// (or re-create over) its previous proxy while Proxy is still deleting it, and that teardown
// (no desired proxy) still deletes it as before.
func Test_proxySynthesizer_Synthesize_DeleteInProgress(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")
	dyingProxy := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{
			Arn:    "arn:proxy-old",
			Name:   "my-proxy",
			Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusDeleteInProgress},
		},
		Tags: map[string]string{tracking.TagKeyResourceType: "my-proxy"},
	}

	t.Run("desired proxy requeues without update or create", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl)
		stack := newTestStack("default", "my-gw")
		resProxy, _ := elbproxymodel.NewProxyResource(stack, "my-proxy")
		resProxy.Name = "my-proxy"

		mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{dyingProxy}, nil)

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)
		err := s.Synthesize(context.Background())
		var requeueErr *reconciler.RequeueNeededAfter
		require.ErrorAs(t, err, &requeueErr)
		assert.Equal(t, 30*time.Second, requeueErr.Duration())
		assert.Equal(t, reconciler.RequeueReasonPreviousProxyDeleting, requeueErr.Reason())
		assert.Nil(t, s.GetProxyDetails())
	})

	t.Run("teardown waits without re-deleting it", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl) // no Delete expected
		stack := newTestStack("default", "my-gw")

		mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{dyingProxy}, nil)

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)
		require.NoError(t, s.Synthesize(context.Background()))
		err := s.PostSynthesize(context.Background())
		assert.True(t, reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonWaitingForProxyDeletion))
	})
}

// Test_proxySynthesizer_MergedGateway covers the merged-gateway lifecycle:
//
//  1. The stack ID for a merged gateway is "envoy-gateway-system/merged-eg" (namespace derived
//     from ENVOY_GATEWAY_NAMESPACE, name is the bare GatewayClass name).
//  2. On reconcile, ListProxies is called with a tag filter containing that stack ID so the
//     synthesizer finds the existing proxy.
//  3. On deletion (empty stack), the existing proxy is unmatched and deleted.
func Test_proxySynthesizer_MergedGateway(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")

	// Merged gateway stack: namespace = "envoy-gateway-system", name = "merged-eg".
	mergedStack := newTestStack("envoy-gateway-system", "merged-eg")
	mergedStackID := "envoy-gateway-system/merged-eg"

	existingSDKProxy := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:merged-proxy", Name: "elbp-mergedeg-668ac7aedd", DnsName: "merged.dns"},
		Tags: map[string]string{
			tracking.TagKeyCluster:      "test-cluster",
			tracking.TagKeyStack:        mergedStackID,
			tracking.TagKeyManagedBy:    tracking.TagValueManagedBy,
			tracking.TagKeyResourceType: "elbp-mergedeg-668ac7aedd",
		},
	}

	t.Run("reconcile: ListProxies called with merged stack ID tag filter", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl)

		resProxy, err := elbproxymodel.NewProxyResource(mergedStack, "elbp-mergedeg-668ac7aedd")
		assert.NoError(t, err)
		resProxy.Name = "elbp-mergedeg-668ac7aedd"

		// Verify the tag filter passed to ListProxies contains the merged stack ID.
		mockTaggingMgr.EXPECT().
			ListProxies(gomock.Any(), gomock.AssignableToTypeOf(tracking.TagFilter{})).
			DoAndReturn(func(_ context.Context, filters ...tracking.TagFilter) ([]ProxyWithTags, error) {
				assert.Len(t, filters, 1)
				assert.Equal(t, []string{mergedStackID}, filters[0][tracking.TagKeyStack],
					"ListProxies must filter by the merged stack ID")
				return []ProxyWithTags{existingSDKProxy}, nil
			})
		mockProxyMgr.EXPECT().
			Update(gomock.Any(), resProxy, existingSDKProxy).
			Return(elbproxymodel.ProxyResourceStatus{Arn: "arn:merged-proxy", DnsName: "merged.dns"}, nil)

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), mergedStack)
		assert.NoError(t, s.Synthesize(context.Background()))
		assert.Equal(t, "arn:merged-proxy", s.GetProxyDetails().Arn)
	})

	t.Run("deletion: empty stack causes existing proxy to be deleted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl)

		// Empty stack — no desired proxy resources.
		emptyStack := newTestStack("envoy-gateway-system", "merged-eg")

		// ListProxies returns the existing proxy; with no desired resources it becomes unmatched.
		mockTaggingMgr.EXPECT().
			ListProxies(gomock.Any(), gomock.AssignableToTypeOf(tracking.TagFilter{})).
			Return([]ProxyWithTags{existingSDKProxy}, nil)
		// The unmatched proxy must be deleted in PostSynthesize, then SG cleanup is held
		// until it is gone.
		mockProxyMgr.EXPECT().Delete(gomock.Any(), existingSDKProxy).Return(nil)

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), emptyStack)
		assert.NoError(t, s.Synthesize(context.Background()))
		err := s.PostSynthesize(context.Background())
		assert.True(t, reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonWaitingForProxyDeletion))
	})

	t.Run("creation: new merged proxy tagged with merged stack ID", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockTaggingMgr := NewMockTaggingManager(ctrl)
		mockProxyMgr := NewMockProxyManager(ctrl)

		freshStack := newTestStack("envoy-gateway-system", "merged-eg")
		resProxy, err := elbproxymodel.NewProxyResource(freshStack, "elbp-mergedeg-668ac7aedd")
		assert.NoError(t, err)
		resProxy.Name = "elbp-mergedeg-668ac7aedd"

		// No existing proxies — triggers Create.
		mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return(nil, nil)
		mockProxyMgr.EXPECT().
			Create(gomock.Any(), resProxy).
			DoAndReturn(func(_ context.Context, rp *elbproxymodel.ProxyResource) (elbproxymodel.ProxyResourceStatus, ProxyWithTags, error) {
				// Verify the tags that will be written contain the merged stack ID.
				tags := tp.ResourceTags(freshStack, rp, nil)
				assert.Equal(t, mergedStackID, tags[tracking.TagKeyStack],
					"created proxy must be tagged with the merged stack ID")
				assert.Equal(t, "test-cluster", tags[tracking.TagKeyCluster])
				assert.Equal(t, tracking.TagValueManagedBy, tags[tracking.TagKeyManagedBy])
				return elbproxymodel.ProxyResourceStatus{Arn: "arn:new-merged", DnsName: "new.dns"},
					ProxyWithTags{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:new-merged"}, Tags: tags}, nil
			})

		s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), freshStack)
		assert.NoError(t, s.Synthesize(context.Background()))
		assert.Equal(t, "arn:new-merged", s.GetProxyDetails().Arn)
	})
}

func Test_matchResAndSDKProxies(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")
	resourceIDTagKey := tp.ResourceIDTagKey()

	tests := []struct {
		name             string
		resProxies       []*elbproxymodel.ProxyResource
		sdkProxies       []ProxyWithTags
		wantMatchedCount int
		wantUnmatchedRes int
		wantUnmatchedSDK int
		wantErr          string
	}{
		{
			name: "matches, unmatched desired, and unmatched SDK",
			resProxies: func() []*elbproxymodel.ProxyResource {
				stack := newTestStack("default", "my-gw")
				resA, _ := elbproxymodel.NewProxyResource(stack, "proxy-a")
				resB, _ := elbproxymodel.NewProxyResource(stack, "proxy-b")
				return []*elbproxymodel.ProxyResource{resA, resB}
			}(),
			sdkProxies: []ProxyWithTags{
				{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:sdk-a"}, Tags: map[string]string{resourceIDTagKey: "proxy-a"}},
				{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:sdk-c"}, Tags: map[string]string{resourceIDTagKey: "proxy-c"}},
			},
			wantMatchedCount: 1,
			wantUnmatchedRes: 1,
			wantUnmatchedSDK: 1,
		},
		{
			name:       "missing resource ID tag returns error",
			resProxies: nil,
			sdkProxies: []ProxyWithTags{
				{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:no-tag"}, Tags: map[string]string{}},
			},
			wantErr: "unexpected proxy with no resourceID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, unmatchedRes, unmatchedSDK, err := matchResAndSDKProxies(
				tt.resProxies, tt.sdkProxies, resourceIDTagKey,
			)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Len(t, matched, tt.wantMatchedCount)
				assert.Len(t, unmatchedRes, tt.wantUnmatchedRes)
				assert.Len(t, unmatchedSDK, tt.wantUnmatchedSDK)
			}
		})
	}
}

// Test_proxySynthesizer_Synthesize_Replacement: a create-only field change deletes the old proxy,
// then creates the new one in the same pass, or requeues when the name is unchanged.
func Test_proxySynthesizer_Synthesize_Replacement(t *testing.T) {
	ipv4Only := elbproxyaws.IpAddressingConfiguration{Ipv4Addressability: elbproxyaws.IpAddressabilityInternal, Ipv6Addressability: elbproxyaws.IpAddressabilityOff}
	dualstack := elbproxyaws.IpAddressingConfiguration{Ipv4Addressability: elbproxyaws.IpAddressabilityInternal, Ipv6Addressability: elbproxyaws.IpAddressabilityInternal}
	const roleA, roleB = "arn:aws:iam::123456789012:role/a", "arn:aws:iam::123456789012:role/b"
	const (
		update = iota
		deleteThenCreate
		deleteThenWait
	)

	tests := []struct {
		name                  string
		irAbsent              bool
		desiredName, liveName string
		desiredIP, liveIP     elbproxyaws.IpAddressingConfiguration
		desiredRole, liveRole string
		want                  int
	}{
		{name: "matches live: update", desiredName: "p-a", liveName: "p-a", desiredIP: ipv4Only, liveIP: ipv4Only, desiredRole: roleA, liveRole: roleA, want: update},
		{name: "ipConfiguration changed, new generated name: delete then create", desiredName: "p-b", liveName: "p-a", desiredIP: dualstack, liveIP: ipv4Only, desiredRole: roleA, liveRole: roleA, want: deleteThenCreate},
		{name: "roleArn changed, new generated name: delete then create", desiredName: "p-b", liveName: "p-a", desiredIP: ipv4Only, liveIP: ipv4Only, desiredRole: roleB, liveRole: roleA, want: deleteThenCreate},
		{name: "spec.name changed: delete then create", desiredName: "new-name", liveName: "old-name", desiredIP: ipv4Only, liveIP: ipv4Only, desiredRole: roleA, liveRole: roleA, want: deleteThenCreate},
		{name: "roleArn changed, explicit name kept: delete then wait", desiredName: "p-a", liveName: "p-a", desiredIP: ipv4Only, liveIP: ipv4Only, desiredRole: roleB, liveRole: roleA, want: deleteThenWait},
		{name: "live values not returned: update", desiredName: "p-a", desiredIP: dualstack, desiredRole: roleB, want: update},
		{name: "IR absent never replaces: update", irAbsent: true, desiredName: "p-b", liveName: "p-a", desiredIP: ipv4Only, liveIP: dualstack, desiredRole: roleB, liveRole: roleA, want: update},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			tp := tracking.NewProvider("test-cluster")
			mockTaggingMgr := NewMockTaggingManager(ctrl)
			mockProxyMgr := NewMockProxyManager(ctrl)

			stack := core.NewDefaultStackWithIRPresence(core.StackID{Namespace: "default", Name: "my-gw"}, !tt.irAbsent)
			resProxy, err := elbproxymodel.NewProxyResource(stack, tracking.ResourceIDProxy)
			require.NoError(t, err)
			resProxy.Name = tt.desiredName
			resProxy.RoleArn = tt.desiredRole
			resProxy.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration = tt.desiredIP

			live := ProxyWithTags{
				ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:old", Name: tt.liveName, RoleArn: tt.liveRole,
					Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}},
				Tags: map[string]string{tracking.TagKeyResourceType: tracking.ResourceIDProxy},
			}
			live.ProxyDetails.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration = tt.liveIP
			mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{live}, nil)

			s := NewProxySynthesizer(tp, mockTaggingMgr, mockProxyMgr, testLogger(), stack)
			switch tt.want {
			case update:
				mockProxyMgr.EXPECT().Update(gomock.Any(), resProxy, live).Return(elbproxymodel.ProxyResourceStatus{Arn: "arn:old"}, nil)
				require.NoError(t, s.Synthesize(context.Background()))
			case deleteThenCreate:
				created := ProxyWithTags{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:new", Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}}}
				gomock.InOrder(
					mockProxyMgr.EXPECT().Delete(gomock.Any(), live).Return(nil),
					mockProxyMgr.EXPECT().Create(gomock.Any(), resProxy).Return(elbproxymodel.ProxyResourceStatus{Arn: "arn:new"}, created, nil),
				)
				require.NoError(t, s.Synthesize(context.Background()))
				assert.Equal(t, "arn:new", s.GetProxyDetails().Arn)
			case deleteThenWait:
				mockProxyMgr.EXPECT().Delete(gomock.Any(), live).Return(nil) // no Create: the name is still taken
				err := s.Synthesize(context.Background())
				assert.True(t, reconciler.IsRequeueAfterReason(err, reconciler.RequeueReasonPreviousProxyDeleting))
			}
		})
	}

	// A failed delete (e.g. deletion protection) is returned, and nothing is created.
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockTaggingMgr := NewMockTaggingManager(ctrl)
	mockProxyMgr := NewMockProxyManager(ctrl)
	stack := newTestStack("default", "my-gw")
	resProxy, err := elbproxymodel.NewProxyResource(stack, "my-proxy")
	require.NoError(t, err)
	resProxy.RoleArn = roleB
	live := ProxyWithTags{
		ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:old", RoleArn: roleA, Status: elbproxyaws.ProxyStatus{Code: elbproxyaws.ProxyStatusActive}},
		Tags:         map[string]string{tracking.TagKeyResourceType: "my-proxy"},
	}
	mockTaggingMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return([]ProxyWithTags{live}, nil)
	mockProxyMgr.EXPECT().Delete(gomock.Any(), live).Return(fmt.Errorf("deletion protection is enabled"))
	s := NewProxySynthesizer(tracking.NewProvider("test-cluster"), mockTaggingMgr, mockProxyMgr, testLogger(), stack)
	assert.ErrorContains(t, s.Synthesize(context.Background()), "deletion protection is enabled")
}
