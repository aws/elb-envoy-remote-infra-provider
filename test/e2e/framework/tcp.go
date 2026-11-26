// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"net"
	"testing"
	"time"
)

// MakeTCPConnect polls until a TCP handshake to hostPort succeeds. Useful for
// listeners with no HTTP responder (e.g. TCPRoute).
func (s *Suite) MakeTCPConnect(t *testing.T, hostPort string) {
	t.Helper()
	s.pollProbe(t, ProtoTCP, hostPort, func(ctx context.Context) error {
		d := &net.Dialer{Timeout: 10 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", hostPort)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	})
}
