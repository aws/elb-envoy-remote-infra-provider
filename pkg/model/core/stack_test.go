// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"sort"
	"testing"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core/util"
	"github.com/stretchr/testify/assert"
)

func Test_defaultStack_StackID(t *testing.T) {
	tests := []struct {
		name  string
		stack core.Stack
		want  core.StackID
	}{
		{
			name:  "stack with ID",
			stack: core.NewDefaultStack(core.StackID{Namespace: "namespace", Name: "name"}),
			want:  core.StackID{Namespace: "namespace", Name: "name"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.stack.StackID()
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_defaultStack_ListResources(t *testing.T) {
	tests := []struct {
		name       string
		operations func(stack core.Stack)
		wantIDs    []string
		wantErr    error
	}{
		{
			name:       "no fake resources",
			operations: func(stack core.Stack) {},
			wantIDs:    nil,
			wantErr:    nil,
		},
		{
			name: "one fake resources",
			operations: func(stack core.Stack) {
				util.NewFakeResource(stack, "fake", "id-A", util.FakeResourceSpec{}, nil)
			},
			wantIDs: []string{"id-A"},
			wantErr: nil,
		},
		{
			name: "multiple fake resources",
			operations: func(stack core.Stack) {
				util.NewFakeResource(stack, "fake", "id-A", util.FakeResourceSpec{}, nil)
				util.NewFakeResource(stack, "fake", "id-B", util.FakeResourceSpec{}, nil)
			},
			wantIDs: []string{"id-A", "id-B"},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := core.NewDefaultStack(core.StackID{Namespace: "namespace", Name: "name"})
			tt.operations(s)
			var gotFakeResources []*util.FakeResource
			err := s.ListResources(&gotFakeResources)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
				sort.Slice(gotFakeResources, func(i, j int) bool {
					return gotFakeResources[i].ID() < gotFakeResources[j].ID()
				})
				var gotIDs []string
				for _, r := range gotFakeResources {
					gotIDs = append(gotIDs, r.ID())
				}
				assert.Equal(t, tt.wantIDs, gotIDs)
			}
		})
	}
}
