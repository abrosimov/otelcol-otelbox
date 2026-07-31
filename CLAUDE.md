# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Scope: the artefact, not the deployment

This repository builds one OCB collector binary that serves **two roles** — the
workstation `edge` and the server `gateway` — and publishes it. It owns nothing
else. No Ansible, no playbooks, no service units, no machine-local setup
scripts: those live in the consuming repositories (`devbox-setup` for the edge,
`remote_server_setup` for the gateway).

The content originated in `devbox-setup` (source commit `750d7a2`, copied not
moved) and initially mirrored that repository's paths. That mirroring has been
dropped: the deployment-specific parts were deleted here, and the manifest was
flattened to the repository root.

There is **no Go source**. The component set is declarative; the binary is
produced by the OpenTelemetry Collector Builder from `builder.yaml`.

## What the two roles share

```
edge     loopback OTLP → redaction → WAL → one remote gateway
gateway  authenticated OTLP → redaction → WAL per backend → N backends
```

The shared invariant: every outbound leg carries its own on-disk `file_storage`
queue, sized from above; TLS verification is never disabled; secrets arrive only
as environment variables injected by the supervisor. What differs between the
roles is the secret store (login keychain vs Ansible Vault) and the supervisor
(launchd vs Docker Compose) — both deployment concerns, not collector concerns.

## Version and release flow

`dist.version` in `builder.yaml` is the **single source of truth**. Scheme:
`<upstream>-custom-<n>`. Bump the upstream part in lockstep with every `gomod`
pin; bump only `-custom-<n>` when the component set changes at the same
upstream. Nothing else records the version.

Pushing to `master` with a change to `builder.yaml` triggers
`.github/workflows/otelcol-otelbox.yml`, which builds and publishes release
`otelcol-otelbox-v<version>`.

- **Never cut a tag by hand** — there is no tag trigger, and pushing one does
  nothing.
- The workflow is idempotent on release existence. To rebuild an existing
  version, delete the release first, then `gh workflow run otelcol-otelbox.yml`.
- CI installs OCB at the **bare upstream** part only (`ocb_version` =
  `${version%%-custom-*}`). `go install ...cmd/builder@v0.156.0-custom-2` 404s:
  `-custom-2` is a valid semver pre-release, so the failure surfaces as a
  missing revision, not a syntax error.
- A lockstep guard fails the build when the upstream part of `dist.version` does
  not equal the `otlpreceiver` gomod pin (the anchor: core, always present,
  moves with every bump).

## Commands

Build locally (native darwin/arm64, mirrors CI):

```bash
go install go.opentelemetry.io/collector/cmd/builder@v0.156.0   # bare upstream, no -custom-N
"$(go env GOPATH)/bin/builder" --config builder.yaml
./smoke-check.sh ./_build/otelcol-otelbox builder.yaml
```

`smoke-check.sh` compares per-kind component *counts* (not names) from the
manifest against the binary's own `components` output, and additionally asserts
`file_storage` and `redaction` by name — the two invariants the collector exists
for. Counts because a component reports its type, not its module path, and the
two do not map mechanically (`resourcedetectionprocessor` → `resource_detection`,
`otlpexporter` → `otlp_grpc`). Do not replace this with a hand-maintained name
table.

Linked is not enabled: `builder.yaml` currently declares 41 components (18
receivers, 8 processors, 3 exporters, 7 extensions, 5 connectors), of which any
given role config wires a handful. A linked component costs binary size only and
is dormant until a pipeline references it, which needs no rebuild.

## Agreed direction (not yet implemented)

- The server runs **our own OCI image** published to GHCR, keeping the existing
  Docker Compose deployment in `remote_server_setup` intact — only the `image:`
  reference, the config path in `command`, and the same path in that role's
  `validate` task change. The image digest is published as part of the release
  so the consuming repository can pin it without reading `builder.yaml` across a
  repository boundary.
- Build matrix: `darwin/arm64` plus `linux/amd64` (the server is x86-64 Ubuntu).
- A shared `config/base.yaml` is published from here and loaded as the first
  `--config`; role layers stay in the consuming repositories. Base carries the
  processors and self-telemetry, so the **redaction patterns cannot diverge**
  between workstation and server. Receivers, exporters, `file_storage`,
  `service.pipelines` and `service.extensions` belong to the role layers.
- `prometheusreceiver` must be added to `builder.yaml` before the gateway role
  can run on this binary — its `prometheus/self` scrape config needs it.
- Gateway queues keep `block_on_overflow: true` on every exporter. Fan-out in
  the collector is **synchronous on the caller's goroutine**
  (`internal/fanoutconsumer`), so splitting backends across pipelines does not
  decouple them; the only real knobs are the overflow policy and queue sizing.

## Known state

- No release matches the current manifest. `dist.version` is `0.156.0-custom-2`,
  while the only published release is the superseded `otelcol-edge-v0.156.0`
  (7 components, old artefact name).
- The deployed edge has been failing every export with `Unauthenticated ...
  provided authorization does not match expected scheme or token`. The gateway's
  token-file format is not the cause — upstream parses that file line by line
  and treats text after the first whitespace as a comment, which is exactly what
  the consuming repository renders. The mismatch is in the credential value or
  in which allowlist the running gateway has. Diagnosis needs server access,
  which `remote_server_setup`'s own agent rules forbid without a separate,
  explicit request.

## Conventions

- **Comments in these files are load-bearing.** Every YAML/bash file carries a
  header explaining *why* a setting is what it is. Preserve and update them
  rather than stripping them.
- `dist.version` in `builder.yaml` is deliberately **unquoted**: CI reads it with
  `awk`, not a YAML parser, and would otherwise keep the quotes.
- British English in all file contents, comments, and commit messages.
