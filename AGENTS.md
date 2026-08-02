# AGENTS.md

This file provides guidance to coding agents when working with code in this repository.

## Scope: the artefact and the shared config layer, not the deployment

This repository builds one OCB collector binary that serves **three roles** —
`edge`, `gateway` and `host-agent` — publishes it as a binary per target, a
container image and a Homebrew formula, and owns the **shared half of the
configuration** every role loads. It owns nothing else. No Ansible, no
playbooks, no service units, no secret stores, no machine-local setup scripts:
those live in the consuming repositories (`devbox-setup` for the edge,
`remote_server_setup` for the gateway and the host agent), and so do the role
configurations those repositories actually deploy. The roles are shapes, not
machines: today the edge runs on a Mac and the other two on one Linux server,
and nothing here depends on that staying true.

The artefact itself has **no Go source**: the component set is declarative, and
the binary is produced by the OpenTelemetry Collector Builder from
`builder.yaml`. The only Go code in the repository is `test/harness/`, a
stdlib-only module that drives the built binary from the outside — it proves
the artefact, it is not part of it.

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
| `config/base.yaml` | Shared base layer: processors every role runs, plus self-telemetry. Not runnable alone. |
| `config/examples/{edge,gateway,host-agent}.yaml` | Reference role profiles. Demonstrations of what each role can do, not copies of any deployment; CI validates them against the base layer. |
| `test/harness/` | Go test harness (stdlib-only, own module) that drives the binary under test: end-to-end edge → authenticated gateway → sink delivery, and a gateway backend-coupling scenario under queue pressure. |
| `test/config/{edge,gateway}-ci.yaml` | CI-only third layer for the delivery test: 34xxx ports, a `file` sink in place of the backends, each role's `metrics/self` pipeline repointed, and the TLS material the harness mints per run so the leg is verified exactly as it is in a deployment. |
| `test/config/gateway-coupling-ci.yaml` | CI-only third layer for the coupling test: keeps both real backend exporters, `block_on_overflow` and both WALs; moves only ports, `queue_size` and `batch.timeout`. |
| `test/config/backend-ci.yaml` | A stoppable backend double for the coupling test — OTLP gRPC in, `file` out. Not a role layer; never composed with `config/base.yaml`. |
| `smoke-check.sh` | Per-kind component counts, manifest vs built binary, plus two named assertions. |
| `Dockerfile` | Packages the CI-built `linux/amd64` binary into `scratch`. Never compiles. |
| `Formula/otelcol-otelbox.rb` | Homebrew formula. **Rewritten by CI**, see below. |
| `.github/workflows/otelcol-otelbox.yml` | The whole build/prove/publish graph. |

## What the three roles share

```
edge        loopback OTLP → redaction → WAL → one remote gateway
gateway     authenticated OTLP → redaction → WAL per backend → N backends
host-agent  host metrics + container stats + journal → redaction → gateway
```

The `host-agent` is the same binary run as a plain host process beside a
gateway. It collects the host's own metrics, container stats through a Docker
API endpoint, journal logs from selected units and its own metrics, and exports
to the gateway. It exists because a container sees neither the host's `/proc`
nor the journal nor a loopback-only admin endpoint — inside a namespace
`127.0.0.1` is the container. It is the one role whose outbound queue is
deliberately **in memory**: the gateway it feeds holds a WAL per backend, and
the host metrics it would replay after a restart describe a machine as it was
minutes ago.

The shared invariants: every leg that can lose data to a network carries its own
on-disk `file_storage` queue, sized from above; every leg carries a credential
in its own right rather than relying on where its socket happens to be
reachable from; and secrets arrive only as environment variables or mounted
files the supervisor injects. Whether a given leg also carries transport
security is the deploying repository's decision, not this one's — this
repository cannot know how far any leg travels. The one exception it does own is
the edge→gateway leg, where the credential is a bare bearer token in a header
and verification therefore stays on — in the integration test too, which mints
a CA and a leaf per run rather than turning verification off.
What differs between the roles is the secret store and the supervisor, and both
are deployment concerns.

## Configuration layering

Composed as **exactly two layers**, base first, as repeated `--config`
arguments:

```
otelcol-otelbox --config config/base.yaml --config <exactly-one-role>.yaml
```

`test/harness` adds a third, CI-only overlay through the `test/config/*-ci.yaml`
files it composes. Nothing else does, and nothing outside `test/` should.

**Merge semantics, and they are load-bearing.** Later `--config` arguments win.
Map keys merge key by key; a list value **replaces** the earlier list wholesale.
Three consequences worth holding before editing any layer:

- An override of a map must restate every key it means to keep. The gateway
  restates all three `memory_limiter` keys for exactly this reason; omitting one
  would silently keep the edge value.
- Replacing `service::telemetry::metrics::readers` moves the scrape endpoint;
  adding to it is not possible, which is how the gateway gets 8889 instead of a
  second endpoint on 8888.
- A key cannot be *removed* by a later layer. No CI overlay can delete the
  role's `metrics/self` pipeline, so each repoints it instead: the gateway's onto
  its one OTLP receiver, away from `docker_stats`, which would open a Docker
  socket the runner does not have, and from `prometheus/self`, whose steady
  stream would distort the queue pressure the coupling scenario measures; the
  edge's down to `prometheus/self` alone. A receiver left configured but out of
  every pipeline is never built, which is what makes the trick work.
