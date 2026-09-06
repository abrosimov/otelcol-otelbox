# AGENTS.md

This file is the repository-specific operating contract for coding agents.
Inspect current files and tests before changing them; do not treat this document
as a substitute for evidence.

## Authority and workspace discipline

The user controls scope and external effects. Reviews and diagnoses are
read-only unless a change is requested. Preserve dirty-worktree changes, keep
edits scoped and never commit, push, publish, deploy or connect to a managed
host without explicit authority. Persisted documentation and comments use
British English.

## Repository boundary

This repository builds one OCB collector binary for three roles—`edge`,
`gateway` and `host-agent`—and owns the reference profile for each role. It owns
no Ansible, service units, launchd agents, Compose deployments, secret stores or
machine-local rendering. Those live in `devbox-setup` and
`remote_server_setup`.

The repository is `abrosimov/otelcol-otelbox`; repository, binary, image,
formula and release assets all use `otelcol-otelbox`. Bare `otelbox` names the
wider telemetry estate, except for the historical Homebrew tap label:
`brew install abrosimov/otelbox/otelcol-otelbox`.

`internal/exporters/` contains thin wrappers around the pinned standard OTLP
gRPC and HTTP exporters. The wrappers own the persistent queue and retry sender,
disable those layers in the inner exporter and reclassify only authentication
failures. `test/harness/` remains a separate, stdlib-only Go module that drives
the built binary from outside.

## Map

| Path | Purpose |
| --- | --- |
| `builder.yaml` | OCB manifest. `dist.version` is the only artefact-version source; `gomod` pins identify upstream; `replaces` states transitive security floors. |
| `internal/exporters/` | OTLP exporter auth-retry adaptations and unit tests. |
| `config/{edge,gateway,host-agent}.yaml` | Three complete, independently loaded role profiles. |
| `tools/ci/` | Tested Go checks for shared regions, built-binary contents and OCI runtime readiness. |
| `test/harness/` | Delivery, routing, authentication, crash durability and recipient-coupling tests. |
| `test/config/` | CI-only overlays and backend doubles. Merge semantics remain load-bearing here. |
| `Dockerfile` | Packages the CI-built Linux binary into `scratch`; never compiles. |
| `Formula/otelcol-otelbox.rb` | Homebrew installation channel; release literals are CI-owned. |
| `.github/workflows/otelcol-otelbox.yml` | Build, prove and publish graph. |

## Role contract

```text
edge        loopback OTLP -> origin -> redaction -> WAL -> one gateway
gateway     authenticated OTLP -> redaction -> eligibility -> WAL per recipient/signal -> N recipients
host-agent  host/journal/self telemetry -> redaction -> WAL -> gateway
```

A consumer loads exactly one profile:

```console
otelcol-otelbox --config config/<role>.yaml
```

There is no base layer and no `config/examples/` compatibility surface. Blocks
between `# >>> SHARED` and `# <<< SHARED` are intended to be one text in all
three profiles, comments included. Edit every copy and run the Go shared-region
check from the verification section.

Profiles demonstrate a contract; they are not mirrors of a current deployment.
Values naming a bind address, neighbour, credential file, storage root or disk
budget are deployment data and use `${env:...}`. Literal component names,
pipeline order and role-owned ports are repository conventions.
Bind-address references carry loopback defaults. A deployment must opt into a
wildcard or routable listener explicitly.

## Environment semantics

An unset `${env:NAME}` logs a warning and expands to an empty value; decoding
may then produce the target type's zero value. It is not a named load error.
Consequences include unlimited `file_storage.max_size`, no sender splitting for
`batch.max_size`, unlimited `max_concurrent_streams`, and `false` for booleans.
`queue_size: 0` is rejected by validation.

Every numeric and boolean reference must therefore carry a `:-default` unless
zero is deliberately safe and documented. Defaults apply only when a variable
is absent, not when it is exported empty. Whole-value references retain the
YAML-parsed type; embedded references such as `${env:OTELBOX_BIND_HOST}:4317`
are strings.

`OTELBOX_STORAGE_DIR` uses `/dev/null/OTELBOX_STORAGE_DIR-is-required` as an
invalid absent-value sentinel, so validation fails instead of accepting a WAL
under `/`. A consuming deployment must also reject exported-empty required
strings before starting the collector because an empty value bypasses defaults.

If a required string reference is added, supply a non-secret dummy in the
workflow's validation invocation. Expansion traverses the merged map even when
the referenced component is not wired by the final pipeline.

