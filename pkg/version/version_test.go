// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractEnvoyVersion(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "standard distroless image — strips patch",
			image: "docker.io/envoyproxy/envoy:distroless-v1.37.2",
			want:  "1.37",
		},
		{
			name:  "dev tag with version — strips patch",
			image: "docker.io/envoyproxy/envoy:v1.38.0",
			want:  "1.38",
		},
		{
			name:  "no tag",
			image: "docker.io/envoyproxy/envoy",
			want:  "",
		},
		{
			name:  "tag without v prefix — strips patch",
			image: "docker.io/envoyproxy/envoy:1.37.2",
			want:  "1.37",
		},
		{
			name:  "empty string",
			image: "",
			want:  "",
		},
		{
			name:  "multiple v in tag — uses last v, strips patch",
			image: "registry.example.com/envoy:dev-v2-v1.40.0",
			want:  "1.40",
		},
		{
			name:  "already major.minor only",
			image: "docker.io/envoyproxy/envoy:v1.37",
			want:  "1.37",
		},
		{
			name:  "digest-pinned image — strips patch and digest",
			image: "docker.io/envoyproxy/envoy:distroless-v1.40.2@sha256:09ca6f2140a61ad8df11779a4a30d52b48433d444466328494f9188c9c35eded",
			want:  "1.40",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractEnvoyVersion(tt.image)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStripPatch(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1.37.2", "1.37"},
		{"1.38.0", "1.38"},
		{"1.37", "1.37"},
		{"1", "1"},
		{"", ""},
		{"1.40.12", "1.40"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, stripPatch(tt.input))
		})
	}
}

func TestGet(t *testing.T) {
	info := Get()
	// Should return a non-empty version extracted from DefaultEnvoyProxyImage.
	assert.NotEmpty(t, info.EnvoyProxyVersion, "EnvoyProxyVersion should not be empty")
	// Format: major.minor only (no patch).
	assert.Regexp(t, `^\d+\.\d+$`, info.EnvoyProxyVersion)
}

// TestGet_DefaultEnvoyMinorIsPinned fails when a dependency bump changes the
// bundled Envoy minor version. Existing proxies can't change version, so a new
// minor would leave them on an older Envoy than the config the controller
// generates. Update this only when the API supports proxy version updates.
func TestGet_DefaultEnvoyMinorIsPinned(t *testing.T) {
	assert.Equal(t, "1.40", Get().EnvoyProxyVersion)
}
