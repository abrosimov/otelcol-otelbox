# syntax=docker/dockerfile:1
# Pinned frontend, because the COPY --chmod flags below are a BuildKit feature:
# without the directive the image would depend on whatever frontend the builder
# happens to default to.

# Packaging only, never compilation. The binary is produced by the
# OpenTelemetry Collector Builder in CI and arrives through the build context,
# so the image ships byte-for-byte what the release asset ships; building here
# would duplicate the OCB toolchain and let the two drift apart.

# Alpine carries the Mozilla trust store in its base layer
# (ca-certificates-bundle), so the bundle can be lifted out without installing
# a package. Pinned to a patch tag rather than a floating one: a rebuild must
# not silently swap the trust store the gateway validates its backends against.
FROM alpine:3.24.1 AS certs

# scratch, because the gateway needs no shell, package manager or libc: the
# collector is statically linked (CGO_ENABLED=0) and every writable path comes
# from the supervisor as a bind mount or tmpfs. What is absent cannot be
# exploited, and the container filesystem stays read-only in practice as well
# as by flag.
FROM scratch

# Per-release facts, hence arguments: hardcoding them would make every release
# a Dockerfile edit, and a stale literal is worse than an honest default.
ARG VERSION=0.0.0-dev
ARG REVISION=unknown
ARG CREATED=1970-01-01T00:00:00Z

# The upstream collector release this binary was assembled from, carried
# separately because VERSION does not encode it. dist.version is this artefact's
# own semantic version by design, so nothing in the image's identity names the
# collector inside it; without this argument the only record would be the gomod
# pins in builder.yaml at whichever commit the image was built from.
ARG UPSTREAM_VERSION=unknown

# source is the label GHCR reads to attach the published package to the
# repository, which is also what gives the package its inherited visibility and
# provenance link.
LABEL org.opencontainers.image.source="https://github.com/abrosimov/otelcol-otelbox" \
      org.opencontainers.image.title="otelcol-otelbox" \
      org.opencontainers.image.description="OCB-built durable OpenTelemetry collector (gateway role)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

# A custom key, because the OCI predefined set has none that means "the version
# of the upstream project this artefact is assembled from" — image.version is
# the image's own version and is already spoken for above. Namespaced under
# reverse-DNS the author controls rather than under io.opentelemetry.*, which
# belongs to the upstream project and would assert a provenance this image does
# not have. It exists so that the answer to "does the next collector CVE affect
# what is running here" comes out of `docker inspect` on the running image
# alone, with neither the release page nor the manifest to hand.
LABEL io.github.abrosimov.otelbox.upstream.collector.version="${UPSTREAM_VERSION}"

# scratch has no trust store of its own. The gateway dials every backend over
# TLS with verification left on, so without this file each export would fail
# with an unknown-authority error rather than degrade quietly.
COPY --from=certs --chown=0:0 --chmod=0644 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Root-owned and world-executable: the supervisor pins an arbitrary numeric uid
# (`user: "10001:10001"`) that exists in no /etc/passwd, so execution has to be
# granted by the mode rather than by ownership. The explicit --chmod also means
# the image does not inherit whatever mode the CI artefact download left behind.
COPY --chown=0:0 --chmod=0755 otelcol-otelbox_linux_amd64 /otelcol-otelbox

# Numeric on purpose: there is no user database to resolve a name against. This
# only sets the default for callers that do not pin their own uid — the Compose
# service sets the same pair, so the two never disagree.
USER 10001:10001

# ENTRYPOINT, not CMD: the supervisor supplies `--config=...` as its `command`,
# and those must arrive as arguments to the collector instead of replacing it.
# No configuration is baked in — it comes as a read-only bind mount.
ENTRYPOINT ["/otelcol-otelbox"]
