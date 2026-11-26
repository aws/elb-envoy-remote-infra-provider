// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// MakeTLSConnect polls until a TLS handshake to hostPort succeeds with the
// given serverName (SNI). Used to verify TLS passthrough routing where the
// proxy does NOT terminate TLS — the handshake completes with the backend's
// cert. InsecureSkipVerify is set because the backend uses a self-signed cert.
func (s *Suite) MakeTLSConnect(t *testing.T, hostPort, serverName string) {
	t.Helper()
	s.pollProbe(t, ProtoTLS, hostPort+" (SNI="+serverName+")", func(ctx context.Context) error {
		d := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: 10 * time.Second},
			Config: &tls.Config{
				ServerName:         serverName,
				InsecureSkipVerify: true, //nolint:gosec // e2e test with self-signed cert
			},
		}
		conn, err := d.DialContext(ctx, "tcp", hostPort)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	})
}
