# Operating the edge role

This is the artefact-level contract for `otelcol-otelbox` run as **edge**: the
environment it needs, the ports it opens, how to compose and validate its
configuration, and how to prove telemetry actually left the machine. It does
not cover how the edge is installed, supervised or granted its secrets on any
particular workstation — that is `devbox-setup`'s launchd agent and login
keychain, and it stays there. See `config/examples/edge.yaml` for the
reference profile these facts are drawn from.

## Required environment

| Variable | Used for |
|---|---|
| `OTELBOX_EDGE_STORAGE` | Root of the `file_storage` WAL directory (`$OTELBOX_EDGE_STORAGE/gateway` plus its compaction directory). |
| `OTELBOX_EDGE_ENDPOINT` | `host:port` of the remote gateway the `otlp_grpc/gateway` exporter dials. |
| `OTELBOX_EDGE_TOKEN` | Bearer token sent as `authorization: Bearer ${OTELBOX_EDGE_TOKEN}` on every export. Must be one of the tokens the target gateway's allowlist file carries — see `docs/gateway.md`. |

All three are load errors, not defaults, if unset — see AGENTS.md
"Configuration layering" for why an unset `${env:...}` reference must never be
allowed to fall through silently.

## Ports and endpoints

All four are loopback-only; the edge has no listener a network can reach, and
that is the whole of its access control (no auth on the receivers below).

| Endpoint | Purpose |
|---|---|
| `127.0.0.1:4317` (gRPC) / `127.0.0.1:4318` (HTTP) | `otlp` receiver — where local applications send telemetry. Max 8 MiB per message. |
| `127.0.0.1:13133` | `health_check` extension. Answers whether the process is up, nothing more — see Verifying delivery below. |
| `127.0.0.1:8888/metrics` | The collector's own Prometheus scrape endpoint (`service::telemetry::metrics`, base layer). |

## Compose and validate

Two layers, base first:

```console
otelcol-otelbox --config config/base.yaml --config <edge-role-config>.yaml
```

`<edge-role-config>.yaml` is `config/examples/edge.yaml` here, or the
authoritative rendered copy `devbox-setup` deploys. Validate a candidate
configuration against the built binary before rolling it out:

```console
OTELBOX_EDGE_STORAGE=/tmp/otelbox-edge \
OTELBOX_EDGE_ENDPOINT=127.0.0.1:4317 \
OTELBOX_EDGE_TOKEN=placeholder \
  otelcol-otelbox validate \
    --config config/base.yaml --config config/examples/edge.yaml
```

The edge role restates nothing from the base layer — it runs
`memory_limiter`, `batch` and `redaction/secrets` at the base layer's
workstation-sized values, and adds `resource_detection` (edge-only: it stamps
this machine's identity, which the gateway must not restamp onto relayed
telemetry).

## Verifying delivery

**A running process, a live `health_check` and an `otlp` receiver accepting
requests prove nothing about delivery.** This repository exists because a
deployed edge failed every export for days while all three stayed green: the
process ran, launchd reported it alive, `127.0.0.1:13133` answered 200, and
the receiver accepted everything handed to it — none of that touches the
network hop to the gateway. Query `127.0.0.1:8888/metrics` instead:

- `otelcol_exporter_send_failed_spans` / `_metric_points` / `_log_records` —
  non-zero means the gateway is rejecting or unreachable. During the incident
  above this counter was climbing for days; what failed was that nobody was
  looking at it and no alert was built on it.
- `otelcol_exporter_enqueue_failed_*` — the WAL write itself failed (disk full
  or a corrupted `bbolt` file). `block_on_overflow: true` means a full queue
  blocks the producer rather than returning a failure, so this metric should
  stay at zero from ordinary backlog and only moves when the storage layer
  itself is unhealthy.
- `otelcol_exporter_queue_size` — current WAL backlog. Should hover near
  zero when the gateway is reachable; a sustained rise is the leading
  indicator of the failure above, well before `send_failed` moves.

Confirming the other end received it means checking the gateway's own
`otelcol_receiver_accepted_*` on its self-metrics endpoint — see
`docs/gateway.md`. The repository's end-to-end test exercises this exact
path, including the case of a token outside the gateway's allowlist, and
asserts on `send_failed` rather than on anything local.
