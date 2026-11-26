// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algorithm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDiffStringMap(t *testing.T) {
	type args struct {
		desired map[string]string
		current map[string]string
	}
	tests := []struct {
		name       string
		args       args
		wantUpdate map[string]string
		wantRemove map[string]string
	}{
		{
			name: "standard case",
			args: args{
				desired: map[string]string{
					"a": "a1",
					"b": "b1",
					"c": "c1",
				},
				current: map[string]string{
					"a": "a1",
					"b": "b2",
					"d": "d1",
				},
			},
			wantUpdate: map[string]string{
				"b": "b1",
				"c": "c1",
			},
			wantRemove: map[string]string{
				"d": "d1",
			},
		},
		{
			name: "only need to update",
			args: args{
				desired: map[string]string{
					"a": "a1",
					"b": "b1",
					"c": "c1",
				},
				current: map[string]string{
					"a": "a1",
					"b": "b1",
				},
			},
			wantUpdate: map[string]string{
				"c": "c1",
			},
			wantRemove: map[string]string{},
		},
		{
			name: "only need to remove",
			args: args{
				desired: map[string]string{
					"a": "a1",
					"b": "b1",
				},
				current: map[string]string{
					"a": "a1",
					"b": "b1",
					"c": "c1",
				},
			},
			wantUpdate: map[string]string{},
			wantRemove: map[string]string{
				"c": "c1",
			},
		},
		{
			name: "both map are equal",
			args: args{
				desired: map[string]string{
					"a": "a1",
					"b": "b1",
					"c": "c1",
				},
				current: map[string]string{
					"a": "a1",
					"b": "b1",
					"c": "c1",
				},
			},
			wantUpdate: map[string]string{},
			wantRemove: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUpdate, gotRemove := DiffStringMap(tt.args.desired, tt.args.current)
			assert.Equal(t, tt.wantUpdate, gotUpdate)
			assert.Equal(t, tt.wantRemove, gotRemove)
		})
	}
}

func TestMergeStringMap(t *testing.T) {
	tests := []struct {
		name string
		maps []map[string]string
		want map[string]string
	}{
		{
			name: "no maps",
			maps: nil,
			want: map[string]string{},
		},
		{
			name: "single map",
			maps: []map[string]string{
				{"a": "a1", "b": "b1"},
			},
			want: map[string]string{"a": "a1", "b": "b1"},
		},
		{
			name: "disjoint keys are unioned",
			maps: []map[string]string{
				{"a": "a1"},
				{"b": "b1"},
			},
			want: map[string]string{"a": "a1", "b": "b1"},
		},
		{
			name: "later map overrides earlier on key collision",
			maps: []map[string]string{
				{"a": "a1", "b": "b1"},
				{"b": "b2", "c": "c1"},
			},
			want: map[string]string{"a": "a1", "b": "b2", "c": "c1"},
		},
		{
			name: "three maps — last wins",
			maps: []map[string]string{
				{"k": "v1"},
				{"k": "v2"},
				{"k": "v3"},
			},
			want: map[string]string{"k": "v3"},
		},
		{
			name: "nil map in the middle is skipped",
			maps: []map[string]string{
				{"a": "a1"},
				nil,
				{"b": "b1"},
			},
			want: map[string]string{"a": "a1", "b": "b1"},
		},
		{
			name: "empty map contributes nothing",
			maps: []map[string]string{
				{"a": "a1"},
				{},
				{"b": "b1"},
			},
			want: map[string]string{"a": "a1", "b": "b1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeStringMap(tt.maps...)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMergeStringMap_doesNotMutateInputs(t *testing.T) {
	a := map[string]string{"a": "a1"}
	b := map[string]string{"a": "a2", "b": "b1"}

	aCopy := map[string]string{"a": "a1"}
	bCopy := map[string]string{"a": "a2", "b": "b1"}

	_ = MergeStringMap(a, b)

	assert.Equal(t, aCopy, a, "first input map was mutated")
	assert.Equal(t, bCopy, b, "second input map was mutated")
}

func TestRemoveKeysByPrefix(t *testing.T) {
	tests := []struct {
		name   string
		input  map[string]string
		prefix string
		want   map[string]string
	}{
		{
			name:   "removes aws: prefixed keys",
			input:  map[string]string{"aws:cloudformation:stack-name": "my-stack", "gateway.elbproxy.aws/cluster": "my-cluster", "aws:cloudformation:logical-id": "NLB"},
			prefix: "aws:",
			want:   map[string]string{"gateway.elbproxy.aws/cluster": "my-cluster"},
		},
		{
			name:   "no matching prefix",
			input:  map[string]string{"gateway.elbproxy.aws/cluster": "my-cluster", "gateway.elbproxy.aws/stack": "default/svc"},
			prefix: "aws:",
			want:   map[string]string{"gateway.elbproxy.aws/cluster": "my-cluster", "gateway.elbproxy.aws/stack": "default/svc"},
		},
		{
			name:   "all keys match prefix",
			input:  map[string]string{"aws:cloudformation:stack-name": "s", "aws:cloudformation:stack-id": "id"},
			prefix: "aws:",
			want:   map[string]string{},
		},
		{
			name:   "empty map",
			input:  map[string]string{},
			prefix: "aws:",
			want:   map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RemoveKeysByPrefix(tt.input, tt.prefix)
			assert.Equal(t, tt.want, tt.input)
		})
	}
}

func TestDiffStringMapIgnoreAWSTags(t *testing.T) {
	tests := []struct {
		name       string
		desired    map[string]string
		current    map[string]string
		wantUpdate map[string]string
		wantRemove map[string]string
	}{
		{
			name: "aws: tags in current are not removed",
			desired: map[string]string{
				"gateway.elbproxy.aws/cluster": "my-cluster",
				"gateway.elbproxy.aws/stack":   "default/svc",
			},
			current: map[string]string{
				"gateway.elbproxy.aws/cluster":  "my-cluster",
				"aws:cloudformation:stack-name": "my-stack",
				"aws:cloudformation:stack-id":   "arn:aws:cloudformation:us-east-1:123:stack/my-stack/abc",
				"aws:cloudformation:logical-id": "NLB",
			},
			wantUpdate: map[string]string{
				"gateway.elbproxy.aws/stack": "default/svc",
			},
			wantRemove: map[string]string{},
		},
		{
			name: "aws: tags in desired are not added",
			desired: map[string]string{
				"gateway.elbproxy.aws/cluster": "my-cluster",
				"aws:createdBy":                "should-be-ignored",
			},
			current: map[string]string{
				"gateway.elbproxy.aws/cluster": "my-cluster",
			},
			wantUpdate: map[string]string{},
			wantRemove: map[string]string{},
		},
		{
			name: "no aws: tags behaves like DiffStringMap",
			desired: map[string]string{
				"a": "1",
				"b": "2",
			},
			current: map[string]string{
				"a": "1",
				"c": "3",
			},
			wantUpdate: map[string]string{"b": "2"},
			wantRemove: map[string]string{"c": "3"},
		},
		{
			name:       "both empty",
			desired:    map[string]string{},
			current:    map[string]string{},
			wantUpdate: map[string]string{},
			wantRemove: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUpdate, gotRemove := DiffStringMapIgnoreAWSTags(tt.desired, tt.current)
			assert.Equal(t, tt.wantUpdate, gotUpdate)
			assert.Equal(t, tt.wantRemove, gotRemove)
		})
	}
}
