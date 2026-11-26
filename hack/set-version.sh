#!/usr/bin/env bash
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# Sets the release version everywhere it is recorded:
#   - version.txt. A push that changes it makes .github/workflows/tag-release.yml create the tag.
#   - the Helm chart's `version` and `appVersion`, and `image.tag` in its values.yaml, which is
#     v<version>. The webhook and the injected sidecar both use it, so the controller image
#     must be published with this exact tag.
#   - links in the docs to files in this repository, which move from a branch to the tag.
#
# Usage: hack/set-version.sh 1.0.0   (no leading "v")

set -euo pipefail

version="${1:-}"
if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$ ]]; then
  echo "usage: $0 <version>, for example 1.0.0 or 1.0.0-rc.1 (no leading v)" >&2
  exit 1
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
chart="${root}/charts/elb-envoy-remote-infra-provider/Chart.yaml"

echo "${version}" > "${root}/version.txt"

# sed -i.bak works on both GNU and BSD (macOS) sed.
sed -i.bak -E \
  -e "s/^version:.*/version: ${version}/" \
  -e "s/^appVersion:.*/appVersion: \"${version}\"/" \
  "${chart}"
rm -f "${chart}.bak"
values="${root}/charts/elb-envoy-remote-infra-provider/values.yaml"
sed -i.bak -E "s/^  tag: .*/  tag: \"v${version}\"/" "${values}"
rm -f "${values}.bak"

# Pin links to this repository's files, such as .../blob/main/k8s/iam-policy.json, to the tag.
repo='github\.com/[^/]+/elb-envoy-remote-infra-provider'
{ grep -rlE "${repo}/blob/[^/]+/" "${root}/docs" "${root}/README.md" --include='*.md' 2>/dev/null || true; } |
  while read -r file; do
    sed -i.bak -E "s#(${repo}/blob/)[^/]+/#\1v${version}/#g" "${file}"
    rm -f "${file}.bak"
  done

echo "Set version ${version} in version.txt, the chart, and the docs links."
