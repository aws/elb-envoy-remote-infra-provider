// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToAPISecurityGroupDetails(t *testing.T) {
	// Empty must be nil so the field is omitted and the live security groups are kept.
	assert.Nil(t, toAPISecurityGroupDetails(nil))
	assert.Nil(t, toAPISecurityGroupDetails([]SecurityGroupDetails{}))

	out := toAPISecurityGroupDetails([]SecurityGroupDetails{{SecurityGroupId: "sg-1"}, {SecurityGroupId: "sg-2"}})
	if assert.Len(t, out, 2) {
		assert.Equal(t, "sg-1", *out[0].SecurityGroupId)
		assert.Equal(t, "sg-2", *out[1].SecurityGroupId)
	}
}
