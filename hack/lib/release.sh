# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
# Shared by hack/publish-image.sh and hack/publish-chart.sh. Source it, don't run it.

chart_dir=charts/elb-envoy-remote-infra-provider

# Runs a command, or prints it when DRY_RUN is set.
run() {
  if [[ -n "${DRY_RUN:-}" ]]; then
    echo "[dry run] $*"
  else
    "$@"
  fi
}

# Sets $version and $tag, and fails unless this is a clean checkout of the release tag whose
# chart matches version.txt. Run it from the repo root.
release_checks() {
  version="$(tr -d '[:space:]' < version.txt)"
  if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$ ]]; then
    echo "Version '${version}' in version.txt must be X.Y.Z, X.Y.Z-alpha.N, X.Y.Z-beta.N, or X.Y.Z-rc.N" >&2
    exit 1
  fi
  local app_version image_tag
  app_version="$(sed -n -E 's/^appVersion:[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/p' "${chart_dir}/Chart.yaml")"
  if [[ "${app_version}" != "${version}" ]]; then
    echo "Chart appVersion '${app_version}' doesn't match version.txt '${version}'. Run 'make release-cut VERSION=${version}'." >&2
    exit 1
  fi
  # The chart's image.tag is the image that gets published, so it must be v<version>.
  image_tag="$(sed -n -E 's/^  tag:[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/p' "${chart_dir}/values.yaml")"
  if [[ "${image_tag}" != "v${version}" ]]; then
    echo "Chart image.tag '${image_tag}' doesn't match v${version}. Run 'make release-cut VERSION=${version}'." >&2
    exit 1
  fi
  tag="v${version}"
  if [[ "$(git rev-parse "${tag}^{commit}" 2> /dev/null || true)" != "$(git rev-parse HEAD)" ]]; then
    echo "HEAD isn't the commit tagged ${tag}. Run 'git fetch --tags && git checkout ${tag}'." >&2
    exit 1
  fi
  if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
    echo "The working tree has uncommitted changes. Publish from a clean checkout of ${tag}." >&2
    exit 1
  fi
}

# Guards against publishing to the public registry by accident. Call it after release_checks, with
# the repository you are about to publish to. It does nothing for a private registry.
#   - A prerelease (1.0.0-rc.1) is refused unless ALLOW_PUBLIC_PRERELEASE=1.
#   - Anything else asks you to type the version. Without a terminal, set CONFIRM_PUBLIC=<version>.
check_publish_target() {
  local target="$1"
  if [[ "${target}" != *public.ecr.aws* ]]; then
    echo "Target: ${target} (private)"
    return 0
  fi
  echo "Target: ${target} (PUBLIC, anyone can pull it)"
  if [[ "${version}" == *-* && -z "${ALLOW_PUBLIC_PRERELEASE:-}" ]]; then
    echo "${version} is a prerelease, and the target is public." >&2
    echo "For a rehearsal, publish to a private repository: set IMAGE_REPO and CHART_REF." >&2
    echo "To publish the prerelease publicly, set ALLOW_PUBLIC_PRERELEASE=1." >&2
    exit 1
  fi
  if [[ -n "${DRY_RUN:-}" || "${CONFIRM_PUBLIC:-}" == "${version}" ]]; then
    return 0
  fi
  if [[ ! -t 0 ]]; then
    echo "This publishes ${version} to a public registry. Set CONFIRM_PUBLIC=${version} to confirm." >&2
    exit 1
  fi
  local answer
  read -r -p "Publish ${version} to ${target}? Type ${version} to continue: " answer
  if [[ "${answer}" != "${version}" ]]; then
    echo "Cancelled." >&2
    exit 1
  fi
}

# Logs in to the registry of the given repository (an image repo or an oci:// chart ref) with each
# given tool, for example `docker`, `oras` or `helm`. Public ECR and private ECR are told apart by
# the registry host, as in aws-application-networking-k8s. If ECR_PUBLISH_ROLE_ARN is set, it
# assumes that role first.
#   ecr_login <repository> <tool>...
ecr_login() {
  local target="${1#oci://}"
  shift
  local registry="${target%%/*}"
  if [[ -n "${DRY_RUN:-}" ]]; then
    echo "[dry run] log in to ${registry} with $*"
    return
  fi
  if [[ -n "${ECR_PUBLISH_ROLE_ARN:-}" ]]; then
    read -r AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN < <(
      aws sts assume-role --role-arn "${ECR_PUBLISH_ROLE_ARN}" --role-session-name publish-release \
        --duration-seconds 3600 --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text
    )
    export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
  fi
  local password tool
  if [[ "${registry}" == public.ecr.aws ]]; then
    # ECR Public authentication is only available in us-east-1.
    password="$(aws ecr-public get-login-password --region us-east-1)"
  elif [[ "${registry}" =~ \.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com ]]; then
    password="$(aws ecr get-login-password --region "${BASH_REMATCH[1]}")"
  else
    echo "Can't log in to ${registry}: only Amazon ECR registries are supported. Log in yourself and leave LOGIN unset." >&2
    exit 1
  fi
  for tool in "$@"; do
    if [[ "${tool}" == helm ]]; then
      echo "${password}" | helm registry login --username AWS --password-stdin "${registry}"
    else
      echo "${password}" | "${tool}" login --username AWS --password-stdin "${registry}"
    fi
  done
}
