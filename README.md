# otelcol-otelbox

OCB-built durable OpenTelemetry collector. One binary, two roles:

- **edge** — runs on a workstation, accepts loopback OTLP from local
  applications, redacts credentials, and forwards to a single remote gateway
  over a `file_storage`-backed persistent WAL;
- **gateway** — runs on the remote server, accepts authenticated OTLP from the
  edges, and fans out to the telemetry backends with an independent persistent
  WAL per backend.

The invariant both roles share: **every outbound leg has its own on-disk queue**,
sized from above, with TLS verification left on and secrets injected only
through the environment. What differs is the secret store and the supervisor,
and both of those belong to the deployment, not to the collector.

## What lives here

This repository owns the **artefact and the shared half of its configuration**,
not the deployment:

| Path | Role |
|------|------|
| `builder.yaml` | OCB manifest. `dist.version` is this artefact's own semantic version and the single source of truth for a release; the upstream collector version lives in the `gomod` pins. |
| `config/base.yaml` | Shared base configuration layer: the processors both roles run, plus the collector's own telemetry. Never a runnable configuration on its own. |
| `config/examples/edge.yaml` | Reference edge profile. Validated by CI; the deployed copy lives in `devbox-setup`. |
| `config/examples/gateway.yaml` | Reference gateway profile. Validated by CI; the deployed copy lives in `remote_server_setup` (`roles/otel_gateway/files/config.yaml`). |
| `test/harness/` | Go test harness (stdlib-only, own module): end-to-end edge → authenticated gateway → sink delivery, plus a gateway backend-coupling scenario under queue pressure. |
| `test/config/*-ci.yaml` | CI-only overlays for the harness: ports in the 34xxx block, a file sink in place of the real backends, and — for the coupling scenario — a stoppable backend double. |
| `smoke-check.sh` | Post-build check: per-kind component counts vs the built binary. |
| `Dockerfile` | Packages the CI-built `linux/amd64` binary into a `scratch` image. It never compiles. |
| `.dockerignore` | Whitelists exactly the one binary the image needs. |
| `Formula/otelcol-otelbox.rb` | Homebrew formula for the macOS artefact. Rewritten by CI after each release. |
| `.github/workflows/otelcol-otelbox.yml` | Build both targets, validate, integration-test, publish the image, the release and the formula. |

The artefact itself has no Go source — the component set is declarative, and the
binary is produced by the OpenTelemetry Collector Builder. The one exception is
`test/harness/`, a stdlib-only module that drives the built binary from the
outside; it proves the artefact rather than being part of it.

Deployment still lives in the consuming repositories: `devbox-setup` supervises
the edge role through launchd, and `remote_server_setup` supervises the gateway
role through Docker Compose. The service units, the secret stores and the
machine-local setup scripts belong there, and so do the **authoritative** role
configurations. What lives here under `config/examples/` are reference profiles:
CI validates that the shared base layer still composes with each of them into a
runnable configuration, which is only worth anything while they remain faithful
mirrors of the deployed copies. Editing one of them changes nothing on any
machine; a change made in a consuming repository belongs here too.

## Configuration

The collector is configured as **exactly two layers**, passed as repeated
`--config` arguments, base first:

```bash
otelcol-otelbox --config config/base.yaml --config <exactly-one-role>.yaml
```

Later arguments win. **Map keys merge key by key; a list value replaces the
earlier list wholesale** rather than appending to it — which is why the gateway
profile restates every key of `memory_limiter` when it overrides it, and why
replacing `service::telemetry::metrics::readers` moves the scrape endpoint
rather than adding a second one.

The base layer carries `memory_limiter`, `batch`, `redaction/secrets` and the
collector's own telemetry, and nothing else. It exists chiefly so **the
redaction patterns cannot diverge** between the workstation and the server: held
in two role repositories they would drift, and nothing would make that visible.
Note that this is a convention rather than an enforced property — a role layer
that redefined `redaction/secrets` would win outright. Add patterns to the base
layer, for both roles, or not at all.

