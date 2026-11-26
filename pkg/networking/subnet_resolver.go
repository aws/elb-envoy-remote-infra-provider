// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/aws/services"
)

const (
	TagKeySubnetInternalELB = "kubernetes.io/role/internal-elb"
	TagKeySubnetPublicELB   = "kubernetes.io/role/elb"
)

// SubnetAddressability represents the addressability type for subnet resolution.
type SubnetAddressability string

const (
	SubnetAddressabilityInternal       SubnetAddressability = "internal"
	SubnetAddressabilityInternetFacing SubnetAddressability = "internet-facing"
)

const (
	zoneTypeAvailabilityZone string = "availability-zone"
	zoneTypeLocalZone        string = "local-zone"
	zoneTypeWavelengthZone   string = "wavelength-zone"
)

const clusterResourceTagPrefix = "kubernetes.io/cluster"

const (
	// Requires minimal 8 ip address count for its subnet.
	defaultMinimalAvailableIPAddressCount = 8
	// Minimum number of subnets required for ELBProxy proxy provisioning.
	defaultMinimalSubnetCount = 2
	// the ec2's vpcID filter.
	ec2FilterNameVpcID = "vpc-id"
)

// SubnetsResolveOptions holds options for subnet resolution.
type SubnetsResolveOptions struct {
	// The subnet addressability (internal or internet-facing).
	// By default, it's internet-facing.
	Addressability SubnetAddressability

	// IPv6Required indicates the proxy needs IPv6 addressing, so only subnets
	// with an associated IPv6 CIDR block are eligible.
	IPv6Required bool
}

// ApplyOptions applies slice of SubnetsResolveOption.
func (opts *SubnetsResolveOptions) ApplyOptions(options []SubnetsResolveOption) {
	for _, option := range options {
		option(opts)
	}
}

// defaultSubnetsResolveOptions generates the default SubnetsResolveOptions
func defaultSubnetsResolveOptions() SubnetsResolveOptions {
	return SubnetsResolveOptions{
		Addressability: SubnetAddressabilityInternetFacing,
	}
}

type SubnetsResolveOption func(opts *SubnetsResolveOptions)

// WithSubnetsResolveAddressability generates an option that configures the subnet addressability.
func WithSubnetsResolveAddressability(addressability SubnetAddressability) SubnetsResolveOption {
	return func(opts *SubnetsResolveOptions) {
		opts.Addressability = addressability
	}
}

// WithSubnetsResolveIPv6Required generates an option that requires resolved subnets
// to have an associated IPv6 CIDR block. Set when the proxy enables IPv6 addressing.
func WithSubnetsResolveIPv6Required(required bool) SubnetsResolveOption {
	return func(opts *SubnetsResolveOptions) {
		opts.IPv6Required = required
	}
}

// SubnetsResolver is responsible for resolving EC2 Subnets.
//
//go:generate mockgen -copyright_file=../../hack/boilerplate.mockgen.txt -destination subnet_resolver_mock.go -package networking github.com/aws/elb-envoy-remote-infra-provider/pkg/networking SubnetsResolver
type SubnetsResolver interface {
	// ResolveViaDiscovery resolves subnets by auto-discovering matching subnets.
	ResolveViaDiscovery(ctx context.Context, opts ...SubnetsResolveOption) ([]ec2types.Subnet, error)

	// ResolveViaNameOrIDSlice resolves subnets using subnet name or ID.
	ResolveViaNameOrIDSlice(ctx context.Context, subnetNameOrIDs []string, opts ...SubnetsResolveOption) ([]ec2types.Subnet, error)
}

