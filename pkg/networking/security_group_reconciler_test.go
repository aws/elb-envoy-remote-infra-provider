// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func Test_defaultSecurityGroupReconciler_ReconcileIngress(t *testing.T) {
	type fetchSGInfosByIDCall struct {
		resp map[string]SecurityGroupInfo
		err  error
	}
	type authorizeSGIngressCall struct {
		err error
	}
	type revokeSGIngressCall struct {
		err error
	}

	type fields struct {
		fetchCalls     []fetchSGInfosByIDCall
		authorizeCalls []authorizeSGIngressCall
		revokeCalls    []revokeSGIngressCall
	}
	type args struct {
		sgID               string
		desiredPermissions []IPPermissionInfo
		opts               []SecurityGroupReconcileOption
	}

	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr error
	}{
		{
			name: "grants missing permissions",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {SecurityGroupID: "sg-aaa", Ingress: nil},
					}},
				},
				authorizeCalls: []authorizeSGIngressCall{
					{},
				},
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
			},
		},
		{
			name: "revokes extra permissions",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {
							SecurityGroupID: "sg-aaa",
							Ingress: []IPPermissionInfo{
								testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
								testCIDRPermission("tcp", 443, 443, "0.0.0.0/0"),
							},
						},
					}},
				},
				revokeCalls: []revokeSGIngressCall{
					{},
				},
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
			},
		},
		{
			name: "no-op when permissions match",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {
							SecurityGroupID: "sg-aaa",
							Ingress: []IPPermissionInfo{
								testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
							},
						},
					}},
				},
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
			},
		},
		{
			name: "returns error when FetchSGInfosByID fails",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					{err: errors.New("fetch failed")},
				},
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
			},
			wantErr: errors.New("fetch failed"),
		},
		{
			name: "AuthorizeOnly skips revoke",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {
							SecurityGroupID: "sg-aaa",
							Ingress: []IPPermissionInfo{
								testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
								testCIDRPermission("tcp", 443, 443, "0.0.0.0/0"),
							},
						},
					}},
				},
				// no revoke calls expected, no authorize calls since desired is subset of existing
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
				opts: []SecurityGroupReconcileOption{WithAuthorizeOnly(true)},
			},
		},
		{
			name: "PermissionSelector filters which permissions to revoke",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {
							SecurityGroupID: "sg-aaa",
							Ingress: []IPPermissionInfo{
								testCIDRPermissionWithLabels("tcp", 80, 80, "10.0.0.0/8", map[string]string{"managed": "true"}),
								testCIDRPermissionWithLabels("tcp", 443, 443, "0.0.0.0/0", map[string]string{"managed": "false"}),
							},
						},
					}},
				},
				// only the "managed=true" permission matches the selector, so only that one gets revoked
				revokeCalls: []revokeSGIngressCall{
					{},
				},
			},
			args: args{
				sgID:               "sg-aaa",
				desiredPermissions: nil,
				opts: []SecurityGroupReconcileOption{
					WithPermissionSelector(labels.SelectorFromSet(labels.Set{"managed": "true"})),
				},
			},
		},
		{
			name: "retries with cache bypass on InvalidPermission.Duplicate",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					// first fetch (cached)
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {SecurityGroupID: "sg-aaa", Ingress: nil},
					}},
					// retry fetch (cache bypass)
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {
							SecurityGroupID: "sg-aaa",
							Ingress: []IPPermissionInfo{
								testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
							},
						},
					}},
				},
				authorizeCalls: []authorizeSGIngressCall{
					// first attempt fails with Duplicate
					{err: &smithy.GenericAPIError{Code: "InvalidPermission.Duplicate", Message: "rule already exists"}},
				},
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
			},
			// retry succeeds because refreshed state shows permission already exists (no-op)
		},
		{
			name: "retries with revoke-first on RulesPerSecurityGroupLimitExceeded",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					// first fetch
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {
							SecurityGroupID: "sg-aaa",
							Ingress: []IPPermissionInfo{
								testCIDRPermission("tcp", 443, 443, "0.0.0.0/0"),
							},
						},
					}},
					// retry fetch
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {
							SecurityGroupID: "sg-aaa",
							Ingress: []IPPermissionInfo{
								testCIDRPermission("tcp", 443, 443, "0.0.0.0/0"),
							},
						},
					}},
				},
				authorizeCalls: []authorizeSGIngressCall{
					// first attempt fails
					{err: &smithy.GenericAPIError{Code: "RulesPerSecurityGroupLimitExceeded", Message: "too many rules"}},
					// retry authorize succeeds
					{},
				},
				revokeCalls: []revokeSGIngressCall{
					// revoke happens first on retry
					{},
				},
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
			},
		},
		{
			name: "does not retry on non-retryable error",
			fields: fields{
				fetchCalls: []fetchSGInfosByIDCall{
					{resp: map[string]SecurityGroupInfo{
						"sg-aaa": {SecurityGroupID: "sg-aaa", Ingress: nil},
					}},
				},
				authorizeCalls: []authorizeSGIngressCall{
					{err: errors.New("some other error")},
				},
			},
			args: args{
				sgID: "sg-aaa",
				desiredPermissions: []IPPermissionInfo{
					testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				},
			},
			wantErr: errors.New("some other error"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			sgManager := NewMockSecurityGroupManager(ctrl)

			fetchIdx := 0
			sgManager.EXPECT().FetchSGInfosByID(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, _ []string, _ ...FetchSGInfoOption) (map[string]SecurityGroupInfo, error) {
					if fetchIdx >= len(tt.fields.fetchCalls) {
						t.Fatalf("unexpected FetchSGInfosByID call #%d", fetchIdx)
					}
					call := tt.fields.fetchCalls[fetchIdx]
					fetchIdx++
					return call.resp, call.err
				}).AnyTimes()

			authIdx := 0
			if len(tt.fields.authorizeCalls) > 0 {
				sgManager.EXPECT().AuthorizeSGIngress(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, _ []IPPermissionInfo) error {
						if authIdx >= len(tt.fields.authorizeCalls) {
							t.Fatalf("unexpected AuthorizeSGIngress call #%d", authIdx)
						}
						call := tt.fields.authorizeCalls[authIdx]
						authIdx++
						return call.err
					}).AnyTimes()
			}

			revokeIdx := 0
			if len(tt.fields.revokeCalls) > 0 {
				sgManager.EXPECT().RevokeSGIngress(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, _ []IPPermissionInfo) error {
						if revokeIdx >= len(tt.fields.revokeCalls) {
							t.Fatalf("unexpected RevokeSGIngress call #%d", revokeIdx)
						}
						call := tt.fields.revokeCalls[revokeIdx]
						revokeIdx++
						return call.err
					}).AnyTimes()
			}

			reconciler := NewDefaultSecurityGroupReconciler(sgManager, log.Log.WithName("test"))
			err := reconciler.ReconcileIngress(context.Background(), tt.args.sgID, tt.args.desiredPermissions, tt.args.opts...)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_diffIPPermissionInfos(t *testing.T) {
	tests := []struct {
		name    string
		source  []IPPermissionInfo
		target  []IPPermissionInfo
		wantLen int
	}{
		{
			name:    "empty source and target",
			source:  nil,
			target:  nil,
			wantLen: 0,
		},
		{
			name: "source has extra permissions",
			source: []IPPermissionInfo{
				testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
				testCIDRPermission("tcp", 443, 443, "0.0.0.0/0"),
			},
			target: []IPPermissionInfo{
				testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
			},
			wantLen: 1,
		},
		{
			name: "identical sets return empty diff",
			source: []IPPermissionInfo{
				testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
			},
			target: []IPPermissionInfo{
				testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
			},
			wantLen: 0,
		},
		{
			name:   "source empty returns empty diff",
			source: nil,
			target: []IPPermissionInfo{
				testCIDRPermission("tcp", 80, 80, "10.0.0.0/8"),
			},
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffIPPermissionInfos(tt.source, tt.target)
			assert.Len(t, got, tt.wantLen)
		})
	}
}

