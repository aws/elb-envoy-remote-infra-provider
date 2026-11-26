// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
)

// GitVersion is the controller version injected at build time via
// -ldflags "-X ...pkg/version.GitVersion=<version>". When built with make,
// this is set to the git tag or short commit SHA. Defaults to "dev" only
// when built outside a git repo (e.g. go run without ldflags).
var GitVersion = "dev"

// GitCommit is the commit SHA, set via -ldflags at build time.
var GitCommit = "unknown"

// Info holds version information derived from the upstream Envoy Gateway dependency.
type Info struct {
	// EnvoyProxyVersion is the Envoy proxy version that EG was built against,
	// extracted from the default container image tag
	EnvoyProxyVersion string
}

// Get returns version info derived from the upstream Envoy Gateway constants.
func Get() Info {
	return Info{
		EnvoyProxyVersion: extractEnvoyVersion(egv1a1.DefaultEnvoyProxyImage),
	}
}

// extractEnvoyVersion parses the Envoy version from an image reference like
// "docker.io/envoyproxy/envoy:distroless-v1.37.2" → "1.37".
// Only major.minor is returned — the ELBProxy API does not accept patch versions.
func extractEnvoyVersion(image string) string {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) < 2 {
		return ""
	}
	tag := parts[1]

	idx := strings.LastIndex(tag, "v")
	if idx < 0 || idx+1 >= len(tag) {
		return stripPatch(tag)
	}
	return stripPatch(tag[idx+1:])
}

// stripPatch removes the patch component from a semver string.
func stripPatch(version string) string {
	first := strings.IndexByte(version, '.')
	if first < 0 {
		return version
	}
	second := strings.IndexByte(version[first+1:], '.')
	if second < 0 {
		return version
	}
	return version[:first+1+second]
}