## Persistence before acknowledgement

Every network exporter has a bounded `file_storage` sending queue. Edge and
gateway set `block_on_overflow: true` because their OTLP producers can retry.
The host agent sets it to `false`: scrapers cannot be back-pressured, so blocking
would lose subsequent collection cycles while retaining the current item. Its
WAL protects a bounded outage and the deployment must alert before capacity.

Do not put the `batch` processor in a role pipeline. In the pinned upstream, it
sends into an internal channel and returns success; a later downstream failure
is logged rather than returned. Placing it before a persistent exporter therefore
allows a receiver to acknowledge data held only in memory. Sender batching
belongs inside `sending_queue.batch`, after persistent enqueue. The batch
processor is deliberately not linked, so returning it to a rendered role fails
validation rather than silently weakening durability.

`TestEdgePersistsAcceptedDataBeforeAcknowledgement` is the regression guard: it
accepts a record with the gateway down, kills the edge with SIGKILL, reopens the
same WAL and requires delivery after recovery. Do not weaken the kill into a
graceful shutdown.

`TestGatewayPersistsSelectedTraceBeforeAcknowledgement` applies the same
boundary to the selected-traces HTTP recipient. Eligible records are required
by the all-signal recipient set and that additional recipient.

`TestGatewayPersistsOrdinarySignalsBeforeAcknowledgement` applies the boundary
to the signal-specific log, metric and trace gRPC WALs in one forced restart.

## Authentication and transport

The gateway authenticates every OTLP request through one
`bearertokenauth/ingest` allowlist. It holds one bare token per line; text after
the first whitespace is ignored upstream, but the repository contract forbids
whitespace and comments so audits cannot disagree with the runtime. An empty
reload retains the previous tokens; revoke the last client by replacing it with
a non-client token, never by truncating the file. Edge and host agent use
`headers_setter/gateway`, whose file contains the complete `Bearer <token>`
value and cannot carry a trailing comment.

The selected-traces HTTP exporter uses another `headers_setter` instance. Its
authorisation file and arbitrary protocol header are generic deployment inputs;
concrete backend names, paths, credentials and versions do not belong here.

Outbound OTLP exporters retain secure TLS defaults. A deployment whose
neighbour deliberately speaks plaintext adds `tls.insecure: true` to its
rendered configuration. The gateway profile leaves inbound TLS termination to
the deployment, but a bearer token must never cross an unencrypted network.
The integration overlay mints a CA and leaf per run and verifies the
edge-to-gateway leg; backend doubles explicitly opt into plaintext.

Edge and host agent may also present a client certificate on that leg through
`OTELBOX_UPSTREAM_TLS_CERT_FILE` and `OTELBOX_UPSTREAM_TLS_KEY_FILE`. This is
the one place where an absent value is deliberately safe rather than a fault:
empty means no certificate is offered, so a deployment tunnelling the leg keeps
working unchanged. Exactly one half of the pair is a named load error; a
configured path that does not resolve is not, and fails the exporter at start
instead. `OTELBOX_UPSTREAM_TLS_RELOAD_INTERVAL` defaults to `1h` because
`configtls` defaults to `0`, which would pin a process to the material it
started with — the pair is polled and re-read at the next handshake, not watched
by fsnotify as the header file is. The gateway profile is unchanged: mTLS
terminates on whatever front end a deployment puts before it.

Edge and host agent compress the gateway leg with `zstd` through
`OTELBOX_UPSTREAM_COMPRESSION`. That choice is scoped to legs whose far end is
this same binary: a gRPC client may only select a codec registered in the server
it dials, and registration follows the peer's build rather than its
configuration. The gateway's recipient exporters therefore keep `gzip`, whose
peer is a third-party backend, and a change there needs that backend's own
evidence. An unrecognised codec is refused by name at load. Compression level is
not configurable because the persistent-queue wrapper in `internal/exporters/`
does not carry `compression_params`. The codec is orthogonal to the sizing
invariants: `sizer: bytes` counts uncompressed queue items and
`max_recv_msg_size_mib` bounds the decompressed message.

Changing either authenticator, file format, header, receiver `auth:` block or
TLS overlay requires the full harness. A process and `/status` can remain green
while every export is rejected.

