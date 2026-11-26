// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ec2

import (
	"context"
	"fmt"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/pkg/errors"
)

var _ core.Resource = &SecurityGroup{}

// SecurityGroup represents a EC2 SecurityGroup.
type SecurityGroup struct {
	core.ResourceMeta `json:"-"`

	//  desired state of SecurityGroup
	Spec SecurityGroupSpec `json:"spec"`

	// observed state of SecurityGroup
	Status *SecurityGroupStatus `json:"status,omitempty"`
}

// NewSecurityGroup constructs new SecurityGroup resource.
func NewSecurityGroup(stack core.Stack, id string, spec SecurityGroupSpec) (*SecurityGroup, error) {
	sg := &SecurityGroup{
		ResourceMeta: core.NewResourceMeta(stack, "AWS::EC2::SecurityGroup", id),
		Spec:         spec,
		Status:       nil,
	}
	if err := stack.AddResource(sg); err != nil {
		return nil, fmt.Errorf("failed to add SecurityGroup resource: %w", err)
	}
	return sg, nil
}

// SetStatus sets the SecurityGroup's status
func (sg *SecurityGroup) SetStatus(status SecurityGroupStatus) {
	sg.Status = &status
}

// GroupID returns a token for this SecurityGroup's groupID.
func (sg *SecurityGroup) GroupID() core.StringToken {
	return core.NewResourceFieldStringToken(sg, "status/groupID",
		func(ctx context.Context, res core.Resource, fieldPath string) (s string, err error) {
			sg := res.(*SecurityGroup)
			if sg.Status == nil {
				return "", errors.Errorf("SecurityGroup is not fulfilled yet: %v", sg.ID())
			}
			return sg.Status.GroupID, nil
		},
	)
}

// SecurityGroupSpec defines the desired state of SecurityGroup
type SecurityGroupSpec struct {
	// The name of the security group.
	GroupName string `json:"groupName"`

	// A description for the security group.
	Description string `json:"description"`

	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// +optional
	Ingress []IPPermission `json:"ingress,omitempty"`
}

// SecurityGroupStatus defines the observed state of SecurityGroup
type SecurityGroupStatus struct {
	// The ID of the security group.
	GroupID string `json:"groupID"`
}
