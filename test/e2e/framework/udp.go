// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// MakeUDPEcho polls until a UDP echo round-trip to hostPort succeeds: sends a
// payload and expects the same payload back. Retries absorb DNS propagation and
// proxy warmup.
func (s *Suite) MakeUDPEcho(t *testing.T, hostPort string) {
	t.Helper()
	payload := []byte("elbproxy-e2e-udp-probe")

	s.pollProbe(t, ProtoUDP, hostPort, func(_ context.Context) error {
		conn, err := net.DialTimeout("udp", hostPort, 10*time.Second)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		defer conn.Close()

		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		if _, err = conn.Write(payload); err != nil {
			return fmt.Errorf("write: %w", err)
		}

		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			return err
		}

		got := strings.TrimSpace(string(buf[:n]))
		if got != string(payload) {
			return fmt.Errorf("echo mismatch: got %q, want %q", got, string(payload))
		}
		return nil
	})
}
