#!/usr/bin/env bash
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
# Builds and pushes the multi-arch controller image for the release in version.txt. Nothing else
# checks the image first, so run the e2e tests before you publish. Publish the image first, then
# the chart (hack/publish-chart.sh).
# Run this script from the repo root, on a clean checkout of the tag.
#
#   hack/publish-image.sh
#
# Options (environment variables):
#   LOGIN=1                 log in to Amazon ECR (public or private, by registry) first, with the current AWS credentials
#   ECR_PUBLISH_ROLE_ARN    assume this role before logging in
#   DOCKER=finch            build with Finch instead of Docker. The script can't check Finch for an existing image tag
#   DRY_RUN=1               print what would run, and push nothing
#   FORCE=1                 overwrite a version that is already published (release versions are immutable, so avoid this)
#   IMAGE_REPO              image repository (default public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider)
#   CONFIRM_PUBLIC=<version>      confirm a public publish without a terminal (otherwise it asks you to type the version)
#   ALLOW_PUBLIC_PRERELEASE=1     allow a prerelease (1.0.0-rc.1) to go to a public repository, which is refused by default
#
# The commit must be the one tagged v<version>, with no uncommitted changes. Run 'make release-cut'
# and merge it first, so the chart and the tag match the image you publish.
set -euo pipefail
# shellcheck source=hack/lib/release.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"

IMAGE_REPO="${IMAGE_REPO:-public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider}"
DOCKER="${DOCKER:-docker}"

release_checks
check_publish_target "${IMAGE_REPO}"
if [[ -n "${LOGIN:-}" ]]; then
  ecr_login "${IMAGE_REPO}" "${DOCKER}"
fi

echo "Publishing ${IMAGE_REPO}:${tag}"
if [[ "${DOCKER}" == finch ]]; then
  echo "Finch can't check whether ${IMAGE_REPO}:${tag} exists. Make sure the version is new." >&2
  run make push-finch DOCKER=finch IMAGE="${IMAGE_REPO}" TAG="${tag}" GIT_VERSION="${tag}"
else
  if [[ -z "${FORCE:-}" && -z "${DRY_RUN:-}" ]] && docker buildx imagetools inspect "${IMAGE_REPO}:${tag}" > /dev/null 2>&1; then
    echo "${IMAGE_REPO}:${tag} already exists. Release versions are immutable. Set FORCE=1 to overwrite it." >&2
    exit 1
  fi
  run make push-multiarch IMAGE="${IMAGE_REPO}" TAG="${tag}" GIT_VERSION="${tag}"
fi
echo "Done."
