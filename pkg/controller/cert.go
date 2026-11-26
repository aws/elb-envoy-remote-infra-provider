// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"time"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/filewatcher"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/infrastore"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/logging"
)

const (
	certWatcherName = "elbproxy-cert-watcher"

	// certDebounce collapses the burst of filesystem events produced by a
	// Kubernetes secret rotation (one per cert file) into a single enqueue.
	certDebounce = 100 * time.Millisecond
)

// StartCertWatcher watches TLS cert files used to build the Envoy config archive.
// When any cert file changes on disk it enqueues all cached gateway keys into the
// controller's work queue so the updated certs are pushed to all proxies.
//
// It blocks until ctx is cancelled, so it should be run in a goroutine.
func StartCertWatcher(ctx context.Context, certPaths []string, keyCh GatewayEventCh, cache *infrastore.Cache, log logging.Logger) error {
	return startCertWatcher(ctx, certPaths, func() {
		enqueueAllGatewayKeys(keyCh, cache)
	}, log)
}

// enqueueAllGatewayKeys pushes all known gateway keys into the channel.
// Uses AllKnownKeys so cert rotation reaches every managed proxy, including
// those without IR after a sidecar restart.
func enqueueAllGatewayKeys(ch GatewayEventCh, c *infrastore.Cache) {
	for _, key := range c.AllKnownKeys() {
		EnqueueGatewayKey(ch, key)
	}
}

// startCertWatcher is the testable core — accepts a callback instead of concrete dependencies.
func startCertWatcher(ctx context.Context, certPaths []string, onChanged func(), log logging.Logger) error {
	log = log.WithName(certWatcherName)

	w := filewatcher.NewWatcher()
	defer func() { _ = w.Close() }()

	for _, path := range certPaths {
		if err := w.Add(path); err != nil {
			return err
		}
	}

	log.Info("watching cert files for changes", "paths", certPaths)

	// One goroutine per cert file — each forwards events into a shared channel.
	// Buffer size 1: acts as a dirty flag. Non-blocking send means additional
	// events while a reconcile is already pending are intentionally dropped.
	changed := make(chan struct{}, 1)
	for _, path := range certPaths {
		go func(p string) {
			for {
				select {
				case e, ok := <-w.Events(p):
					if !ok {
						return
					}
					log.Info("cert file changed", "name", e.Name, "op", e.Op.String())
					select {
					case changed <- struct{}{}:
					default:
					}
				case err, ok := <-w.Errors(p):
					if !ok {
						return
					}
					log.Error(err, "cert watcher error", "path", p)
				case <-ctx.Done():
					return
				}
			}
		}(path)
	}

	var debounceTimer *time.Timer
	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return nil
		case <-changed:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(certDebounce, func() {
				log.Info("cert changed, enqueuing all gateways for reconcile")
				onChanged()
			})
		}
	}
}
