// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/pkg/errors"
)

var _ core.Resource = &FakeResource{}

func NewFakeResource(stack core.Stack, resType string, id string, spec FakeResourceSpec, status *FakeResourceStatus) *FakeResource {
	r := &FakeResource{
		ResourceMeta: core.NewResourceMeta(stack, resType, id),
		Spec:         spec,
		Status:       status,
	}
	stack.AddResource(r)
	return r
}

func (r *FakeResource) FieldB() core.StringToken {
	return core.NewResourceFieldStringToken(r, "status/fieldB",
		func(ctx context.Context, res core.Resource, fieldPath string) (s string, err error) {
			r := res.(*FakeResource)
			if r.Status == nil {
				return "", errors.Errorf("FakeResource is not fulfilled yet: %v", r.ID())
			}
			return r.Status.FieldB, nil
		},
	)
}

type FakeResource struct {
	core.ResourceMeta `json:"-"`

	Spec   FakeResourceSpec    `json:"spec"`
	Status *FakeResourceStatus `json:"status,omitempty"`
}

type FakeResourceSpec struct {
	FieldA []core.StringToken `json:"fieldA"`
}

type FakeResourceStatus struct {
	FieldB string `json:"fieldB"`
}