// NewDefaultSubnetsResolver constructs new defaultSubnetsResolver.
func NewDefaultSubnetsResolver(
	azInfoProvider AZInfoProvider,
	ec2Client services.EC2,
	vpcID string,
	clusterName string,
	clusterTagCheckEnabled bool,
	discoveryByReachabilityEnabled bool,
	logger logr.Logger) *defaultSubnetsResolver {
	return &defaultSubnetsResolver{
		azInfoProvider:                 azInfoProvider,
		ec2Client:                      ec2Client,
		vpcID:                          vpcID,
		clusterName:                    clusterName,
		clusterTagCheckEnabled:         clusterTagCheckEnabled,
		discoverByReachabilityEnabled:  discoveryByReachabilityEnabled,
		minimalAvailableIPAddressCount: defaultMinimalAvailableIPAddressCount,
		logger:                         logger,
	}
}

var _ SubnetsResolver = &defaultSubnetsResolver{}

// subnetIDPattern matches valid AWS subnet IDs (e.g. subnet-0abc123def456789a).
var subnetIDPattern = regexp.MustCompile(`^subnet-[0-9a-f]{8}$|^subnet-[0-9a-f]{17}$`)

// default implementation for SubnetsResolver.
//  1. If a specific subnet name/id is provided, those subnets will be selected directly.
//  2. Otherwise, the controller selects one subnet per Availability Zone using the subnet selection algorithm:
//     a. Candidate subnet determination:
//     - If subnets with role tags exist: Only these become candidates
//     - If no subnets have role tags and reachability discovery is enabled:
//     Candidates are subnets whose reachability (public/private) matches the scheme
//     b. Subnet filtering:
//     - Subnets tagged for other clusters (not the current cluster) are filtered out
//     - Subnets with insufficient available IP addresses are filtered out
//     c. Final selection:
//     - One subnet per AZ is selected based on:
//     * Priority given to subnets with cluster tag for current cluster
//     * When priority is equal, selection is based on lexicographical ordering of subnet IDs
type defaultSubnetsResolver struct {
	azInfoProvider AZInfoProvider
	ec2Client      services.EC2
	vpcID          string
	clusterName    string
	// whether enable the cluster tag check on subnets
	// when enabled, only below subnets are eligible to be used as subnet
	// - The subnet has no Kubernetes cluster tags at all
	// - The subnet has a Kubernetes cluster tag matching the current cluster
	clusterTagCheckEnabled bool
	// whether to enable discovery subnet by reachability(public/private)
	discoverByReachabilityEnabled bool
	// the minimal available IP address required for subnets
	minimalAvailableIPAddressCount int32
	logger                         logr.Logger
}

func (r *defaultSubnetsResolver) ResolveViaDiscovery(ctx context.Context, opts ...SubnetsResolveOption) ([]ec2types.Subnet, error) {
	resolveOpts := defaultSubnetsResolveOptions()
	resolveOpts.ApplyOptions(opts)

	var subnetRoleTagKey string
	var needsPublicSubnet bool
	switch resolveOpts.Addressability {
	case SubnetAddressabilityInternal:
		subnetRoleTagKey = TagKeySubnetInternalELB
		needsPublicSubnet = false
	case SubnetAddressabilityInternetFacing:
		subnetRoleTagKey = TagKeySubnetPublicELB
		needsPublicSubnet = true
	default:
		return nil, fmt.Errorf("unknown addressability: %v", resolveOpts.Addressability)
	}
	tagFilters := map[string][]string{subnetRoleTagKey: {"", "1"}}
	subnets, err := r.listSubnetsByTagFilters(ctx, tagFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to list subnets by role tag: %w", err)
	}
	// when there are no subnets with matching role tag, we fallback to discovery by subnet reachability.
	if len(subnets) == 0 && r.discoverByReachabilityEnabled {
		subnets, err = r.listSubnetsByReachability(ctx, needsPublicSubnet)
		if err != nil {
			return nil, fmt.Errorf("failed to list subnets by reachability: %w", err)
		}
	}
	chosenSubnets, err := r.chooseAndValidateSubnetsPerAZ(ctx, subnets, resolveOpts)
	if err != nil {
		return nil, err
	}
	return chosenSubnets, nil
}

