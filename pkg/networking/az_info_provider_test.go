// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func Test_defaultAZInfoProvider_FetchAZInfos(t *testing.T) {
	type describeAZCall struct {
		resp []ec2types.AvailabilityZone
		err  error
	}
	type fetchAZInfoCall struct {
		azIDs   []string
		want    map[string]ec2types.AvailabilityZone
		wantErr error
	}
	tests := []struct {
		name             string
		describeAZCalls  []describeAZCall
		fetchAZInfoCalls []fetchAZInfoCall
	}{
		{
			name: "fetch AZInfo for two AZ sequentially",
			describeAZCalls: []describeAZCall{
				{
					resp: []ec2types.AvailabilityZone{
						{ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
					},
				},
				{
					resp: []ec2types.AvailabilityZone{
						{ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
			fetchAZInfoCalls: []fetchAZInfoCall{
				{
					azIDs: []string{"usw2-az1"},
					want: map[string]ec2types.AvailabilityZone{
						"usw2-az1": {ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
					},
				},
				{
					azIDs: []string{"usw2-az2"},
					want: map[string]ec2types.AvailabilityZone{
						"usw2-az2": {ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
		},
		{
			name: "fetch AZInfo for two AZ in batch",
			describeAZCalls: []describeAZCall{
				{
					resp: []ec2types.AvailabilityZone{
						{ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
						{ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
			fetchAZInfoCalls: []fetchAZInfoCall{
				{
					azIDs: []string{"usw2-az1", "usw2-az2"},
					want: map[string]ec2types.AvailabilityZone{
						"usw2-az1": {ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
						"usw2-az2": {ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
		},
		{
			name: "fetch AZInfo for one AZ first then two AZ in batch - second call uses cache for first AZ",
			describeAZCalls: []describeAZCall{
				{
					resp: []ec2types.AvailabilityZone{
						{ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
					},
				},
				{
					resp: []ec2types.AvailabilityZone{
						{ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
			fetchAZInfoCalls: []fetchAZInfoCall{
				{
					azIDs: []string{"usw2-az1"},
					want: map[string]ec2types.AvailabilityZone{
						"usw2-az1": {ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
					},
				},
				{
					azIDs: []string{"usw2-az1", "usw2-az2"},
					want: map[string]ec2types.AvailabilityZone{
						"usw2-az1": {ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
						"usw2-az2": {ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ec2Client := services.NewMockEC2(ctrl)
			for _, call := range tt.describeAZCalls {
				ec2Client.EXPECT().DescribeAvailabilityZonesAsList(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}

			p := NewDefaultAZInfoProvider(ec2Client, log.Log.WithName("test"))

			for _, call := range tt.fetchAZInfoCalls {
				got, err := p.FetchAZInfos(context.Background(), call.azIDs)
				if call.wantErr != nil {
					assert.EqualError(t, err, call.wantErr.Error())
				} else {
					assert.NoError(t, err)
					assert.Equal(t, call.want, got)
				}
			}
		})
	}
}

func Test_defaultAZInfoProvider_fetchAZInfosFromAWS(t *testing.T) {
	type describeAZCall struct {
		resp []ec2types.AvailabilityZone
		err  error
	}
	tests := []struct {
		name            string
		describeAZCalls []describeAZCall
		azIDs           []string
		want            map[string]ec2types.AvailabilityZone
		wantErr         error
	}{
		{
			name: "successfully fetched 1 AZ info",
			describeAZCalls: []describeAZCall{
				{
					resp: []ec2types.AvailabilityZone{
						{ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
			azIDs: []string{"usw2-az1"},
			want: map[string]ec2types.AvailabilityZone{
				"usw2-az1": {ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
			},
		},
		{
			name: "successfully fetched 2 AZ info",
			describeAZCalls: []describeAZCall{
				{
					resp: []ec2types.AvailabilityZone{
						{ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
						{ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
					},
				},
			},
			azIDs: []string{"usw2-az1", "usw2-az2"},
			want: map[string]ec2types.AvailabilityZone{
				"usw2-az1": {ZoneId: awssdk.String("usw2-az1"), ZoneType: awssdk.String("availability-zone")},
				"usw2-az2": {ZoneId: awssdk.String("usw2-az2"), ZoneType: awssdk.String("availability-zone")},
			},
		},
		{
			name: "failed to fetch AZ info with invalid AZ ID",
			describeAZCalls: []describeAZCall{
				{err: errors.New("Invalid availability zone-id: wrong-az-id")},
			},
			azIDs:   []string{"usw2-az1", "wrong-az-id"},
			wantErr: errors.New("Invalid availability zone-id: wrong-az-id"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ec2Client := services.NewMockEC2(ctrl)
			for _, call := range tt.describeAZCalls {
				ec2Client.EXPECT().DescribeAvailabilityZonesAsList(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}

			p := NewDefaultAZInfoProvider(ec2Client, log.Log.WithName("test"))
			got, err := p.fetchAZInfosFromAWS(context.Background(), tt.azIDs)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func Test_computeAZIDsWithoutAZInfo(t *testing.T) {
	tests := []struct {
		name         string
		azIDs        []string
		azInfoByAZID map[string]ec2types.AvailabilityZone
		want         []string
	}{
		{
			name:  "all AZs have AZInfo",
			azIDs: []string{"usw2-az1", "usw2-az2"},
			azInfoByAZID: map[string]ec2types.AvailabilityZone{
				"usw2-az1": {},
				"usw2-az2": {},
			},
			want: []string{},
		},
		{
			name:  "some AZ don't have AZInfo",
			azIDs: []string{"usw2-az1", "usw2-az2"},
			azInfoByAZID: map[string]ec2types.AvailabilityZone{
				"usw2-az1": {},
			},
			want: []string{"usw2-az2"},
		},
		{
			name:         "no AZs have AZInfo",
			azIDs:        []string{"usw2-az1", "usw2-az2"},
			azInfoByAZID: nil,
			want:         []string{"usw2-az1", "usw2-az2"},
		},
		{
			name:         "empty inputs",
			azIDs:        nil,
			azInfoByAZID: nil,
			want:         []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeAZIDsWithoutAZInfo(tt.azIDs, tt.azInfoByAZID)
			assert.Equal(t, tt.want, got)
		})
	}
}
