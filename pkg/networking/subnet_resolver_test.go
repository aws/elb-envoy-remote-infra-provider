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
	gomock "github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func sdkSubnet(id, az, azID string, availableIPs int32, tags map[string]string) ec2types.Subnet {
	var sdkTags []ec2types.Tag
	for k, v := range tags {
		sdkTags = append(sdkTags, ec2types.Tag{Key: awssdk.String(k), Value: awssdk.String(v)})
	}
	return ec2types.Subnet{
		SubnetId:                awssdk.String(id),
		AvailabilityZone:        awssdk.String(az),
		AvailabilityZoneId:      awssdk.String(azID),
		AvailableIpAddressCount: awssdk.Int32(availableIPs),
		VpcId:                   awssdk.String("vpc-test"),
		Tags:                    sdkTags,
	}
}

func newTestSubnetsResolver(ctrl *gomock.Controller, azProvider AZInfoProvider) (*defaultSubnetsResolver, *services.MockEC2) {
	ec2Client := services.NewMockEC2(ctrl)
	resolver := &defaultSubnetsResolver{
		azInfoProvider:                 azProvider,
		ec2Client:                      ec2Client,
		vpcID:                          "vpc-test",
		clusterName:                    "test-cluster",
		clusterTagCheckEnabled:         true,
		discoverByReachabilityEnabled:  true,
		minimalAvailableIPAddressCount: 8,
		logger:                         log.Log.WithName("test"),
	}
	return resolver, ec2Client
}

func newDefaultMockAZInfoProvider(ctrl *gomock.Controller) *MockAZInfoProvider {
	mock := NewMockAZInfoProvider(ctrl)
	azInfos := map[string]ec2types.AvailabilityZone{
		"use1-az1": {ZoneId: awssdk.String("use1-az1"), ZoneName: awssdk.String("us-east-1a"), ZoneType: awssdk.String("availability-zone")},
		"use1-az2": {ZoneId: awssdk.String("use1-az2"), ZoneName: awssdk.String("us-east-1b"), ZoneType: awssdk.String("availability-zone")},
		"use1-az3": {ZoneId: awssdk.String("use1-az3"), ZoneName: awssdk.String("us-east-1c"), ZoneType: awssdk.String("availability-zone")},
	}
	mock.EXPECT().FetchAZInfos(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, azIDs []string) (map[string]ec2types.AvailabilityZone, error) {
			result := make(map[string]ec2types.AvailabilityZone)
			for _, id := range azIDs {
				if info, ok := azInfos[id]; ok {
					result[id] = info
				}
			}
			return result, nil
		},
	).AnyTimes()
	return mock
}