func (r *defaultSubnetsResolver) ResolveViaNameOrIDSlice(ctx context.Context, subnetNameOrIDs []string, opts ...SubnetsResolveOption) ([]ec2types.Subnet, error) {
	resolveOpts := defaultSubnetsResolveOptions()
	resolveOpts.ApplyOptions(opts)

	subnets, err := r.listSubnetsByNameOrIDs(ctx, subnetNameOrIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to list subnets by names or IDs: %w", err)
	}
	if err := r.validateSpecifiedSubnets(ctx, subnets, resolveOpts); err != nil {
		return nil, err
	}
	return subnets, nil
}

// listSubnetsByNameOrIDs lists subnets within vpc matching given ID or name.
// The returned subnets will be in the same order as the input subnetNameOrIDs slice.
func (r *defaultSubnetsResolver) listSubnetsByNameOrIDs(ctx context.Context, subnetNameOrIDs []string) ([]ec2types.Subnet, error) {
	var subnetIDs []string
	var subnetNames []string
	for _, nameOrID := range subnetNameOrIDs {
		if subnetIDPattern.MatchString(nameOrID) {
			subnetIDs = append(subnetIDs, nameOrID)
		} else {
			subnetNames = append(subnetNames, nameOrID)
		}
	}

	subnetByID := make(map[string]ec2types.Subnet)
	subnetsByName := make(map[string][]ec2types.Subnet)

	if len(subnetIDs) > 0 {
		subnets, err := r.listSubnetsByIDs(ctx, subnetIDs)
		if err != nil {
			return nil, err
		}
		for _, subnet := range subnets {
			subnetByID[awssdk.ToString(subnet.SubnetId)] = subnet
		}
	}
	if len(subnetNames) > 0 {
		subnets, err := r.listSubnetsByNames(ctx, subnetNames)
		if err != nil {
			return nil, err
		}
		// Sort by subnet ID for deterministic ordering when multiple subnets share the same Name tag.
		// AWS DescribeSubnets does not guarantee a stable order, so sorting ensures consistent
		// subnet-to-name assignment across reconciliation loops.
		sortSubnetsByID(subnets)
		for _, subnet := range subnets {
			// Extract the Name tag value for mapping
			var subnetName string
			for _, tag := range subnet.Tags {
				if awssdk.ToString(tag.Key) == "Name" {
					subnetName = awssdk.ToString(tag.Value)
					break
				}
			}
			if subnetName != "" {
				// Use a slice to support multiple subnets with the same Name tag
				subnetsByName[subnetName] = append(subnetsByName[subnetName], subnet)
			}
		}
	}

	// Reconstruct the subnet list in the original requested order
	resolvedSubnets := make([]ec2types.Subnet, 0, len(subnetNameOrIDs))
	// Track how many times we've used each subnet name to handle duplicates
	nameUsageCount := make(map[string]int)
	for _, nameOrID := range subnetNameOrIDs {
		if subnetIDPattern.MatchString(nameOrID) {
			if subnet, ok := subnetByID[nameOrID]; ok {
				resolvedSubnets = append(resolvedSubnets, subnet)
			} else {
				return nil, fmt.Errorf("subnet ID not found: %s", nameOrID)
			}
		} else {
			if subnets, ok := subnetsByName[nameOrID]; ok {
				// Get the next available subnet with this name
				usageIndex := nameUsageCount[nameOrID]
				if usageIndex >= len(subnets) {
					return nil, fmt.Errorf("subnet with Name tag %q requested %d times but only %d subnet(s) found with that name", nameOrID, usageIndex+1, len(subnets))
				}
				resolvedSubnets = append(resolvedSubnets, subnets[usageIndex])
				nameUsageCount[nameOrID]++
			} else {
				return nil, fmt.Errorf("subnet with Name tag not found: %s", nameOrID)
			}
		}
	}

	return resolvedSubnets, nil
}

