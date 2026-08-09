# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89
# The COPY --chmod flags below are a BuildKit feature.

# Packaging only, never compilation: the binary comes from CI's OCB build, so
# the image ships byte-for-byte what the release asset ships.

# Alpine's base layer carries the Mozilla trust store, so the bundle is lifted
# out without installing a package. Pinned so a rebuild cannot swap it silently.
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS rootfs

RUN mkdir -p /rootfs/etc/ssl/certs \
    && chmod 0755 \
        /rootfs \
        /rootfs/etc \
        /rootfs/etc/ssl \
        /rootfs/etc/ssl/certs \
    && cp /etc/ssl/certs/ca-certificates.crt \
        /rootfs/etc/ssl/certs/ca-certificates.crt \
    && chmod 0644 \
        /rootfs/etc/ssl/certs/ca-certificates.crt

# scratch: the collector is statically linked (CGO_ENABLED=0) and every writable
# path comes from the supervisor, so a shell or libc could only add exposure.
FROM scratch

# Per-release facts as arguments: hardcoded, they would make every release a
# Dockerfile edit.
ARG VERSION=0.0.0-dev
ARG REVISION=unknown
ARG CREATED=1970-01-01T00:00:00Z

# The upstream collector version, carried separately because VERSION is this
# artefact's own and says nothing about the collector inside.
ARG UPSTREAM_VERSION=unknown

# `source` is what attaches the published package to the repository on GHCR, and
# with it the package's inherited visibility and provenance link.
LABEL org.opencontainers.image.source="https://github.com/abrosimov/otelcol-otelbox" \
      org.opencontainers.image.title="otelcol-otelbox" \
      org.opencontainers.image.description="OCB-built durable OpenTelemetry collector (gateway role)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

# A custom key, because no OCI predefined label means "the upstream project this
# artefact is assembled from". It makes "does this collector CVE affect what is
# running" answerable from `docker inspect` alone.
LABEL io.github.abrosimov.otelbox.upstream.collector.version="${UPSTREAM_VERSION}"

# COPY creates missing parent directories with file-like permissions, so their
# searchable modes are fixed in the source tree before it enters scratch.
COPY --from=rootfs --chown=0:0 /rootfs/ /

# World-executable because the supervisor pins a numeric uid that exists in no
# /etc/passwd, so ownership cannot grant execution.
COPY --chown=0:0 --chmod=0755 otelcol-otelbox_linux_amd64 /otelcol-otelbox

# Numeric: there is no user database to resolve a name against.
USER 10001:10001

# ENTRYPOINT, not CMD: the supervisor's `command` must reach the collector as
# arguments rather than replace it.
ENTRYPOINT ["/otelcol-otelbox"]
