# otelcol-otelbox

OCB-built durable OpenTelemetry collector. One binary, three roles:

- **edge** — accepts unauthenticated OTLP from local applications, conventionally
  on loopback, scrapes the host and its own metrics, redacts credentials, and
  forwards to a single remote gateway over a `file_storage`-backed persistent
  WAL;
- **gateway** — accepts authenticated OTLP on a single receiver, from the edges
  and from host-local producers alike, and fans out to N telemetry backends with
  an independent persistent WAL and queue per backend;
- **host-agent** — a plain host process beside a gateway, collecting the host's
  metrics, container stats over a Docker API endpoint and journal logs from
  selected units, and exporting to the gateway. It exists because a container
  sees neither the host's `/proc` nor the journal nor a loopback-only admin
  endpoint.

The roles are shapes, not machines. The invariants they share: **every leg that
can lose data to a network has its own on-disk queue**, sized from above; every
leg carries a credential of its own rather than relying on where its socket is
reachable from; and secrets arrive only through the environment. Whether a given
leg also carries transport security is the deployment's decision — the one
exception is the edge→gateway leg, whose credential is a bare bearer token, so
verification stays on there. The host agent is the one deliberate exception on
durability: it keeps its queue in memory, because the gateway it feeds holds a
WAL per backend.

## What lives here

This repository owns the **artefact and the shared half of its configuration**,
not the deployment:

| Path | Role |
|------|------|
| `builder.yaml` | OCB manifest. `dist.version` is this artefact's own semantic version and the single source of truth for a release; the upstream collector version lives in the `gomod` pins. |
| `config/base.yaml` | Shared base configuration layer: the processors every role runs, plus the collector's own telemetry. Never a runnable configuration on its own. |
| `config/examples/edge.yaml` | Reference edge profile — a demonstration of the role, validated by CI, not a copy of any deployment. |
| `config/examples/gateway.yaml` | Reference gateway profile. Same status. |
| `config/examples/host-agent.yaml` | Reference host-agent profile. Same status. |
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

Deployment lives in the consuming repositories: `devbox-setup` supervises the
edge role, `remote_server_setup` the gateway and host-agent roles. The service
units, the secret stores and the machine-local setup scripts belong there, and
so do the configurations those repositories actually deploy.

What lives here under `config/examples/` are **reference profiles, not
mirrors.** They do two things: prove that the shared base layer still composes
with each role into a runnable configuration, and demonstrate the capabilities
and invariants a consuming repository is expected to preserve. They are not
copies of anything, and a difference between a profile and a deployment is not
automatically a defect in either — this repository publishes an artefact and a
shared configuration layer, and cannot know what the estate around them looks
like. Accordingly, **every value that names a neighbour, a socket path, a port
belonging to another process or a product is an `${env:...}` reference**, and so
is **every address a listener binds** — `${env:OTELBOX_<ROLE>_BIND_HOST}`, with
the port written literally beside it, because a port is a convention this
repository owns and documents while the choice between loopback and a wildcard
depends on whether the role runs as a container, a pod or a host process.

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
layer, for every role, or not at all.

Receivers, exporters, extensions, `service::pipelines` and `service::extensions`
name a specific machine's endpoints, storage directories and credentials, so
they belong to the role layer. Consequently neither layer alone is a runnable
configuration.

Component types are spelled canonically and several may look unfamiliar.
Upstream is renaming types to `snake_case` and keeping the old spelling as a
deprecated alias, so 16 of the 46 components here have two names: the OTLP gRPC
exporter is `otlp_grpc` (plain `otlp` is the alias, and the OTLP *receiver* is
still `otlp`), the resource detection processor is `resource_detection`, the
host metrics receiver is `host_metrics`, and so on. Do not guess — check
`metadata.yaml`'s `type` and `deprecated_type` at the pinned version. The
deprecated names still load, so a deployed configuration using them keeps
working, but both consuming repositories carry one such rename to make.

## Components

Two different sets, and the distinction matters:

- **Linked** — everything in `builder.yaml`. Compiled into the binary, available
  to any config, costs binary size only. Currently 46: 20 receivers, 10
  processors, 3 exporters, 8 extensions, 5 connectors.
