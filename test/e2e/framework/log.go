// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Logf writes a timestamped, test-named log line straight to stderr. Unlike
// t.Logf (buffered until the test ends), this surfaces progress live, which
// matters for long waits on cloud resources.
func Logf(t *testing.T, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s [%s] %s\n", time.Now().Format("15:04:05.000"), t.Name(), msg)
}