func Test_ResolveViaNameOrIDSlice(t *testing.T) {
	type describeSubnetsCall struct {
		resp []ec2types.Subnet
		err  error
	}
	tests := []struct {
		name                 string
		inputs               []string
		describeSubnetsCalls []describeSubnetsCall
		wantIDs              []string
		wantErr              string
	}{
		{
			name:   "resolves two subnets by ID",
			inputs: []string{"subnet-0305b46f4e26d263a", "subnet-0a1b2c3d4e5f67890"},
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-0305b46f4e26d263a", "us-east-1a", "use1-az1", 100, nil),
					sdkSubnet("subnet-0a1b2c3d4e5f67890", "us-east-1b", "use1-az2", 100, nil),
				}},
			},
			wantIDs: []string{"subnet-0305b46f4e26d263a", "subnet-0a1b2c3d4e5f67890"},
		},
		{
			name:   "returns error when subnet ID not found",
			inputs: []string{"subnet-0305b46f4e26d263a", "subnet-00000000000000000"},
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-0305b46f4e26d263a", "us-east-1a", "use1-az1", 100, nil),
				}},
			},
			wantErr: "couldn't find all subnets",
		},
		{
			name:   "returns error on AWS failure",
			inputs: []string{"subnet-0305b46f4e26d263a"},
			describeSubnetsCalls: []describeSubnetsCall{
				{err: errors.New("ec2 api error")},
			},
			wantErr: "ec2 api error",
		},
		{
			name:   "resolves subnets by Name tag",
			inputs: []string{"my-private-subnet-a", "my-private-subnet-b"},
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-aaa", "us-east-1a", "use1-az1", 100, map[string]string{"Name": "my-private-subnet-a"}),
					sdkSubnet("subnet-bbb", "us-east-1b", "use1-az2", 100, map[string]string{"Name": "my-private-subnet-b"}),
				}},
			},
			wantIDs: []string{"subnet-aaa", "subnet-bbb"},
		},
		{
			name:   "returns error when name not found",
			inputs: []string{"nonexistent-subnet"},
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{}},
			},
			wantErr: "missing names: [nonexistent-subnet]",
		},
		{
			name:   "mixed IDs and names",
			inputs: []string{"subnet-0305b46f4e26d263a", "my-subnet-b"},
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{sdkSubnet("subnet-0305b46f4e26d263a", "us-east-1a", "use1-az1", 100, nil)}},
				{resp: []ec2types.Subnet{sdkSubnet("subnet-0a1b2c3d4e5f67890", "us-east-1b", "use1-az2", 100, map[string]string{"Name": "my-subnet-b"})}},
			},
			wantIDs: []string{"subnet-0305b46f4e26d263a", "subnet-0a1b2c3d4e5f67890"},
		},
		{
			name:   "rejects duplicate AZ",
			inputs: []string{"subnet-0305b46f4e26d263a", "subnet-0a1b2c3d4e5f67890"},
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-0305b46f4e26d263a", "us-east-1a", "use1-az1", 100, nil),
					sdkSubnet("subnet-0a1b2c3d4e5f67890", "us-east-1a", "use1-az1", 100, nil),
				}},
			},
			wantErr: "multiple subnets in same Availability Zone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			resolver, ec2Client := newTestSubnetsResolver(ctrl, newDefaultMockAZInfoProvider(ctrl))
			for _, call := range tt.describeSubnetsCalls {
				ec2Client.EXPECT().DescribeSubnetsAsList(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			got, err := resolver.ResolveViaNameOrIDSlice(context.Background(), tt.inputs,
				WithSubnetsResolveAddressability(SubnetAddressabilityInternal))
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

func Test_ResolveViaDiscovery(t *testing.T) {
	type describeSubnetsCall struct {
		resp []ec2types.Subnet
		err  error
	}
	type describeRouteTablesCall struct {
		resp []ec2types.RouteTable
		err  error
	}
	tests := []struct {
		name                     string
		addressability           SubnetAddressability
		describeSubnetsCalls     []describeSubnetsCall
		describeRouteTablesCalls []describeRouteTablesCall
		wantIDs                  []string
		wantLen                  int // use when exact IDs don't matter, just count
		wantErr                  string
	}{
		{
			name:           "internal addressability finds subnets with internal-elb tag",
			addressability: SubnetAddressabilityInternal,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-aaa", "us-east-1a", "use1-az1", 100, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
					sdkSubnet("subnet-bbb", "us-east-1b", "use1-az2", 100, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
				}},
			},
			wantIDs: []string{"subnet-aaa", "subnet-bbb"},
		},
		{
			name:           "internet-facing addressability finds subnets with elb tag",
			addressability: SubnetAddressabilityInternetFacing,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-pub-a", "us-east-1a", "use1-az1", 100, map[string]string{"kubernetes.io/role/elb": "1"}),
					sdkSubnet("subnet-pub-b", "us-east-1b", "use1-az2", 100, map[string]string{"kubernetes.io/role/elb": "1"}),
				}},
			},
			wantIDs: []string{"subnet-pub-a", "subnet-pub-b"},
		},
		{
			name:           "returns error when no subnets found and reachability also empty",
			addressability: SubnetAddressabilityInternal,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{}}, // role tag empty
				{resp: nil},                 // reachability empty
			},
			describeRouteTablesCalls: []describeRouteTablesCall{
				{resp: nil},
			},
			wantErr: "unable to resolve at least one subnet",
		},
		{
			name:           "fallback to reachability picks private subnets for internal",
			addressability: SubnetAddressabilityInternal,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: nil}, // role tag empty
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-priv-a", "us-east-1a", "use1-az1", 100, nil),
					sdkSubnet("subnet-priv-b", "us-east-1b", "use1-az2", 100, nil),
					sdkSubnet("subnet-pub-c", "us-east-1c", "use1-az3", 100, nil),
				}},
			},
			describeRouteTablesCalls: []describeRouteTablesCall{
				{resp: []ec2types.RouteTable{
					{
						Associations: []ec2types.RouteTableAssociation{{Main: awssdk.Bool(true)}},
						Routes:       []ec2types.Route{{GatewayId: awssdk.String("local")}},
					},
					{
						Associations: []ec2types.RouteTableAssociation{{SubnetId: awssdk.String("subnet-pub-c")}},
						Routes:       []ec2types.Route{{GatewayId: awssdk.String("igw-12345"), DestinationCidrBlock: awssdk.String("0.0.0.0/0")}},
					},
				}},
			},
			wantIDs: []string{"subnet-priv-a", "subnet-priv-b"},
		},
		{
			name:           "filters out subnets tagged for other clusters",
			addressability: SubnetAddressabilityInternal,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-ours", "us-east-1a", "use1-az1", 100, map[string]string{
						"kubernetes.io/role/internal-elb": "1", "kubernetes.io/cluster/test-cluster": "shared",
					}),
					sdkSubnet("subnet-theirs", "us-east-1b", "use1-az2", 100, map[string]string{
						"kubernetes.io/role/internal-elb": "1", "kubernetes.io/cluster/other-cluster": "owned",
					}),
					sdkSubnet("subnet-untagged", "us-east-1c", "use1-az3", 100, map[string]string{
						"kubernetes.io/role/internal-elb": "1",
					}),
				}},
			},
			wantIDs: []string{"subnet-ours", "subnet-untagged"},
		},
		{
			name:           "filters out subnets with insufficient IPs",
			addressability: SubnetAddressabilityInternal,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-ok", "us-east-1a", "use1-az1", 100, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
					sdkSubnet("subnet-low", "us-east-1b", "use1-az2", 3, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
					sdkSubnet("subnet-ok2", "us-east-1c", "use1-az3", 50, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
				}},
			},
			wantIDs: []string{"subnet-ok", "subnet-ok2"},
		},
		{
			name:           "chooses one subnet per AZ",
			addressability: SubnetAddressabilityInternal,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-aaa", "us-east-1a", "use1-az1", 100, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
					sdkSubnet("subnet-bbb", "us-east-1a", "use1-az1", 100, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
					sdkSubnet("subnet-ccc", "us-east-1b", "use1-az2", 100, map[string]string{"kubernetes.io/role/internal-elb": "1"}),
				}},
			},
			wantLen: 2,
		},
		{
			name:           "prioritizes current cluster tag in same AZ",
			addressability: SubnetAddressabilityInternal,
			describeSubnetsCalls: []describeSubnetsCall{
				{resp: []ec2types.Subnet{
					sdkSubnet("subnet-aaa", "us-east-1a", "use1-az1", 100, map[string]string{
						"kubernetes.io/role/internal-elb": "1",
					}),
					sdkSubnet("subnet-bbb", "us-east-1a", "use1-az1", 100, map[string]string{
						"kubernetes.io/role/internal-elb": "1", "kubernetes.io/cluster/test-cluster": "shared",
					}),
					sdkSubnet("subnet-ccc", "us-east-1b", "use1-az2", 100, map[string]string{
						"kubernetes.io/role/internal-elb": "1",
					}),
				}},
			},
			wantIDs: []string{"subnet-bbb", "subnet-ccc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			resolver, ec2Client := newTestSubnetsResolver(ctrl, newDefaultMockAZInfoProvider(ctrl))

			for _, call := range tt.describeSubnetsCalls {
				ec2Client.EXPECT().DescribeSubnetsAsList(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}
			for _, call := range tt.describeRouteTablesCalls {
				ec2Client.EXPECT().DescribeRouteTablesAsList(gomock.Any(), gomock.Any()).Return(call.resp, call.err)
			}

			got, err := resolver.ResolveViaDiscovery(context.Background(), WithSubnetsResolveAddressability(tt.addressability))
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
			if tt.wantLen > 0 {
				assert.Len(t, got, tt.wantLen)
			}
			if len(tt.wantIDs) > 0 {
				var gotIDs []string
				for _, s := range got {
					gotIDs = append(gotIDs, awssdk.ToString(s.SubnetId))
				}
				assert.ElementsMatch(t, tt.wantIDs, gotIDs)
			}
		})
	}
}

