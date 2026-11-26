// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package tracking

import (
	"fmt"
	"strings"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
)

// Tag key constants for ELBProxy resource tracking.
const (
	// TagKeyCluster identifies the Kubernetes cluster.
	TagKeyCluster = "gateway.elbproxy.aws/cluster"

	// TagKeyStack identifies the stack (gateway namespace/name) that owns this resource.
	TagKeyStack = "gateway.elbproxy.aws/stack"

	// TagKeyResourceType identifies the resource type.
	TagKeyResourceType = "gateway.elbproxy.aws/resource"

	// TagKeyManagedBy identifies the controller.
	TagKeyManagedBy = "gateway.elbproxy.aws/managed-by"

	// TagKeyControllerNS identifies the controller instance by namespace.
	TagKeyControllerNS = "gateway.elbproxy.aws/controller-ns"

	// TagKeyDeployMode indicates the gateway deployment mode for this resource.
	// Only present on merged gateway resources.
	TagKeyDeployMode = "gateway.elbproxy.aws/deploy-mode"

	// Resource IDs, written as the TagKeyResourceType value.
	ResourceIDProxy                 = "Proxy"
	ResourceIDFrontendSecurityGroup = "FrontendSecurityGroup"
	ResourceIDBackendSecurityGroup  = "BackendSecurityGroup"

	// TagValueManagedBy is the value for controller-managed resources.
	TagValueManagedBy = "elb-envoy-remote-infra-provider"

	// TagValueDeployModeMerged indicates a merged gateway deployment.
	TagValueDeployModeMerged = "merged"
)

// TagFilter represents a set of tag-based filters for resource discovery.
// Keys are tag names and values are the acceptable values for that tag.
type TagFilter map[string][]string

// Provider generates tags and filters for tracking controller-managed resources.
// It is the single source of truth for the ELBProxy tagging convention.
type Provider interface {
	// BuildOwnershipTags creates tags for controller-managed Gateway-level resources.
	BuildOwnershipTags(gateway string, resourceType string) map[string]string

	// BuildClusterTags creates tags for cluster-level resources (e.g., Backend SG).
	BuildClusterTags(resourceType string) map[string]string

	// IsOwnedByController checks if a resource's tags indicate ownership by the controller for a specific gateway.
	IsOwnedByController(tags map[string]string, gateway string) bool

	// IsClusterResource checks if a resource's tags indicate it is a cluster-level resource.
	IsClusterResource(tags map[string]string) bool

	// ExtractGatewayName extracts the gateway name from resource tags.
	ExtractGatewayName(tags map[string]string) (string, error)

	// StackTags returns the tag filters for discovering resources belonging to a specific stack (gateway).
	StackTags(stackID string) map[string]string

	// StackLabels returns the Kubernetes labels used to discover resources belonging
	// to a specific stack (gateway) in the cluster.
	StackLabels(stack core.Stack) map[string]string

	// KubernetesSafeStackLabels returns the same logical labels as StackLabels with
	// values sanitized to satisfy Kubernetes label-value rules. Use this when applying
	// labels to Kubernetes objects; use StackLabels for AWS tag-equivalent contexts.
	KubernetesSafeStackLabels(stack core.Stack) map[string]string

	// ResourceIDTagKey returns the tag key used to identify individual resources.
	ResourceIDTagKey() string

	// ResourceTags provides the tags for stack resources, merging stack tags with resource-specific and additional tags.
	ResourceTags(stack core.Stack, res core.Resource, additionalTags map[string]string) map[string]string
}

type defaultProvider struct {
	clusterName string
}

// NewProvider creates a new tracking Provider for the given cluster.
func NewProvider(clusterName string) Provider {
	return &defaultProvider{
		clusterName: clusterName,
	}
}

