# Base image for localllm sandboxes: agent + Node + Go toolchains.
# Versions are pinned so builds are reproducible; bump them deliberately.
ARG NODE_IMAGE=node:24-trixie-slim
ARG GO_IMAGE=golang:1.27-trixie

FROM ${GO_IMAGE} AS go

FROM ${NODE_IMAGE}

ARG OPENCODE_VERSION=2.0.25

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl fd-find git jq ripgrep \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd \
 && rm -rf /var/lib/apt/lists/*

COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH

RUN corepack enable \
 && npm install -g --allow-scripts=@opencode/cli @opencode/cli@${OPENCODE_VERSION} \
 && npm cache clean --force

# Sessions run as the host UID with HOME on a tmpfs; nothing here needs root.
WORKDIR /workspace
CMD ["opencode"]