Receivers, exporters, extensions, `service::pipelines` and `service::extensions`
name a specific machine's endpoints, storage directories and credentials, so
they belong to the role layer. Consequently neither layer alone is a runnable
configuration.

Two component types are spelled canonically and may look unfamiliar: the OTLP
gRPC exporter is `otlp_grpc` (plain `otlp` is a deprecated alias in v0.156.0)
and the resource detection processor is `resource_detection` (not
`resourcedetection`). The deprecated names still load, so a deployed
configuration using them keeps working — but the copy in `devbox-setup` uses
both old names and should be renamed in lockstep when it adopts these profiles.
The OTLP *receiver* is still `otlp`.

## Components

Two different sets, and the distinction matters:

- **Linked** — everything in `builder.yaml`. Compiled into the binary, available
  to any config, costs binary size only. Currently 42: 19 receivers, 8
  processors, 3 exporters, 7 extensions, 5 connectors.
- **Wired** — what a role configuration actually instantiates. The rest of the
  linked set is dormant until a pipeline references it, which needs no rebuild.
  The linked set is therefore the union of what both roles may ever need, not
  what either one runs.

CI enforces the linked set: `smoke-check.sh` compares per-kind counts from
`builder.yaml` against the built binary's own `components` output, so a dropped
component fails the build rather than shipping a silently thin binary. Counts,
not names — a component reports its type, not its module path, and the two do
not map mechanically (`resourcedetectionprocessor` → `resource_detection`,
`otlpexporter` → `otlp_grpc`).

On top of the counts, `file_storage` and `redaction` are asserted by name: they
carry the two invariants the collector exists for, durability and credential
stripping.

## Build locally

Native darwin/arm64, mirroring the CI steps:

```bash
go install go.opentelemetry.io/collector/cmd/builder@v0.156.0   # the version the gomod pins carry
"$(go env GOPATH)/bin/builder" --config builder.yaml
./smoke-check.sh ./_build/otelcol-otelbox builder.yaml
```

Check that the shared base layer still composes with each role profile, as CI
does — every `${env:...}` reference is expanded at load time, so validation
needs a value for each one:

```bash
OTELBOX_EDGE_STORAGE=/tmp/otelbox-edge \
OTELBOX_EDGE_ENDPOINT=127.0.0.1:4317 \
OTELBOX_EDGE_TOKEN=placeholder \
  ./_build/otelcol-otelbox validate \
    --config config/base.yaml --config config/examples/edge.yaml
```

## The test harness

`test/harness` stands up processes of the binary under test, wired to each
other over loopback with the same authenticated hop the deployment uses, and
asserts on what came out of the far end. Two tests:

`TestEdgeToGatewayDelivery`, the edge → gateway pipeline:

1. a log posted to the edge reaches the gateway's sink;
2. credential-shaped attribute values are stripped before the sink, while the
   record carrying them still arrives;
3. an edge holding a token outside the gateway's allowlist drops the data **and
   says so** in `otelcol_exporter_send_failed_*` on its own metrics endpoint.

A precondition ahead of them checks that unauthenticated ingest is rejected with
HTTP 401, so assertion 3 cannot pass by proving nothing.

Assertion 3 is the one that matters. The deployed edge once failed every export
for days while every local signal stayed green — the process ran, the supervisor
reported it alive, the health endpoint answered 200, the OTLP receiver accepted
everything sent to it. Those prove the local half of the pipeline and none of
them prove delivery, so a wrong ingestion token looked exactly like a healthy
collector and the telemetry was simply gone. If you are changing anything on the
authentication path, this test is what stands between you and a repeat.

`TestBackendCouplingUnderQueuePressure`, a gateway with two backends: a stopped
backend with queue headroom leaves the healthy one untouched, but once its queue
fills, `block_on_overflow: true` plus synchronous fan-out
(`internal/fanoutconsumer`) stalls the healthy backend too — and clears again
once the stopped backend returns.

