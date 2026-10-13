# Forwarder: the only bridge from the internal sandbox network to the gateway.
ARG ALPINE_IMAGE=alpine:3.22

FROM ${ALPINE_IMAGE}

RUN apk add --no-cache socat

USER 65534:65534
ENTRYPOINT ["socat"]
