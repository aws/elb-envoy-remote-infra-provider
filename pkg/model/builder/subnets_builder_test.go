// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	elbproxyaws "github.com/aws/elb-envoy-remote-infra-provider/pkg/aws"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
)

func TestMapIpv4AddressabilityToSubnetAddressability(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    networking.SubnetAddressability
		wantErr string
	}{
		{name: "internal", input: "internal", want: networking.SubnetAddressabilityInternal},
		{name: "internet-facing", input: "internet-facing", want: networking.SubnetAddressabilityInternetFacing},
		{name: "unknown value", input: "public", wantErr: `unknown ipv4Addressability value: "public"`},
		{name: "empty string", input: "", wantErr: `unknown ipv4Addressability value: ""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mapIpv4AddressabilityToSubnetAddressability(tt.input)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestExtractSubnetIDs(t *testing.T) {
	tests := []struct {
		name    string
		subnets []elbproxyv1alpha1.SubnetDetails
		want    []string
	}{
		{name: "nil input", subnets: nil, want: nil},
		{name: "empty input", subnets: []elbproxyv1alpha1.SubnetDetails{}, want: nil},
		{
			name: "filters empty IDs",
			subnets: []elbproxyv1alpha1.SubnetDetails{
				{SubnetId: "subnet-aaa"},
				{SubnetId: ""},
				{SubnetId: "subnet-bbb"},
			},
			want: []string{"subnet-aaa", "subnet-bbb"},
		},
		{
			name:    "all empty IDs",
			subnets: []elbproxyv1alpha1.SubnetDetails{{SubnetId: ""}, {SubnetId: ""}},
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSubnetIDs(tt.subnets)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveSubnets(t *testing.T) {
	tp := tracking.NewProvider("test-cluster")
	stackID := "default/my-gw"
	expectedTagFilter := tracking.TagsAsTagFilter(tp.StackTags(stackID))

	type listProxiesCall struct {
		resp []elbproxy.ProxyWithTags
		err  error
	}
	type resolveByIDCall struct {
		resp []ec2types.Subnet
		err  error
	}
	type discoveryCall struct {
		resp []ec2types.Subnet
		err  error
	}
	tests := []struct {
		name               string
		subnets            []elbproxyv1alpha1.SubnetDetails
		ipv4Addressability string
		ipv6Required       bool
		listProxies        *listProxiesCall
		resolveByID        *resolveByIDCall
		discovery          *discoveryCall
		wantIDs            []string
		wantErr            string
	}{
		{
			name: "explicit subnet IDs provided resolves via ResolveViaNameOrIDSlice",
			subnets: []elbproxyv1alpha1.SubnetDetails{
				{SubnetId: "subnet-aaa"},
				{SubnetId: "subnet-bbb"},
			},
			ipv4Addressability: "internal",
			resolveByID: &resolveByIDCall{
				resp: []ec2types.Subnet{
					{SubnetId: awssdk.String("subnet-aaa")},
					{SubnetId: awssdk.String("subnet-bbb")},
				},
			},
			wantIDs: []string{"subnet-aaa", "subnet-bbb"},
		},
		{
			name:               "no explicit subnets and no existing proxy falls through to discovery",
			ipv4Addressability: "internet-facing",
			listProxies:        &listProxiesCall{resp: nil},
			discovery: &discoveryCall{
				resp: []ec2types.Subnet{
					{SubnetId: awssdk.String("subnet-discovered-a")},
					{SubnetId: awssdk.String("subnet-discovered-b")},
				},
			},
			wantIDs: []string{"subnet-discovered-a", "subnet-discovered-b"},
		},
		{
			name:               "ipv6 required is threaded into discovery",
			ipv4Addressability: "internet-facing",
			ipv6Required:       true,
			listProxies:        &listProxiesCall{resp: nil},
			discovery: &discoveryCall{
				resp: []ec2types.Subnet{
					{SubnetId: awssdk.String("subnet-discovered-a")},
					{SubnetId: awssdk.String("subnet-discovered-b")},
				},
			},
			wantIDs: []string{"subnet-discovered-a", "subnet-discovered-b"},
		},
		{
			name: "ipv6 required is threaded into explicit subnet resolution",
			subnets: []elbproxyv1alpha1.SubnetDetails{
				{SubnetId: "subnet-aaa"},
			},
			ipv4Addressability: "internal",
			ipv6Required:       true,
			resolveByID: &resolveByIDCall{
				resp: []ec2types.Subnet{
					{SubnetId: awssdk.String("subnet-aaa")},
				},
			},
			wantIDs: []string{"subnet-aaa"},
		},
		{
			name: "empty subnet IDs treated as no explicit subnets and falls through to discovery",
			subnets: []elbproxyv1alpha1.SubnetDetails{
				{SubnetId: ""},
				{SubnetId: ""},
			},
			ipv4Addressability: "internal",
			listProxies:        &listProxiesCall{resp: nil},
			discovery: &discoveryCall{
				resp: []ec2types.Subnet{
					{SubnetId: awssdk.String("subnet-disc")},
				},
			},
			wantIDs: []string{"subnet-disc"},
		},
		{
			name: "invalid ipv4Addressability returns error before any resolver call",
			subnets: []elbproxyv1alpha1.SubnetDetails{
				{SubnetId: "subnet-aaa"},
			},
			ipv4Addressability: "invalid-scheme",
			wantErr:            "unknown ipv4Addressability value",
		},
		{
			name: "ResolveViaNameOrIDSlice returns error and error is propagated",
			subnets: []elbproxyv1alpha1.SubnetDetails{
				{SubnetId: "subnet-aaa"},
			},
			ipv4Addressability: "internal",
			resolveByID: &resolveByIDCall{
				err: errors.New("subnet resolution failed"),
			},
			wantErr: "subnet resolution failed",
		},
		{
			name:               "ResolveViaDiscovery returns error and error is propagated",
			ipv4Addressability: "internal",
			listProxies:        &listProxiesCall{resp: nil},
			discovery: &discoveryCall{
				err: errors.New("discovery failed"),
			},
			wantErr: "discovery failed",
		},
		{
			name:               "existing proxy found reuses its subnets via ResolveViaNameOrIDSlice",
			ipv4Addressability: "internet-facing",
			listProxies: &listProxiesCall{resp: []elbproxy.ProxyWithTags{{
				ProxyDetails: elbproxyaws.ProxyDetails{
					NetworkConfiguration: elbproxyaws.NetworkConfiguration{
						VpcConfiguration: elbproxyaws.VpcConfiguration{
							AvailabilityZones: []elbproxyaws.SubnetAndZone{
								{SubnetId: "subnet-existing-a", ZoneId: "us-east-1a"},
								{SubnetId: "subnet-existing-b", ZoneId: "us-east-1b"},
							},
						},
					},
				},
			}}},
			resolveByID: &resolveByIDCall{
				resp: []ec2types.Subnet{
					{SubnetId: awssdk.String("subnet-existing-a")},
					{SubnetId: awssdk.String("subnet-existing-b")},
				},
			},
			wantIDs: []string{"subnet-existing-a", "subnet-existing-b"},
		},
		{
			name:               "existing proxy found but has empty AZs falls through to discovery",
			ipv4Addressability: "internal",
			listProxies: &listProxiesCall{resp: []elbproxy.ProxyWithTags{{
				ProxyDetails: elbproxyaws.ProxyDetails{
					NetworkConfiguration: elbproxyaws.NetworkConfiguration{
						VpcConfiguration: elbproxyaws.VpcConfiguration{
							AvailabilityZones: nil,
						},
					},
				},
			}}},
			discovery: &discoveryCall{
				resp: []ec2types.Subnet{
					{SubnetId: awssdk.String("subnet-disc")},
				},
			},
			wantIDs: []string{"subnet-disc"},
		},
		{
			name:               "ListProxies returns error and error is propagated",
			ipv4Addressability: "internal",
			listProxies:        &listProxiesCall{err: errors.New("list proxies failed")},
			wantErr:            "list proxies failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockResolver := networking.NewMockSubnetsResolver(ctrl)
			mockTaggingMgr := elbproxy.NewMockTaggingManager(ctrl)

			if tt.listProxies != nil {
				mockTaggingMgr.EXPECT().
					ListProxies(gomock.Any(), expectedTagFilter).
					Return(tt.listProxies.resp, tt.listProxies.err)
			}
			assertIPv6Required := func(opts []networking.SubnetsResolveOption) {
				resolveOpts := networking.SubnetsResolveOptions{}
				resolveOpts.ApplyOptions(opts)
				assert.Equal(t, tt.ipv6Required, resolveOpts.IPv6Required)
			}
			if tt.resolveByID != nil {
				mockResolver.EXPECT().
					ResolveViaNameOrIDSlice(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, _ []string, opts ...networking.SubnetsResolveOption) ([]ec2types.Subnet, error) {
						assertIPv6Required(opts)
						return tt.resolveByID.resp, tt.resolveByID.err
					})
			}
			if tt.discovery != nil {
				mockResolver.EXPECT().
					ResolveViaDiscovery(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, opts ...networking.SubnetsResolveOption) ([]ec2types.Subnet, error) {
						assertIPv6Required(opts)
						return tt.discovery.resp, tt.discovery.err
					})
			}

			b := newELBProxySubnetsBuilder(mockResolver, tp, mockTaggingMgr)

			got, err := b.resolveSubnets(context.Background(), tt.subnets, tt.ipv4Addressability, tt.ipv6Required, stackID)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				var gotIDs []string
				for _, s := range got {
					gotIDs = append(gotIDs, awssdk.ToString(s.SubnetId))
				}
				assert.Equal(t, tt.wantIDs, gotIDs)
			}
		})
	}
}
