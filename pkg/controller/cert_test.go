// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartCertWatcher_triggersReconcileOnChange(t *testing.T) {
	tmpDir := t.TempDir()
	certFile := tmpDir + "/tls.crt"
	require.NoError(t, os.WriteFile(certFile, []byte("original"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount atomic.Int32
	reconciled := make(chan struct{}, 1)

	onChanged := func() {
		callCount.Add(1)
		reconciled <- struct{}{}
	}

	go func() {
		err := startCertWatcher(ctx, []string{certFile}, onChanged, logging.DefaultLogger(os.Stdout, "info"))
		assert.NoError(t, err)
	}()

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, os.WriteFile(certFile, []byte("rotated"), 0o600))

	select {
	case <-reconciled:
		assert.Equal(t, int32(1), callCount.Load())
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reconcile to be triggered")
	}
}

func TestStartCertWatcher_multipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	certFile := tmpDir + "/tls.crt"
	keyFile := tmpDir + "/tls.key"
	require.NoError(t, os.WriteFile(certFile, []byte("cert"), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte("key"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount atomic.Int32
	reconciled := make(chan struct{}, 2)

	onChanged := func() {
		callCount.Add(1)
		reconciled <- struct{}{}
	}

	go func() {
		err := startCertWatcher(ctx, []string{certFile, keyFile}, onChanged, logging.DefaultLogger(os.Stdout, "info"))
		assert.NoError(t, err)
	}()

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, os.WriteFile(keyFile, []byte("rotated-key"), 0o600))

	select {
	case <-reconciled:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reconcile after key file change")
	}
}

// TestStartCertWatcher_debounce verifies that a burst of cert file changes
// (simulating a Kubernetes secret rotation) is coalesced into a single callback.
func TestStartCertWatcher_debounce(t *testing.T) {
	tmpDir := t.TempDir()
	certFile := tmpDir + "/tls.crt"
	keyFile := tmpDir + "/tls.key"
	caFile := tmpDir + "/ca.crt"
	require.NoError(t, os.WriteFile(certFile, []byte("cert"), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte("key"), 0o600))
	require.NoError(t, os.WriteFile(caFile, []byte("ca"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount atomic.Int32
	reconciled := make(chan struct{}, 10)

	onChanged := func() {
		callCount.Add(1)
		reconciled <- struct{}{}
	}

	go func() {
		err := startCertWatcher(ctx, []string{certFile, keyFile, caFile}, onChanged, logging.DefaultLogger(os.Stdout, "info"))
		assert.NoError(t, err)
	}()

	time.Sleep(100 * time.Millisecond)

	require.NoError(t, os.WriteFile(certFile, []byte("cert-rotated"), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte("key-rotated"), 0o600))
	require.NoError(t, os.WriteFile(caFile, []byte("ca-rotated"), 0o600))

	select {
	case <-reconciled:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reconcile after cert rotation")
	}

	time.Sleep(certDebounce * 3)

	if got := callCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 callback after rotation burst, got %d", got)
	}
}

func TestStartCertWatcher_stopsOnContextCancel(t *testing.T) {
	tmpDir := t.TempDir()
	certFile := tmpDir + "/tls.crt"
	require.NoError(t, os.WriteFile(certFile, []byte("cert"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- startCertWatcher(ctx, []string{certFile}, func() {}, logging.DefaultLogger(os.Stdout, "info"))
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher to stop after context cancel")
	}
}