func (r *defaultSubnetsResolver) listSubnetsByIDs(ctx context.Context, subnetIDs []string) ([]ec2types.Subnet, error) {
	req := &ec2sdk.DescribeSubnetsInput{
		SubnetIds: subnetIDs,
	}
	subnets, err := r.ec2Client.DescribeSubnetsAsList(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(subnets) != len(subnetIDs) {
		return nil, fmt.Errorf("couldn't find all subnets, want: %v, found: %v", subnetIDs, extractSubnetIDs(subnets))
	}
	return subnets, nil
}

func (r *defaultSubnetsResolver) listSubnetsByNames(ctx context.Context, subnetNames []string) ([]ec2types.Subnet, error) {
	// Deduplicate subnet names for the AWS query while preserving order
	seen := make(map[string]bool)
	deduplicatedNames := make([]string, 0, len(subnetNames))
	for _, name := range subnetNames {
		if !seen[name] {
			deduplicatedNames = append(deduplicatedNames, name)
			seen[name] = true
		}
	}

	req := &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   awssdk.String(ec2FilterNameVpcID),
				Values: []string{r.vpcID},
			},
			{
				Name:   awssdk.String("tag:Name"),
				Values: deduplicatedNames,
			},
		},
	}
	subnets, err := r.ec2Client.DescribeSubnetsAsList(ctx, req)
	if err != nil {
		return nil, err
	}

	// Verify we found at least one subnet for each unique name
	// Note: There may be multiple subnets with the same Name tag, which is valid
	foundNames := make(map[string]bool)
	for _, subnet := range subnets {
		for _, tag := range subnet.Tags {
			if awssdk.ToString(tag.Key) == "Name" {
				foundNames[awssdk.ToString(tag.Value)] = true
				break
			}
		}
	}

	var missingNames []string
	for name := range seen {
		if !foundNames[name] {
			missingNames = append(missingNames, name)
		}
	}
	if len(missingNames) > 0 {
		return nil, fmt.Errorf("couldn't find all subnets, missing names: %v", missingNames)
	}

	return subnets, nil
}

// listSubnetsByTagFilters list subnets in vpc matches specified tag filter
func (r *defaultSubnetsResolver) listSubnetsByTagFilters(ctx context.Context, tagFilters map[string][]string) ([]ec2types.Subnet, error) {
	req := &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   awssdk.String(ec2FilterNameVpcID),
				Values: []string{r.vpcID},
			},
		},
	}
	for key, values := range tagFilters {
		req.Filters = append(req.Filters, ec2types.Filter{
			Name:   awssdk.String("tag:" + key),
			Values: values,
		})
	}
	return r.ec2Client.DescribeSubnetsAsList(ctx, req)
}

// listSubnetsByReachability list subnets in vpc that matches given public/private subnet
func (r *defaultSubnetsResolver) listSubnetsByReachability(ctx context.Context, needsPublicSubnet bool) ([]ec2types.Subnet, error) {
	subnetsReq := &ec2sdk.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   awssdk.String(ec2FilterNameVpcID),
				Values: []string{r.vpcID},
			},
		},
	}
	subnets, err := r.ec2Client.DescribeSubnetsAsList(ctx, subnetsReq)
	if err != nil {
		return nil, err
	}
	routeTablesReq := &ec2sdk.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{
				Name:   awssdk.String(ec2FilterNameVpcID),
				Values: []string{r.vpcID},
			},
		},
	}
	routeTables, err := r.ec2Client.DescribeRouteTablesAsList(ctx, routeTablesReq)
	if err != nil {
		return nil, err
	}
	var subnetsMatchingReachability []ec2types.Subnet
	for _, subnet := range subnets {
		isPublicSubnet, err := r.isSubnetContainsRouteToIGW(awssdk.ToString(subnet.SubnetId), routeTables)
		if err != nil {
			return nil, err
		}
		if isPublicSubnet == needsPublicSubnet {
			subnetsMatchingReachability = append(subnetsMatchingReachability, subnet)
		}
	}
	return subnetsMatchingReachability, nil
}