func Test_shouldRetryWithoutCache(t *testing.T) {
	r := &defaultSecurityGroupReconciler{}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"InvalidPermission.Duplicate", &smithy.GenericAPIError{Code: "InvalidPermission.Duplicate"}, true},
		{"InvalidPermission.NotFound", &smithy.GenericAPIError{Code: "InvalidPermission.NotFound"}, true},
		{"RulesPerSecurityGroupLimitExceeded", &smithy.GenericAPIError{Code: "RulesPerSecurityGroupLimitExceeded"}, true},
		{"other API error", &smithy.GenericAPIError{Code: "SomeOtherError"}, false},
		{"non-API error", errors.New("random"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, r.shouldRetryWithoutCache(tt.err))
		})
	}
}

func Test_shouldRemoveSGRulesFirst(t *testing.T) {
	r := &defaultSecurityGroupReconciler{}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"RulesPerSecurityGroupLimitExceeded", &smithy.GenericAPIError{Code: "RulesPerSecurityGroupLimitExceeded"}, true},
		{"InvalidPermission.Duplicate", &smithy.GenericAPIError{Code: "InvalidPermission.Duplicate"}, false},
		{"non-API error", errors.New("random"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, r.shouldRemoveSGRulesFirst(tt.err))
		})
	}
}