func Test_mapSDKSubnetsByAZ(t *testing.T) {
	subnets := []ec2types.Subnet{
		sdkSubnet("subnet-1", "us-east-1a", "use1-az1", 100, nil),
		sdkSubnet("subnet-2", "us-east-1a", "use1-az1", 100, nil),
		sdkSubnet("subnet-3", "us-east-1b", "use1-az2", 100, nil),
	}
	result := mapSDKSubnetsByAZ(subnets)
	assert.Len(t, result["us-east-1a"], 2)
	assert.Len(t, result["us-east-1b"], 1)
}

func Test_sortSubnetsByID(t *testing.T) {
	subnets := []ec2types.Subnet{
		sdkSubnet("subnet-ccc", "us-east-1a", "use1-az1", 100, nil),
		sdkSubnet("subnet-aaa", "us-east-1b", "use1-az2", 100, nil),
		sdkSubnet("subnet-bbb", "us-east-1c", "use1-az3", 100, nil),
	}
	sortSubnetsByID(subnets)
	assert.Equal(t, "subnet-aaa", awssdk.ToString(subnets[0].SubnetId))
	assert.Equal(t, "subnet-bbb", awssdk.ToString(subnets[1].SubnetId))
	assert.Equal(t, "subnet-ccc", awssdk.ToString(subnets[2].SubnetId))
}