// isSubnetContainsRouteToIGW tests whether a subnet contains a route to internetIGW, i.e. have route to IGW.
func (r *defaultSubnetsResolver) isSubnetContainsRouteToIGW(subnetID string, routeTables []ec2types.RouteTable) (bool, error) {
	var mainRT *ec2types.RouteTable
	var subnetRT *ec2types.RouteTable
	for i, rt := range routeTables {
		for _, rtAssoc := range rt.Associations {
			if awssdk.ToBool(rtAssoc.Main) {
				mainRT = &routeTables[i]
			}
			if awssdk.ToString(rtAssoc.SubnetId) == subnetID {
				subnetRT = &routeTables[i]
				break
			}
		}
	}
	// If there is no explicit association, the subnet will be implicitly associated with the VPC's main routing table.
	if subnetRT == nil {
		if mainRT == nil {
			// every VPC shall have a main route table. let's make this an error so we know if this precondition breaks.
			return false, fmt.Errorf("[this shall never happen] could not identify main route table for vpc: %v", r.vpcID)
		}
		subnetRT = mainRT
	}
	for _, route := range subnetRT.Routes {
		if strings.HasPrefix(awssdk.ToString(route.GatewayId), "igw") {
			return true, nil
		}
	}
	return false, nil
}

// validateSpecifiedSubnets will validate explicitly supplied subnets from given id or name.
func (r *defaultSubnetsResolver) validateSpecifiedSubnets(ctx context.Context, subnets []ec2types.Subnet, resolveOpts SubnetsResolveOptions) error {
	if len(subnets) == 0 {
		return errors.New("unable to resolve at least one subnet")
	}
	if err := r.validateSubnetsAZExclusivity(subnets); err != nil {
		return err
	}
	if err := r.validateSubnetsAllInAvailabilityZones(ctx, subnets); err != nil {
		return err
	}
	if err := r.validateSubnetsMinimalCount(subnets); err != nil {
		return err
	}
	if err := r.validateSubnetsIPv6CIDR(subnets, resolveOpts); err != nil {
		return err
	}
	return nil
}

// chooseAndValidateSubnetsPerAZ will choose one subnet per AZ from eligible subnets and then validate against chosen subnets.
func (r *defaultSubnetsResolver) chooseAndValidateSubnetsPerAZ(ctx context.Context, subnets []ec2types.Subnet, resolveOpts SubnetsResolveOptions) ([]ec2types.Subnet, error) {
	categorizedSubnets := r.categorizeSubnetsByEligibility(subnets, resolveOpts)
	chosenSubnets := r.chooseSubnetsPerAZ(categorizedSubnets.eligible)
	if len(chosenSubnets) == 0 {
		if resolveOpts.IPv6Required {
			return nil, fmt.Errorf("unable to resolve at least one subnet. Evaluated %d subnets: %d are tagged for other clusters, %d have insufficient available IP addresses, and %d don't have an associated IPv6 CIDR block, which is required for IPv6-enabled proxies",
				len(subnets), len(categorizedSubnets.ineligibleClusterTag), len(categorizedSubnets.insufficientIPs), len(categorizedSubnets.missingIPv6CIDR))
		}
		return nil, fmt.Errorf("unable to resolve at least one subnet. Evaluated %d subnets: %d are tagged for other clusters, and %d have insufficient available IP addresses",
			len(subnets), len(categorizedSubnets.ineligibleClusterTag), len(categorizedSubnets.insufficientIPs))
	}
	chosenSubnets, err := r.filterSubnetsInAvailabilityZones(ctx, chosenSubnets)
	if err != nil {
		return nil, err
	}
	if err := r.validateSubnetsMinimalCount(chosenSubnets); err != nil {
		return nil, err
	}
	return chosenSubnets, nil
}

