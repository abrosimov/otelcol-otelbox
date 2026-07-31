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

This repository owns the **artefact**, not its deployment:

| Path | Role |
|------|------|
| `builder.yaml` | OCB manifest. `dist.version` is the single source of truth for the release version. |
| `smoke-check.sh` | Post-build check: per-kind component counts vs the built binary. |
| `.github/workflows/otelcol-otelbox.yml` | Build (darwin/arm64) + publish the `otelcol-otelbox-v<version>` release. |

There is no Go source here — the component set is declarative, and the binary is
produced by the OpenTelemetry Collector Builder.

Deployment lives in the consuming repositories: `devbox-setup` supervises the
edge role through launchd, and `remote_server_setup` supervises the gateway role
through Docker Compose. Neither the role configurations, nor the service units,
nor the machine-local setup scripts belong here.

## Components

Two different sets, and the distinction matters:

- **Linked** — everything in `builder.yaml`. Compiled into the binary, available
  to any config, costs binary size only. Currently 41: 18 receivers, 8
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

## Release flow

1. Edit `builder.yaml`. `dist.version` is `<upstream>-custom-<n>`: bump the
   upstream part in lockstep with the `gomod` pins, or bump only `-custom-<n>`
   when the component set changes at the same upstream. CI rejects a mismatch
   between the two.
2. Merge to master. The push triggers `.github/workflows/otelcol-otelbox.yml`
   (`on.push.paths: builder.yaml`) — **do not cut a tag by hand**, there is no
   tag trigger and pushing one does nothing. CI builds `darwin/arm64` via OCB,
   runs `smoke-check.sh`, and publishes `otelcol-otelbox_darwin_arm64` +
   `.sha256` to the `otelcol-otelbox-v<version>` release, which it creates. To
   rebuild an existing version, delete the release first, then
   `gh workflow run otelcol-otelbox.yml`.

   The OCB toolchain is installed at the **upstream** part of `dist.version`
   only: `-custom-<n>` names our component set and has no upstream tag, so
   `go install ...cmd/builder@v<dist.version>` would 404 (it parses as a valid
   semver pre-release, so the failure surfaces as a missing revision, not a
   syntax error).
3. The consuming repository pulls the published asset and reconciles its own
   deployment. **Ordering matters:** the release must exist before that run.

## Build locally

Native darwin/arm64, mirroring the CI steps:

```bash
go install go.opentelemetry.io/collector/cmd/builder@v0.156.0   # bare upstream, no -custom-N
"$(go env GOPATH)/bin/builder" --config builder.yaml
./smoke-check.sh ./_build/otelcol-otelbox builder.yaml
```
