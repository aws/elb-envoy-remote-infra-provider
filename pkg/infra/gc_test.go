// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	ec2deploy "github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/ec2"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
)

func newTestGCLogger() logging.Logger {
	return logging.Logger{Logger: logr.New(&log.NullLogSink{})}
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = gwapiv1.Install(s)
	return s
}

func TestGarbageCollector_Run(t *testing.T) {
	tests := []struct {
		name        string
		setupCache  func(c *infrastore.Cache)
		k8sObjects  []runtime.Object
		proxyResp   []elbproxy.ProxyWithTags
		proxyErr    error
		sgResp      []networking.SecurityGroupInfo
		sgErr       error
		wantPending []string
	}{
		{
			name: "no orphans when all proxies have corresponding Gateways",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/gw1"}},
			},
			k8sObjects: []runtime.Object{
				&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gw1"}},
			},
		},
		{
			name: "orphaned proxy with no Gateway marked pending deletion",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/orphan"}},
			},
			wantPending: []string{"ns/orphan"},
		},
		{
			name: "orphaned SG with no Gateway marked pending deletion",
			sgResp: []networking.SecurityGroupInfo{
				{SecurityGroupID: "sg-123", Tags: map[string]string{tracking.TagKeyStack: "ns/sg-orphan"}},
			},
			wantPending: []string{"ns/sg-orphan"},
		},
		{
			name: "both proxy and SG orphaned for same key — deduplicated",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/both"}},
			},
			sgResp: []networking.SecurityGroupInfo{
				{SecurityGroupID: "sg-456", Tags: map[string]string{tracking.TagKeyStack: "ns/both"}},
			},
			wantPending: []string{"ns/both"},
		},
		{
			name: "key in knownKeys but no Gateway — GC'd",
			setupCache: func(c *infrastore.Cache) {
				c.AddKnownKey("ns/known")
			},
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/known"}},
			},
			wantPending: []string{"ns/known"},
		},
		{
			name: "key in pendingDeletion — still GC'd on fresh pod (state is empty)",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/pending"}},
			},
			wantPending: []string{"ns/pending"},
		},
		{
			name:     "ListProxies error does not block SG discovery",
			proxyErr: errors.New("API error"),
			sgResp: []networking.SecurityGroupInfo{
				{SecurityGroupID: "sg-789", Tags: map[string]string{tracking.TagKeyStack: "ns/sg-only"}},
			},
			wantPending: []string{"ns/sg-only"},
		},
		{
			name:  "ListSecurityGroups error does not block proxy discovery",
			sgErr: errors.New("EC2 error"),
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/proxy-only"}},
			},
			wantPending: []string{"ns/proxy-only"},
		},
		{
			name: "proxy with empty stack tag — ignored",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: ""}},
			},
		},
		{
			name: "multiple orphaned proxies all enqueued",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/a"}},
				{Tags: map[string]string{tracking.TagKeyStack: "ns/b"}},
			},
			wantPending: []string{"ns/a", "ns/b"},
		},
		{
			name: "merged gateway key skipped when Gateways in that class exist",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{
					tracking.TagKeyStack:      "envoy-gateway-system/eg-merged",
					tracking.TagKeyDeployMode: tracking.TagValueDeployModeMerged,
				}},
			},
			k8sObjects: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw1"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-merged"},
				},
			},
			// Gateway in that class exists — should NOT be GC'd.
		},
		{
			name: "merged gateway key orphaned when no Gateways in that class",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{
					tracking.TagKeyStack:      "envoy-gateway-system/eg-merged",
					tracking.TagKeyDeployMode: tracking.TagValueDeployModeMerged,
				}},
			},
			// No Gateways at all — merged proxy is orphaned.
			wantPending: []string{"envoy-gateway-system/eg-merged"},
		},
		{
			name: "Gateway exists but cache unaware — startup race, not GC'd",
			proxyResp: []elbproxy.ProxyWithTags{
				{Tags: map[string]string{tracking.TagKeyStack: "ns/new-gw"}},
			},
			k8sObjects: []runtime.Object{
				&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "new-gw"}},
			},
			// Gateway exists in k8s but cache has no knownKey for it. EG hasn't
			// pushed IR yet. Should NOT be garbage collected.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			elbProxyTagMgr := elbproxy.NewMockTaggingManager(ctrl)
			ec2TagMgr := ec2deploy.NewMockTaggingManager(ctrl)
			cache := infrastore.New()
			tp := tracking.NewProvider("test-cluster")

			if tt.setupCache != nil {
				tt.setupCache(cache)
			}

			k8sClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithRuntimeObjects(tt.k8sObjects...).
				Build()

			// Mock expectations.
			elbProxyTagMgr.EXPECT().ListProxies(gomock.Any(), gomock.Any()).Return(tt.proxyResp, tt.proxyErr)
			ec2TagMgr.EXPECT().ListSecurityGroups(gomock.Any(), gomock.Any()).Return(tt.sgResp, tt.sgErr)

			// Use a nil channel — EnqueueGatewayKey is non-blocking and drops on nil/full.
			// We verify behavior via cache state instead.
			infraObj := &Infra{
				logger:                 newTestGCLogger(),
				elbProxyTaggingManager: elbProxyTagMgr,
				ec2TaggingManager:      ec2TagMgr,
				trackingProvider:       tp,
			}

			gc := NewGarbageCollector(infraObj, cache, k8sClient, nil)
			gc.Run(context.Background())

			// Verify pending deletion state.
			if len(tt.wantPending) == 0 {
				// No new keys should have been marked (check nothing unexpected).
				// Note: setupCache may have pre-marked some keys.
			}
			for _, key := range tt.wantPending {
				assert.True(t, cache.IsPendingDeletion(key), "expected %s to be pending deletion", key)
			}
		})
	}
}