// TODO(multi-tenancy): consider stamping the controller-instance identity
// (controller-name, and/or controller-ns) on all resources here for
// observability/audit. NOT required for correctness: gateway-scoped resources
// are already tenant-isolated via the stack tag (gateway namespace/name), and
// the only cluster-level resource (backend SG) is already scoped by controller-ns
// in backend_sg_provider.go. This would be a stamping-only addition (do NOT add
// it to the discovery filters / StackTags, or pre-existing resources would stop
// matching and get orphaned/recreated). controller-name is the truer ownership
// identity (tags allow "/" and length); controller-ns is the label-safe proxy.
func (p *defaultProvider) BuildOwnershipTags(gateway string, resourceType string) map[string]string {
	return map[string]string{
		TagKeyCluster:      p.clusterName,
		TagKeyStack:        gateway,
		TagKeyResourceType: resourceType,
		TagKeyManagedBy:    TagValueManagedBy,
	}
}

func (p *defaultProvider) BuildClusterTags(resourceType string) map[string]string {
	return map[string]string{
		TagKeyCluster:      p.clusterName,
		TagKeyResourceType: resourceType,
		TagKeyManagedBy:    TagValueManagedBy,
	}
}

func (p *defaultProvider) IsOwnedByController(tags map[string]string, gateway string) bool {
	return tags[TagKeyCluster] == p.clusterName &&
		tags[TagKeyStack] == gateway &&
		tags[TagKeyManagedBy] == TagValueManagedBy
}

func (p *defaultProvider) IsClusterResource(tags map[string]string) bool {
	return tags[TagKeyCluster] == p.clusterName &&
		tags[TagKeyManagedBy] == TagValueManagedBy
}

func (p *defaultProvider) ExtractGatewayName(tags map[string]string) (string, error) {
	gateway, ok := tags[TagKeyStack]
	if !ok || gateway == "" {
		return "", fmt.Errorf("gateway tag not found on resource")
	}
	return gateway, nil
}

func (p *defaultProvider) StackTags(stackID string) map[string]string {
	return map[string]string{
		TagKeyCluster:   p.clusterName,
		TagKeyStack:     stackID,
		TagKeyManagedBy: TagValueManagedBy,
	}
}

// StackLabels returns Kubernetes labels for discovering resources belonging to a stack.
// Labels mirror the AWS tag taxonomy but use dot-separated keys to stay within the
// 63-character Kubernetes label-value limit when clusterName or stackID are long.
func (p *defaultProvider) StackLabels(stack core.Stack) map[string]string {
	return map[string]string{
		TagKeyCluster:   p.clusterName,
		TagKeyStack:     stack.StackID().String(),
		TagKeyManagedBy: TagValueManagedBy,
	}
}

// KubernetesSafeStackLabels returns the same logical labels as StackLabels but with
// values normalized to satisfy Kubernetes label-value rules. StackID().String() can
// contain a "/" separator (namespace/name) which is invalid in label values, so it is
// replaced with "_". Values are also truncated to the 63-character limit if needed.
func (p *defaultProvider) KubernetesSafeStackLabels(stack core.Stack) map[string]string {
	labels := p.StackLabels(stack)
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = sanitizeLabelValue(v)
	}
	return out
}

// sanitizeLabelValue normalizes a string into a valid Kubernetes label value.
// Per the Kubernetes spec, label values must be 63 characters or fewer and consist
// of alphanumerics, '-', '_', or '.', starting and ending with an alphanumeric.
func sanitizeLabelValue(v string) string {
	// Replace characters valid in tag values but invalid in label values.
	v = strings.ReplaceAll(v, "/", "_")
	if len(v) > 63 {
		v = v[:63]
	}
	// Trim any leading/trailing non-alphanumerics that would invalidate the label.
	v = strings.Trim(v, "-_.")
	return v
}

func (p *defaultProvider) ResourceIDTagKey() string {
	return TagKeyResourceType
}

func (p *defaultProvider) ResourceTags(stack core.Stack, res core.Resource, additionalTags map[string]string) map[string]string {
	tags := p.StackTags(stack.StackID().String())
	tags[p.ResourceIDTagKey()] = res.ID()
	for k, v := range additionalTags {
		tags[k] = v
	}
	return tags
}

// TagsAsTagFilter converts a tag map to a TagFilter for use with ListSecurityGroups.
func TagsAsTagFilter(tags map[string]string) TagFilter {
	tf := make(TagFilter, len(tags))
	for k, v := range tags {
		tf[k] = []string{v}
	}
	return tf
}
