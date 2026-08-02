# otelcol-otelbox

`otelcol-otelbox` is one OpenTelemetry Collector Builder (OCB) binary with
three self-contained role profiles:

| Role | Ingests | Exports to |
| --- | --- | --- |
| `edge` | Local OTLP and the collector's own metrics | One authenticated gateway |
| `gateway` | Authenticated OTLP and its own metrics | Two independently queued backends |
| `host-agent` | Host metrics, Docker statistics, selected journal units and neighbouring Prometheus targets | One authenticated gateway |

Version 2.0 removes the old base-plus-role configuration model. A process loads
exactly one file:

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

The role profiles are demonstrations of a deployment contract, not production
mirrors. Values that name a neighbour, bind address, credential file or storage
location are supplied through `${env:...}` references.

## What lives here

| Path | Purpose |
| --- | --- |
| `builder.yaml` | Component manifest. `dist.version` is the artefact version; all `gomod` pins identify the upstream Collector version. |
| `config/{edge,gateway,host-agent}.yaml` | The three complete role profiles. |
| `shared-config-check.sh` | Fails when any marked shared region differs byte for byte. |
| `smoke-check.sh` | Compares manifest component counts with the built binary and asserts the `file_storage` and `redaction` invariants by name. |
| `test/harness/` | Black-box delivery, durability and queue-pressure tests against the built binary. |
| `test/config/` | CI-only overlays and backend doubles used by the harness. |
| `Dockerfile` | Packages the already-built Linux binary in `scratch`; it never compiles. |
| `Formula/otelcol-otelbox.rb` | Homebrew installation channel for unmanaged Apple silicon Macs; CI rewrites release literals. |
| `.github/workflows/otelcol-otelbox.yml` | Build, validation, test, image, release and formula update graph. |

## Safety and durability model

All roles redact credential-shaped attributes before export. Every network
exporter uses a bounded `file_storage` sending queue and retries transient
failures indefinitely. Edge and gateway queues block their OTLP callers when
full so those callers can retry. The host agent cannot back-pressure scrapers;
it retains an outage up to its WAL capacity and rejects new samples once full.

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
`:-default`. An exported but empty variable does not use that default.

Required string and path variables are documented in the role guides:

- [Edge](docs/edge.md)
- [Gateway](docs/gateway.md)
- [Host agent](docs/host-agent.md)
- [Adopting 2.0](docs/adoption.md)

## Build and verify locally

The manifest pins upstream Collector `v0.157.0`. CI pins Go `1.25.12` because
the initial Go 1.25 release mislinks this generated collector while the patched
toolchain builds it successfully.

```console
go install go.opentelemetry.io/collector/cmd/builder@v0.157.0
CGO_ENABLED=0 "$(go env GOPATH)/bin/builder" --config builder.yaml
./smoke-check.sh ./_build/otelcol-otelbox builder.yaml
./shared-config-check.sh
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
2. an unknown token is rejected and visible in edge exporter metrics;
3. a record acknowledged while the gateway is down survives an edge SIGKILL;
4. gateway backend fan-out leaves ingest unaffected only while every backend
   queue has headroom; a full blocking queue backpressures the receiver.

## Release flow

`dist.version` in `builder.yaml` is the only artefact-version source. A push to
the default branch builds and tests even when that version already exists.
Publishing additionally requires a new version, the default branch and a
non-pull-request event. The workflow creates `v<version>` itself; a manually
pushed tag triggers nothing.

A release contains two binaries and checksums, the three-profile configuration
archive, a single-image digest reference and the exact-version GHCR image. The
formula job then updates the version, URLs and checksums on the default branch.
Do not hand-edit those release literals.

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