type categorizeSubnetsByEligibilityResult struct {
	eligible             []ec2types.Subnet
	ineligibleClusterTag []ec2types.Subnet
	insufficientIPs      []ec2types.Subnet
	missingIPv6CIDR      []ec2types.Subnet
}

// categorizeSubnetsByEligibility will categorize subnets based on eligibility
func (r *defaultSubnetsResolver) categorizeSubnetsByEligibility(subnets []ec2types.Subnet, resolveOpts SubnetsResolveOptions) categorizeSubnetsByEligibilityResult {
	var ret categorizeSubnetsByEligibilityResult
	for _, subnet := range subnets {
		if !r.isSubnetContainsEligibleClusterTag(subnet) {
			ret.ineligibleClusterTag = append(ret.ineligibleClusterTag, subnet)
			continue
		}
		if !r.isSubnetContainsSufficientIPAddresses(subnet) {
			ret.insufficientIPs = append(ret.insufficientIPs, subnet)
			continue
		}
		if resolveOpts.IPv6Required && !isSubnetContainsIPv6CIDR(subnet) {
			ret.missingIPv6CIDR = append(ret.missingIPv6CIDR, subnet)
			continue
		}
		ret.eligible = append(ret.eligible, subnet)
	}
	return ret
}

// isSubnetContainsIPv6CIDR reports whether the subnet has at least one IPv6 CIDR
// block in the "associated" state.
func isSubnetContainsIPv6CIDR(subnet ec2types.Subnet) bool {
	for _, association := range subnet.Ipv6CidrBlockAssociationSet {
		if association.Ipv6CidrBlock == nil || awssdk.ToString(association.Ipv6CidrBlock) == "" {
			continue
		}
		if association.Ipv6CidrBlockState != nil &&
			association.Ipv6CidrBlockState.State != ec2types.SubnetCidrBlockStateCodeAssociated {
			continue
		}
		return true
	}
	return false
}

// chooseSubnetsPerAZ will choose one subnet per AZ.
// * subnets with current cluster tag will be prioritized.
func (r *defaultSubnetsResolver) chooseSubnetsPerAZ(subnets []ec2types.Subnet) []ec2types.Subnet {
	subnetsByAZ := mapSDKSubnetsByAZ(subnets)
	chosenSubnets := make([]ec2types.Subnet, 0, len(subnetsByAZ))
	for az, azSubnets := range subnetsByAZ {
		if len(azSubnets) == 1 {
			chosenSubnets = append(chosenSubnets, azSubnets[0])
		} else if len(azSubnets) > 1 {
			sort.Slice(azSubnets, func(i, j int) bool {
				subnetIHasCurrentClusterTag := r.isSubnetContainsCurrentClusterTag(azSubnets[i])
				subnetJHasCurrentClusterTag := r.isSubnetContainsCurrentClusterTag(azSubnets[j])
				if subnetIHasCurrentClusterTag && (!subnetJHasCurrentClusterTag) {
					return true
				} else if (!subnetIHasCurrentClusterTag) && subnetJHasCurrentClusterTag {
					return false
				}
				return awssdk.ToString(azSubnets[i].SubnetId) < awssdk.ToString(azSubnets[j].SubnetId)
			})
			r.logger.V(1).Info("multiple subnets in the same AvailabilityZone", "AvailabilityZone", az,
				"chosen", azSubnets[0].SubnetId, "ignored", extractSubnetIDs(azSubnets[1:]))
			chosenSubnets = append(chosenSubnets, azSubnets[0])
		}
	}
	sortSubnetsByID(chosenSubnets)
	return chosenSubnets
}

// isSubnetContainsCurrentClusterTag checks whether a subnet is tagged with current Kubernetes cluster tag.
func (r *defaultSubnetsResolver) isSubnetContainsCurrentClusterTag(subnet ec2types.Subnet) bool {
	clusterResourceTagKey := fmt.Sprintf("%s/%s", clusterResourceTagPrefix, r.clusterName)
	for _, tag := range subnet.Tags {
		tagKey := awssdk.ToString(tag.Key)
		if tagKey == clusterResourceTagKey {
			return true
		}
	}
	return false
}

