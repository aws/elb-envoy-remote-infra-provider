// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/algorithm"
)

// TagHelperConfig holds configuration for tag processing.
type TagHelperConfig struct {
	// ExternalManagedTags are tag keys owned by an external system.
	ExternalManagedTags sets.Set[string]
	// DefaultTags are controller-level tags applied to every proxy resource.
	DefaultTags map[string]string
	// AdditionalTagsOverrideDefaultTags controls merge order.
	// When true, user-supplied tags take precedence over DefaultTags.
	AdditionalTagsOverrideDefaultTags bool
}

// TagHelper provides common tag processing functionality.
type TagHelper interface {
	// ProcessTags validates userTags against externally managed keys and
	// merges them with default tags.
	ProcessTags(userTags map[string]string) (map[string]string, error)
}

// NewTagHelper creates a new TagHelper with the given configuration.
func NewTagHelper(config TagHelperConfig) TagHelper {
	return &tagHelperImpl{config: config}
}

type tagHelperImpl struct {
	config TagHelperConfig
}

func (t *tagHelperImpl) ProcessTags(userTags map[string]string) (map[string]string, error) {
	if err := t.validateTagCollisionWithExternalManagedTags(userTags); err != nil {
		return nil, err
	}
	if t.config.AdditionalTagsOverrideDefaultTags {
		return algorithm.MergeStringMap(t.config.DefaultTags, userTags), nil
	}
	return algorithm.MergeStringMap(userTags, t.config.DefaultTags), nil
}

func (t *tagHelperImpl) validateTagCollisionWithExternalManagedTags(tags map[string]string) error {
	for k := range tags {
		if t.config.ExternalManagedTags.Has(k) {
			return fmt.Errorf("tag key %q is externally managed and cannot be set via ELBProxyConfiguration", k)
		}
	}
	return nil
}
