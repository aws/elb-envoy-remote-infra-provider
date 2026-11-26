#!/usr/bin/env bash
# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
# Publishes the Helm chart for the release in version.txt. Publish the image first
# (hack/publish-image.sh), because the chart pulls it.
# Run this script from the repo root, on a clean checkout of the tag. It needs helm and oras.
#
#   hack/publish-chart.sh
#
# `helm push` always names the repository after the chart (elb-envoy-remote-infra-provider), but the
# public repository is called ...-chart. So the script copies the chart in four steps:
#   1. helm push to a staging registry, which names the repository after the chart.
#   2. oras cp from staging to CHART_REF. It copies by digest, so nothing in the chart changes.
#   3. helm pull the chart back from CHART_REF.
#   4. cmp it with the packaged file. The script fails if they differ.
# If the version is already published, it skips the copy and only compares the contents.
#
# Options (environment variables):
#   LOGIN=1                 log in to Amazon ECR (public or private, by registry) first, with the current AWS credentials
#   ECR_PUBLISH_ROLE_ARN    assume this role before logging in
#   DOCKER=finch            run the temporary staging registry with Finch instead of Docker
#   DRY_RUN=1               print what would run, and push nothing
#   CHART_REF               target (default oci://public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider-chart)
#   CONFIRM_PUBLIC=<version>      confirm a public publish without a terminal (otherwise it asks you to type the version)
#   ALLOW_PUBLIC_PRERELEASE=1     allow a prerelease (1.0.0-rc.1) to go to a public repository, which is refused by default
#   STAGING_REGISTRY        host:port of the registry to stage in. By default the script runs a
#                           temporary registry on localhost:5000 and stops it at the end.
#   PLAIN_HTTP=1            CHART_REF uses plain HTTP. Only for testing against a local registry.
set -euo pipefail
# shellcheck source=hack/lib/release.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/release.sh"

CHART_REF="${CHART_REF:-oci://public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider-chart}"
STAGING_REGISTRY="${STAGING_REGISTRY:-}"
DOCKER="${DOCKER:-docker}"
helm_target=()
oras_target=()
if [[ -n "${PLAIN_HTTP:-}" ]]; then
  helm_target=(--plain-http)
  oras_target=(--to-plain-http)
fi

for tool in helm oras; do
  command -v "${tool}" > /dev/null || { echo "${tool} is required but isn't installed." >&2; exit 1; }
done
release_checks
check_publish_target "${CHART_REF}"
chart_name="$(sed -n -E 's/^name:[[:space:]]*(.*)$/\1/p' "${chart_dir}/Chart.yaml")"
target="${CHART_REF#oci://}"
out="_output/release/${tag}"
package="${out}/${chart_name}-${version}.tgz"

if [[ -n "${LOGIN:-}" ]]; then
  ecr_login "${CHART_REF}" oras helm
fi

echo "Publishing ${CHART_REF}:${version}"
mkdir -p "${out}"
helm package "${chart_dir}" --destination "${out}" > /dev/null

if helm show chart "${CHART_REF}" --version "${version}" ${helm_target[@]+"${helm_target[@]}"} > /dev/null 2>&1; then
  echo "${CHART_REF}:${version} is already published. Skipping the copy and comparing the contents."
else
  if [[ -z "${STAGING_REGISTRY}" ]]; then
    STAGING_REGISTRY=localhost:5000
    # Finch doesn't accept -d with --rm, so the container is removed in the exit trap instead.
    "${DOCKER}" rm -f chart-staging > /dev/null 2>&1 || true
    run "${DOCKER}" run -d --name chart-staging -p 5000:5000 registry:2 > /dev/null
    trap '"${DOCKER}" rm -f chart-staging > /dev/null 2>&1 || true' EXIT
    if [[ -z "${DRY_RUN:-}" ]]; then
      for _ in $(seq 1 30); do
        curl -fs "http://${STAGING_REGISTRY}/v2/" > /dev/null 2>&1 && break
        sleep 1
      done
    fi
  fi
  run helm push "${package}" "oci://${STAGING_REGISTRY}/staging" --plain-http
  run oras cp --from-plain-http ${oras_target[@]+"${oras_target[@]}"} \
    "${STAGING_REGISTRY}/staging/${chart_name}:${version}" "${target}:${version}"
fi

if [[ -n "${DRY_RUN:-}" ]]; then
  echo "[dry run] helm pull ${CHART_REF} --version ${version}, then cmp with ${package}"
else
  verify="$(mktemp -d)"
  helm pull "${CHART_REF}" --version "${version}" --destination "${verify}" ${helm_target[@]+"${helm_target[@]}"} > /dev/null
  # helm pull names the file after the repository, so compare whichever .tgz it downloaded.
  if cmp "${package}" "${verify}"/*.tgz; then
    echo "Verified: the published chart is identical to ${package}."
  else
    echo "The chart published at ${CHART_REF}:${version} differs from ${package}." >&2
    exit 1
  fi
fi
echo "Done."