Every outbound exporter enables `retry_on_auth_failure`. gRPC
`Unauthenticated`/`PermissionDenied` and HTTP 401/403 responses retain the
request and retry after `${env:OTELBOX_AUTH_RETRY_INTERVAL:-1h}`. This interval
is separate from the 5–30 second transient backoff. The watched credential file
is read again before a later attempt, so replacing it can drain the queue
without restarting the Collector. Other permanent errors remain permanent so
an invalid payload cannot pin a queue forever.

Delivery is at least once. If a recipient accepts a request but its success
response is lost, retry can duplicate it; exactly-once delivery requires a
recipient-side idempotency contract that this transport does not own.

## Redaction boundary

`redaction/secrets` is a bounded credential deny-list, not a general PII or
arbitrary-payload guarantee. It covers resource, scope and record/datapoint
attributes; span and event attributes; scalar log bodies by value; and
structured log bodies recursively. It does not inspect span/event names, span
links, metric identity fields, exemplars or nested map/slice attribute values.
`redact_all_types` stays false because a nested match would coerce the complete
structured attribute to a string.

The canonical carrier and threat boundary is `docs/redaction.md`. Producers
must source-sanitise unstructured text and must not place secrets in uncovered
carriers. `summary: info` supplies record-local masked counts, never key names;
it is evidence that a rule fired, not proof that no secret escaped. The harness
attributes edge and gateway redaction independently across logs, traces and
metrics. On Linux it also proves generic host-agent startup, but target-host
collection remains deployment acceptance evidence.

Changing key/value patterns, summary behaviour or processor placement requires
the shared check and full harness. Every new value pattern needs a positive leak
case and benign preservation cases; do not widen it with ordinary fragments
such as `api`.

## Role-specific facts

### Edge

- OTLP listens on `${OTELBOX_BIND_HOST}:4317` and `:4318`; self metrics use
  port 8888. Ingest is capped at 1 MiB and sender batches at 1.5 MiB.
- One persistent gateway queue blocks on overflow and retries indefinitely.
- `resource_detection` stamps the local origin before redaction.

### Gateway

- One authenticated OTLP receiver listens on ports 14319 and 14320; self
  metrics use 8889. Ingest is capped at 2 MiB.
- Each logical all-signal recipient has one exporter, storage extension and WAL
  per signal, permitting independent queue and disk budgets.
- The reference represents one all-signal gRPC recipient with the
  `otlp_grpc/{logs,metrics,traces}` triplet and adds one traces-only HTTP
  recipient selected by `otelbox.telemetry.class=llm`. Deployments render a
  uniquely named triplet for every required all-signal recipient.
- Fan-out is synchronous. A stopped recipient is isolated while its queue has
  headroom; once that blocking queue fills, backpressure reaches ingest. The
  healthy exporter may already have accepted the current record before the
  request blocks, so do not assert on exporter ordering.
  `TestRequiredRecipientCouplingUnderQueuePressure` proves both states and that
  a full log queue does not block the separate metrics and traces queues.
- Concrete recipient authentication and protocol values stay in the consuming
  deployment.

### Host agent

- `host_metrics`, `prometheus/host` and selected `journald` units feed
  metrics/log pipelines; self metrics use port 8890.
- The journald cursor and outbound queue use distinct storage extensions.
- `journald` executes `journalctl`, so this role cannot run from the published
  `scratch` image.
- Container statistics are omitted until a constrained rootless Podman API or
  a cgroup/systemd-unit alternative is designed and tested.
- The `process` scraper is deliberately disabled. It combines privilege gaps,
  unbounded per-PID series and credential-bearing command lines. Do not enable
  it by merely deleting PID attributes: that creates a single-writer violation.

## Component names

Use canonical v0.158 component types. In current profiles these include
`otlp_grpc` and `otlp_http` for exporters, `resource_detection`, `host_metrics`,
`filter`, `file_storage`, `healthcheckv2`, `headers_setter`,
`bearertokenauth`, `prometheus` and `journald`. The OTLP receiver remains
`otlp`. `health_check` is a separate, unlinked component, not an alias.

Before introducing any multi-word type, inspect that component's pinned
`metadata.yaml` for `type` and `deprecated_type`. Deprecated aliases may still
load and conceal adoption drift.

## Version and release flow

`builder.yaml`'s `dist.version` is this artefact's semantic version:

- major: a consumer must change its role configuration;
- minor: linked capability grows without obliging consumers to change;
- patch: same component/configuration contract rebuilt.

