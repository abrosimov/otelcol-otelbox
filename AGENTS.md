# AGENTS.md

This file provides guidance to coding agents when working with code in this repository.

## Scope: the artefact and the shared config layer, not the deployment

This repository builds one OCB collector binary that serves **two roles** — the
workstation `edge` and the server `gateway` — publishes it as a binary per
target, a container image and a Homebrew formula, and owns the **shared half of
the configuration** both roles load. It owns nothing else. No Ansible, no
playbooks, no service units, no secret stores, no machine-local setup scripts:
those live in the consuming repositories (`devbox-setup` for the edge,
`remote_server_setup` for the gateway), and so do the authoritative role
configurations.

There is **no Go source**. The component set is declarative; the binary is
produced by the OpenTelemetry Collector Builder from `builder.yaml`.

### Names

The repository is `abrosimov/otelcol-otelbox` — named after the artefact it
produces, so repository, binary, container image, Homebrew formula and release
assets all carry one name and nothing has to be translated between them (the
release *tag* is the exception, and deliberately bare: `v<version>`).

`otelbox` alone is **not** this repository's name and must not be used as one:
it denotes the whole telemetry estate, the server's backends under
`/var/lib/otelbox/` included, and one member of that estate cannot claim it. The
one place the bare name survives is the Homebrew tap label, which is why
`brew install abrosimov/otelbox/otelcol-otelbox` names two different things and
is correct as written.

## Map

| Path | What it is |
|------|------------|
| `builder.yaml` | OCB manifest. `dist.version` is the single source of truth for this artefact's own version; the `gomod` pins are the only record of the upstream one. |
| `config/base.yaml` | Shared base layer: processors both roles run, plus self-telemetry. Not runnable alone. |
| `config/examples/{edge,gateway}.yaml` | Reference role profiles. Mirrors of the deployed copies; CI validates them against the base layer. |
| `test/integration-test.sh` | End-to-end edge → authenticated gateway → sink test. |
| `test/config/{edge,gateway}-ci.yaml` | CI-only third layer: 34xxx ports, a `file` sink in place of the backends, and — edge only — TLS off on a hop that never leaves the kernel. |
| `smoke-check.sh` | Per-kind component counts, manifest vs built binary, plus two named assertions. |
| `Dockerfile` | Packages the CI-built `linux/amd64` binary into `scratch`. Never compiles. |
| `Formula/otelcol-otelbox.rb` | Homebrew formula. **Rewritten by CI**, see below. |
| `.github/workflows/otelcol-otelbox.yml` | The whole build/prove/publish graph. |

## What the two roles share

```
edge     loopback OTLP → redaction → WAL → one remote gateway
gateway  authenticated OTLP → redaction → WAL per backend → N backends
```

The shared invariant: every outbound leg carries its own on-disk `file_storage`
queue, sized from above; TLS verification is never disabled on a leg that leaves
the host (the gateway's `insecure: true` is on loopback to a co-located
container, and the only exception on the edge→gateway leg is fenced into
`test/config/edge-ci.yaml`); secrets arrive only as environment variables or
mounted files injected by the supervisor. What differs between the roles is the
secret store (login keychain vs Ansible Vault) and the supervisor (launchd vs
Docker Compose) — both deployment concerns, not collector concerns.

## Configuration layering

Composed as **exactly two layers**, base first, as repeated `--config`
arguments:

```
otelcol-otelbox --config config/base.yaml --config <exactly-one-role>.yaml
```

`test/integration-test.sh` adds a third, CI-only overlay. Nothing else does, and
nothing outside `test/` should.

**Merge semantics, and they are load-bearing.** Later `--config` arguments win.
Map keys merge key by key; a list value **replaces** the earlier list wholesale.
Three consequences worth holding before editing any layer:

- An override of a map must restate every key it means to keep. The gateway
  restates all three `memory_limiter` keys for exactly this reason; omitting one
  would silently keep the edge value.
- Replacing `service::telemetry::metrics::readers` moves the scrape endpoint;
  adding to it is not possible, which is how the gateway gets 8889 instead of a
  second endpoint on 8888.
- A key cannot be *removed* by a later layer. `test/config/gateway-ci.yaml`
  cannot delete the `metrics/self` pipeline, so it repoints it at an unused
  receiver instead — otherwise `docker_stats` would open a Docker socket the
  runner does not have and kill startup.

**Why the base layer exists:** so the redaction patterns cannot diverge between
workstation and server. Held in two role repositories they would drift, and
nothing would fail to make that visible. Note the property is **conventional,
not enforced** — `redaction/secrets` is an ordinary config key, and a role layer
redefining it wins outright. The comment in `config/base.yaml` says as much.
Patterns go in the base layer, for both roles, or nowhere.