func Test_extractSubnetIDs(t *testing.T) {
	subnets := []ec2types.Subnet{
		sdkSubnet("subnet-aaa", "us-east-1a", "use1-az1", 100, nil),
		sdkSubnet("subnet-bbb", "us-east-1b", "use1-az2", 100, nil),
	}
	ids := extractSubnetIDs(subnets)
	assert.Equal(t, []string{"subnet-aaa", "subnet-bbb"}, ids)
}

func Test_isSubnetContainsEligibleClusterTag(t *testing.T) {
	tests := []struct {
		name    string
		tags    map[string]string
		enabled bool
		want    bool
	}{
		{name: "no cluster tags → eligible", tags: map[string]string{"Name": "my-subnet"}, enabled: true, want: true},
		{name: "current cluster tag → eligible", tags: map[string]string{"kubernetes.io/cluster/test-cluster": "shared"}, enabled: true, want: true},
		{name: "other cluster tag only → ineligible", tags: map[string]string{"kubernetes.io/cluster/other-cluster": "owned"}, enabled: true, want: false},
		{name: "other cluster tag but check disabled → eligible", tags: map[string]string{"kubernetes.io/cluster/other-cluster": "owned"}, enabled: false, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &defaultSubnetsResolver{clusterName: "test-cluster", clusterTagCheckEnabled: tt.enabled}
			subnet := sdkSubnet("subnet-test", "us-east-1a", "use1-az1", 100, tt.tags)
			assert.Equal(t, tt.want, r.isSubnetContainsEligibleClusterTag(subnet))
		})
	}
}

func withIPv6CIDR(subnet ec2types.Subnet, cidr string, state ec2types.SubnetCidrBlockStateCode) ec2types.Subnet {
	subnet.Ipv6CidrBlockAssociationSet = []ec2types.SubnetIpv6CidrBlockAssociation{
		{
			Ipv6CidrBlock:      awssdk.String(cidr),
			Ipv6CidrBlockState: &ec2types.SubnetCidrBlockState{State: state},
		},
	}
	return subnet
}

