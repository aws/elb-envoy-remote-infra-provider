#!/usr/bin/env bash
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# Tags the current commit as v<version>, where <version> is in version.txt, and pushes the tag.
# .github/workflows/tag-release.yml runs it when a push changes version.txt. Run from the repo root.

set -euo pipefail

version="$(tr -d '[:space:]' < version.txt)"
if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$ ]]; then
  echo "Version '${version}' in version.txt must be X.Y.Z, X.Y.Z-alpha.N, X.Y.Z-beta.N, or X.Y.Z-rc.N" >&2
  exit 1
fi

# The chart's appVersion must match the version being tagged.
chart=charts/elb-envoy-remote-infra-provider/Chart.yaml
app_version="$(sed -n -E 's/^appVersion:[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/p' "${chart}")"
if [[ "${app_version}" != "${version}" ]]; then
  echo "Chart appVersion '${app_version}' doesn't match version.txt '${version}'. Run 'make release-cut VERSION=${version}'." >&2
  exit 1
fi

# The chart's image.tag is the image that gets published, so it must be v<version>.
image_tag="$(sed -n -E 's/^  tag:[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/p' charts/elb-envoy-remote-infra-provider/values.yaml)"
if [[ "${image_tag}" != "v${version}" ]]; then
  echo "Chart image.tag '${image_tag}' doesn't match v${version}. Run 'make release-cut VERSION=${version}'." >&2
  exit 1
fi
tag="v${version}"
if git rev-parse -q --verify "refs/tags/${tag}" > /dev/null; then
  echo "Tag ${tag} already exists"
  exit 0
fi

git tag -a -m "Release ${version}" "${tag}"
git push origin "${tag}"
echo "Created and pushed ${tag}"