// isSubnetContainsEligibleClusterTag checks whether a subnet is eligible based on Kubernetes cluster tag existence.
// Returns true if either:
//   - The subnet has no Kubernetes cluster tags at all
//   - The subnet has a Kubernetes cluster tag matching the current cluster
//   - The clusterTagCheck feature is disabled
//
// Returns false if the subnet only contains tags for other clusters and subnetsClusterTagCheck is enabled.
func (r *defaultSubnetsResolver) isSubnetContainsEligibleClusterTag(subnet ec2types.Subnet) bool {
	if !r.clusterTagCheckEnabled {
		return true
	}
	clusterResourceTagKey := fmt.Sprintf("%s/%s", clusterResourceTagPrefix, r.clusterName)
	hasClusterResourceTagPrefix := false
	for _, tag := range subnet.Tags {
		tagKey := awssdk.ToString(tag.Key)
		if tagKey == clusterResourceTagKey {
			return true
		}
		if strings.HasPrefix(tagKey, clusterResourceTagPrefix) {
			hasClusterResourceTagPrefix = true
		}
	}
	return !hasClusterResourceTagPrefix
}

// isSubnetContainsSufficientIPAddresses checks whether subnet has minimal AvailableIPAddressCount needed.
func (r *defaultSubnetsResolver) isSubnetContainsSufficientIPAddresses(subnet ec2types.Subnet) bool {
	return awssdk.ToInt32(subnet.AvailableIpAddressCount) >= r.minimalAvailableIPAddressCount
}

// validateSubnetsIPv6CIDR validates explicitly specified subnets have an associated
// IPv6 CIDR block when the proxy enables IPv6. Failing here (rather than letting the
// ELBProxy API reject the request) names the offending subnets.
func (r *defaultSubnetsResolver) validateSubnetsIPv6CIDR(subnets []ec2types.Subnet, resolveOpts SubnetsResolveOptions) error {
	if !resolveOpts.IPv6Required {
		return nil
	}
	var subnetsWithoutIPv6 []string
	for _, subnet := range subnets {
		if !isSubnetContainsIPv6CIDR(subnet) {
			subnetsWithoutIPv6 = append(subnetsWithoutIPv6, awssdk.ToString(subnet.SubnetId))
		}
	}
	if len(subnetsWithoutIPv6) > 0 {
		return fmt.Errorf("subnets %v do not have an associated IPv6 CIDR block, which is required for IPv6-enabled proxies", subnetsWithoutIPv6)
	}
	return nil
}

// validateSubnetsAZExclusivity validates subnets belong to different AZs.
// subnets passed-in must be non-empty
func (r *defaultSubnetsResolver) validateSubnetsAZExclusivity(subnets []ec2types.Subnet) error {
	subnetsByAZ := mapSDKSubnetsByAZ(subnets)
	for az, azSubnets := range subnetsByAZ {
		if len(azSubnets) > 1 {
			return fmt.Errorf("multiple subnets in same Availability Zone %v: %v", az, extractSubnetIDs(azSubnets))
		}
	}
	return nil
}

