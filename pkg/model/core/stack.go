// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"reflect"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core/graph"
)

// Stack presents a resource graph, where resources can depend on each other.
type Stack interface {
	// stackID returns a unique ID for stack.
	StackID() StackID

	// Add a resource into stack.
	AddResource(res Resource) error

	// Add a dependency relationship between resources.
	AddDependency(dependee Resource, depender Resource) error

	// ListResources list all resources for specific type.
	// pResourceSlice must be a pointer to a slice of resources, which will be filled.
	ListResources(pResourceSlice interface{}) error

	// TopologicalTraversal visits resources in stack in topological order.
	TopologicalTraversal(visitor ResourceVisitor) error

	// IRPresent reports whether the upstream Envoy Gateway InfraIR was available
	// when this stack was built. When false (e.g. after a sidecar restart that lost
	// its IR cache and EG has not yet replayed), IR-derived facets — the Envoy
	// bootstrap, frontend SG ingress, the proxy's SG set, and the Kubernetes
	// Service — must be PRESERVED (left as-is), never reverted to an empty desired
	// state. Only IR-free facets (tags, attributes, subnets, the config archive)
	// reconcile in that mode. See pkg/aws/doc.md ("Root cause: IR presence is
	// overloaded as the reconcile gate").
	IRPresent() bool
}

// NewDefaultStack constructs new stack with IR present (the normal path).
func NewDefaultStack(stackID StackID) *defaultStack {
	return &defaultStack{
		stackID:       stackID,
		resources:     make(map[graph.ResourceUID]Resource),
		resourceGraph: graph.NewDefaultResourceGraph(),
		irPresent:     true,
	}
}

// NewDefaultStackWithIRPresence constructs a stack, explicitly marking whether the
// upstream InfraIR was present. Pass false for the IR-absent reconcile path.
func NewDefaultStackWithIRPresence(stackID StackID, irPresent bool) *defaultStack {
	s := NewDefaultStack(stackID)
	s.irPresent = irPresent
	return s
}

var _ Stack = &defaultStack{}

// default implementation for stack.
type defaultStack struct {
	stackID       StackID
	resources     map[graph.ResourceUID]Resource
	resourceGraph graph.ResourceGraph
	irPresent     bool
}

func (s *defaultStack) StackID() StackID {
	return s.stackID
}

func (s *defaultStack) IRPresent() bool {
	return s.irPresent
}

func (s *defaultStack) AddResource(res Resource) error {
	resUID := s.computeResourceUID(res)
	if _, ok := s.resources[resUID]; ok {
		return fmt.Errorf("resource already exists, type: %v, id: %v", res.Type(), res.ID())
	}
	s.resources[resUID] = res
	s.resourceGraph.AddNode(resUID)
	return nil
}

func (s *defaultStack) AddDependency(dependee Resource, depender Resource) error {
	dependeeResUID := s.computeResourceUID(dependee)
	dependerResUID := s.computeResourceUID(depender)
	if _, ok := s.resources[dependeeResUID]; !ok {
		return fmt.Errorf("dependee resource not found, type: %v, id: %v", dependee.Type(), dependee.ID())
	}
	if _, ok := s.resources[dependerResUID]; !ok {
		return fmt.Errorf("depender resource not found, type: %v, id: %v", depender.Type(), depender.ID())
	}
	s.resourceGraph.AddEdge(dependeeResUID, dependerResUID)
	return nil
}

func (s *defaultStack) ListResources(pResourceSlice interface{}) error {
	v := reflect.ValueOf(pResourceSlice)
	if v.Kind() != reflect.Ptr {
		return fmt.Errorf("pResourceSlice must be pointer to resource slice")
	}
	v = v.Elem()
	if v.Kind() != reflect.Slice {
		return fmt.Errorf("pResourceSlice must be pointer to resource slice")
	}
	resType := v.Type().Elem()
	var resForType []Resource
	for resUID, res := range s.resources {
		if resUID.ResType == resType {
			resForType = append(resForType, res)
		}
	}
	v.Set(reflect.MakeSlice(v.Type(), len(resForType), len(resForType)))
	for i := range resForType {
		v.Index(i).Set(reflect.ValueOf(resForType[i]))
	}
	return nil
}

func (s *defaultStack) TopologicalTraversal(visitor ResourceVisitor) error {
	return graph.TopologicalTraversal(s.resourceGraph, func(uid graph.ResourceUID) error {
		return visitor.Visit(s.resources[uid])
	})
}

func (s *defaultStack) computeResourceUID(res Resource) graph.ResourceUID {
	return graph.ResourceUID{
		ResType: reflect.TypeOf(res),
		ResID:   res.ID(),
	}
}
