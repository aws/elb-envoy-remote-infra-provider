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
	"k8s.io/utils/ptr"
)

func Test_proxyAttributeReconciler_Reconcile(t *testing.T) {
	type listProxyAttributesCall struct {
		req  elbproxyaws.GetProxyAttributesInput
		resp *elbproxyaws.GetProxyAttributesOutput
		err  error
	}
	type updateProxyAttributesCall struct {
		req  elbproxyaws.UpdateProxyAttributesInput
		resp *elbproxyaws.UpdateProxyAttributesOutput
		err  error
	}
	type fields struct {
		listProxyAttributesCalls   []listProxyAttributesCall
		updateProxyAttributesCalls []updateProxyAttributesCall
	}

	listReq := elbproxyaws.GetProxyAttributesInput{ProxyIdentifier: "arn:proxy-1"}

	tests := []struct {
		name         string
		desiredAttrs []elbproxyaws.Attribute
		fields       fields
		wantErr      string
	}{
		{
			name: "empty desired attributes with existing current attributes results in no update",
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false", DefaultValue: ptr.To("false")},
							},
						},
					},
				},
			},
		},
		{
			name: "desired attributes match current attributes results in no update",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false", DefaultValue: ptr.To("false")},
							},
						},
					},
				},
			},
		},
		{
			name: "desired attribute differs from current triggers update with only changed attribute",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
				{Key: elbproxyaws.AttributeKeyWafFailOpenEnabled, Value: "true"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false", DefaultValue: ptr.To("false")},
								{Key: elbproxyaws.AttributeKeyWafFailOpenEnabled, Value: "true", DefaultValue: ptr.To("false")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
							},
						},
						resp: &elbproxyaws.UpdateProxyAttributesOutput{},
					},
				},
			},
		},
		{
			name: "desired attribute not in current triggers update with new attribute",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyWafWebAclArn, Value: "arn:aws:wafv2:us-east-1:123456789:regional/webacl/my-acl/id"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false", DefaultValue: ptr.To("false")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyWafWebAclArn, Value: "arn:aws:wafv2:us-east-1:123456789:regional/webacl/my-acl/id"},
							},
						},
						resp: &elbproxyaws.UpdateProxyAttributesOutput{},
					},
				},
			},
		},
		{
			name: "GetProxyAttributes returns error and error is propagated",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{req: listReq, err: fmt.Errorf("list failed")},
				},
			},
			wantErr: "failed to list proxy attributes",
		},
		{
			name: "UpdateProxyAttributes returns error and error is propagated",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false", DefaultValue: ptr.To("false")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true"},
							},
						},
						err: fmt.Errorf("update failed"),
					},
				},
			},
			wantErr: "failed to update proxy attributes",
		},
		{
			name:         "attribute removed from CRD is reset to default value",
			desiredAttrs: []elbproxyaws.Attribute{},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "true", DefaultValue: ptr.To("false")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false"},
							},
						},
						resp: &elbproxyaws.UpdateProxyAttributesOutput{},
					},
				},
			},
		},
		{
			name:         "attribute removed from CRD already at default value results in no update",
			desiredAttrs: []elbproxyaws.Attribute{},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyDeletionProtectionEnabled, Value: "false", DefaultValue: ptr.To("false")},
							},
						},
					},
				},
			},
		},
		{
			name:         "attribute removed from CRD without default value is not reset",
			desiredAttrs: []elbproxyaws.Attribute{},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyWafWebAclArn, Value: "arn:aws:wafv2:us-east-1:123456789:regional/webacl/my-acl/id"},
							},
						},
					},
				},
			},
		},
		{
			name: "envoy log level changed triggers update",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "trace"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug", DefaultValue: ptr.To("warning")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "trace"},
							},
						},
						resp: &elbproxyaws.UpdateProxyAttributesOutput{},
					},
				},
			},
		},
		{
			name: "envoy log level set from default triggers update",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "warning", DefaultValue: ptr.To("warning")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug"},
							},
						},
						resp: &elbproxyaws.UpdateProxyAttributesOutput{},
					},
				},
			},
		},
		{
			name:         "envoy log level removed from EnvoyProxy is reset to warning default",
			desiredAttrs: []elbproxyaws.Attribute{},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug", DefaultValue: ptr.To("warning")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "warning"},
							},
						},
						resp: &elbproxyaws.UpdateProxyAttributesOutput{},
					},
				},
			},
		},
		{
			name: "envoy log level unchanged results in no update",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "warning"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "warning", DefaultValue: ptr.To("warning")},
							},
						},
					},
				},
			},
		},
		{
			name: "component log level removed is reset to empty default",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								{Key: elbproxyaws.AttributeKeyEnvoyLogLevel, Value: "debug", DefaultValue: ptr.To("warning")},
								{Key: elbproxyaws.AttributeKeyEnvoyComponentLogLevel, Value: "http:debug", DefaultValue: ptr.To("")},
							},
						},
					},
				},
				updateProxyAttributesCalls: []updateProxyAttributesCall{
					{
						req: elbproxyaws.UpdateProxyAttributesInput{
							ProxyIdentifier: "arn:proxy-1",
							Attributes: []elbproxyaws.Attribute{
								{Key: elbproxyaws.AttributeKeyEnvoyComponentLogLevel, Value: ""},
							},
						},
						resp: &elbproxyaws.UpdateProxyAttributesOutput{},
					},
				},
			},
		},
		{
			name: "read-only attribute changed in CRD returns error",
			desiredAttrs: []elbproxyaws.Attribute{
				{Key: elbproxyaws.AttributeKeyWafWebAclArn, Value: "arn:aws:wafv2:us-east-1:123456789:regional/webacl/my-acl/id"},
			},
			fields: fields{
				listProxyAttributesCalls: []listProxyAttributesCall{
					{
						req: listReq,
						resp: &elbproxyaws.GetProxyAttributesOutput{
							Attributes: []elbproxyaws.AttributeDetails{
								// No DefaultValue means read-only
								{Key: elbproxyaws.AttributeKeyWafWebAclArn, Value: ""},
							},
						},
					},
				},
			},
			wantErr: `attribute "waf.web_acl_arn" is read-only and cannot be changed`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := elbproxyaws.NewMockELBProxyClient(ctrl)

			for _, call := range tt.fields.listProxyAttributesCalls {
				mockClient.EXPECT().GetProxyAttributes(gomock.Any(), call.req).
					Return(call.resp, call.err)
			}
			for _, call := range tt.fields.updateProxyAttributesCalls {
				mockClient.EXPECT().UpdateProxyAttributes(gomock.Any(), call.req).
					Return(call.resp, call.err)
			}

			reconciler := NewDefaultProxyAttributeReconciler(mockClient, testLogger())

			stack := core.NewDefaultStack(core.StackID{Namespace: "default", Name: "my-gw"})
			resProxy, err := elbproxymodel.NewProxyResource(stack, "my-proxy")
			assert.NoError(t, err)
			resProxy.Attributes = tt.desiredAttrs

			sdkProxy := ProxyWithTags{
				ProxyDetails: elbproxyaws.ProxyDetails{Arn: "arn:proxy-1"},
			}

			err = reconciler.Reconcile(context.Background(), resProxy, sdkProxy)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
