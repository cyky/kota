# Base image for kota sandboxes: agent + Node + Go toolchains.
# Versions are pinned so builds are reproducible; Renovate opens a PR to
# bump each one (docs/development.md). Base images are pinned by digest
# (the multi-arch index); the tag says what it is. The `# renovate:`
# comments tell Renovate where each version comes from.
ARG NODE_IMAGE=node:24-trixie-slim@sha256:43b9ba1142b8dcf33fa8f922ca1be85714adc444e0586c7cf7726daaeabc1bea
ARG GO_IMAGE=golang:1.27-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4

FROM ${GO_IMAGE} AS go

# crane downloads Engine images during prep. go install checks the module
# against the Go checksum database.
FROM ${GO_IMAGE} AS crane
# renovate: datasource=go depName=github.com/google/go-containerregistry
ARG CRANE_VERSION=v0.22.1
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/google/go-containerregistry/cmd/crane@${CRANE_VERSION}

FROM ${NODE_IMAGE}

# renovate: datasource=npm depName=@opencode/cli
ARG OPENCODE_VERSION=2.0.29
# pi needs Node >= 22.19.
# renovate: datasource=npm depName=@earendil-works/pi-coding-agent
ARG PI_VERSION=1.2.0

# docker-cli is the client only: in a session it talks to the project's
# Engine VM, never to a local daemon.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl docker-cli fd-find git jq ripgrep \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd \
 && rm -rf /var/lib/apt/lists/*

COPY --from=go /usr/local/go /usr/local/go
COPY --from=crane /out/crane /usr/local/bin/crane
ENV PATH=/usr/local/go/bin:$PATH

RUN corepack enable \
 && npm install -g --allow-scripts=@opencode/cli @opencode/cli@${OPENCODE_VERSION} \
 && npm install -g --ignore-scripts @earendil-works/pi-coding-agent@${PI_VERSION} \
 && npm cache clean --force

# The per-project state volume and the shared caches (Go modules, corepack,
# pnpm store) are mounted here. Docker copies this mode into a new volume,
# so the session's UID (not known at build time) can write it.
RUN mkdir -p /var/lib/kota/state /var/lib/kota/gomod \
      /var/lib/kota/corepack /var/lib/kota/pnpm-store \
 && chmod 1777 /var/lib/kota/state /var/lib/kota/gomod \
      /var/lib/kota/corepack /var/lib/kota/pnpm-store

# Sessions run as the host UID with HOME on a tmpfs; nothing here needs root.
WORKDIR /workspace
CMD ["opencode"]