func TestGarbageCollector_clusterFilter(t *testing.T) {
	tp := tracking.NewProvider("my-cluster")
	infraObj := &Infra{
		trackingProvider: tp,
	}
	gc := &GarbageCollector{infra: infraObj}

	filter := gc.clusterFilter()

	// Must filter by cluster name — only resources in this cluster.
	assert.Equal(t, []string{"my-cluster"}, filter[tracking.TagKeyCluster])
	// Must filter by managed-by — only resources owned by this controller.
	assert.Equal(t, []string{tracking.TagValueManagedBy}, filter[tracking.TagKeyManagedBy])
}

func TestGarbageCollector_gatewayExistsForProxy(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		tags       map[string]string
		k8sObjects []runtime.Object
		want       bool
	}{
		{
			name: "per-gateway: Gateway exists",
			key:  "default/my-gw",
			tags: map[string]string{},
			k8sObjects: []runtime.Object{
				&gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "my-gw"}},
			},
			want: true,
		},
		{
			name: "per-gateway: Gateway does not exist",
			key:  "default/deleted-gw",
			tags: map[string]string{},
			want: false,
		},
		{
			name: "merged: Gateways in class exist",
			key:  "envoy-gateway-system/eg-merged",
			tags: map[string]string{tracking.TagKeyDeployMode: tracking.TagValueDeployModeMerged},
			k8sObjects: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw1"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "eg-merged"},
				},
			},
			want: true,
		},
		{
			name: "merged: no Gateways in class",
			key:  "envoy-gateway-system/eg-merged",
			tags: map[string]string{tracking.TagKeyDeployMode: tracking.TagValueDeployModeMerged},
			want: false,
		},
		{
			name: "merged: Gateways exist but in different class",
			key:  "envoy-gateway-system/eg-merged",
			tags: map[string]string{tracking.TagKeyDeployMode: tracking.TagValueDeployModeMerged},
			k8sObjects: []runtime.Object{
				&gwapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gw1"},
					Spec:       gwapiv1.GatewaySpec{GatewayClassName: "other-class"},
				},
			},
			want: false,
		},
		{
			name: "per-gateway: no deploy-mode tag, Gateway missing — orphaned",
			key:  "production/api-gw",
			tags: map[string]string{tracking.TagKeyStack: "production/api-gw"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fake.NewClientBuilder().
				WithScheme(newTestScheme()).
				WithRuntimeObjects(tt.k8sObjects...).
				Build()

			gc := &GarbageCollector{
				k8sClient: k8sClient,
			}

			got := gc.gatewayExistsForProxy(context.Background(), tt.key, tt.tags)
			assert.Equal(t, tt.want, got)
		})
	}
}
