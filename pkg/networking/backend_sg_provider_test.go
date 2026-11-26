// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"fmt"
	"os"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/aws/smithy-go"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	elbproxyconfig "github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
)

const (
	testVPCID       = "vpc-test"
	testClusterName = "test-cluster"
)

func newTestBackendSGProvider(ctrl *gomock.Controller, backendSG string) (*defaultBackendSGProvider, *services.MockEC2) {
	return newTestBackendSGProviderWithObjects(ctrl, backendSG)
}

// fakePendingDeletionChecker is a test helper implementing PendingDeletionChecker.
type fakePendingDeletionChecker struct {
	hasPending bool
}

func (f *fakePendingDeletionChecker) HasPendingDeletionsExcluding(_ []string) bool {
	return f.hasPending
}

// realPendingDeletionChecker tracks pending keys for testing the excluding logic.
type realPendingDeletionChecker struct {
	pending map[string]struct{}
}

func (r *realPendingDeletionChecker) HasPendingDeletionsExcluding(excludeKeys []string) bool {
	exclude := make(map[string]struct{}, len(excludeKeys))
	for _, k := range excludeKeys {
		exclude[k] = struct{}{}
	}
	for key := range r.pending {
		if _, excluded := exclude[key]; !excluded {
			return true
		}
	}
	return false
}