func testCIDRPermission(protocol string, fromPort, toPort int32, cidr string) IPPermissionInfo {
	return IPPermissionInfo{
		Permission: ec2types.IpPermission{
			IpProtocol: awssdk.String(protocol),
			FromPort:   awssdk.Int32(fromPort),
			ToPort:     awssdk.Int32(toPort),
			IpRanges:   []ec2types.IpRange{{CidrIp: awssdk.String(cidr)}},
		},
		Labels: map[string]string{},
	}
}

func testCIDRPermissionWithLabels(protocol string, fromPort, toPort int32, cidr string, lbls map[string]string) IPPermissionInfo {
	return IPPermissionInfo{
		Permission: ec2types.IpPermission{
			IpProtocol: awssdk.String(protocol),
			FromPort:   awssdk.Int32(fromPort),
			ToPort:     awssdk.Int32(toPort),
			IpRanges:   []ec2types.IpRange{{CidrIp: awssdk.String(cidr)}},
		},
		Labels: lbls,
	}
}

func testGroupIDPermissionWithLabels(protocol string, fromPort, toPort int32, groupID string, lbls map[string]string) IPPermissionInfo {
	return IPPermissionInfo{
		Permission: ec2types.IpPermission{
			IpProtocol:       awssdk.String(protocol),
			FromPort:         awssdk.Int32(fromPort),
			ToPort:           awssdk.Int32(toPort),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: awssdk.String(groupID)}},
		},
		Labels: lbls,
	}
}

// Test_ReconcileIngress_MultiTenantIsolation proves that two controllers sharing the
// same node SG won't revoke each other's rules because the PermissionSelector is scoped
// by controller-ns.
//
// Scenario:
//   - Controller in ns-alpha created a rule: (tcp, 8080, sg-backend-alpha) with labels {managed-by, controller-ns=ns-alpha}
//   - Controller in ns-beta created a rule: (tcp, 9090, sg-backend-beta) with labels {managed-by, controller-ns=ns-beta}
//   - Controller ns-beta reconciles with empty desired state (all gateways removed)
//   - Expected: ns-beta's rule is revoked, ns-alpha's rule is NOT revoked
func Test_ReconcileIngress_MultiTenantIsolation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	labelsAlpha := map[string]string{
		"gateway.elbproxy.aws/managed-by":    "elb-envoy-remote-infra-provider",
		"gateway.elbproxy.aws/controller-ns": "ns-alpha",
	}
	labelsBeta := map[string]string{
		"gateway.elbproxy.aws/managed-by":    "elb-envoy-remote-infra-provider",
		"gateway.elbproxy.aws/controller-ns": "ns-beta",
	}

	// Both controllers' rules exist on the shared node SG.
	existingRules := []IPPermissionInfo{
		testGroupIDPermissionWithLabels("tcp", 8080, 8080, "sg-backend-alpha", labelsAlpha),
		testGroupIDPermissionWithLabels("tcp", 9090, 9090, "sg-backend-beta", labelsBeta),
	}

	sgManager := NewMockSecurityGroupManager(ctrl)
	sgManager.EXPECT().FetchSGInfosByID(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(map[string]SecurityGroupInfo{
			"sg-node": {SecurityGroupID: "sg-node", Ingress: existingRules},
		}, nil)

	// Only ns-beta's rule should be revoked.
	sgManager.EXPECT().RevokeSGIngress(gomock.Any(), "sg-node", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, perms []IPPermissionInfo) error {
			assert.Len(t, perms, 1, "should only revoke ns-beta's rule")
			assert.Equal(t, "sg-backend-beta", awssdk.ToString(perms[0].Permission.UserIdGroupPairs[0].GroupId))
			return nil
		})

	reconciler := NewDefaultSecurityGroupReconciler(sgManager, log.Log.WithName("test"))

	// ns-beta reconciles with empty desired (all gateways gone), scoped to its own labels.
	err := reconciler.ReconcileIngress(context.Background(), "sg-node", nil,
		WithPermissionSelector(labels.SelectorFromSet(labelsBeta)))
	assert.NoError(t, err)
}

