// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// MakeGRPCHealthCheck polls until a gRPC-style HTTP/2 request to hostPort
// returns a successful status (200). This exercises the gRPC routing path
// without importing the full gRPC client stack — it sends an HTTP/2 POST to
// the grpc.health.v1.Health/Check endpoint. The grpc-echo backend responds to
// any gRPC method with OK.
func (s *Suite) MakeGRPCHealthCheck(t *testing.T, hostPort string) {
	t.Helper()
	s.pollProbe(t, ProtoGRPC, hostPort, func(ctx context.Context) error {
		// Use a fresh HTTP/2 client per attempt (h2c — cleartext HTTP/2).
		transport := &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				// h2c: dial plain TCP, not TLS.
				d := &net.Dialer{Timeout: 10 * time.Second}
				return d.DialContext(ctx, network, addr)
			},
		}
		client := &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
		}
		defer client.CloseIdleConnections()

		url := fmt.Sprintf("http://%s/yages.Echo/Ping", hostPort)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/grpc")
		req.Header.Set("TE", "trailers")

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d (want 200)", resp.StatusCode)
		}
		return nil
	})
}
