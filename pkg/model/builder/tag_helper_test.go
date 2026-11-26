// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestTagHelper_ProcessTags(t *testing.T) {
	tests := []struct {
		name                string
		defaultTags         map[string]string
		externalManagedTags []string
		userTags            map[string]string
		wantTags            map[string]string
		wantErr             string
	}{
		{
			name:        "default tags applied when no user tags",
			defaultTags: map[string]string{"env": "prod", "team": "platform"},
			userTags:    nil,
			wantTags:    map[string]string{"env": "prod", "team": "platform"},
		},
		{
			name:        "user tags override default tags",
			defaultTags: map[string]string{"env": "prod", "team": "platform"},
			userTags:    map[string]string{"env": "staging"},
			wantTags:    map[string]string{"env": "staging", "team": "platform"},
		},
		{
			name:        "user tags merged with default tags — no overlap",
			defaultTags: map[string]string{"team": "platform"},
			userTags:    map[string]string{"cost-center": "12345"},
			wantTags:    map[string]string{"team": "platform", "cost-center": "12345"},
		},
		{
			name:     "no default tags — user tags returned as-is",
			userTags: map[string]string{"cost-center": "12345"},
			wantTags: map[string]string{"cost-center": "12345"},
		},
		{
			name:     "no default tags and no user tags — empty map",
			userTags: nil,
			wantTags: map[string]string{},
		},
		{
			name:                "user tag collides with external managed key — error",
			externalManagedTags: []string{"CostCenter"},
			userTags:            map[string]string{"CostCenter": "12345"},
			wantErr:             `"CostCenter" is externally managed`,
		},
		{
			name:                "external managed key not in user tags — no error",
			externalManagedTags: []string{"CostCenter"},
			userTags:            map[string]string{"team": "platform"},
			wantTags:            map[string]string{"team": "platform"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewTagHelper(TagHelperConfig{
				ExternalManagedTags:               sets.New(tt.externalManagedTags...),
				DefaultTags:                       tt.defaultTags,
				AdditionalTagsOverrideDefaultTags: true,
			})
			got, err := h.ProcessTags(tt.userTags)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantTags, got)
			}
		})
	}
}
