# Base image for localllm sandboxes: agent + Node + Go toolchains.
# Versions are pinned so builds are reproducible; bump them deliberately.
ARG NODE_IMAGE=node:24-trixie-slim
ARG GO_IMAGE=golang:1.27-trixie

FROM ${GO_IMAGE} AS go

FROM ${NODE_IMAGE}

ARG OPENCODE_VERSION=2.0.25
# pi needs Node >= 22.19.
ARG PI_VERSION=1.2.0

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl fd-find git jq ripgrep \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd \
 && rm -rf /var/lib/apt/lists/*

COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH

RUN corepack enable \
 && npm install -g --allow-scripts=@opencode/cli @opencode/cli@${OPENCODE_VERSION} \
 && npm install -g --ignore-scripts @earendil-works/pi-coding-agent@${PI_VERSION} \
 && npm cache clean --force

# The per-project state volume and the shared caches (Go modules, corepack,
# pnpm store) are mounted here. Docker copies this mode into a new volume,
# so the session's UID (not known at build time) can write it.
RUN mkdir -p /var/lib/localllm/state /var/lib/localllm/gomod \
      /var/lib/localllm/corepack /var/lib/localllm/pnpm-store \
 && chmod 1777 /var/lib/localllm/state /var/lib/localllm/gomod \
      /var/lib/localllm/corepack /var/lib/localllm/pnpm-store

# Sessions run as the host UID with HOME on a tmpfs; nothing here needs root.
WORKDIR /workspace
CMD ["opencode"]
