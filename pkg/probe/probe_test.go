// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package probe

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortTempDir avoids t.TempDir(), whose macOS paths exceed the Unix socket path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "probe")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func listenUnix(t *testing.T, path string) {
	t.Helper()
	lis, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })
}

func TestRun(t *testing.T) {
	dir := shortTempDir(t)

	sock := filepath.Join(dir, "server.sock")
	listenUnix(t, sock)

	symlink := filepath.Join(dir, "link.sock")
	require.NoError(t, os.Symlink(sock, symlink))

	regular := filepath.Join(dir, "regular")
	require.NoError(t, os.WriteFile(regular, []byte("x"), 0o600))

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{name: "socket", args: []string{"--socket=" + sock}, wantCode: ExitHealthy},
		{name: "symlink to socket", args: []string{"--socket", symlink}, wantCode: ExitHealthy},
		{name: "missing path", args: []string{"--socket=" + filepath.Join(dir, "nope")}, wantCode: ExitUnhealthy, wantStderr: "no such file"},
		{name: "regular file", args: []string{"--socket=" + regular}, wantCode: ExitUnhealthy, wantStderr: "not a socket"},
		{name: "directory", args: []string{"--socket=" + dir}, wantCode: ExitUnhealthy, wantStderr: "not a socket"},
		{name: "no flag", args: nil, wantCode: ExitUsage, wantStderr: "--socket is required"},
		{name: "unknown flag", args: []string{"--bogus"}, wantCode: ExitUsage, wantStderr: "unknown flag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(tt.args, &stderr)
			assert.Equal(t, tt.wantCode, code, "stderr: %s", stderr.String())
			if tt.wantStderr != "" {
				assert.Contains(t, stderr.String(), tt.wantStderr)
			} else {
				assert.Empty(t, stderr.String())
			}
		})
	}
}

func TestCheckSocket_ClosedListenerLeavesNoSocket(t *testing.T) {
	// Closing a Go UnixListener removes the socket file.
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "server.sock")
	lis, err := net.Listen("unix", sock)
	require.NoError(t, err)
	require.NoError(t, CheckSocket(sock))
	require.NoError(t, lis.Close())
	assert.Error(t, CheckSocket(sock))
}