func newTestBackendSGProviderWithObjects(ctrl *gomock.Controller, backendSG string, objs ...runtime.Object) (*defaultBackendSGProvider, *services.MockEC2) {
	ec2Client := services.NewMockEC2(ctrl)
	tp := tracking.NewProvider(testClusterName)
	logger := logging.DefaultLogger(os.Stdout, egv1a1.LogLevelInfo)
	scheme := runtime.NewScheme()
	_ = gwapiv1.Install(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	p := NewBackendSGProvider(testClusterName, backendSG, testVPCID, ec2Client, k8sClient, tp, logger,
		elbproxyconfig.GatewayControllerName, "envoy-gateway-system", nil)
	return p, ec2Client
}

func Test_defaultBackendSGProvider_Get(t *testing.T) {
	type describeSGCall struct {
		resp []ec2types.SecurityGroup
		err  error
	}
	type createSGCall struct {
		resp *ec2sdk.CreateSecurityGroupOutput
		err  error
	}
	type createTagsCall struct {
		err error
	}
	tests := []struct {
		name           string
		backendSG      string
		describeSGCall *describeSGCall
		createSGCall   *createSGCall
		createTagsCall *createTagsCall
		want           string
		wantErr        string
	}{
		{
			name:      "explicit backend SG returns it directly",
			backendSG: "sg-explicit",
			want:      "sg-explicit",
		},
		{
			name: "auto-gen SG already exists in EC2",
			describeSGCall: &describeSGCall{
				resp: []ec2types.SecurityGroup{
					{GroupId: awssdk.String("sg-existing")},
				},
			},
			createTagsCall: &createTagsCall{},
			want:           "sg-existing",
		},
		{
			name: "auto-gen SG not found, creates new one",
			describeSGCall: &describeSGCall{
				err: &smithy.GenericAPIError{Code: "InvalidGroup.NotFound", Message: ""},
			},
			createSGCall: &createSGCall{
				resp: &ec2sdk.CreateSecurityGroupOutput{GroupId: awssdk.String("sg-new")},
			},
			want: "sg-new",
		},
		{
			name: "describe SG returns unexpected error",
			describeSGCall: &describeSGCall{
				err: &smithy.GenericAPIError{Code: "Some.Error", Message: "describe failed"},
			},
			wantErr: "describe failed",
		},
		{
			name: "create SG returns error",
			describeSGCall: &describeSGCall{
				err: &smithy.GenericAPIError{Code: "InvalidGroup.NotFound", Message: ""},
			},
			createSGCall: &createSGCall{
				err: &smithy.GenericAPIError{Code: "Create.Error", Message: "create failed"},
			},
			wantErr: "create failed",
		},
		{
			name: "tag reconciliation error",
			describeSGCall: &describeSGCall{
				resp: []ec2types.SecurityGroup{
					{GroupId: awssdk.String("sg-existing")},
				},
			},
			createTagsCall: &createTagsCall{
				err: &smithy.GenericAPIError{Code: "Tag.Error", Message: "tag failed"},
			},
			wantErr: "tag failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			p, ec2Client := newTestBackendSGProvider(ctrl, tt.backendSG)

			if tt.describeSGCall != nil {
				ec2Client.EXPECT().DescribeSecurityGroupsAsList(gomock.Any(), gomock.Any()).Return(tt.describeSGCall.resp, tt.describeSGCall.err)
			}
			if tt.createSGCall != nil {
				ec2Client.EXPECT().CreateSecurityGroupWithContext(gomock.Any(), gomock.Any()).Return(tt.createSGCall.resp, tt.createSGCall.err)
			}
			if tt.createTagsCall != nil {
				ec2Client.EXPECT().CreateTagsWithContext(gomock.Any(), gomock.Any()).Return(nil, tt.createTagsCall.err).AnyTimes()
				ec2Client.EXPECT().DeleteTagsWithContext(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			}

			activeResources := []types.NamespacedName{{Namespace: "default", Name: "gw-1"}}
			got, err := p.Get(context.Background(), ResourceTypeGateway, activeResources)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// Test_defaultBackendSGProvider_Get_SharedAcrossGateways verifies that multiple
// gateways managed by the same controller share a single backend SG. The SG is
// described/created only once; the second gateway's Get reuses the cached
// auto-generated SG without any further EC2 calls.
func Test_defaultBackendSGProvider_Get_SharedAcrossGateways(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, ec2Client := newTestBackendSGProvider(ctrl, "")

	// SG does not exist yet -> describe returns not-found, then create once.
	ec2Client.EXPECT().
		DescribeSecurityGroupsAsList(gomock.Any(), gomock.Any()).
		Return(nil, &smithy.GenericAPIError{Code: "InvalidGroup.NotFound", Message: ""}).
		Times(1)
	ec2Client.EXPECT().
		CreateSecurityGroupWithContext(gomock.Any(), gomock.Any()).
		Return(&ec2sdk.CreateSecurityGroupOutput{GroupId: awssdk.String("sg-shared")}, nil).
		Times(1)

	// First gateway allocates the SG.
	got1, err := p.Get(context.Background(), ResourceTypeGateway,
		[]types.NamespacedName{{Namespace: "default", Name: "gw-1"}})
	assert.NoError(t, err)
	assert.Equal(t, "sg-shared", got1)

	// Second, distinct gateway under the same controller reuses the cached SG.
	// No further DescribeSecurityGroupsAsList / CreateSecurityGroupWithContext
	// calls are expected (gomock would fail on any extra calls).
	got2, err := p.Get(context.Background(), ResourceTypeGateway,
		[]types.NamespacedName{{Namespace: "other", Name: "gw-2"}})
	assert.NoError(t, err)
	assert.Equal(t, "sg-shared", got2)
	assert.Equal(t, got1, got2, "both gateways must share the same backend SG")
}

func Test_defaultBackendSGProvider_Release(t *testing.T) {
	type deleteSGCall struct {
		err error
	}
	tests := []struct {
		name                   string
		backendSG              string
		autoGenSG              string
		objectsMap             map[string]bool // pre-populated objects map
		pendingDeletionChecker PendingDeletionChecker
		deleteSGCall           *deleteSGCall
		wantErr                string
	}{
		{
			name:      "explicit backend SG, no-op",
			backendSG: "sg-explicit",
		},
		{
			name:      "no auto-generated SG, no-op",
			autoGenSG: "",
			backendSG: "", // no explicit SG either — Release should be a no-op since autoGeneratedSG is empty
		},
		{
			name:      "auto-gen SG still required by another gateway",
			autoGenSG: "sg-autogen",
			objectsMap: map[string]bool{
				"gateway/default/gw-other": true,
			},
		},
		{
			name:      "auto-gen SG not required, deletes it",
			autoGenSG: "sg-autogen",
			objectsMap: map[string]bool{
				"gateway/default/gw-1": false,
			},
			deleteSGCall: &deleteSGCall{},
		},
		{
			name:      "delete SG fails with non-dependency error",
			autoGenSG: "sg-autogen",
			objectsMap: map[string]bool{
				"gateway/default/gw-1": false,
			},
			deleteSGCall: &deleteSGCall{
				err: &smithy.GenericAPIError{Code: "Something.Else", Message: "delete failed"},
			},
			wantErr: "failed to delete securityGroup",
		},
		{
			name:      "pending deletions exist, skips backend SG delete",
			autoGenSG: "sg-autogen",
			objectsMap: map[string]bool{
				"gateway/default/gw-1": false,
			},
			pendingDeletionChecker: &fakePendingDeletionChecker{hasPending: true},
			// No deleteSGCall — deletion should not be attempted
		},
		{
			name:      "no pending deletions, proceeds with delete",
			autoGenSG: "sg-autogen",
			objectsMap: map[string]bool{
				"gateway/default/gw-1": false,
			},
			pendingDeletionChecker: &fakePendingDeletionChecker{hasPending: false},
			deleteSGCall:           &deleteSGCall{},
		},
		{
			name:      "only current key pending (last gateway), proceeds with delete",
			autoGenSG: "sg-autogen",
			objectsMap: map[string]bool{
				"gateway/default/gw-1": false,
			},
			pendingDeletionChecker: &realPendingDeletionChecker{
				pending: map[string]struct{}{"default/gw-1": {}},
			},
			deleteSGCall: &deleteSGCall{},
		},
		{
			name:      "other keys pending besides current, skips delete",
			autoGenSG: "sg-autogen",
			objectsMap: map[string]bool{
				"gateway/default/gw-1": false,
			},
			pendingDeletionChecker: &realPendingDeletionChecker{
				pending: map[string]struct{}{
					"default/gw-1": {},
					"default/gw-2": {},
				},
			},
			// No deleteSGCall — deletion should not be attempted
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			p, ec2Client := newTestBackendSGProvider(ctrl, tt.backendSG)
			if tt.autoGenSG != "" {
				p.backendSG = ""
				p.autoGeneratedSG = tt.autoGenSG
			}
			for k, v := range tt.objectsMap {
				p.objectsMap.Store(k, v)
			}
			if tt.pendingDeletionChecker != nil {
				p.pendingDeletionChecker = tt.pendingDeletionChecker
			}

			if tt.deleteSGCall != nil {
				ec2Client.EXPECT().DeleteSecurityGroupWithContext(gomock.Any(), gomock.Any()).Return(nil, tt.deleteSGCall.err)
			}

			inactiveResources := []types.NamespacedName{{Namespace: "default", Name: "gw-1"}}
			err := p.Release(context.Background(), ResourceTypeGateway, inactiveResources)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_getObjectKey(t *testing.T) {
	tests := []struct {
		name         string
		resourceType ResourceType
		resource     types.NamespacedName
		want         string
	}{
		{
			name:         "gateway key",
			resourceType: ResourceTypeGateway,
			resource:     types.NamespacedName{Namespace: "default", Name: "my-gw"},
			want:         "gateway/default/my-gw",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getObjectKey(tt.resourceType, tt.resource))
		})
	}
}

func Test_getBackendSGName(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	p, _ := newTestBackendSGProvider(ctrl, "")

	name := p.getBackendSGName()
	assert.Contains(t, name, "elbproxy-backend-")
	assert.Contains(t, name, "testcluster")

	// Deterministic
	assert.Equal(t, name, p.getBackendSGName())
}

func Test_getBackendSGName_DifferentNamespacesProduceDifferentNames(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	pA, _ := newTestBackendSGProvider(ctrl, "")
	pA.controllerNamespace = "ns-alpha"

	pB, _ := newTestBackendSGProvider(ctrl, "")
	pB.controllerNamespace = "ns-beta"

	nameA := pA.getBackendSGName()
	nameB := pB.getBackendSGName()
	assert.NotEqual(t, nameA, nameB, "different controller namespaces must produce different SG names")
}

func Test_buildBackendSGTagsMap_IncludesControllerNS(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, _ := newTestBackendSGProvider(ctrl, "")
	p.controllerNamespace = "team-gamma"

	tags := p.buildBackendSGTagsMap()
	assert.Equal(t, "team-gamma", tags[tracking.TagKeyControllerNS])
	assert.Equal(t, testClusterName, tags[tracking.TagKeyCluster])
	assert.Equal(t, tracking.ResourceIDBackendSecurityGroup, tags[tracking.TagKeyResourceType])
}

func Test_getBackendSGFromEC2_FiltersOnControllerNS(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	p, ec2Client := newTestBackendSGProvider(ctrl, "")
	p.controllerNamespace = "team-delta"

	// Capture the DescribeSecurityGroupsInput to verify the filter includes controller-ns.
	ec2Client.EXPECT().DescribeSecurityGroupsAsList(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, input *ec2sdk.DescribeSecurityGroupsInput) ([]ec2types.SecurityGroup, error) {
			// Verify that the controller-ns filter is present.
			var foundControllerNSFilter bool
			for _, f := range input.Filters {
				if awssdk.ToString(f.Name) == "tag:"+tracking.TagKeyControllerNS {
					assert.Equal(t, []string{"team-delta"}, f.Values)
					foundControllerNSFilter = true
				}
			}
			assert.True(t, foundControllerNSFilter, "expected filter on tag:%s", tracking.TagKeyControllerNS)
			return nil, nil
		})

	sg, err := p.getBackendSGFromEC2(context.Background(), testVPCID)
	assert.NoError(t, err)
	assert.Nil(t, sg)
}

func Test_defaultBackendSGProvider_checkGatewayListForUnmapped(t *testing.T) {
	managedClass := &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "elb-proxy-aws"},
		Spec:       gwapiv1.GatewayClassSpec{ControllerName: "gateway.elbproxy.aws/gatewayclass-controller"},
	}
	otherClass := &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other-class"},
		Spec:       gwapiv1.GatewayClassSpec{ControllerName: "other-controller"},
	}
	managedGateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-1", Namespace: "default"},
		Spec:       gwapiv1.GatewaySpec{GatewayClassName: "elb-proxy-aws", Listeners: []gwapiv1.Listener{{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType}}},
	}
	otherGateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-other", Namespace: "default"},
		Spec:       gwapiv1.GatewaySpec{GatewayClassName: "other-class", Listeners: []gwapiv1.Listener{{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType}}},
	}
	orphanGateway := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-orphan", Namespace: "default"},
		Spec:       gwapiv1.GatewaySpec{GatewayClassName: "non-existent-class", Listeners: []gwapiv1.Listener{{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType}}},
	}

	tests := []struct {
		name       string
		objects    []runtime.Object
		objectsMap map[string]bool
		want       bool
	}{
		{
			name:    "managed gateway not in objectsMap returns true",
			objects: []runtime.Object{managedClass, managedGateway},
			want:    true,
		},
		{
			name:    "managed gateway already tracked returns false",
			objects: []runtime.Object{managedClass, managedGateway},
			objectsMap: map[string]bool{
				"gateway/default/gw-1": true,
			},
			want: false,
		},
		{
			name:    "only non-managed gateways returns false",
			objects: []runtime.Object{otherClass, otherGateway},
			want:    false,
		},
		{
			name:    "gateway with missing GatewayClass is skipped",
			objects: []runtime.Object{orphanGateway},
			want:    false,
		},
		{
			name:    "no gateways returns false",
			objects: []runtime.Object{managedClass},
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			p, _ := newTestBackendSGProviderWithObjects(ctrl, "", tt.objects...)
			for k, v := range tt.objectsMap {
				p.objectsMap.Store(k, v)
			}

			got, err := p.checkGatewayListForUnmapped(context.Background())
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_defaultBackendSGProvider_checkGatewayListForUnmapped_transientError(t *testing.T) {
	gw := &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-1", Namespace: "default"},
		Spec:       gwapiv1.GatewaySpec{GatewayClassName: "elb-proxy-aws", Listeners: []gwapiv1.Listener{{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType}}},
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ec2Client := services.NewMockEC2(ctrl)
	tp := tracking.NewProvider(testClusterName)
	logger := logging.DefaultLogger(os.Stdout, egv1a1.LogLevelInfo)

	scheme := runtime.NewScheme()
	_ = gwapiv1.Install(scheme)

	// Injects a transient error when Get is called for a GatewayClass.
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(gw).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*gwapiv1.GatewayClass); ok {
					return fmt.Errorf("connection refused")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	p := NewBackendSGProvider(testClusterName, "", testVPCID, ec2Client, k8sClient, tp, logger,
		elbproxyconfig.GatewayControllerName, "envoy-gateway-system", nil)

	got, err := p.checkGatewayListForUnmapped(context.Background())
	assert.NoError(t, err)
	// Transient error should assume managed — return true to avoid premature SG release.
	assert.True(t, got)
}
