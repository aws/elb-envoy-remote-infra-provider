// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package probe implements the `probe` subcommand used by the sidecar's exec probes.
package probe

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/pflag"
)

// Command is the subcommand name: `<binary> probe --socket=<path>`.
const Command = "probe"

// Exit codes returned by Run.
const (
	ExitHealthy   = 0
	ExitUnhealthy = 1
	ExitUsage     = 2
)

// CheckSocket returns nil if path is a Unix socket, following symlinks like `test -S`.
func CheckSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists but is not a socket (mode %s)", path, info.Mode())
	}
	return nil
}

// Run checks the socket given in args and returns the exit code.
func Run(args []string, stderr io.Writer) int {
	fs := pflag.NewFlagSet(Command, pflag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "Path of the Unix domain socket to check (required)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return ExitHealthy
		}
		fmt.Fprintf(stderr, "probe: %v\n", err)
		return ExitUsage
	}
	if *socket == "" {
		fmt.Fprintln(stderr, "probe: --socket is required")
		return ExitUsage
	}
	if err := CheckSocket(*socket); err != nil {
		fmt.Fprintf(stderr, "probe: %v\n", err)
		return ExitUnhealthy
	}
	return ExitHealthy
}