func Test_isSubnetContainsIPv6CIDR(t *testing.T) {
	tests := []struct {
		name   string
		subnet ec2types.Subnet
		want   bool
	}{
		{
			name:   "no IPv6 association",
			subnet: sdkSubnet("subnet-a", "us-east-1a", "use1-az1", 100, nil),
			want:   false,
		},
		{
			name:   "associated IPv6 CIDR",
			subnet: withIPv6CIDR(sdkSubnet("subnet-a", "us-east-1a", "use1-az1", 100, nil), "2600:1f18::/64", ec2types.SubnetCidrBlockStateCodeAssociated),
			want:   true,
		},
		{
			name:   "IPv6 CIDR still associating is not usable",
			subnet: withIPv6CIDR(sdkSubnet("subnet-a", "us-east-1a", "use1-az1", 100, nil), "2600:1f18::/64", ec2types.SubnetCidrBlockStateCodeAssociating),
			want:   false,
		},
		{
			name:   "empty IPv6 CIDR value",
			subnet: withIPv6CIDR(sdkSubnet("subnet-a", "us-east-1a", "use1-az1", 100, nil), "", ec2types.SubnetCidrBlockStateCodeAssociated),
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSubnetContainsIPv6CIDR(tt.subnet))
		})
	}
}

func Test_validateSubnetsIPv6CIDR(t *testing.T) {
	ipv6Subnet := withIPv6CIDR(sdkSubnet("subnet-v6", "us-east-1a", "use1-az1", 100, nil), "2600:1f18::/64", ec2types.SubnetCidrBlockStateCodeAssociated)
	ipv4Subnet := sdkSubnet("subnet-v4", "us-east-1b", "use1-az2", 100, nil)

	tests := []struct {
		name         string
		subnets      []ec2types.Subnet
		ipv6Required bool
		wantErr      string
	}{
		{
			name:         "IPv6 not required allows IPv4-only subnets",
			subnets:      []ec2types.Subnet{ipv4Subnet},
			ipv6Required: false,
		},
		{
			name:         "IPv6 required and all subnets have IPv6",
			subnets:      []ec2types.Subnet{ipv6Subnet},
			ipv6Required: true,
		},
		{
			name:         "IPv6 required but subnet lacks IPv6 CIDR",
			subnets:      []ec2types.Subnet{ipv6Subnet, ipv4Subnet},
			ipv6Required: true,
			wantErr:      "subnets [subnet-v4] do not have an associated IPv6 CIDR block",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &defaultSubnetsResolver{}
			err := r.validateSubnetsIPv6CIDR(tt.subnets, SubnetsResolveOptions{IPv6Required: tt.ipv6Required})
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Test_categorizeSubnetsByEligibility_IPv6Required asserts that for IPv6-enabled proxies,
// subnets lacking an associated IPv6 CIDR block are filtered out of discovery instead of
// being handed to the ELBProxy CreateProxy API, which rejects them.
func Test_categorizeSubnetsByEligibility_IPv6Required(t *testing.T) {
	subnetNoIPv6 := sdkSubnet("subnet-no-ipv6", "us-east-1a", "use1-az1", 100, nil)
	subnetWithIPv6 := withIPv6CIDR(sdkSubnet("subnet-with-ipv6", "us-east-1b", "use1-az2", 100, nil), "2600:1f18::/64", ec2types.SubnetCidrBlockStateCodeAssociated)

	tests := []struct {
		name                string
		ipv6Required        bool
		subnets             []ec2types.Subnet
		wantEligible        []string
		wantMissingIPv6CIDR []string
	}{
		{
			name:                "IPv6 not required accepts subnets without an IPv6 CIDR",
			ipv6Required:        false,
			subnets:             []ec2types.Subnet{subnetNoIPv6, subnetWithIPv6},
			wantEligible:        []string{"subnet-no-ipv6", "subnet-with-ipv6"},
			wantMissingIPv6CIDR: []string{},
		},
		{
			name:                "IPv6 required filters out subnets without an IPv6 CIDR",
			ipv6Required:        true,
			subnets:             []ec2types.Subnet{subnetNoIPv6, subnetWithIPv6},
			wantEligible:        []string{"subnet-with-ipv6"},
			wantMissingIPv6CIDR: []string{"subnet-no-ipv6"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			resolver, _ := newTestSubnetsResolver(ctrl, newDefaultMockAZInfoProvider(ctrl))
			got := resolver.categorizeSubnetsByEligibility(tt.subnets, SubnetsResolveOptions{IPv6Required: tt.ipv6Required})
			assert.Equal(t, tt.wantEligible, extractSubnetIDs(got.eligible))
			assert.Equal(t, tt.wantMissingIPv6CIDR, extractSubnetIDs(got.missingIPv6CIDR))
		})
	}
}

func Test_chooseAndValidateSubnetsPerAZ_IPv6RequiredError(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver, _ := newTestSubnetsResolver(ctrl, newDefaultMockAZInfoProvider(ctrl))

	subnets := []ec2types.Subnet{
		sdkSubnet("subnet-a", "us-east-1a", "use1-az1", 100, nil),
		sdkSubnet("subnet-b", "us-east-1b", "use1-az2", 100, nil),
	}

	_, err := resolver.chooseAndValidateSubnetsPerAZ(context.Background(), subnets, SubnetsResolveOptions{
		Addressability: SubnetAddressabilityInternetFacing,
		IPv6Required:   true,
	})

	assert.ErrorContains(t, err, "2 don't have an associated IPv6 CIDR block")
}

func Test_isSubnetContainsSufficientIPAddresses(t *testing.T) {
	tests := []struct {
		name         string
		availableIPs int32
		want         bool
	}{
		{name: "exactly at minimum", availableIPs: 8, want: true},
		{name: "above minimum", availableIPs: 100, want: true},
		{name: "one below minimum", availableIPs: 7, want: false},
		{name: "zero IPs", availableIPs: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &defaultSubnetsResolver{minimalAvailableIPAddressCount: 8}
			assert.Equal(t, tt.want, r.isSubnetContainsSufficientIPAddresses(sdkSubnet("s", "a", "az1", tt.availableIPs, nil)))
		})
	}
}

func Test_isSubnetContainsRouteToIGW(t *testing.T) {
	tests := []struct {
		name        string
		subnetID    string
		routeTables []ec2types.RouteTable
		want        bool
		wantErr     string
	}{
		{
			name:     "explicit association with IGW route → public",
			subnetID: "subnet-pub",
			routeTables: []ec2types.RouteTable{
				{
					Associations: []ec2types.RouteTableAssociation{{SubnetId: awssdk.String("subnet-pub")}},
					Routes:       []ec2types.Route{{GatewayId: awssdk.String("igw-123"), DestinationCidrBlock: awssdk.String("0.0.0.0/0")}},
				},
				{
					Associations: []ec2types.RouteTableAssociation{{Main: awssdk.Bool(true)}},
					Routes:       []ec2types.Route{{GatewayId: awssdk.String("local")}},
				},
			},
			want: true,
		},
		{
			name:     "no explicit association, main RT has no IGW → private",
			subnetID: "subnet-priv",
			routeTables: []ec2types.RouteTable{
				{
					Associations: []ec2types.RouteTableAssociation{{Main: awssdk.Bool(true)}},
					Routes:       []ec2types.Route{{GatewayId: awssdk.String("local")}},
				},
			},
			want: false,
		},
		{
			name:        "no main RT found → error",
			subnetID:    "subnet-orphan",
			routeTables: []ec2types.RouteTable{},
			wantErr:     "could not identify main route table",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &defaultSubnetsResolver{vpcID: "vpc-test"}
			got, err := r.isSubnetContainsRouteToIGW(tt.subnetID, tt.routeTables)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func Test_validateSubnetsMinimalCount(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		wantErr string
	}{
		{name: "needs 2, has 1 → error", count: 1, wantErr: "less than minimal required count"},
		{name: "needs 2, has 2 → ok", count: 2},
		{name: "needs 2, has 3 → ok", count: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &defaultSubnetsResolver{}
			subnets := make([]ec2types.Subnet, tt.count)
			for i := range subnets {
				subnets[i] = sdkSubnet("s"+string(rune('0'+i)), "a", "az", 100, nil)
			}
			err := r.validateSubnetsMinimalCount(subnets)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_subnetIDPattern(t *testing.T) {
	tests := []struct {
		input string
		match bool
	}{
		// valid: 8 hex chars
		{"subnet-0abc1234", true},
		// valid: 17 hex chars
		{"subnet-0305b46f4e26d263a", true},
		// too short: 3 hex chars
		{"subnet-aaa", false},
		// too short: 7 hex chars
		{"subnet-0abc123", false},
		// in between: 12 hex chars (neither 8 nor 17)
		{"subnet-0abc12345678", false},
		// too long: 18 hex chars
		{"subnet-0305b46f4e26d263ab", false},
		// not hex
		{"subnet-zzzzzzzz", false},
		// missing prefix
		{"0305b46f4e26d263a", false},
		// name that starts with subnet-
		{"subnet-my-name", false},
		// plain name
		{"my-private-subnet", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.match, subnetIDPattern.MatchString(tt.input))
		})
	}
}

func Test_filterSubnetsInAvailabilityZones(t *testing.T) {
	tests := []struct {
		name    string
		subnets []ec2types.Subnet
		azInfos map[string]ec2types.AvailabilityZone
		wantIDs []string
		wantErr string
	}{
		{
			name: "keeps only availability-zone subnets",
			subnets: []ec2types.Subnet{
				sdkSubnet("subnet-az1", "us-east-1a", "use1-az1", 100, nil),
				sdkSubnet("subnet-az2", "us-east-1b", "use1-az2", 100, nil),
			},
			azInfos: map[string]ec2types.AvailabilityZone{
				"use1-az1": {ZoneId: awssdk.String("use1-az1"), ZoneType: awssdk.String(zoneTypeAvailabilityZone)},
				"use1-az2": {ZoneId: awssdk.String("use1-az2"), ZoneType: awssdk.String(zoneTypeAvailabilityZone)},
			},
			wantIDs: []string{"subnet-az1", "subnet-az2"},
		},
		{
			name: "filters out local-zone and wavelength-zone subnets",
			subnets: []ec2types.Subnet{
				sdkSubnet("subnet-az", "us-east-1a", "use1-az1", 100, nil),
				sdkSubnet("subnet-lz", "us-east-1-bos-1a", "use1-bos1-az1", 100, nil),
				sdkSubnet("subnet-wl", "us-east-1-wl1-bos-wlz-1", "use1-wl1-bos-wlz-1", 100, nil),
			},
			azInfos: map[string]ec2types.AvailabilityZone{
				"use1-az1":           {ZoneId: awssdk.String("use1-az1"), ZoneType: awssdk.String(zoneTypeAvailabilityZone)},
				"use1-bos1-az1":      {ZoneId: awssdk.String("use1-bos1-az1"), ZoneType: awssdk.String(zoneTypeLocalZone)},
				"use1-wl1-bos-wlz-1": {ZoneId: awssdk.String("use1-wl1-bos-wlz-1"), ZoneType: awssdk.String(zoneTypeWavelengthZone)},
			},
			wantIDs: []string{"subnet-az"},
		},
		{
			name: "filters out outpost subnets",
			subnets: []ec2types.Subnet{
				sdkSubnet("subnet-az", "us-east-1a", "use1-az1", 100, nil),
				{
					SubnetId:           awssdk.String("subnet-op"),
					AvailabilityZone:   awssdk.String("us-east-1a"),
					AvailabilityZoneId: awssdk.String("use1-az1"),
					OutpostArn:         awssdk.String("arn:aws:outposts:us-east-1:123456789012:outpost/op-1234567890abcdef0"),
				},
			},
			azInfos: map[string]ec2types.AvailabilityZone{
				"use1-az1": {ZoneId: awssdk.String("use1-az1"), ZoneType: awssdk.String(zoneTypeAvailabilityZone)},
			},
			wantIDs: []string{"subnet-az"},
		},
		{
			name: "returns nil when all subnets are filtered out",
			subnets: []ec2types.Subnet{
				{
					SubnetId:           awssdk.String("subnet-op"),
					AvailabilityZone:   awssdk.String("us-east-1a"),
					AvailabilityZoneId: awssdk.String("use1-az1"),
					OutpostArn:         awssdk.String("arn:aws:outposts:us-east-1:123456789012:outpost/op-1234567890abcdef0"),
				},
			},
			azInfos: map[string]ec2types.AvailabilityZone{},
			wantIDs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockAZ := NewMockAZInfoProvider(ctrl)
			mockAZ.EXPECT().FetchAZInfos(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, azIDs []string) (map[string]ec2types.AvailabilityZone, error) {
					result := make(map[string]ec2types.AvailabilityZone)
					for _, id := range azIDs {
						if info, ok := tt.azInfos[id]; ok {
							result[id] = info
						}
					}
					return result, nil
				},
			).AnyTimes()
			resolver, _ := newTestSubnetsResolver(ctrl, mockAZ)
			got, err := resolver.filterSubnetsInAvailabilityZones(context.Background(), tt.subnets)
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

func Test_validateSubnetsAllInAvailabilityZones(t *testing.T) {
	tests := []struct {
		name    string
		subnets []ec2types.Subnet
		azInfos map[string]ec2types.AvailabilityZone
		wantErr string
	}{
		{
			name: "all subnets in availability zones → ok",
			subnets: []ec2types.Subnet{
				sdkSubnet("subnet-az1", "us-east-1a", "use1-az1", 100, nil),
				sdkSubnet("subnet-az2", "us-east-1b", "use1-az2", 100, nil),
			},
			azInfos: map[string]ec2types.AvailabilityZone{
				"use1-az1": {ZoneId: awssdk.String("use1-az1"), ZoneType: awssdk.String(zoneTypeAvailabilityZone)},
				"use1-az2": {ZoneId: awssdk.String("use1-az2"), ZoneType: awssdk.String(zoneTypeAvailabilityZone)},
			},
		},
		{
			name: "local-zone subnet → error",
			subnets: []ec2types.Subnet{
				sdkSubnet("subnet-lz", "us-east-1-bos-1a", "use1-bos1-az1", 100, nil),
			},
			azInfos: map[string]ec2types.AvailabilityZone{
				"use1-bos1-az1": {ZoneId: awssdk.String("use1-bos1-az1"), ZoneType: awssdk.String(zoneTypeLocalZone)},
			},
			wantErr: "only standard availability zones are supported",
		},
		{
			name: "wavelength-zone subnet → error",
			subnets: []ec2types.Subnet{
				sdkSubnet("subnet-wl", "us-east-1-wl1-bos-wlz-1", "use1-wl1-bos-wlz-1", 100, nil),
			},
			azInfos: map[string]ec2types.AvailabilityZone{
				"use1-wl1-bos-wlz-1": {ZoneId: awssdk.String("use1-wl1-bos-wlz-1"), ZoneType: awssdk.String(zoneTypeWavelengthZone)},
			},
			wantErr: "only standard availability zones are supported",
		},
		{
			name: "outpost subnet → error",
			subnets: []ec2types.Subnet{
				{
					SubnetId:           awssdk.String("subnet-op"),
					AvailabilityZone:   awssdk.String("us-east-1a"),
					AvailabilityZoneId: awssdk.String("use1-az1"),
					OutpostArn:         awssdk.String("arn:aws:outposts:us-east-1:123456789012:outpost/op-1234567890abcdef0"),
				},
			},
			azInfos: map[string]ec2types.AvailabilityZone{},
			wantErr: "subnet subnet-op is in an outpost",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockAZ := NewMockAZInfoProvider(ctrl)
			mockAZ.EXPECT().FetchAZInfos(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, azIDs []string) (map[string]ec2types.AvailabilityZone, error) {
					result := make(map[string]ec2types.AvailabilityZone)
					for _, id := range azIDs {
						if info, ok := tt.azInfos[id]; ok {
							result[id] = info
						}
					}
					return result, nil
				},
			).AnyTimes()
			resolver, _ := newTestSubnetsResolver(ctrl, mockAZ)
			err := resolver.validateSubnetsAllInAvailabilityZones(context.Background(), tt.subnets)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
