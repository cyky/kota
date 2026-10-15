# Forwarder: the only bridge from the internal sandbox network to the gateway.
# Pinned by digest (the multi-arch index); the tag says what it is.
ARG ALPINE_IMAGE=alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8

FROM ${ALPINE_IMAGE}

RUN apk add --no-cache socat

USER 65534:65534
ENTRYPOINT ["socat"]
