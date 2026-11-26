// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each --sidecar-extra-args is one sidecar arg; commas inside a value must not split it.
func TestBindFlags_SidecarExtraArgsKeepsCommas(t *testing.T) {
	cfg := &ControllerConfig{}
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	cfg.BindFlags(fs)
	require.NoError(t, fs.Parse([]string{
		"--sidecar-extra-args=--default-tags=team=a,env=b",
		"--sidecar-extra-args=--log-level=debug",
	}))
	assert.Equal(t, []string{"--default-tags=team=a,env=b", "--log-level=debug"}, cfg.WebhookConfig.SidecarExtraArgs)
	// Unset flags keep their defaults; the chart's readiness probe targets this port.
	assert.Equal(t, ":61779", cfg.WebhookConfig.HealthProbeBindAddress)
}

func TestControllerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ControllerConfig
		wantErr string
	}{
		{
			name: "valid — no tags",
			cfg:  ControllerConfig{},
		},
		{
			name: "valid — non-reserved default tags",
			cfg:  ControllerConfig{DefaultTags: map[string]string{"team": "platform"}},
		},
		{
			name: "valid — non-reserved external managed tags",
			cfg:  ControllerConfig{ExternalManagedTags: []string{"cost-center"}},
		},
		{
			name: "valid — default and external managed tags with no overlap",
			cfg: ControllerConfig{
				DefaultTags:         map[string]string{"team": "platform"},
				ExternalManagedTags: []string{"cost-center"},
			},
		},
		// default-tags collision with tracking keys
		{
			name:    "default-tags collides with cluster tracking key",
			cfg:     ControllerConfig{DefaultTags: map[string]string{"gateway.elbproxy.aws/cluster": "my-cluster"}},
			wantErr: `"gateway.elbproxy.aws/cluster" cannot be specified in --default-tags`,
		},
		{
			name:    "default-tags collides with managed-by tracking key",
			cfg:     ControllerConfig{DefaultTags: map[string]string{"gateway.elbproxy.aws/managed-by": "external"}},
			wantErr: `"gateway.elbproxy.aws/managed-by" cannot be specified in --default-tags`,
		},
		// external-managed-tags collision with tracking keys
		{
			name:    "external-managed-tags collides with stack tracking key",
			cfg:     ControllerConfig{ExternalManagedTags: []string{"gateway.elbproxy.aws/stack"}},
			wantErr: `"gateway.elbproxy.aws/stack" cannot be specified in --external-managed-tags`,
		},
		{
			name:    "external-managed-tags collides with resource tracking key",
			cfg:     ControllerConfig{ExternalManagedTags: []string{"gateway.elbproxy.aws/resource"}},
			wantErr: `"gateway.elbproxy.aws/resource" cannot be specified in --external-managed-tags`,
		},
		// external-managed-tags collision with default-tags
		{
			name: "external-managed-tags overlaps with default-tags",
			cfg: ControllerConfig{
				DefaultTags:         map[string]string{"team": "platform"},
				ExternalManagedTags: []string{"team"},
			},
			wantErr: `"team" cannot be specified in both --default-tags and --external-managed-tags`,
		},
		// webhook config
		{
			name: "valid — webhook disabled, no sidecar image",
			cfg:  ControllerConfig{WebhookConfig: WebhookConfig{Enable: false}},
		},
		{
			name: "valid — webhook enabled with sidecar image",
			cfg:  ControllerConfig{WebhookConfig: WebhookConfig{Enable: true, SidecarImage: "example.com/elb-envoy-remote-infra-provider:v1"}},
		},
		{
			name:    "webhook enabled without sidecar image",
			cfg:     ControllerConfig{WebhookConfig: WebhookConfig{Enable: true}},
			wantErr: "--sidecar-image is required when --enable-sidecar-webhook is set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