// validateSubnetsAllInAvailabilityZones validates all subnets are in standard availability zones.
// Returns an explicit error if any subnet is in an outpost, local zone, or wavelength zone.
func (r *defaultSubnetsResolver) validateSubnetsAllInAvailabilityZones(ctx context.Context, subnets []ec2types.Subnet) error {
	var azIDs []string
	for _, subnet := range subnets {
		if subnet.OutpostArn != nil && len(*subnet.OutpostArn) != 0 {
			return fmt.Errorf("subnet %v is in an outpost, only standard availability zones are supported", awssdk.ToString(subnet.SubnetId))
		}
		azIDs = append(azIDs, awssdk.ToString(subnet.AvailabilityZoneId))
	}
	if len(azIDs) == 0 {
		return nil
	}
	azInfoByAZID, err := r.azInfoProvider.FetchAZInfos(ctx, azIDs)
	if err != nil {
		return fmt.Errorf("failed to batch-fetch AZ infos: %w", err)
	}
	for _, subnet := range subnets {
		azID := awssdk.ToString(subnet.AvailabilityZoneId)
		azInfo, exists := azInfoByAZID[azID]
		if !exists {
			return fmt.Errorf("cannot resolve AZ info for subnet %v in AZ %v", awssdk.ToString(subnet.SubnetId), azID)
		}
		zoneType := awssdk.ToString(azInfo.ZoneType)
		if zoneType != zoneTypeAvailabilityZone {
			return fmt.Errorf("subnet %v is in zone type %q, only standard availability zones are supported", awssdk.ToString(subnet.SubnetId), zoneType)
		}
	}
	return nil
}

// filterSubnetsInAvailabilityZones filters out subnets that are not in standard availability zones.
// ELBProxy only supports availability zones; local zones, wavelength zones, and outposts are ignored.
func (r *defaultSubnetsResolver) filterSubnetsInAvailabilityZones(ctx context.Context, subnets []ec2types.Subnet) ([]ec2types.Subnet, error) {
	var nonOutpostSubnets []ec2types.Subnet
	var azIDs []string
	for _, subnet := range subnets {
		if subnet.OutpostArn != nil && len(*subnet.OutpostArn) != 0 {
			continue
		}
		nonOutpostSubnets = append(nonOutpostSubnets, subnet)
		azIDs = append(azIDs, awssdk.ToString(subnet.AvailabilityZoneId))
	}
	if len(azIDs) == 0 {
		return nil, nil
	}
	azInfoByAZID, err := r.azInfoProvider.FetchAZInfos(ctx, azIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to batch-fetch AZ infos: %w", err)
	}
	var filtered []ec2types.Subnet
	for _, subnet := range nonOutpostSubnets {
		azID := awssdk.ToString(subnet.AvailabilityZoneId)
		azInfo, exists := azInfoByAZID[azID]
		if !exists {
			continue
		}
		if awssdk.ToString(azInfo.ZoneType) == zoneTypeAvailabilityZone {
			filtered = append(filtered, subnet)
		}
	}
	return filtered, nil
}

// validateSubnetsMinimalCount validates subnets meets minimal count requirement.
// ELBProxy always requires a minimum of 2 subnets.
func (r *defaultSubnetsResolver) validateSubnetsMinimalCount(subnets []ec2types.Subnet) error {
	if len(subnets) < defaultMinimalSubnetCount {
		return fmt.Errorf("subnets count less than minimal required count: %v < %v", len(subnets), defaultMinimalSubnetCount)
	}
	return nil
}

// mapSDKSubnetsByAZ builds the subnets slice by AZ mapping.
func mapSDKSubnetsByAZ(subnets []ec2types.Subnet) map[string][]ec2types.Subnet {
	subnetsByAZ := make(map[string][]ec2types.Subnet)
	for _, subnet := range subnets {
		subnetAZ := awssdk.ToString(subnet.AvailabilityZone)
		subnetsByAZ[subnetAZ] = append(subnetsByAZ[subnetAZ], subnet)
	}
	return subnetsByAZ
}

// sortSubnetsByID sorts given subnets slice by subnetID.
func sortSubnetsByID(subnets []ec2types.Subnet) {
	sort.Slice(subnets, func(i, j int) bool {
		return awssdk.ToString(subnets[i].SubnetId) < awssdk.ToString(subnets[j].SubnetId)
	})
}

// extractSubnetIDs for given subnets.
func extractSubnetIDs(subnets []ec2types.Subnet) []string {
	subnetIDs := make([]string, 0, len(subnets))
	for _, subnet := range subnets {
		subnetIDs = append(subnetIDs, awssdk.ToString(subnet.SubnetId))
	}
	return subnetIDs
}
