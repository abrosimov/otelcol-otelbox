# otelcol-otelbox

`otelcol-otelbox` is one OpenTelemetry Collector Builder (OCB) binary with
three self-contained role profiles:

| Role | Ingests | Exports to |
| --- | --- | --- |
| `edge` | Local OTLP and the collector's own metrics | One authenticated gateway |
| `gateway` | Authenticated OTLP and its own metrics | N explicitly rendered required recipients, each with its own WAL |
| `host-agent` | Host metrics, selected journal units, neighbouring Prometheus targets and the collector's own metrics | One authenticated gateway |

Version 2.1 retains the self-contained configuration model introduced in 2.0
and replaces copied upstream exporters and repository-owned Bash checks with
thin exporter wrappers and a tested Go contract tool. A process loads exactly
one file:

```console
otelcol-otelbox --config config/edge.yaml
otelcol-otelbox --config config/gateway.yaml
otelcol-otelbox --config config/host-agent.yaml
```

## Repository boundary

This repository owns the binary, its three reference profiles and the CI that
builds and proves them. It does not own service units, launchd agents, Compose
files, Ansible, secret stores or machine-local rendered configuration. Those
belong to the consuming repositories.

The binary links only components used by a role or the black-box backend
double: 4 receivers, 4 processors, 3 exporters, 4 extensions and no connectors.
Adding a dormant capability is a deliberate size and support-contract decision.

The role profiles are demonstrations of a deployment contract, not production
mirrors. Values that name a neighbour, bind address, credential file or storage
location are supplied through `${env:...}` references.

## What lives here

| Path | Purpose |
| --- | --- |
| `builder.yaml` | Component manifest. `dist.version` is the artefact version; all `gomod` pins identify the upstream Collector version. |
| `config/{edge,gateway,host-agent}.yaml` | The three complete role profiles. |
| `internal/exporters/` | Thin OTLP exporter wrappers that retain auth failures and retry after a long interval. |
| `tools/ci/` | Tested Go contract checks for shared profile regions and built-binary contents. |
| `test/harness/` | Black-box delivery, durability and queue-pressure tests against the built binary. |
| `test/config/` | CI-only overlays and backend doubles used by the harness. |
| `Dockerfile` | Packages the already-built Linux binary in `scratch`; it never compiles. |
| `Formula/otelcol-otelbox.rb` | Homebrew installation channel for unmanaged Apple silicon Macs; CI rewrites release literals. |
| `.github/workflows/otelcol-otelbox.yml` | Build, validation, test, image, release and formula update graph. |

## Safety and durability model

All roles apply the same bounded credential redaction before export. This is
defence in depth rather than a general PII guarantee; the exact inspected
carriers, blind spots and producer duties are in the
[credential redaction boundary](docs/redaction.md). Every network exporter uses
a bounded `file_storage` sending queue and retries transient failures
indefinitely. Edge and gateway queues block their OTLP callers when full so
those callers can retry. The host agent cannot back-pressure scrapers; it
retains an outage up to its WAL capacity and rejects new samples once full.

Outbound authentication failures are graceful degradation, not a drop path.
gRPC `Unauthenticated`/`PermissionDenied` and HTTP 401/403 retain the accepted
request and retry every `${env:OTELBOX_AUTH_RETRY_INTERVAL:-1h}`. Replacing the
watched credential file lets the live process drain its queue. Other permanent
protocol errors still fail permanently so a malformed payload cannot block all
later data. The transport is at least once: a lost success response can cause a
duplicate, and exactly-once delivery would require recipient-side idempotency.

Gateway recipient eligibility is part of required delivery. Ordinary signals
fan out to every rendered gRPC recipient. The reference shows three
signal-specific exporter/WAL pairs—logs, metrics and traces—forming one logical
all-signal recipient. This permits a separate outage budget for each signal.
Traces classified with
`otelbox.telemetry.class=llm` additionally enter a generic OTLP/HTTP recipient.
The consuming deployment owns its concrete endpoint, credentials and protocol
headers.

The binary deliberately does not link the `batch` processor. It acknowledges
upstream before its in-memory batch reaches the exporter and logs, rather than
returns, a later send failure. Sender batching is therefore performed inside
each persistent exporter queue: an accepted OTLP record reaches the WAL before
success is returned.

Transport security is secure by default on outbound OTLP exporters. A
deployment whose neighbour deliberately speaks plaintext must add
`tls.insecure: true` to its rendered configuration. The edge-to-gateway
integration test always verifies a per-run CA and leaf certificate.

## Environment expansion

An unset `${env:NAME}` is not a named configuration error. The Collector warns,
substitutes an empty value and the decoder may turn it into the target type's
zero value. Every numeric reference in these profiles therefore carries a
`:-default`. Storage references use an invalid `/dev/null/...-is-required`
default so omission fails validation instead of silently selecting a root-level
directory. An exported but empty variable bypasses either default and is
forbidden in a rendered deployment.