- **Wired** — what a role configuration actually instantiates. The rest of the
  linked set is dormant until a pipeline references it, which needs no rebuild.
  The linked set is therefore the union of what any role may ever need, not
  what any one of them runs. `journald` is Linux-only and wired by the host
  agent alone; `oidc`, `oauth2client`, `groupbyattrs` and `cumulative_to_delta`
  are wired by nothing at all, and deliberately so.

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
go install go.opentelemetry.io/collector/cmd/builder@v0.157.0   # the version the gomod pins carry
"$(go env GOPATH)/bin/builder" --config builder.yaml
./smoke-check.sh ./_build/otelcol-otelbox builder.yaml
```

Check that the shared base layer still composes with each role profile, as CI
does — every `${env:...}` reference is expanded at load time, so validation
needs a value for each one:

```bash
OTELBOX_EDGE_BIND_HOST=127.0.0.1 \
OTELBOX_EDGE_STORAGE=/tmp/otelbox-edge \
OTELBOX_EDGE_ENDPOINT=127.0.0.1:4317 \
OTELBOX_EDGE_TOKEN=placeholder \
OTELBOX_EDGE_PROBE_URL=http://127.0.0.1:4318/v1/logs \
OTELBOX_EDGE_NTP_ENDPOINT=pool.ntp.org:123 \
OTELBOX_EDGE_HEALTH_ENDPOINT=127.0.0.1:13133 \
  ./_build/otelcol-otelbox validate \
    --config config/base.yaml --config config/examples/edge.yaml
```

The gateway and host-agent profiles need their own variables; the
`validate-config` job in `.github/workflows/otelcol-otelbox.yml` carries a
working dummy for every one of them and is the list to copy from.

`validate` parses without starting anything, which matters on the edge and
host-agent profiles: `host_metrics` and `resource_detection` both read the
boot time at start-up, and a restricted command sandbox denies that `sysctl`.
The failure reads like a configuration fault and is not one — see the harness
section below.

## The test harness

`test/harness` stands up processes of the binary under test, wired to each
other over loopback across the same authenticated hop the roles define, and
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
collector and the telemetry was simply gone. **Moving every role to
`healthcheckv2` did not close this**, and no setting on it would: the health
extension aggregates component lifecycle events, and a rejected export emits
none — see AGENTS.md "Component facts". The test polls the edge healthy *after*
handing it a token the gateway refuses, which is that fact written down as an
assertion. If you are changing anything on the authentication path, this test is
what stands between you and a repeat.

`TestBackendCouplingUnderQueuePressure`, a gateway with two backends: a stopped
backend with queue headroom leaves the healthy one untouched, but once its queue
fills, `block_on_overflow: true` plus synchronous fan-out
(`internal/fanoutconsumer`) stalls the healthy backend too — and clears again
once the stopped backend returns.

Both tests wire the profiles through CI-only overlays under `test/config/`, so
the harness needs the same environment the profiles do plus its own
`OTELBOX_CI_*` values; `test/harness` sets them.

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
`system` detector is being denied the boot-time `sysctl`, and every
`host_metrics` scraper reads the same value in `start()`, so a profile carrying
either will not start there. Run it outside the sandbox — CI runners are
unaffected, and `TestBackendCouplingUnderQueuePressure` uses neither and is
unaffected too.

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
   | `otelcol-otelbox_linux_amd64` + `.sha256` | The gateway and host-agent binary, and the exact bytes the container image ships. |
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

- `builder.yaml` declares `2.0.0`, built on upstream `v0.157.0`. **Not released
  yet** — the published version is `v1.0.0` on upstream `v0.156.0`, and the
  formula carries the checksums that run wrote, so it installs. The version line
  and both digests belong to CI, and stay at 1.0.0 until 2.0.0 publishes.
- 2.0.0 links five more components (`journald`, `oidc`, `oauth2client`,
  `groupbyattrs`, `cumulative_to_delta`), unlinks `health_check` in favour of
  `healthcheckv2`, adds the host-agent reference profile, and grows the edge
  profile by `host_metrics`, `prometheus/self`, `ntp` and `http_check`.
- **The reference profiles stopped being mirrors of the deployed copies**, and
  everything naming a neighbour, a socket or a product became an `${env:...}`
  reference. The gateway's two backend exporters and their WALs are now
  `backend_1` and `backend_2`; the host agent authenticates with
  `headers_setter` instead of `bearertokenauth`, which is what makes its
  exporter start at all over a plaintext leg.
- **The gateway now has one OTLP receiver and one allowlist**, not two of each:
  14321/14322 are free, `OTELBOX_GATEWAY_LOCAL_TOKEN_FILE` is gone, and the host
  agent's token is a line in the same file every edge's is.
- **Every listener binds `${env:OTELBOX_<ROLE>_BIND_HOST}`** with a literal port,
  and the OTLP receivers accept 32 MiB on both transports — stated on HTTP too,
  where `confighttp` would otherwise default to 20 MiB and quietly be the
  tighter of the two.
- **The number is a major, and settled.** A consuming repository has to rewrite
  its role configuration to take this release: the gateway's two receivers
  collapse into one, `health_check` no longer exists to name, and three new
  variables have to be rendered. That is the major criterion as the table below
  states it.
- The server runs the artefact (`remote_server_setup`, changes uncommitted
  there); the workstation's adoption is staged in `devbox-setup` and not
  committed either. Neither has taken 2.0.0.