Upstream module versions never appear in the artefact version. The workflow
derives OCB's version from the `otlpreceiver` pin and rejects any other `gomod`
pin that disagrees. Go is pinned exactly, currently to 1.27.0, because 1.25.0
mislinked this generated collector while the patched 1.25 toolchain built the
same manifest successfully. Move the pin only under a reviewed change that has
a green build job behind it.

The image scan rejects a fixable HIGH or CRITICAL finding in the built binary,
and a component pin cannot answer one that lives in a transitive dependency: the
`gomod` line names the component, and OCB generates the `go.mod` that resolves
everything under it. `builder.yaml`'s `replaces` block is where such a floor is
stated, one entry per advisory, each naming its CVE and the first upstream
version that fixes it. A floor is not a pin and not a capability change: it
raises one module to a released version the scan accepts, and it is removed once
a component pin requires that version or later on its own, because a replace
that has outlived its advisory holds a dependency back where nobody is looking
for it. Reachability does not decide this. `govulncheck` reads call paths and
the image scan reads the binary's build information, so a module linked for one
package is reported for an advisory against another; a finding the repository
believes is genuinely not exploitable is answered with a VEX statement and
evidence, never by widening the scan's severity or unfixed filters.

Never create a release tag manually. A default-branch workflow run publishes
`v<dist.version>` only when neither that release nor an orphan tag exists. Build
and test jobs are always ungated; image, release and formula publication are
additionally gated on a new version, default branch and non-PR event. Versioned
release assets and GHCR tags are immutable. To rebuild a version, deliberately
remove its release/tag and exact-version image before dispatching the workflow.

Published assets are two native binaries with bare-hex checksum files, an
archive containing exactly the three role profiles, `image-digest.txt`, and one
exact-version GHCR image with no `latest` tag. Every downstream job consumes
the build artefact rather than rebuilding it.

Image release readiness is a separate claim from binary, profile and harness
readiness. Hadolint is static analysis, and `components` inventories the linked
binary without proving that the packaged runtime can traverse its rootfs. The
Go-owned image smoke must load a configuration from `/etc/otelbox`, load the
system CA bundle and answer readiness as the declared `10001:10001` user under
the runtime restrictions. Run it against both the locally built image before
push and the immutable registry digest after push, following build -> boot ->
probe -> kill with unconditional cleanup. A release depends on both image
checks.

The formula job rewrites its version, tag URL segments and two checksums after
publication, verifies the resulting literals, commits and pushes. Do not
hand-edit those literals or reformat their one-literal-per-line shape. The
formula installs no service. `devbox-setup`-managed workstations use the
playbook-owned binary and launchd agent, not Homebrew.

## Verification

Build with OCB v0.158.0 and Go 1.27.0, then run:

```console
go -C tools/ci run ./cmd/otelbox-ci binary check \
  --binary ../../_build/otelcol-otelbox --manifest ../../builder.yaml
go -C tools/ci run ./cmd/otelbox-ci shared check \
  ../../config/edge.yaml ../../config/gateway.yaml ../../config/host-agent.yaml
go -C tools/ci run ./cmd/otelbox-ci image smoke \
  --image <tag-or-digest> --config ../../test/config/image-smoke.yaml
./_build/otelcol-otelbox validate --config config/<role>.yaml
go test -C test/harness . -count=1 -timeout 15m -v \
  -args -otelcol-binary "$PWD/_build/otelcol-otelbox"
```

Supply every required role environment value for `validate`. Run host-agent
validation on Linux because the journald receiver rejects other operating
systems during component construction. Validation does not start receivers or
exporters. The full harness proves edge/gateway delivery, redaction,
authentication backoff and live credential recovery, selected-trace routing
and headers, persistence across SIGKILL, token replacement, client-certificate
enforcement and in-place certificate replacement on the gateway leg, and
recipient coupling under pressure. On Linux it also starts the complete
host-agent profile and requires health and self metrics; target-host
permissions and collection remain deployment acceptance evidence.

`resource_detection`'s system detector and every `host_metrics` scraper read
boot time during startup. A command sandbox may deny that read and report
`getting boot time: operation not permitted`; this is an environment failure,
not a configuration failure. Run the delivery/durability tests outside that
sandbox. Smoke checks and `validate` are safe inside it.

Also run proportionate static checks for any changed surface: `go vet` and
`go test` for the harness, `shellcheck` for shell, `actionlint` for the workflow,
`hadolint` plus an image smoke test for Docker, `ruby -c`/Homebrew checks for the
formula, YAML parsing, and `git diff --check`. Never suppress a failing lint or
test.
