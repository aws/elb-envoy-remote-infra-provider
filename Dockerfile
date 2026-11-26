# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# Runtime-only image: packages binaries built by `make build-multiarch` (context: bin/).

# Override with --build-arg BASE_IMAGE=<image>. Must precede the first FROM.
ARG BASE_IMAGE=base

# Same base as upstream Envoy Gateway. Literal FROM so Dependabot can bump the digest.
FROM gcr.io/distroless/base-nossl:nonroot@sha256:4006ac92504be607b70de01d1f64f38288c69fcd8faa820f6d3aed266ea32ac5 AS base

FROM ${BASE_IMAGE}
# Set by buildx, e.g. linux/arm64.
ARG TARGETPLATFORM
# Binary name under bin/; passed by the Makefile.
ARG BINARY_NAME=elb-envoy-remote-infra-provider
# Fixed destination: the sidecar's exec probes call it (sidecar.BinaryPath).
COPY ${TARGETPLATFORM}/${BINARY_NAME} /usr/local/bin/elb-envoy-remote-infra-provider
USER 65532:65532
# Metrics and webhook ports.
EXPOSE 8082 9443
ENTRYPOINT ["/usr/local/bin/elb-envoy-remote-infra-provider"]
