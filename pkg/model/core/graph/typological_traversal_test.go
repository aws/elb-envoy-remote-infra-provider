// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestTopologicalTraversal_EmptyGraph(t *testing.T) {
	g := NewDefaultResourceGraph()
	var visited []string

	err := TopologicalTraversal(g, func(uid ResourceUID) error {
		visited = append(visited, uid.ResID)
		return nil
	})

	assert.NoError(t, err)
	assert.Nil(t, visited)
}

func TestTopologicalTraversal_SingleNode(t *testing.T) {
	g := NewDefaultResourceGraph()
	g.AddNode(fakeResourceUID("A"))
	var visited []string

	err := TopologicalTraversal(g, func(uid ResourceUID) error {
		visited = append(visited, uid.ResID)
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, []string{"A"}, visited)
}

func TestTopologicalTraversal_LinearChain(t *testing.T) {
	g := NewDefaultResourceGraph()
	g.AddNode(fakeResourceUID("A"))
	g.AddNode(fakeResourceUID("B"))
	g.AddNode(fakeResourceUID("C"))
	g.AddEdge(fakeResourceUID("A"), fakeResourceUID("B"))
	g.AddEdge(fakeResourceUID("B"), fakeResourceUID("C"))

	var visited []string
	err := TopologicalTraversal(g, func(uid ResourceUID) error {
		visited = append(visited, uid.ResID)
		return nil
	})

	assert.NoError(t, err)
	idx := make(map[string]int)
	for i, id := range visited {
		idx[id] = i
	}
	assert.Less(t, idx["A"], idx["B"])
	assert.Less(t, idx["B"], idx["C"])
}

func TestTopologicalTraversal_DiamondDependency(t *testing.T) {
	g := NewDefaultResourceGraph()
	g.AddNode(fakeResourceUID("A"))
	g.AddNode(fakeResourceUID("B"))
	g.AddNode(fakeResourceUID("C"))
	g.AddNode(fakeResourceUID("D"))
	g.AddEdge(fakeResourceUID("A"), fakeResourceUID("B"))
	g.AddEdge(fakeResourceUID("A"), fakeResourceUID("C"))
	g.AddEdge(fakeResourceUID("B"), fakeResourceUID("D"))
	g.AddEdge(fakeResourceUID("C"), fakeResourceUID("D"))

	var visited []string
	err := TopologicalTraversal(g, func(uid ResourceUID) error {
		visited = append(visited, uid.ResID)
		return nil
	})

	assert.NoError(t, err)
	assert.Len(t, visited, 4)
	idx := make(map[string]int)
	for i, id := range visited {
		idx[id] = i
	}
	assert.Less(t, idx["A"], idx["B"])
	assert.Less(t, idx["A"], idx["C"])
	assert.Less(t, idx["B"], idx["D"])
	assert.Less(t, idx["C"], idx["D"])
}

func TestTopologicalTraversal_CycleDetection(t *testing.T) {
	g := NewDefaultResourceGraph()
	g.AddNode(fakeResourceUID("A"))
	g.AddNode(fakeResourceUID("B"))
	g.AddEdge(fakeResourceUID("A"), fakeResourceUID("B"))
	g.AddEdge(fakeResourceUID("B"), fakeResourceUID("A"))

	err := TopologicalTraversal(g, func(uid ResourceUID) error {
		return nil
	})

	assert.EqualError(t, err, "ResourceGraph is not a DAG")
}

func TestTopologicalTraversal_VisitorErrorPropagates(t *testing.T) {
	g := NewDefaultResourceGraph()
	g.AddNode(fakeResourceUID("A"))
	g.AddNode(fakeResourceUID("B"))
	g.AddEdge(fakeResourceUID("A"), fakeResourceUID("B"))

	visitErr := errors.New("visit failed")
	err := TopologicalTraversal(g, func(uid ResourceUID) error {
		if uid.ResID == "A" {
			return visitErr
		}
		return nil
	})

	assert.ErrorIs(t, err, visitErr)
}

func TestTopologicalTraversal_IndependentNodes(t *testing.T) {
	g := NewDefaultResourceGraph()
	g.AddNode(fakeResourceUID("A"))
	g.AddNode(fakeResourceUID("B"))
	g.AddNode(fakeResourceUID("C"))

	var visited []string
	err := TopologicalTraversal(g, func(uid ResourceUID) error {
		visited = append(visited, uid.ResID)
		return nil
	})

	assert.NoError(t, err)
	assert.Len(t, visited, 3)
}
