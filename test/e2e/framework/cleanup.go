// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// cleanupTracker records teardown actions. Gateways are deleted first (so the
// ELBProxyConfiguration finalizer can release), then everything else in reverse
// insertion order — the Namespace, added first, is deleted last and cascades
// its contents.
type cleanupTracker struct {
	mu      sync.Mutex
	entries []cleanupEntry
}

type cleanupEntry struct {
	describe    string
	deleteFirst bool // Gateways: deleted before anything else.
	del         func(ctx context.Context, t *testing.T) error
}

func (c *cleanupTracker) add(deleteFirst bool, describe string, del func(ctx context.Context, t *testing.T) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, cleanupEntry{describe: describe, deleteFirst: deleteFirst, del: del})
}

// cleanupAll gives each delete its own fresh context and perItem budget so one
// slow delete can't starve the rest. It uses context.Background, not the test
// context, since t.Cleanup may run after the test context is cancelled.
func (c *cleanupTracker) cleanupAll(perItem time.Duration, t *testing.T) {
	t.Helper()
	c.mu.Lock()
	entries := append([]cleanupEntry(nil), c.entries...)
	c.mu.Unlock()

	// Reverse insertion order, then hoist Gateways to the front.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].deleteFirst && !entries[j].deleteFirst
	})

	for _, e := range entries {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), perItem)
			defer cancel()
			if err := e.del(ctx, t); err != nil {
				Logf(t, "cleanup: %s: %v", e.describe, err)
			}
		}()
	}
}

// DeleteObjectAndWaitGone deletes obj and polls until it's gone, bounded by
// s.Timeouts.Delete. A missing object is treated as success.
func (s *Suite) DeleteObjectAndWaitGone(t *testing.T, obj client.Object) error {
	t.Helper()
	gvk := s.gvkFor(t, obj)
	ref := refFromObject(gvk, obj)

	ctx, cancel := context.WithTimeout(context.Background(), s.Timeouts.Delete)
	defer cancel()
	return deleteAndWait(ctx, t, s.Client, ref)
}

// deleteAndWait deletes obj and polls until it's gone.
func deleteAndWait(ctx context.Context, t *testing.T, cl client.Client, obj *unstructured.Unstructured) error {
	t.Helper()
	Logf(t, "deleting %s %s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName())

	if err := cl.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, deleteWaitTimeout(ctx), true, func(ctx context.Context) (bool, error) {
		probe := &unstructured.Unstructured{}
		probe.SetGroupVersionKind(obj.GroupVersionKind())
		err := cl.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: obj.GetName()}, probe)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// deleteWaitTimeout bounds a single delete by the context's remaining time,
// defaulting to 5m if there's no deadline.
func deleteWaitTimeout(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 5 * time.Minute
	}
	d := time.Until(deadline)
	if d <= 0 {
		return time.Second
	}
	return d
}