- `${env:...}` expansion runs over the **merged** map, after every layer is
  applied — so replacing a list that held a reference removes the reference
  rather than leaving it unset. The CI overlays rely on this to drop
  `OTELBOX_EDGE_PROBE_URL` and both `_BIND_HOST` variables, which the harness
  does not supply. It is also why a receiver the overlay never builds still
  needs its `${env:...}` values replaced with literals: expansion does not care
  whether a component is wired.

**Why the base layer exists:** so the redaction patterns cannot diverge between
workstation and server. Held in three role configurations across two
repositories they would drift, and nothing would fail to make that visible.
Note the property is **conventional, not enforced** — `redaction/secrets` is an
ordinary config key, and a role layer redefining it wins outright. The comment
in `config/base.yaml` says as much. Patterns go in the base layer, for every
role, or nowhere.

The base layer owns `memory_limiter`, `batch`, `redaction/secrets` and
`service::telemetry`. Everything that names a specific machine — receivers,
exporters, extensions, `service::pipelines`, `service::extensions` — belongs to
the role layer. `resource_detection` is deliberately in the *role* layers, not
the base: it stamps origin, and origin is exactly what the roles do not share.

### Canonical component types

Upstream is renaming component types to `snake_case`, keeping the old spelling
as a deprecated alias that still loads (contrib #45339, still open). **The
pattern rather than a table: assume any multi-word type has been renamed, and
check `metadata.yaml`'s `type` and `deprecated_type` at the pinned version
before spelling it.** Sixteen of the 46 components in `builder.yaml` carry a
`deprecated_type` at v0.157.0 — including three whose canonical names this
document itself used to get wrong: the connectors are `span_metrics`,
`signal_to_metrics` and `otlp_json`, not `spanmetrics`, `signaltometrics` and
`otlpjson`.

The ones this repository's configuration actually names: `otlp_grpc`
(exporter — the OTLP *receiver* is still `otlp`, and that one is not an alias),
`resource_detection`, `host_metrics`, `http_check`, `file_stats`, `tcp_check`,
`file_log`. Canonical as spelled, no alias to confuse them with:
`docker_stats`, `file_storage`, `healthcheckv2`, `headers_setter`,
`bearertokenauth`, `ntp`, `prometheus`, `journald`, `groupbyattrs`, `oidc`,
`oauth2client`. `cumulative_to_delta` was renamed at v0.157.0, with
`cumulativetodelta` left as the alias. `healthcheckv2` is not a versioned alias
of anything: `health_check` is a separate component, and it is no longer linked.

Two consuming repositories still spell deprecated names, one change each:
`devbox-setup` uses `otlp` and `resourcedetection` on the edge, and
`remote_server_setup` uses `hostmetrics` and `resourcedetection` in the host
agent's template.

### The reference profiles are demonstrations, not mirrors

**This overturns the earlier doctrine, deliberately.** `config/examples/*.yaml`
used to be maintained as mirrors of the deployed copies in `devbox-setup` and
`remote_server_setup`. They are not, any more. Two things they now do:

1. prove that `config/base.yaml` still composes into a runnable role;
2. demonstrate the capabilities and the invariants a consuming repository is
   expected to preserve — which components, in what pipeline order, with what
   durability, redaction and authentication around them.

They are not copies of anything, and **a difference between a profile and a
deployment is not automatically a defect in either.** This repository publishes
an artefact and a shared configuration layer; it cannot know what the estate
around either looks like. Today's server has an ingress on the host and Docker
Compose; tomorrow it could be a sidecar in Kubernetes. The edge is a Mac today
and could be a Linux desktop, an Orange Pi or a Jetson tomorrow.

The rule that follows: **a value that names a neighbour, a socket path, a port
belonging to another process, or a product is deployment data and belongs in
`${env:...}`**, with a comment saying what class of thing goes there. What stays
literal is what the file itself fixes — its own pipeline order, its own
component names.

**What this costs.** CI's validation and the integration test no longer testify
about one specific production system. Nothing here will now catch a consuming
repository drifting into a configuration that does not work; that repository
owns its own validation.

**What it does not cost.** They still prove that the two layers compose, and the
harness still proves end-to-end delivery through an authenticated hop with
redaction applied — neither of which depends on what the neighbours are called.
The clearest instance of why the old doctrine had to go: the gateway profile
used to dial `127.0.0.1:14317` and claim in a comment that *the traffic never
leaves the kernel*. The deployed gateway dials across a Docker bridge shared by
three Compose projects. The comment was not stale, it was false — and it was
only ever a claim about somebody else's deployment.

**If you add an `${env:...}` reference to any layer**, add a dummy value for it
to the `validate-config` job's `env:` block in the workflow. An unset variable
is a load error, not a default, so the omission fails CI at a point that reads
like a config bug.

### Bind address from the environment, port literal

Every address a listener binds is `${env:OTELBOX_<ROLE>_BIND_HOST}:<port>`, the
port written out. The split is the point: **a port is a convention this
repository owns and documents, while the choice between a loopback address and a
wildcard depends on whether the role runs as a container, a pod or a host
process — which the artefact cannot know.** Inside a container namespace
`127.0.0.1` is the container, so a loopback literal is not a security statement,
it is an assumption about a deployment.

Two consequences worth holding:

- **A self-scrape target follows the bind.** `prometheus/self` and
  `prometheus/host` scrape the collector's own metrics endpoint, so they take
  the same variable rather than a `127.0.0.1` literal. A literal there would
  have been an assumption about a value the file no longer fixes — the same
  class of mistake as the gateway comment above claiming its backend traffic
  never left the kernel.
- **Health endpoints are the one exception, and it is not an inconsistency.**
  `${env:OTELBOX_<ROLE>_HEALTH_ENDPOINT}` was already a whole `host:port`, and
  splitting it would move a port a supervisor already chooses into this
  repository's list of conventions — the opposite direction from every other
  change here.

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

**The unreleased version is the worked example, and it ratcheted.** Read it as
the rule being applied rather than as history: each step is a bump the previous
one absorbs.

1. The upstream bump `v0.156.0` → `v0.157.0` on its own is a **patch**: same
   component set, same base layer.
2. Five newly linked components and a third reference profile make it a
   **minor**, and minor absorbs the patch. That was `1.1.0`, and it was correct
   while it stood.
3. Retiring the mirror doctrine renamed the gateway's exporters and WALs,
   swapped the host agent's authenticator and turned a dozen literals into
   `${env:...}` references a consuming repository has to render. The
   health-check migration then unlinked `health_check` outright, so a consumer
   still naming it does not merely drift — its collector refuses to load.
   Collapsing the gateway's two OTLP receivers into one retired a receiver and a
   variable with it, and every listener's bind address became
   `${env:OTELBOX_<ROLE>_BIND_HOST}`. `config/base.yaml` still has not moved, but
   the role layers a consumer loads changed shape under it several times over,
   which is the **major** criterion as written. Hence `2.0.0` in the manifest.

The lesson the example teaches, and the reason it is kept rather than reduced to
its answer: the bump is decided by what a consumer has to do, not by how much
work went in. Step 2's five components cost more effort than step 3's collapsed
receiver and obliged a consumer to change nothing; the collapsed receiver
obliges every one of them to rewrite a receiver block. Ask what breaks if a
deployment upgrades the binary and leaves its configuration alone.

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

Linked is not enabled: `builder.yaml` currently declares 46 components (20
receivers, 10 processors, 3 exporters, 8 extensions, 5 connectors), of which any
given role config wires a handful. A linked component costs binary size only and
is dormant until a pipeline references it, which needs no rebuild. Four are
wired by nothing at all and deliberately so: `oidc` and `oauth2client` sit ahead
of an undecided delegated-token design, `groupbyattrs` and `cumulative_to_delta`
ahead of the aggregation chain under "The process scraper".

`prometheusreceiver` alone accounts for ~67 MB of the 264 MB `darwin/arm64`
binary — the same manifest without it built to 197 MB. It stays because all
three roles now scrape something with it. `journaldreceiver` is the mirror
case: Linux-only, wired only by the host agent, and carried unused on every
macOS workstation.

Its blind spot is why the next section exists: it proves the binary contains
components, not that anything reaches the far end.

### test/harness

```bash
go test -C test/harness . -count=1 -timeout 15m -v \
    -args -otelcol-binary "$PWD/_build/otelcol-otelbox"
```

Two tests. `TestEdgeToGatewayDelivery` stands up two processes of the binary in
the edge and gateway roles, wired over loopback across the same authenticated
hop the roles define, and asserts on what came out of the far end: delivery,
redaction, and — the one that matters — that an edge holding a token outside the
gateway's allowlist both drops the data and makes the drop visible in
`otelcol_exporter_send_failed_*`. A precondition checks that unauthenticated
ingest gets HTTP 401, so that assertion cannot pass vacuously.

`TestBackendCouplingUnderQueuePressure` settles, on a live run rather than by
reading source, the last of the Component facts below, against a runbook in
`remote_server_setup` that claims the opposite: with queue
headroom a stopped gateway backend leaves the healthy one untouched; once its
queue fills, `block_on_overflow: true` plus synchronous fan-out
(`internal/fanoutconsumer`) stalls the healthy backend too. A third subtest
confirms the withheld record arrives once the stopped backend returns — the same
non-vacuity role HTTP 401 plays for the token assertion above.

**The incident `TestEdgeToGatewayDelivery` exists for.** The deployed edge
failed every export for days with `Unauthenticated ... provided authorization
does not match expected scheme or token`, and nothing caught it: the process
ran, launchd reported it alive, the health endpoint answered 200, the OTLP
receiver accepted everything sent to it. Every one of those proves the local
half of the pipeline and none proves delivery, so a wrong credential looked
exactly like a healthy collector and the telemetry was simply gone. **Anyone
changing the authentication path — the `bearertokenauth` extension, the token
file format, the edge's `authorization` header, the receiver's `auth:` blocks —
should know this test is the thing standing between them and a repeat.** Do not
weaken it into a startup check.

Reading it before editing it repays the time: each assertion is a subtest, so
one failing does not abort the others — half the value of a failure is what the
*other* assertions did. There are no fixed sleeps; every wait is a bounded poll
that also watches the collector process, so one that dies on a config error is
reported in a second with its last log lines rather than after the full
timeout. The token file's trailing comment is a format assertion rather than
decoration.

### The sandbox trap

**Anything carrying `resource_detection` or `host_metrics` cannot start under
this environment's default command sandbox**, `TestEdgeToGatewayDelivery`
included — `TestBackendCouplingUnderQueuePressure` uses neither and runs under
it fine. Both read the boot time the sandbox denies: `resource_detection`'s
`system` detector, and **every one of the ten `host_metrics` scrapers**, in
`start()` rather than in `scrape()`, so the failure is at startup and no
scraper selection avoids it. The edge process dies with

```
Error: cannot start pipelines: failed to start "resource_detection" processor:
failed getting OS version: OSVersion failed to get os version: getting boot
time: operation not permitted
```

which reads like a configuration fault and is not one. Nothing in `config/` is
wrong when this happens. Run outside the sandbox; CI runners are unaffected.
`validate` and `smoke-check.sh` are fine inside it — nothing is started.

### `journald` can never run in the gateway image

`journaldreceiver` does not read the journal: it execs the `journalctl` binary
and parses its output. The gateway image is `FROM scratch`, so there is no
binary to exec and no shell to find one — the receiver is usable in the
`host-agent` role and nowhere else.

This is permanent, not a gap waiting on upstream. Both routes out were closed
as `not_planned`: a Go-native journal reader (contrib #32711) and shipping
`journalctl` inside the contrib image (opentelemetry-collector-releases #462).

### The `process` scraper

**Not enabled anywhere, and the naive fix is wrong in a way that fails
silently.** Recorded here because the reasoning is not recoverable from the
absent config.

The cardinality. The server forks 4.2 times a second with `pid_max` at
4194304, so PIDs are effectively never reused and every process the scraper
catches becomes a permanent new series. `process.pid`, `process.command_line`
and `process.parent_pid` are *resource* attributes enabled by default, and
`process.command_line` carries argv, which routinely contains credentials.

**Deleting `process.pid` to collapse the series is not a fix.** It produces
many datapoints sharing one identity in one collection window, which the
OpenTelemetry data model calls a Single-Writer violation and which makes
cumulative sums unusable rather than merely inaccurate. The correct route is
aggregation, and it needs a four-stage chain: `transform` to copy the resource
attributes worth keeping down onto the datapoints, `transform` to delete the
unbounded resource attributes, `groupbyattrs` with an empty `keys` list to
compact what is left into one resource, then `transform` with
`aggregate_on_attributes` to sum. Monotonic cumulative metrics must pass
`cumulative_to_delta` **before** the resource identity is destroyed, or the
aggregate saw-tooths downward every time a process exits. That is why those two
processors are linked while nothing wires them.

The cost model, for whoever revisits this. Steady series ≈ live processes × 7
datapoints (`process.cpu.time` carries a 3-valued `state`, `process.disk.io` a
2-valued `direction`, plus two memory metrics). New series per day ≈
E(T) × 86400 / T, where E(T) is the number of processes younger than the scrape
interval alive at an instant. E(T) is directly measurable and is bounded above
by fork-rate × T, which at a 60 s interval spans three orders of magnitude
between the plausible extremes — **measure it before choosing an interval**
rather than estimating an average process lifetime.

And the privilege question comes before the cardinality question. A ptrace
access-mode check gates `/proc/<pid>/io` and `/proc/<pid>/exe`, so an
unprivileged agent gets complete data for its own two or three processes and
permission errors for everything that matters. `mute_process_io_error` and its
siblings only make that look healthy.

### `host_metrics` filter semantics

Recorded once here rather than repeated in three profiles. All verified against
the v0.157.0 scraper source and gopsutil v4.26.6.

- **Includes and excludes are ANDed, per dimension.** `includePartition` is
  `includeDevice && includeFSType && includeMountPoint`, and each of those is
  `(no include filter || it matches) && (no exclude filter || it does not
  match)`. A strict `include_mount_points` allowlist therefore makes every
  exclude list beside it dead code — which is why the edge profile carries an
  allowlist of `/` and no excludes at all.
- **`match_type` has no default.** `filterset.CreateFilterSet` falls through to
  `unrecognized match_type: ''`, so a filter list without one is a load error,
  not a permissive default.
- **Regexp patterns are not anchored**, contradicting the doc comment directly
  above the code: `FilterSet.Matches` calls `r.MatchString(toMatch)`, which is a
  substring search, while the comment claims the string "must be fully matched".
  Anchor explicitly.
- **`include_virtual_filesystems: false` is a no-op on darwin.** On Linux it
  keeps only filesystems listed in `/proc/filesystems` without the `nodev`
  prefix (plus zfs), which is how `overlay` disappears for free and why
  `squashfs` — `FS_REQUIRES_DEV` in the kernel — does not. On darwin gopsutil's
  `PartitionsWithContext` discards the parameter outright.
- **Series identity is the attribute set, and it is not the same for the two
  scrapers.** `filesystem` records `device`, `mode`, `mountpoint` and `type`;
  `disk` records `device` alone. Anything that renumbers a device or moves a
  mount point creates a permanent new series, which is what makes loop devices,
  container overlays and removable volumes a cardinality problem rather than a
  tidiness one.

## Known state

- `builder.yaml` declares `2.0.0`, built on upstream `v0.157.0`. **Not yet
  released** — the published version is still `v1.0.0` (2026-08-01, upstream
  `v0.156.0`), and `Formula/otelcol-otelbox.rb` carries the checksums CI wrote
  for it in `94e2b59`. Both digests and the version line stay CI's to write, so
  the formula stays at 1.0.0 until a publishing run rewrites it.
- **The gateway has one OTLP receiver and one allowlist.** `otlp/public` and
  `otlp/local` were merged into `otlp`, `bearertokenauth/local` and
  `OTELBOX_GATEWAY_LOCAL_TOKEN_FILE` are gone, and 14321/14322 are free. Two
  credential sets bought separation of revocation and nothing else — both
  receivers already fed the same pipelines, the same WALs and the same queues —
  and the capability the profile no longer demonstrates is recorded under
  Component facts.
- **Every listener binds `${env:OTELBOX_<ROLE>_BIND_HOST}`**, port literal, in
  all three profiles; see "Bind address from the environment, port literal".
  Three new variables, one per role, and the CI overlays replace every reference
  to them so the harness supplies none.
- **32 MiB on both transports of every OTLP receiver**, `max_recv_msg_size_mib`
  on gRPC and `max_request_body_size: 33554432` on HTTP. The HTTP half is the
  point: unset, it takes `confighttp`'s 20 MiB default, so raising gRPC alone
  would have made HTTP the tighter limit with a different status code and a
  different metric.
- **Every sender bounds its outgoing payload in bytes.** The edge's
  `sending_queue.batch.max_size` went 3 → 8 MiB, and the host agent gained a
  `batch:` block it did not have. A record larger than the sender's `max_size`
  is dropped by the sender rather than split, so the invariant is
  `batch.max_size` < receiver `max_recv_msg_size_mib` ≤ `max_request_body_size`.
  The gateway's backend exporters stay at 3 MiB: their peer is a backend whose
  receive limit this repository does not own.
- **The edge runs `service::telemetry::metrics::level: detailed`**, overriding
  the base layer's `normal`, so `error_type` and `error_permanent` survive on
  `otelcol_exporter_send_failed_*`. The host agent is deliberately still at
  `normal`; see "Still outstanding".
- **Two `host_metrics` defaults moved at v0.157.0**, and both profiles take the
  new ones: the per-core `cpu` attribute on `system.cpu.time` and
  `system.cpu.utilization` became opt-in (#49161) and is left off, and
  `system.cpu.logical.count` became enabled by default (#49325) and is left on.
  Anything already dashboarding CPU time by core loses that breakdown.
- **The `batchprocessor` deprecation horizon.** Upstream's accepted
  `docs/rfcs/batching-migration.md` puts a deprecation warning at v0.158.0
  ("tentatively"), removal from the default distribution and the feature gate at
  Beta at v0.164.0 — still available to custom builds, which this is — and the
  gate Stable and removed at v0.170.0. `queuebatchprocessor` is the replacement,
  with `wait_for_result` and `block_on_overflow` defaulted true so migration
  preserves backpressure. What this repository has to do about it is under
  Known defects.
- **The profiles are no longer mirrors**, and every value that named a
  neighbour, a socket or a product has become an `${env:...}` reference. The
  gateway's two backends are now `otlp_grpc/backend_1` and `otlp_grpc/backend_2`
  with `file_storage/backend_{1,2}` beside them; the host agent authenticates
  with `headers_setter` rather than `bearertokenauth`. See "The reference
  profiles are demonstrations, not mirrors".
- **All three roles now wire `healthcheckv2`, and `health_check` is unlinked.**
  Extensions went 9 → 8 and the component total 47 → 46. Each profile serves
  `/status` on `${env:OTELBOX_<ROLE>_HEALTH_ENDPOINT}` with
  `include_permanent_errors: true`, no gRPC responder and the `/config` dump
  off — that endpoint would serve the merged configuration with every
  `${env:...}` expanded, tokens included. What the setting does and does not
  catch is under Component facts; read it before believing the endpoint proves
  anything about delivery.

### Component facts

Capabilities of the components, not claims about any deployment. All verified
against upstream at v0.157.0 (grpc-go v1.82.0, otelgrpc v0.69.0, `configauth`
and `configtls` v1.63.0); the first two and the two health-check entries
additionally on a live run of the binary.

- **`bearertokenauth`'s client credential requires transport security on
  gRPC.** `(*perRPCAuth).RequireTransportSecurity()` returns `true`
  unconditionally, and grpc-go's `validateTransportCredentials` refuses at
  `grpc.NewClient` — which the collector reaches from the exporter's `start`,
  so the process fails to start rather than dropping RPCs. The error is
  `grpc: the credentials require transport level security`. Over plaintext
  **HTTP** it is fine: the `RoundTripper` path never consults transport
  security. **The check only engages when the exporter attaches per-RPC
  credentials, which happens only via `auth:`** — a static `authorization`
  header, as the edge uses, attaches none — so nothing would have stopped that
  leg running unverified, and it no longer does: `test/config/edge-ci.yaml`
  supplies a CA instead of disabling verification.
- **`headers_setter` does not require it on either transport**, returning
  `false` unless `additional_auth:` chains it to something that returns `true`.
  Both extensions read a credential file through the same fsnotify-backed
  `extension/internal/credentialsfile` watcher, so rotation without a restart
  survives the swap. The file formats differ and nothing warns: `filename:` is
  scanned line by line with everything after the first whitespace treated as a
  comment and the scheme prepended, while `value_file:` is `TrimSpace`d whole
  and prepends nothing — so it must contain literally `Bearer <token>`, and a
  trailing comment becomes part of the header value.
- **`insecure: true` and `insecure_skip_verify: true` are not degrees of the
  same thing.** grpc-go's check is `transportCreds.Info().SecurityProtocol ==
  "insecure"`; a `credentials.NewTLS` credential reports `"tls"`, so
  `insecure_skip_verify` passes the check while validating no certificate at
  all. A rule that bans the word `insecure` invites the strictly worse setting.
  Also: **`insecure: true` combined with a `ca_file` silently yields working
  TLS** — `LoadTLSConfig` short-circuits only when there is no CA, so the
  `insecure` is ignored, the connection is verified against that CA, and against
  a plaintext listener it fails at handshake rather than at load. Nothing warns
  in either direction, and `ClientConfig` has no `Validate` override.
- **`reload_interval` reloads only the component's own leaf certificate** —
  `loadCertificate()` reads `cert_file` and `key_file` and nothing else, lazily
  on the next handshake. A client's `ca_file` is read once, inside the single
  `LoadTLSConfig` call the exporter's `start` makes, so **rotating a CA restarts
  every client.** The one hot-reloadable CA is server-side and a different knob:
  `client_ca_file` plus `client_ca_file_reload`, which watches the file.
- **There is no client-certificate-identity authenticator in contrib at
  v0.157.0**, and the gap is structural rather than an omission:
  `extensionauth.Server.Authenticate` takes only `map[string][]string`, and
  `client.Info` carries no `tls.ConnectionState`. `client_ca_file` enforces mTLS
  but yields no identity to check an allowlist against, so mTLS authenticates
  the issuing CA and cannot replace a token allowlist.
- **An expired certificate on an outbound leg fills that leg's queue**, and
  where `block_on_overflow: true` meets synchronous fan-out
  (`internal/fanoutconsumer`) a full queue stalls the healthy backend and then
  ingest — the behaviour `TestBackendCouplingUnderQueuePressure` already proves
  on a live run. That is a consequence of how the exporter and the fan-out
  consumer are built, not of any particular deployment. **Whether to enable TLS
  on a given leg is the deploying repository's decision**; this repository only
  records that whoever enables it needs expiry alerting first.
- **`healthcheckv2` reports component lifecycle, never delivery — and that is
  the whole of it.** The extension aggregates `componentstatus` events. Nothing
  emits one for a failed export: `exporterhelper` (v0.157.0), the `exporter`
  module (v1.63.0) and `scraperhelper` contain no reference to `componentstatus`
  at all, and of everything the three profiles wire only `otlpreceiver` and
  `prometheusreceiver` report anything at runtime — a `FatalError` when their
  listener dies, which already answers 500 without `component_health`. Every
  `PermanentError` in `service/internal/graph` and `service/extensions` is a
  component's `Start` or `Shutdown` failing. **So
  `include_permanent_errors: true` does not turn the endpoint into a delivery
  check**, and the profiles set it for what it does mean rather than for that.
  This is the incident restated as a property of the code:
  `TestEdgeToGatewayDelivery` depends on it, polling the edge healthy *after*
  giving it a token the gateway rejects.
- **The gRPC responder is off only while its key is absent.** `Config.Unmarshal`
  nils `GRPCConfig` unless `conf.IsSet("grpc")`, and `NewHealthCheckExtension`
  builds the server only when it is non-nil — so the `localhost:13132` default
  the README documents never applies to a config that omits the block. There is
  no `enabled: false`: writing `grpc:` bare opens the port, confirmed on a live
  run. `use_v2: true` is likewise still required at v0.157.0; without it the
  extension serves the v1 legacy responder, which registers `/` on a
  `http.ServeMux` and therefore answers 200 on `/status` too — a probe cannot
  detect the omission.
- **A receiver carries exactly one authenticator.** `configauth.Config` is a
  single `AuthenticatorID component.ID`, not a list, so a deployment that
  genuinely needs two independent credential sets needs two receivers. The
  gateway profile used to demonstrate that and no longer does — one allowlist
  now holds every client's token — so it is a documented option rather than a
  shipped example. What the second set bought was separation of revocation, and
  nothing else: both receivers already fed the same pipelines, the same WALs and
  the same queues.
- **Message size limits bind the uncompressed payload, on both transports.**
  grpc-go checks the length prefix against `MaxRecvMsgSize` and checks again
  after decompression against the same number; `confighttp` wraps the body in
  `http.MaxBytesReader` and wraps the *decompressed* stream in a second one. So
  `compression: gzip` buys no headroom either way. `max_request_body_size` is in
  bytes and defaults to 20 MiB when unset — which is why raising only the gRPC
  limit would silently make HTTP the tighter of the two.
- **The two transports fail differently, and the difference is the ordering.**
  On gRPC `recvAndDecompress` runs in `processUnaryRPC` *before* the interceptor
  chain, so the size check precedes authentication: any client that can open a
  socket can trip it, and the answer is `RESOURCE_EXHAUSTED`. On HTTP the
  handler chain is built in reverse, putting the auth interceptor outside the
  body-size interceptor, so authentication runs first and the answer is a 400 —
  the same 400 a malformed payload gets.
- **`otelcol_receiver_refused_*` does not count protocol-level rejections.** It
  means the *consumer chain* returned an error — `memory_limiter` back-pressure,
  a full queue with `block_on_overflow: false`, a permanent downstream error —
  because `receiverhelper`'s `endOp` is only reached once the handler has run.
  An oversized message never reaches the handler, so it moves nothing in
  `otelcol_receiver_*` at all.
- **No record-level counter for oversized payloads can exist.** grpc-go rejects
  on the length prefix before the body is allocated, let alone decoded, so the
  collector never learns how many records were inside.
- **What can be counted instead, and its blind spots.** On the gateway at
  `detailed`, `rpc_server_call_duration_count{rpc_response_status_code=
  "RESOURCE_EXHAUSTED"}` counts *requests* — but otelgrpc adds `server.address`
  only on the client side (`stats_handler.go` marks the server half a TODO), so
  a collector running two gRPC receivers could not tell them apart in that
  series. On the sender at `detailed`,
  `otelcol_exporter_send_failed_*{error_type="ResourceExhausted",
  error_permanent="true"}` counts records. On HTTP the nearest signal is the 400
  on the route, conflated with malformed payloads. All three series need
  `detailed`, which is one more reason the edge no longer runs `normal`.
- **Which errors are retried, and what that means for the WAL.** `otlpexporter`
  retries `Canceled`, `DeadlineExceeded`, `Aborted`, `OutOfRange`, `Unavailable`
  and `DataLoss`, plus `ResourceExhausted` when the server supplies `RetryInfo`.
  Everything else becomes `consumererror.NewPermanent`: dropped, dequeued, gone.
  **State the consequence plainly, because it reframes the incident.**
  `Unauthenticated` is permanent, so during that outage nothing was ever parked
  in the WAL — the data was not delayed, it was discarded on arrival, and fixing
  the token afterwards could not have replayed it. The on-disk queue survives an
  unreachable peer, not a peer that answers "no". There is no dead-letter
  mechanism.
- **`sending_queue` without a `batch:` key has no batcher at all.**
  `queuebatch.Config.Batch` is `configoptional.Default(...)`, and `Default` is
  not `Some`: `HasValue()` stays false unless the key is present in the
  configuration, so the documented default of 8192 items applies only to a
  `batch:` written out. Either way the outgoing payload is unbounded in bytes —
  the default carries a minimum in items and no maximum — which is why every
  sender here states `sizer: bytes` and a `max_size`. Verified by unmarshalling
  the real `Optional[QueueBatchConfig]` shape at v0.157.0.
- **Splitting backends across pipelines would not decouple them.** Fan-out is
  synchronous on the caller's goroutine, so a blocked enqueue on one exporter
  stalls everything queued behind it however the pipelines are drawn, and an
  exporter named in two pipelines is one instance with one queue. The only real
  knobs are the overflow policy and queue sizing. This settles a
  `remote_server_setup` runbook that claims the opposite.

## Known defects

Each with its owner. Nothing here is speculative — they are known-broken or
known-missing, and where the fix belongs is stated.

**In this repository:**

- **Double batching in every role**: a `batch` processor *and*
  `sending_queue::batch` on the exporter. The RFC above names exactly this
  ("silent double batching") as a Phase 2 risk and adds a startup warning at
  v0.158.0, so this goes noisy on the next upstream bump. Redundant rather than
  harmful today, and the RFC notes the processor form uses less memory when one
  pipeline fans out to several exporters, which is the gateway's situation.

**In `remote_server_setup`:**

- **The host agent's exporter cannot start as the template writes it**:
  `auth: bearertokenauth` plus `tls: insecure: true` on a gRPC exporter, which
  passes `validate` and then fails at start (see Component facts). The fix this
  repository now demonstrates is `headers_setter` with `value_file:`; adopting
  it means the credential file must hold `Bearer <token>` rather than a bare
  token, which is why `config/examples/host-agent.yaml` renamed the variable to
  `OTELBOX_HOST_AGENT_AUTH_HEADER_FILE` rather than changing a file's format
  under the same name.
- **The journald receiver has no storage extension**, so it gets a no-op
  persister whose `Get` returns nil and keeps no cursor at all: every
  `journalctl` respawn either loses records or replays the whole journal.
- **Deprecated spellings** `hostmetrics` and `resourcedetection`. They still
  load; the rename is one edit each.

**In both consuming repositories:** the 1.0.0 adoption is written and
**uncommitted**. `remote_server_setup` has the GHCR image pinned by digest, both
config layers and `files/base.yaml` copied from `config/base.yaml`.
`devbox-setup` has the release-asset download, both config layers, the launchd
supervision, deletion of the old `otelcol-edge/` build pipeline, the
`otlp` → `otlp_grpc` and `resourcedetection` → `resource_detection` renames, and
migration logic for machines on the old layout.

**Unowned:** the edge authentication incident was never diagnosed server-side.
The gateway's token-file format is not the cause — upstream parses that file
line by line and treats text after the first whitespace as a comment, which is
exactly what the consuming repository renders. The mismatch is in the credential
value or in which allowlist the running gateway has. Diagnosis needs server
access, which `remote_server_setup`'s own agent rules forbid without a separate,
explicit request.

## Still outstanding

In order.

1. **`test/harness` has not followed the collapsed gateway receiver, and
   `TestBackendCouplingUnderQueuePressure` fails until it does.** Two changes,
   both in Go and both outside this document's reach:
   `main_test.go:32` has `couplingGatewayHTTPPort = 34352`, the port the retired
   `otlp/local` held — it must become `34348`, which is where
   `test/config/gateway-coupling-ci.yaml` now puts the one receiver's HTTP
   listener. And `coupling_test.go` posts with `localToken` read from
   `localTokenFile`; there is one allowlist now, so the scenario must present a
   token written into `OTELBOX_GATEWAY_TOKEN_FILE`. Nothing else moves: no new
   environment variable, and `TestEdgeToGatewayDelivery` still uses 34327/34328
   and the ingest token it already writes.
2. **Decide the host agent's telemetry level.** The edge and the gateway run
   `detailed`; the host agent is still on the base layer's `normal`, and that is
   a decision to take on data rather than by symmetry. What `normal` costs is
   known — the otelgrpc and otelhttp meters, and `error_type` and
   `error_permanent` on `otelcol_exporter_send_failed_*`. What `detailed` costs
   on a host scraping ten `host_metrics` scrapers, container stats and two
   neighbours is not, and that is the number to get before changing it.
3. **Publish.** `builder.yaml` says `2.0.0` and the reasoning is settled under
   "Version and release flow". Merge to `master` and let CI publish — nothing
   here is released until it does.
4. **`devbox-setup`**: commit the staged 1.0.0 adoption, then take the new
   release's edge profile, which needs `OTELBOX_EDGE_BIND_HOST`,
   `OTELBOX_EDGE_PROBE_URL`, `OTELBOX_EDGE_NTP_ENDPOINT` and
   `OTELBOX_EDGE_HEALTH_ENDPOINT` rendered from the launchd agent — and, if
   anything probes the edge, the health path moving from `/` to `/status`.
5. **`remote_server_setup`**: commit the staged 1.0.0 adoption, then take the
   new release — the gateway's renamed exporters, its two OTLP receivers
   collapsed into one (the host agent's export moves to the remaining listener
   and its token joins the one allowlist), and the eight variables the profile
   now expects, `OTELBOX_HOST_AGENT_BIND_HOST` and
   `OTELBOX_HOST_AGENT_HEALTH_ENDPOINT` for the host agent, and in
   `roles/telemetry_source` the three edits under Known defects plus
   `telemetry_source_journal_enabled: true`, now the receiver exists.

## Conventions

- **Comments answer "why", in one or two sentences.** Every YAML/bash file
  carries a short header, and a setting carries a line only where the reason is
  not obvious from the value — several are the only record of a decision or a
  trap. Update them with the code rather than stripping them, but do not restate
  what the line already says, and do not let one grow into a paragraph: the
  rewrite that cut this repository's comments by half deleted no reasons.
- `dist.version` in `builder.yaml` is deliberately **unquoted**: CI reads it with
  `awk`, not a YAML parser, and would otherwise keep the quotes.
- Ports, meaning *listened on* — and only the port: the address beside every one
  of them is `${env:OTELBOX_<ROLE>_BIND_HOST}`. The edge holds 4317/4318/8888,
  the gateway 14319/14320 and 8889, the host agent 8890 alone, and everything in
  `test/` sits in a 34xxx block chosen to miss all three — so a developer running
  their own edge can still run the test. 14321 and 14322 were freed when the
  gateway's two OTLP receivers collapsed into one; leave them free rather than
  reusing them, so a stale configuration binds nothing instead of binding
  something else. The backends' own OTLP ports are not in this list and never
  were: they are dialled outbound, they belong to whatever binds them, and since
  the profiles stopped naming them they are `${env:...}` values this repository
  holds no opinion about. The gateway takes 8889 and the host agent 8890 on the
  assumption that a host running all three has already spent 8888.
- **Health-check ports are no longer among them**, for the same reason the
  backends never were: each role takes its bind address from
  `${env:OTELBOX_<ROLE>_HEALTH_ENDPOINT}`, so a supervisor decides it. The
  conventional values, and the dummies CI validates with, are 13133 for the
  edge, 14323 for the gateway and 14324 for the host agent — the last two
  extending the server's block rather than sharing upstream's 13133, since the
  gateway and the host agent run on one host. `test/` pins 34133, 34134 and
  34135.
- There is no `insecure: true` anywhere under `test/`. The harness mints a
  throwaway CA and a leaf carrying `127.0.0.1` as an IP SAN, so the integration
  test runs the same `insecure: false` a deployment does. Keep it that way: the
  credential on that leg is a bare bearer token in a header, so an unverified
  peer is handed it.
  The permissive `tls` blocks in the gateway and host-agent profiles are a
  different case — they are defaults a deployment is expected to decide on, not
  claims that the traffic stays local, and their comments say so.
- A profile may not assert anything about the estate around it. Endpoints,
  socket paths, credentials, scrape targets, unit names and every bind address
  are `${env:...}`; what a file fixes for itself — its own port numbers, its own
  pipeline order — stays literal.
- British English in all file contents, comments, and commit messages.
