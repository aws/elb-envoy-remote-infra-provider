// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"context"
	"testing"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core/util"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestLiteralStringToken_Resolve(t *testing.T) {
	tests := []struct {
		name    string
		t       core.LiteralStringToken
		want    string
		wantErr error
	}{
		{
			name: "empty string",
			t:    core.LiteralStringToken(""),
			want: "",
		},
		{
			name: "non-empty string",
			t:    core.LiteralStringToken("my-token"),
			want: "my-token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.t.Resolve(context.Background())
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestLiteralStringToken_Dependencies(t *testing.T) {
	tests := []struct {
		name string
		t    core.LiteralStringToken
		want []core.Resource
	}{
		{
			name: "LiteralStringToken have no dependency",
			t:    core.LiteralStringToken("my-token"),
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.t.Dependencies()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResourceFieldStringToken_Resolve(t *testing.T) {
	stack := core.NewDefaultStack(core.StackID{Namespace: "namespace", Name: "name"})
	resWithStatus := util.NewFakeResource(stack, "Fake", "fake", util.FakeResourceSpec{}, &util.FakeResourceStatus{FieldB: "value"})
	resWithoutStatus := util.NewFakeResource(stack, "Fake", "fake-no-status", util.FakeResourceSpec{}, nil)

	tests := []struct {
		name    string
		t       core.StringToken
		want    string
		wantErr error
	}{
		{
			name: "ResourceFieldStringToken with status fulfilled",
			t:    resWithStatus.FieldB(),
			want: "value",
		},
		{
			name:    "ResourceFieldStringToken without status fulfilled",
			t:       resWithoutStatus.FieldB(),
			wantErr: errors.New("FakeResource is not fulfilled yet: fake-no-status"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t1 *testing.T) {
			got, err := tt.t.Resolve(context.Background())
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestResourceFieldStringToken_Dependencies(t *testing.T) {
	stack := core.NewDefaultStack(core.StackID{Namespace: "namespace", Name: "name"})
	resWithStatus := util.NewFakeResource(stack, "Fake", "fake", util.FakeResourceSpec{}, &util.FakeResourceStatus{FieldB: "value"})
	tests := []struct {
		name string
		t    core.StringToken
		want []core.Resource
	}{
		{
			name: "ResourceFieldStringToken have dependency on the resource itself",
			t:    resWithStatus.FieldB(),
			want: []core.Resource{resWithStatus},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t1 *testing.T) {
			got := tt.t.Dependencies()
			assert.Equal(t, tt.want, got)
		})
	}
}
