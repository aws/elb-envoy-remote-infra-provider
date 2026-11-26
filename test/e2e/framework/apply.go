// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateObject creates obj and tracks it for cleanup. Every object a test
// creates has a name of its own (random, or fixed inside the test's own
// namespace), so AlreadyExists is an error: the existing object belongs to
// someone else and is neither reused nor tracked for deletion.
func (s *Suite) CreateObject(t *testing.T, obj client.Object, opts ...client.CreateOption) error {
	t.Helper()
	// GVK comes from the scheme — Create strips the object's TypeMeta.
	gvk := s.gvkFor(t, obj)
	Logf(t, "creating %s %s/%s", gvk.Kind, obj.GetNamespace(), obj.GetName())

	ctx, cancel := contextWithTimeout(t, s.Timeouts.Apply)
	defer cancel()

	err := s.Client.Create(ctx, obj, opts...)
	if !apierrors.IsAlreadyExists(err) {
		// Track on other errors too: a create that timed out may still have
		// been persisted.
		s.trackObject(refFromObject(gvk, obj))
	}
	if err != nil {
		return fmt.Errorf("create %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
	}
	return nil
}

// trackObject registers a client-deleted resource for cleanup. Gateways are
// flagged deleteFirst so the ELBProxyConfiguration finalizer can release.
func (s *Suite) trackObject(ref *unstructured.Unstructured) {
	cl := s.Client
	s.cleanup.add(
		ref.GetKind() == "Gateway",
		fmt.Sprintf("%s %s/%s", ref.GetKind(), ref.GetNamespace(), ref.GetName()),
		func(ctx context.Context, t *testing.T) error {
			return deleteAndWait(ctx, t, cl, ref)
		},
	)
}

// gvkFor resolves an object's GVK via the scheme. Create/Update strip TypeMeta,
// so the object itself isn't a reliable source.
func (s *Suite) gvkFor(t *testing.T, obj client.Object) schema.GroupVersionKind {
	t.Helper()
	gvks, _, err := s.scheme.ObjectKinds(obj)
	if err != nil || len(gvks) == 0 {
		t.Fatalf("cannot resolve GVK for %T: %v", obj, err)
	}
	return gvks[0]
}

// refFromObject builds a minimal reference (GVK + namespace + name) for Delete.
func refFromObject(gvk schema.GroupVersionKind, obj client.Object) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(obj.GetNamespace())
	u.SetName(obj.GetName())
	return u
}