// Test_ReconcileIngress_MultiTenantIsolation_SamePort is the harder variant of the
// isolation test: both tenants open the SAME port (tcp/8080) on the shared node SG.
// The two rules are distinct AWS rules only because each references its own per-tenant
// backend SG as the source. This verifies that when one tenant reconciles to empty,
// the same-port rule of the OTHER tenant is not revoked — the per-tenant backend SG
// source plus the controller-ns label selector together keep them separate.
//
// Scenario:
//   - ns-alpha rule: (tcp, 8080, sg-backend-alpha) labels {managed-by, controller-ns=ns-alpha}
//   - ns-beta  rule: (tcp, 8080, sg-backend-beta)  labels {managed-by, controller-ns=ns-beta}
//   - ns-beta reconciles with empty desired
//   - Expected: only the (tcp, 8080, sg-backend-beta) rule is revoked; ns-alpha's
//     identical-port rule is untouched.
func Test_ReconcileIngress_MultiTenantIsolation_SamePort(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	labelsAlpha := map[string]string{
		"gateway.elbproxy.aws/managed-by":    "elb-envoy-remote-infra-provider",
		"gateway.elbproxy.aws/controller-ns": "ns-alpha",
	}
	labelsBeta := map[string]string{
		"gateway.elbproxy.aws/managed-by":    "elb-envoy-remote-infra-provider",
		"gateway.elbproxy.aws/controller-ns": "ns-beta",
	}

	// Both tenants opened the SAME port 8080, distinguished only by their per-tenant
	// backend SG source.
	existingRules := []IPPermissionInfo{
		testGroupIDPermissionWithLabels("tcp", 8080, 8080, "sg-backend-alpha", labelsAlpha),
		testGroupIDPermissionWithLabels("tcp", 8080, 8080, "sg-backend-beta", labelsBeta),
	}

	sgManager := NewMockSecurityGroupManager(ctrl)
	sgManager.EXPECT().FetchSGInfosByID(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(map[string]SecurityGroupInfo{
			"sg-node": {SecurityGroupID: "sg-node", Ingress: existingRules},
		}, nil)

	// Only ns-beta's same-port rule should be revoked; ns-alpha's identical-port
	// rule must remain because it carries a different controller-ns label and a
	// different backend SG source.
	sgManager.EXPECT().RevokeSGIngress(gomock.Any(), "sg-node", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, perms []IPPermissionInfo) error {
			assert.Len(t, perms, 1, "should only revoke ns-beta's same-port rule")
			assert.Equal(t, "sg-backend-beta", awssdk.ToString(perms[0].Permission.UserIdGroupPairs[0].GroupId),
				"must revoke the beta-sourced rule, never alpha's identical-port rule")
			assert.Equal(t, int32(8080), awssdk.ToInt32(perms[0].Permission.FromPort))
			return nil
		})

	reconciler := NewDefaultSecurityGroupReconciler(sgManager, log.Log.WithName("test"))

	err := reconciler.ReconcileIngress(context.Background(), "sg-node", nil,
		WithPermissionSelector(labels.SelectorFromSet(labelsBeta)))
	assert.NoError(t, err)
}
