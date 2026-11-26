// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package elbproxy

import (
	"context"
	"errors"
	"fmt"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	rgtsdk "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/smithy-go"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func testLogger() logging.Logger {
	return logging.Logger{Logger: logr.New(&log.NullLogSink{})}
}

func Test_defaultTaggingManager_ReconcileTags(t *testing.T) {
	tests := []struct {
		name        string
		resourceArn string
		desiredTags map[string]string
		opts        []ReconcileTagsOption
		setupMock   func(client *elbproxyaws.MockELBProxyClient)
		wantErr     bool
	}{
		{
			name:        "add and remove tags with WithCurrentTags",
			resourceArn: "arn:proxy-1",
			desiredTags: map[string]string{
				"keyA": "valueA",
				"keyB": "valueB2",
				"keyD": "valueD",
			},
			opts: []ReconcileTagsOption{
				WithCurrentTags(map[string]string{
					"keyA": "valueA",
					"keyB": "valueB",
					"keyC": "valueC",
				}),
			},
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().TagResource(gomock.Any(), gomock.Any()).Return(nil)
				client.EXPECT().UntagResource(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name:        "fetch current tags when WithCurrentTags not provided",
			resourceArn: "arn:proxy-2",
			desiredTags: map[string]string{
				"keyA": "valueA2",
			},
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().ListTagsForResource(gomock.Any(), elbproxyaws.ListTagsForResourceInput{
					ResourceArn: "arn:proxy-2",
				}).Return(&elbproxyaws.ListTagsForResourceOutput{
					Tags: map[string]string{"keyA": "valueA"},
				}, nil)
				client.EXPECT().TagResource(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name:        "ignored tag keys are not modified or removed",
			resourceArn: "arn:proxy-3",
			desiredTags: map[string]string{
				"keyA": "valueA2",
				"keyB": "valueB2",
			},
			opts: []ReconcileTagsOption{
				WithCurrentTags(map[string]string{
					"keyA": "valueA",
					"keyB": "valueB",
					"keyC": "valueC",
				}),
				WithIgnoredTagKeys([]string{"keyA", "keyC"}),
			},
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				// Only keyB should be updated; keyA is ignored, keyC deletion is ignored
				client.EXPECT().TagResource(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name:        "no changes needed",
			resourceArn: "arn:proxy-4",
			desiredTags: map[string]string{
				"keyA": "valueA",
			},
			opts: []ReconcileTagsOption{
				WithCurrentTags(map[string]string{
					"keyA": "valueA",
				}),
			},
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				// No TagResource or UntagResource calls expected
			},
		},
		{
			name:        "TagResource error propagates",
			resourceArn: "arn:proxy-5",
			desiredTags: map[string]string{
				"keyA": "valueA",
			},
			opts: []ReconcileTagsOption{
				WithCurrentTags(map[string]string{}),
			},
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().TagResource(gomock.Any(), gomock.Any()).Return(fmt.Errorf("tag failed"))
			},
			wantErr: true,
		},
		{
			name:        "ListTagsForResource error propagates",
			resourceArn: "arn:proxy-6",
			desiredTags: map[string]string{"keyA": "valueA"},
			setupMock: func(client *elbproxyaws.MockELBProxyClient) {
				client.EXPECT().ListTagsForResource(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("list tags failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
			tt.setupMock(mockClient)

			m := &defaultTaggingManager{
				client: mockClient,
				logger: testLogger(),
			}
			err := m.ReconcileTags(context.Background(), tt.resourceArn, tt.desiredTags, tt.opts...)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

const (
	testStackKey   = "gateway.elbproxy.aws/stack"
	testClusterKey = "gateway.elbproxy.aws/cluster"
)

var (
	// rgtTestFilter is the stack filter used by the tagging API tests.
	rgtTestFilter = tracking.TagFilter{
		testStackKey:   {"default/my-gw"},
		testClusterKey: {"my-cluster"},
	}
	rgtTestTags = map[string]string{testStackKey: "default/my-gw", testClusterKey: "my-cluster"}
)

func rgtResource(arn string, tags map[string]string) rgttypes.ResourceTagMapping {
	var rgtTags []rgttypes.Tag
	for k, v := range tags {
		rgtTags = append(rgtTags, rgttypes.Tag{Key: awssdk.String(k), Value: awssdk.String(v)})
	}
	return rgttypes.ResourceTagMapping{ResourceARN: awssdk.String(arn), Tags: rgtTags}
}

// rgtInput is the GetResources request for a tag filter with a single key.
func rgtInput(key, value string) *rgtsdk.GetResourcesInput {
	return &rgtsdk.GetResourcesInput{
		TagFilters:          []rgttypes.TagFilter{{Key: awssdk.String(key), Values: []string{value}}},
		ResourceTypeFilters: []string{services.ResourceTypeELBProxy},
	}
}

// proxyNotFound is the ELBProxy API's error for a proxy that doesn't exist.
func proxyNotFound() error {
	return &smithy.OperationError{ServiceID: "ELB Proxy", OperationName: "GetProxy",
		Err: &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "Proxy not found"}}
}

func Test_defaultTaggingManager_ListProxies(t *testing.T) {
	tests := []struct {
		name       string
		tagFilters []tracking.TagFilter
		setupMock  func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient)
		want       []ProxyWithTags
		wantErr    string
	}{
		{
			name:       "returns the proxies GetResources finds, with the tags it returns",
			tagFilters: []tracking.TagFilter{rgtTestFilter},
			setupMock: func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient) {
				rgt.EXPECT().GetResourcesAsList(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input *rgtsdk.GetResourcesInput) ([]rgttypes.ResourceTagMapping, error) {
						assert.Equal(t, []string{services.ResourceTypeELBProxy}, input.ResourceTypeFilters)
						assert.ElementsMatch(t, []rgttypes.TagFilter{
							{Key: awssdk.String(testStackKey), Values: []string{"default/my-gw"}},
							{Key: awssdk.String(testClusterKey), Values: []string{"my-cluster"}},
						}, input.TagFilters)
						return []rgttypes.ResourceTagMapping{rgtResource("arn:proxy-1", rgtTestTags)}, nil
					})
				client.EXPECT().GetProxy(gomock.Any(), "arn:proxy-1").Return(&elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", Name: "proxy-1"}, nil)
			},
			want: []ProxyWithTags{{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1", Name: "proxy-1"}, Tags: rgtTestTags}},
		},
		{
			name: "queries each tag filter and reads a proxy found twice only once",
			tagFilters: []tracking.TagFilter{
				{testStackKey: {"default/my-gw"}},
				{testClusterKey: {"my-cluster"}},
			},
			setupMock: func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient) {
				rgt.EXPECT().GetResourcesAsList(gomock.Any(), rgtInput(testStackKey, "default/my-gw")).
					Return([]rgttypes.ResourceTagMapping{rgtResource("arn:proxy-1", rgtTestTags)}, nil)
				rgt.EXPECT().GetResourcesAsList(gomock.Any(), rgtInput(testClusterKey, "my-cluster")).
					Return([]rgttypes.ResourceTagMapping{
						rgtResource("arn:proxy-1", rgtTestTags),
						rgtResource("arn:proxy-2", map[string]string{testClusterKey: "my-cluster"}),
					}, nil)
				client.EXPECT().GetProxy(gomock.Any(), "arn:proxy-1").Return(&elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"}, nil)
				client.EXPECT().GetProxy(gomock.Any(), "arn:proxy-2").Return(&elbproxyaws.ProxyDetails{Arn: "arn:proxy-2"}, nil)
			},
			want: []ProxyWithTags{
				{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"}, Tags: rgtTestTags},
				{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-2"}, Tags: map[string]string{testClusterKey: "my-cluster"}},
			},
		},
		{
			name:       "skips a proxy that GetResources returns but no longer exists",
			tagFilters: []tracking.TagFilter{rgtTestFilter},
			setupMock: func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient) {
				rgt.EXPECT().GetResourcesAsList(gomock.Any(), gomock.Any()).Return([]rgttypes.ResourceTagMapping{
					rgtResource("arn:proxy-deleted", rgtTestTags),
					rgtResource("arn:proxy-1", rgtTestTags),
				}, nil)
				client.EXPECT().GetProxy(gomock.Any(), "arn:proxy-deleted").Return(nil, proxyNotFound())
				client.EXPECT().GetProxy(gomock.Any(), "arn:proxy-1").Return(&elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"}, nil)
			},
			want: []ProxyWithTags{{ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"}, Tags: rgtTestTags}},
		},
		{
			name:       "returns no proxies without filters, and calls nothing",
			tagFilters: nil,
			setupMock:  func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient) {},
			want:       nil,
		},
		{
			name:       "returns no proxies when GetResources finds none",
			tagFilters: []tracking.TagFilter{rgtTestFilter},
			setupMock: func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient) {
				rgt.EXPECT().GetResourcesAsList(gomock.Any(), gomock.Any()).Return(nil, nil)
			},
			want: nil,
		},
		{
			name:       "returns GetResources errors, including access denied",
			tagFilters: []tracking.TagFilter{rgtTestFilter},
			setupMock: func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient) {
				rgt.EXPECT().GetResourcesAsList(gomock.Any(), gomock.Any()).Return(nil, &smithy.OperationError{
					ServiceID: "Resource Groups Tagging API", OperationName: "GetResources",
					Err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: tag:GetResources"},
				})
			},
			wantErr: "not authorized to perform: tag:GetResources",
		},
		{
			name:       "returns GetProxy errors other than not found",
			tagFilters: []tracking.TagFilter{rgtTestFilter},
			setupMock: func(rgt *services.MockRGT, client *elbproxyaws.MockELBProxyClient) {
				rgt.EXPECT().GetResourcesAsList(gomock.Any(), gomock.Any()).
					Return([]rgttypes.ResourceTagMapping{rgtResource("arn:proxy-1", rgtTestTags)}, nil)
				client.EXPECT().GetProxy(gomock.Any(), "arn:proxy-1").Return(nil, fmt.Errorf("get proxy failed"))
			},
			wantErr: "get proxy failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRGT := services.NewMockRGT(ctrl)
			mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)
			tt.setupMock(mockRGT, mockClient)

			m := NewDefaultTaggingManager(mockClient, mockRGT, testLogger())
			got, err := m.ListProxies(context.Background(), tt.tagFilters...)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func Test_convertTagFiltersToRGTTagFilters(t *testing.T) {
	got := convertTagFiltersToRGTTagFilters(tracking.TagFilter{"keyA": {"a"}, "keyB": {"b1", "b2"}})
	assert.ElementsMatch(t, []rgttypes.TagFilter{
		{Key: awssdk.String("keyA"), Values: []string{"a"}},
		{Key: awssdk.String("keyB"), Values: []string{"b1", "b2"}},
	}, got)
	assert.Nil(t, convertTagFiltersToRGTTagFilters(nil))
}

func Test_isProxyNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "not found", err: proxyNotFound(), want: true},
		{name: "other API error", err: &smithy.GenericAPIError{Code: "ThrottlingException"}, want: false},
		{name: "non-API error", err: errors.New("boom"), want: false},
		{name: "no error", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isProxyNotFoundError(tt.err))
		})
	}
}