The base layer owns `memory_limiter`, `batch`, `redaction/secrets` and
`service::telemetry`. Everything that names a specific machine — receivers,
exporters, extensions, `service::pipelines`, `service::extensions` — belongs to
the role layer. `resource_detection` is deliberately in the *edge role* layer,
not the base: it stamps origin, and origin is what the two roles do not share.

### Canonical component types

This repository uses `otlp_grpc` (exporter) and `resource_detection`
(processor). In v0.156.0 plain `otlp` and `resourcedetection` are deprecated
aliases that still load. The OTLP *receiver* is still `otlp` — the rename is on
the exporter side only. The deployed edge configuration in `devbox-setup` uses
both old names; those two renames are one change there, to be made when it
adopts these profiles.

### The reference profiles are mirrors

`config/examples/*.yaml` are not deployed. The authoritative copies are
`devbox-setup`'s rendered launchd configuration and
`remote_server_setup/roles/otel_gateway/files/config.yaml`. Editing an example
changes nothing on any machine. Their value is entirely in staying faithful:
CI's validation and the integration test only prove something about production
while they mirror it. A change in a consuming repository belongs here too.

**If you add an `${env:...}` reference to any layer**, add a dummy value for it
to the `validate-config` job's `env:` block in the workflow. An unset variable
is a load error, not a default, so the omission fails CI at a point that reads
like a config bug.

## Version and release flow

`dist.version` in `builder.yaml` is the **single source of truth**, and it is
**this artefact's own semantic version** — it does not encode the upstream
collector release. The manifest header is the authoritative statement of what
each component means; read it before bumping. Because a release carries the
shared configuration layer as well as the binary, the number describes a
contract a consuming repository can depend on:

- **major** — a consuming repository has to change its role configuration,
  because `config/` changed shape under it;
- **minor** — a component is linked, or the base layer grew something a role
  layer may use without being obliged to;
- **patch** — a rebuild over the same component set and the same base layer.

Nothing else records the artefact version — the formula's `version` line mirrors
it and is written by CI, never by hand. The upstream collector version is
recorded only in the `gomod` pins.

- **Never cut a tag by hand** — there is no tag trigger, and pushing one does
  nothing. A publishing run tags itself `v<version>`, bare: this repository
  builds exactly one artefact, so an artefact prefix would only repeat the
  repository's own name on every tag.
- CI installs OCB at the version the `otlpreceiver` gomod pin carries (`resolve`
  job, output `upstream_version`) — the toolchain must match the core modules it
  compiles — and deliberately never at `dist.version`.
- **The pin-agreement guard.** `resolve` fails the build when *any* `gomod` line
  in the manifest is at a version other than the `otlpreceiver` pin. It exists
  because an upstream bump rewrites every pin by hand: one line left behind
  compiles perfectly and ships a collector assembled from two upstream releases,
  and nothing downstream would notice. If upstream ever stops releasing its
  modules in lockstep, the guard is the thing that is wrong — not the pins.
- The workflow is idempotent on release existence: an existing release is never
  re-published or overwritten. To rebuild a version, delete its release first,
  then `gh workflow run otelcol-otelbox.yml`.

### What CI runs, and what it gates

Read the header of `.github/workflows/otelcol-otelbox.yml` before changing it —
it states which half of the graph is gated and which half is not, and why.

- **Triggers**: push to `master`, and pull requests, on `builder.yaml`,
  `smoke-check.sh`, `Dockerfile`, `.dockerignore`, `config/**`, `test/**` and
  the workflow file; plus `workflow_dispatch`. `README.md` and `AGENTS.md` are
  deliberately absent — they cannot break a build.
- **Building and testing are ungated.** They run on every triggering event,
  including one whose version is already released. Gate them on the
  release-existence check instead and a change to `config/base.yaml` or to the
  test harness is never compiled and never exercised, while CI reports green on
  code it has never built.
- **Publishing is gated** on all three of: the version being new, the ref being
  the default branch, and the event not being a pull request. That single
  `publish` output drives the GHCR push, the release and the formula job.
- **Jobs**: `resolve` → `build` (matrix `darwin/arm64` on macOS, `linux/amd64`
  on Ubuntu) → `validate-config`, `integration-test` → `image` → `release` →
  `formula`; `config-bundle` runs alongside.
- **Each target builds on a runner of its own architecture**, because
  `smoke-check.sh` executes the binary to read its `components` output. A
  cross-built binary cannot be smoke-checked at all.