It needs only Go, a repository checkout (it resolves `config/base.yaml` and the
role profiles relative to its own package directory), and the ports in the
34xxx block, which are chosen to miss a running edge or gateway:

```bash
go test -C test/harness . -count=1 -timeout 15m -v \
    -args -otelcol-binary "$PWD/_build/otelcol-otelbox"
```

If `TestEdgeToGatewayDelivery` dies within seconds with `failed getting OS
version: OSVersion failed to get os version: getting boot time: operation not
permitted`, it is being run under a restricted command sandbox. That reads like
a configuration fault and is not one: the `resource_detection` processor's
`system` detector is being denied the boot-time `sysctl`. Run it outside the
sandbox — CI runners are unaffected, and `TestBackendCouplingUnderQueuePressure`
uses no `resource_detection` and is unaffected too.

## Release flow

1. Edit `builder.yaml`. `dist.version` is **this artefact's own semantic
   version**, not the upstream collector's — a release carries the shared
   configuration layer as well as the binary, so the number describes something
   a consuming repository can depend on:

   | Bump | When |
   |------|------|
   | major | A consuming repository has to change its role configuration, because `config/` changed shape under it. |
   | minor | A component is linked, or the base layer grows something a role layer may use without being obliged to. |
   | patch | A rebuild over the same component set and the same base layer. |

   The upstream collector version lives only in the `gomod` pins. CI fails the
   build when they are not all at one version: an upstream bump rewrites every
   pin by hand, and a single line left behind compiles perfectly and ships a
   collector assembled from two upstream releases, which nothing downstream
   would notice.
2. Merge to master. The push triggers
   `.github/workflows/otelcol-otelbox.yml` — **do not cut a tag by hand**, there
   is no tag trigger and pushing one does nothing.

   Building and testing run on every change to an input that could break them
   (`builder.yaml`, `smoke-check.sh`, `Dockerfile`, `.dockerignore`, `config/**`,
   `test/**`, the workflow itself) and on every pull request touching those
   paths. Publishing is gated separately, on all three of: the version being new,
   the run being on the default branch, and the event not being a pull request.
   An existing release is never re-published or overwritten. To rebuild a
   version, delete its release first, then
   `gh workflow run otelcol-otelbox.yml`.

   The OCB toolchain is installed at the version the manifest's `otlpreceiver`
   pin carries — the toolchain has to match the core modules it compiles — and
   deliberately never at `dist.version`.
3. A publishing run produces, under the tag `v<version>` — bare, with no
   artefact prefix, because this repository builds exactly one artefact and the
   prefix would repeat its name on every tag:

   | Asset | What it is |
   |-------|------------|
   | `otelcol-otelbox_darwin_arm64` + `.sha256` | The edge binary. The checksum file holds bare hex, no filename column. |
   | `otelcol-otelbox_linux_amd64` + `.sha256` | The gateway binary, and the exact bytes the container image ships. |
   | `otelcol-otelbox_config.tar.gz` | `config/base.yaml` and the reference profiles as validated for this version. |
   | `image-digest.txt` | One pinnable `ghcr.io/<owner>/otelcol-otelbox@sha256:...` reference on a single line. |

   The image itself is pushed to `ghcr.io/<owner>/otelcol-otelbox:<version>` —
   one tag, the exact version, no `latest`. It carries the label
   `io.github.abrosimov.otelbox.upstream.collector.version`, which is how a
   `docker inspect` on a running container answers *which upstream collector
   release is inside this thing* — a question the tag cannot answer, because it
   carries this artefact's own version. The release notes state the same value
   in prose.
4. After the release succeeds, CI **pushes a commit to this repository**
   rewriting `Formula/otelcol-otelbox.rb` with the new version and the checksums
   of the assets the release actually serves. That is unavoidable rather than
   convenient: a checksum cannot exist before the bytes it describes, so nobody
   can commit a correct formula ahead of the build. It is bounded to that one
   file, and the job refuses to push anything it cannot verify it just wrote.
