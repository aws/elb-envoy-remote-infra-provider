// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"context"
	"fmt"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/elbproxy"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/deploy/tracking"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/networking"
)

type elbProxySubnetsBuilder struct {
	subnetsResolver        networking.SubnetsResolver
	trackingProvider       tracking.Provider
	elbProxyTaggingManager elbproxy.TaggingManager
}

// newELBProxySubnetsBuilder creates a new elbProxySubnetsBuilder.
func newELBProxySubnetsBuilder(
	subnetsResolver networking.SubnetsResolver,
	trackingProvider tracking.Provider,
	elbProxyTaggingManager elbproxy.TaggingManager,
) *elbProxySubnetsBuilder {
	return &elbProxySubnetsBuilder{
		subnetsResolver:        subnetsResolver,
		trackingProvider:       trackingProvider,
		elbProxyTaggingManager: elbProxyTaggingManager,
	}
}

// resolveSubnets selects the resolution strategy based on the provided inputs
// and delegates to the SubnetsResolver.
//
// Strategy selection:
//   - If subnet IDs are present → ResolveViaNameOrIDSlice (explicit)
//   - If no subnet IDs and an existing proxy has subnets → reuse them
//   - Otherwise → ResolveViaDiscovery using ipv4Addressability
func (b *elbProxySubnetsBuilder) resolveSubnets(
	ctx context.Context,
	subnets []elbproxyv1alpha1.SubnetDetails,
	ipv4Addressability string,
	ipv6Required bool,
	stackID string,
) ([]ec2types.Subnet, error) {
	addressability, err := mapIpv4AddressabilityToSubnetAddressability(ipv4Addressability)
	if err != nil {
		return nil, err
	}
	resolveOpts := []networking.SubnetsResolveOption{
		networking.WithSubnetsResolveAddressability(addressability),
		networking.WithSubnetsResolveIPv6Required(ipv6Required),
	}

	// Strategy 1: Explicit subnet IDs provided.
	if ids := extractSubnetIDs(subnets); len(ids) > 0 {
		return b.subnetsResolver.ResolveViaNameOrIDSlice(ctx, ids, resolveOpts...)
	}

	// Strategy 2: If the proxy already exists, read its current subnets and reuse them.
	if b.elbProxyTaggingManager != nil {
		stackTags := b.trackingProvider.StackTags(stackID)
		sdkProxies, err := b.elbProxyTaggingManager.ListProxies(ctx, tracking.TagsAsTagFilter(stackTags))
		if err != nil {
			return nil, err
		}
		if len(sdkProxies) > 0 {
			storedSubnetIDs := make([]string, 0, len(sdkProxies[0].ProxyDetails.NetworkConfiguration.VpcConfiguration.AvailabilityZones))
			for _, az := range sdkProxies[0].ProxyDetails.NetworkConfiguration.VpcConfiguration.AvailabilityZones {
				if az.SubnetId != "" {
					storedSubnetIDs = append(storedSubnetIDs, az.SubnetId)
				}
			}
			if len(storedSubnetIDs) > 0 {
				return b.subnetsResolver.ResolveViaNameOrIDSlice(ctx, storedSubnetIDs, resolveOpts...)
			}
		}
	}

	// Strategy 3: Auto-discovery.
	return b.subnetsResolver.ResolveViaDiscovery(ctx, resolveOpts...)
}

// mapIpv4AddressabilityToSubnetAddressability converts the CRD's Ipv4Addressability value
// to the networking package's SubnetAddressability type.
func mapIpv4AddressabilityToSubnetAddressability(ipv4Addressability string) (networking.SubnetAddressability, error) {
	switch ipv4Addressability {
	case "internal":
		return networking.SubnetAddressabilityInternal, nil
	case "internet-facing":
		return networking.SubnetAddressabilityInternetFacing, nil
	default:
		return "", fmt.Errorf("unknown ipv4Addressability value: %q", ipv4Addressability)
	}
}

// extractSubnetIDs collects non-empty subnet IDs from the CRD SubnetDetails slice.
func extractSubnetIDs(subnets []elbproxyv1alpha1.SubnetDetails) []string {
	var ids []string
	for _, s := range subnets {
		if s.SubnetId != "" {
			ids = append(ids, s.SubnetId)
		}
	}
	return ids
}