Required string and path variables are documented in the role guides:

- [Edge](docs/edge.md)
- [Gateway](docs/gateway.md)
- [Host agent](docs/host-agent.md)
- [Credential redaction boundary](docs/redaction.md)
- [Adopting 2.0](docs/adoption.md)
- [Roadmap](docs/roadmap.md)

## Build and verify locally

The manifest pins upstream Collector `v0.158.0`. CI pins Go exactly, currently
`1.27.0`, because the initial Go 1.25 release mislinked this generated collector
while the patched 1.25 toolchain built it successfully.

```console
go install go.opentelemetry.io/collector/cmd/builder@v0.158.0
CGO_ENABLED=0 "$(go env GOPATH)/bin/builder" --config builder.yaml
go -C tools/ci run ./cmd/otelbox-ci binary check \
  --binary ../../_build/otelcol-otelbox --manifest ../../builder.yaml
go -C tools/ci run ./cmd/otelbox-ci shared check \
  ../../config/edge.yaml ../../config/gateway.yaml ../../config/host-agent.yaml
```

Validate a profile with all of its required string values supplied. Validation
parses and decodes configuration but does not start receivers or exporters:

```console
OTELBOX_BIND_HOST=127.0.0.1 \
OTELBOX_HEALTH_ENDPOINT=127.0.0.1:13133 \
OTELBOX_STORAGE_DIR=/tmp/otelbox-edge \
OTELBOX_UPSTREAM_ENDPOINT=127.0.0.1:14319 \
OTELBOX_UPSTREAM_AUTH_HEADER_FILE=/tmp/otelbox-edge-auth-header \
  ./_build/otelcol-otelbox validate --config config/edge.yaml
```

Run the black-box harness outside command sandboxes that deny the boot-time
sysctl used by `resource_detection`:

```console
go test -C test/harness . -count=1 -timeout 15m -v \
  -args -otelcol-binary "$PWD/_build/otelcol-otelbox"
```

It proves:

1. authenticated edge-to-gateway delivery and redaction;
2. an unknown outbound token enters throttled backoff, retains the marker and
   delivers it after live credential-file rotation without a restart;
3. a record acknowledged while the gateway is down survives an edge SIGKILL;
4. selected traces alone reach OTLP/HTTP with required auth/protocol headers and
   survive a gateway SIGKILL in that recipient's WAL;
5. ordinary logs, metrics and traces acknowledged by the gateway survive its
   SIGKILL in their signal-specific WALs;
6. gateway fan-out leaves ingest unaffected only while every required recipient
   queue has headroom; a full log queue backpressures log ingest without blocking
   the separate metrics and traces queues;
7. non-empty allowlist replacement revokes the previous bearer token;
8. against a gateway that names a client CA, an edge holding a trusted client
   leaf delivers, an edge offering no client certificate never does, and
   replacing a rejected certificate and key in place drains the retained marker
   without a restart;
9. on Linux, the complete host-agent profile starts with `journald`, host
   metrics and self telemetry, and publishes health and process metrics.

The black-box harness starts the `edge` and `gateway` roles on every supported
test host and also starts the host-agent role on Linux. The generic Linux smoke
does not replace acceptance on the intended target host and service account;
that boundary is described in the host-agent guide.

## Release flow

`dist.version` in `builder.yaml` is the only artefact-version source. A push to
the default branch builds and tests even when that version already exists.
Publishing additionally requires a new version, the default branch and a
non-pull-request event. The workflow creates `v<version>` itself; a manually
pushed tag triggers nothing.

A release contains two binaries and checksums, the three-profile configuration
archive, a single-image digest reference and the exact-version GHCR image. The
formula job then updates the version, URLs and checksums on the default branch.
The workflow refuses orphan Git tags and existing exact-version image tags
rather than reusing or overwriting them. Do not hand-edit the formula's release
literals.

The version describes the consumer contract: configuration-shape breaks are
major, newly available capabilities are minor, and a rebuild over the same
component/configuration contract is patch. See [Adopting 2.0](docs/adoption.md)
for the incompatible configuration changes and atomic rollout guidance.

## Installation boundaries

The container image is packaging for Linux roles, not a deployment. In
particular, the `host-agent` cannot use `journald` from the `scratch` image
because that receiver executes `journalctl`; run that role as a host process
with `journalctl` available.

Homebrew installs the Apple silicon binary and all three profiles, but supplies
no `brew services` definition. On workstations managed by `devbox-setup`, that
repository owns both installation and launchd supervision; installing a second
Homebrew copy would create two independently drifting binaries.