- **Every downstream job consumes the build artefact**, never a rebuild: the
  image, the config validation, the integration test and the release all ship
  the exact bytes that were tested. Artefact downloads lose the executable bit,
  so each such job does `chmod +x`.
- **The two path lists under `on:` are duplicated on purpose.** The Actions
  parser does not resolve YAML aliases, so an anchor would silently produce an
  empty filter and the workflow would run on everything. Keep them in step.
- **Published assets**: two binaries plus a `.sha256` each (bare hex, no
  filename column), `otelcol-otelbox_config.tar.gz`, and `image-digest.txt`
  holding one pinnable `ghcr.io/<owner>/otelcol-otelbox@sha256:...` line. The
  image is pushed under one tag, the exact version — no `latest`, and
  `provenance`/`sbom` are off so the published digest names the image rather
  than a manifest index.
- **Where the upstream version surfaces.** The version number does not carry it,
  so `resolve` reads it from the `otlpreceiver` pin and hands it on twice: the
  release notes state it in prose, and the image carries it as
  `io.github.abrosimov.otelbox.upstream.collector.version` (a custom key — no
  OCI predefined label means "the upstream project this artefact is assembled
  from", and `image.version` is already the image's own). That label is what
  makes "does this collector CVE affect the running gateway" answerable from
  `docker inspect` alone. There is no equivalent on the Homebrew path.

### CI writes to this repository

The `formula` job commits and pushes `Formula/otelcol-otelbox.rb` to the default
branch after a successful publish. This is the one place where `git log` here is
not only "what a human decided", and it is deliberate: a formula pins a version
and the checksums of the assets at that version, and a checksum cannot exist
before the bytes it describes. A human cannot commit a correct formula ahead of
the build, and a check that merely failed on drift would go red on every version
bump by construction — a gate that always fails is a gate people ignore.

Consequences to keep in mind:

- The rewrite is a `sed` anchored on the formula's *shape*: the `version` line,
  the release-tag segment of each URL, and the two `sha256` lines told apart by
  their indentation (two spaces at formula level, four inside `resource`).
  Reformatting the formula, or putting two literals on one line, breaks the
  anchors. A verification step reads every literal back and fails the job rather
  than pushing a stale formula, so the failure is loud — but it is still a
  failure of a job at the very end of a successful release.
- The push does not re-trigger the workflow: `GITHUB_TOKEN` pushes do not start
  runs, and `Formula/**` is absent from the path filters anyway.
- **Do not hand-edit the version or the checksums** in the formula. Edit its
  structure, its caveats and its `test do` block freely — but the caveats carry
  two boundary statements that are not decoration. One: the formula supervises
  nothing, because launchd does. Two: **on a workstation `devbox-setup` manages,
  Homebrew is not the installation path at all** — Ansible owns the binary
  there, downloading the release asset, checking it against the published digest
  and installing it under `~/.local/bin/` for the launchd agent to run. Both
  ways at once puts two copies on disk, free to drift, with launchd running the
  one Homebrew did not install. The formula's audience is machines the playbook
  does not manage, plus manual use.

## Verification

### smoke-check.sh

```bash
./smoke-check.sh ./_build/otelcol-otelbox builder.yaml
```

Compares per-kind component *counts* (not names) from the manifest against the
binary's own `components` output, and additionally asserts `file_storage` and
`redaction` by name — the two invariants the collector exists for. Counts
because a component reports its type, not its module path, and the two do not
map mechanically (`resourcedetectionprocessor` → `resource_detection`,
`otlpexporter` → `otlp_grpc`). **Do not replace this with a hand-maintained name
table.**

Linked is not enabled: `builder.yaml` currently declares 42 components (19
receivers, 8 processors, 3 exporters, 7 extensions, 5 connectors), of which any
given role config wires a handful. A linked component costs binary size only and
is dormant until a pipeline references it, which needs no rebuild.

Its blind spot is why the next section exists: it proves the binary contains
components, not that anything reaches the far end.

### test/integration-test.sh

```bash
./test/integration-test.sh ./_build/otelcol-otelbox
```

Stands up two processes of the binary in the two roles it serves, wired over
loopback with the same authenticated hop the deployment uses, and asserts on
what came out of the far end: delivery, redaction, and — the one that matters —
that an edge holding a token outside the gateway's allowlist both drops the data
and makes the drop visible in `otelcol_exporter_send_failed_*`. A precondition
checks that unauthenticated ingest gets HTTP 401, so that assertion cannot pass
vacuously.

**The incident it exists for.** The deployed edge failed every export for days
with `Unauthenticated ... provided authorization does not match expected scheme
or token`, and nothing caught it: the process ran, launchd reported it alive,
the health endpoint answered 200, the OTLP receiver accepted everything sent to
it. Every one of those proves the local half of the pipeline and none proves
delivery, so a wrong credential looked exactly like a healthy collector and the
telemetry was simply gone. **Anyone changing the authentication path — the
`bearertokenauth` extension, the token file format, the edge's `authorization`
header, the receiver's `auth:` blocks — should know this test is the thing
standing between them and a repeat.** Do not weaken it into a startup check.

Reading it before editing it repays the time: `set -e` is deliberately absent
(half the control flow is a probe expected to fail), there are no fixed sleeps,
every wait is a bounded poll that also watches the collector PID, and the token
file's trailing comment is a format assertion rather than decoration.

### The sandbox trap

**The integration test cannot run under this environment's default command
sandbox.** The `resource_detection` processor's `system` detector is denied the
boot-time `sysctl`, so the edge process dies at startup with

```
Error: cannot start pipelines: failed to start "resource_detection" processor:
failed getting OS version: OSVersion failed to get os version: getting boot
time: operation not permitted
```

which reads like a configuration fault and is not one. Nothing in `config/` is
wrong when this happens. Run the test outside the sandbox; CI runners are
unaffected. `validate` and `smoke-check.sh` are fine inside it — the detector
only runs when the pipeline starts.

## Known state

- `builder.yaml` declares `1.0.0`, built on upstream `v0.156.0`.
- **No release exists.** `Formula/otelcol-otelbox.rb` already names `1.0.0` in
  its `version` line and in both release URLs, but carries
  `PENDING-FIRST-CI-PUBLISH...` in place of both checksums, which Homebrew
  rejects outright — deliberately, since a formula with no `sha256` at all
  installs after only a warning, and no honest digest can be written for bytes
  that have not been published. Both the version and the checksums are CI's to
  write; leave them alone.
- Neither consuming repository has been touched yet.
  `remote_server_setup/roles/otel_gateway/templates/compose.yaml.j2` still pins
  the upstream `otel/opentelemetry-collector-contrib:0.156.0` image.
- The edge authentication incident above was never diagnosed on the server side.
  The gateway's token-file format is not the cause — upstream parses that file
  line by line and treats text after the first whitespace as a comment, which is
  exactly what the consuming repository renders. The mismatch is in the
  credential value or in which allowlist the running gateway has. Diagnosis
  needs server access, which `remote_server_setup`'s own agent rules forbid
  without a separate, explicit request.

## Still outstanding

The remaining work is entirely on the consuming side, and neither half has been
started:

- `remote_server_setup` swaps the upstream contrib image for this repository's
  GHCR image (pinned by the digest from `image-digest.txt`) and adopts the
  shared base layer, so its `command` passes `--config base.yaml --config
  <role>.yaml`. The existing Docker Compose deployment stays otherwise intact —
  the `image:` reference, the config paths in `command`, and the same paths in
  that role's `validate` task are what change.
- `devbox-setup` adopts the edge profile, which is also where the `otlp` →
  `otlp_grpc` and `resourcedetection` → `resource_detection` renames land.

A design note worth keeping while doing either: gateway queues keep
`block_on_overflow: true` on every exporter, and splitting backends across
pipelines would **not** decouple them. Fan-out in the collector is synchronous
on the caller's goroutine (`internal/fanoutconsumer`), so a blocked enqueue on
one exporter stalls everything queued behind it regardless of how the pipelines
are drawn. The only real knobs are the overflow policy and queue sizing.

## Conventions

- **Comments in these files are load-bearing.** Every YAML/bash file carries a
  header, and most settings carry a line, explaining *why* the value is what it
  is. Several of them are the only record of a decision or a trap. Preserve and
  update them rather than stripping them; a tidy-up that deletes one is a
  regression.
- `dist.version` in `builder.yaml` is deliberately **unquoted**: CI reads it with
  `awk`, not a YAML parser, and would otherwise keep the quotes.
- Ports: the workstation edge holds 4317/4318/13133/8888, the server gateway
  holds 14317–14322/8888/8889, and everything in `test/` sits in a 34xxx block
  chosen to miss both — so a developer running their own edge can still run the
  test.
- The one `insecure: true` on the edge→gateway leg lives in
  `test/config/edge-ci.yaml`, fenced in by a comment there. Every configuration
  on a leg that leaves the host keeps verification on. Do not copy that line
  anywhere.
- British English in all file contents, comments, and commit messages.
