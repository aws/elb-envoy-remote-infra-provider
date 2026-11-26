// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"time"

	"github.com/spf13/pflag"
)

const (
	flagSyncPeriod    = "sync-period"
	defaultSyncPeriod = 10 * time.Hour
)

// RuntimeConfig stores the configuration for the controller-runtime
type RuntimeConfig struct {
	SyncPeriod time.Duration
}

// BindFlags binds the command line flags to the fields in the config object
func (c *RuntimeConfig) BindFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&c.SyncPeriod, flagSyncPeriod, defaultSyncPeriod,
		"Period at which the controller forces the repopulation of its local object stores.")
}