5. The consuming repository pulls the published asset — or pins the image digest
   — and reconciles its own deployment. **Ordering matters:** the release must
   exist before that run.

## Install on macOS (Apple silicon)

**On a workstation the `devbox-setup` playbook manages, Homebrew is not the
installation path.** There Ansible owns the binary: it downloads the release
asset, checks it against the published digest, installs it under
`~/.local/bin/`, and points the launchd agent at that copy. Install through
Homebrew as well and you have two collectors on disk at two paths, free to drift
to different versions, while launchd goes on running the one Homebrew did not
install — so `otelcol-otelbox --version` in a terminal can disagree with the
version that is actually collecting anything, a symptom a long way from its
cause. That is the same hazard as the missing `brew services` block below, one
layer down, and the formula states both in its caveats. What follows is for
machines the playbook does not manage, and for use by hand.

The tap is this repository. Because it is *not* named `homebrew-otelbox`, the
one-argument `brew tap` shorthand cannot resolve it and the two-argument form is
required — which makes the tap name part of the procedure rather than a matter
of taste:

```bash
brew tap abrosimov/otelbox https://github.com/abrosimov/otelcol-otelbox   # once
brew install abrosimov/otelbox/otelcol-otelbox
```

The two names in that second command are not a slip. The repository is named
after the artefact it produces, so repository, binary, image, formula and
release assets all carry `otelcol-otelbox`; the tap keeps the label `otelbox`,
and the command reads as *the `otelcol-otelbox` formula, from the `otelbox`
tap*. `otelbox` is deliberately not the repository's name: it names the whole
telemetry estate, the backends on the server included, and one member of that
estate should not claim it.

The tap is the repository itself, so `brew update` picks up every later version
from the same clone:

```bash
brew upgrade abrosimov/otelbox/otelcol-otelbox
```

The formula declares `depends_on arch: :arm64` and `depends_on :macos`: the
release ships one binary per target and this formula wants only the macOS one,
so on anything else there is nothing here to install.

The formula installs the artefact and the published configuration layers (under
`$(brew --prefix otelcol-otelbox)/share/otelcol-otelbox/config`) and stops
there — `brew info` prints the exact path in its caveats. It carries
**no `brew services` block, on purpose**: on a workstation the edge role is
supervised by the launchd agent `devbox-setup` installs, and a second supervisor
would race it for the same loopback listeners.

One gap, stated rather than papered over: an installed binary reports
`dist.version`, which names this artefact and not the collector inside it, and
the formula records the upstream version nowhere. So on a workstation **the
upstream collector release is not recoverable from the installed artefact
alone** — it is in the release notes, and on the container image as a label, but
nothing on the machine holds it. If the question is "does this collector CVE
affect my edge", the release page is where to look.

## Run the gateway from the image

```bash
docker run --rm \
  -v <host-config-dir>:<container-config-dir>:ro \
  ghcr.io/abrosimov/otelcol-otelbox@sha256:<digest> \
  --config <container-config-dir>/base.yaml \
  --config <container-config-dir>/gateway.yaml
```

Pin the digest, not the tag. The image is `scratch` with a trust store and the
statically linked collector: no shell, no package manager, no baked-in
configuration, and every writable path comes from the supervisor as a bind mount
or tmpfs. The entrypoint is the collector itself, so the supervisor's `command`
is the argument list — including both `--config` layers. The paths above are the
deployment's to choose; `remote_server_setup` owns the ones the server uses.

## Current state

- `builder.yaml` declares `1.0.0`, built on upstream `v0.156.0`.
- **`v1.0.0` is published**, and the formula carries the checksums the
  publishing run wrote for it, so it installs. The version line and both digests
  belong to CI.
- The server runs the artefact (`remote_server_setup`, changes uncommitted
  there); the workstation does not — `devbox-setup` still builds an edge
  collector of its own.
