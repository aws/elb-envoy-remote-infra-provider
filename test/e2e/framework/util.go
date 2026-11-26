// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// classifyErr categorizes a probe error (DNS / timeout / refused) for logging.
func classifyErr(err error) string {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("DNS not resolving yet (%s)", dnsErr.Err)
	case errors.Is(err, context.DeadlineExceeded):
		return "request timeout"
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused"
	case strings.Contains(err.Error(), "i/o timeout"):
		return "timeout"
	default:
		return err.Error()
	}
}

// Protocol constants for logProgress.
const (
	ProtoHTTP  = "HTTP"
	ProtoHTTPS = "HTTPS"
	ProtoTLS   = "TLS"
	ProtoGRPC  = "gRPC"
	ProtoUDP   = "UDP"
	ProtoTCP   = "TCP"
)

// logProgress logs the first probe attempt, then ~every 30s, so a long probe
// shows liveness. protocol is a short label like ProtoHTTP, ProtoTLS, etc.
func logProgress(t *testing.T, protocol string, attempt int, start time.Time, detail string) {
	t.Helper()
	elapsed := time.Since(start)
	if attempt == 1 || int(elapsed.Seconds())%30 < 2 {
		Logf(t, "  %s probe attempt %d (%s elapsed): %s", protocol, attempt, elapsed.Round(time.Second), detail)
	}
}

// pollProbe runs the shared probe polling skeleton used by TLS, gRPC, and UDP
// probe helpers. It handles context/timeout setup, attempt counting, heartbeat
// logging, and fatal/success messaging. The caller provides:
//   - proto: protocol label (e.g. ProtoTLS)
//   - target: human-readable target (e.g. "host:port")
//   - probeFn: per-attempt closure; return nil on success, non-nil error to retry
func (s *Suite) pollProbe(t *testing.T, proto, target string, probeFn func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := contextWithTimeout(t, s.Timeouts.HTTPProbe)
	defer cancel()

	Logf(t, "probing %s at %s (up to %s)", proto, target, s.Timeouts.HTTPProbe)

	var lastErr error
	attempts := 0
	start := time.Now()

	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.HTTPProbe, true, func(ctx context.Context) (bool, error) {
		attempts++
		if probeErr := probeFn(ctx); probeErr != nil {
			lastErr = probeErr
			logProgress(t, proto, attempts, start, classifyErr(probeErr))
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("%s probe to %s failed after %d attempts over %s; last error=%v",
			proto, target, attempts, time.Since(start).Round(time.Second), lastErr)
	}
	Logf(t, "%s probe to %s succeeded after %d attempts (%s)", proto, target, attempts, time.Since(start).Round(time.Second))
}
