# Operating the gateway role

This is the artefact-level contract for `otelcol-otelbox` run as **gateway**:
the environment it needs, the ports it opens, the shape of its ingest
allowlist, how to compose and validate its configuration, and how to prove
telemetry reached both backends. It does not cover Docker Compose, the
reverse proxy terminating TLS in front of it, Ansible Vault, or the backend
bootstrap (SigNoz/ClickStack) — those belong to `remote_server_setup` and
stay there. See `config/examples/gateway.yaml` for the reference profile
these facts are drawn from.

## Required environment

| Variable | Used for |
|---|---|
| `OTELBOX_GATEWAY_TOKEN_FILE` | Path to the `bearertokenauth/ingest` allowlist file (see below). |
| `OTELBOX_GATEWAY_STORAGE` | Root of the two per-backend `file_storage` WALs (`$OTELBOX_GATEWAY_STORAGE/{signoz,clickstack}` plus their compaction directories). |
| `CLICKSTACK_INGESTION_API_KEY` | Sent as the raw `authorization` header value on the `otlp_grpc/clickstack` exporter — not templated as `Bearer ...`; ClickStack expects the key bare. |

All three are load errors, not defaults, if unset.

## Ports and endpoints

| Endpoint | Purpose |
|---|---|
| `127.0.0.1:14319` (gRPC) / `127.0.0.1:14320` (HTTP) | `otlp/public` — authenticated ingest for remote edges. Max 8 MiB per message. Loopback-bound even though this is the public path: a reverse proxy in front terminates TLS and forwards here, so authentication — not the socket — is the trust boundary. |
| `127.0.0.1:14321` (gRPC) / `127.0.0.1:14322` (HTTP) | `otlp/local` — unauthenticated, for workloads co-located on the same host with no token to present. Reachability is the only control, hence loopback. |
| `127.0.0.1:14317` | Not opened by the gateway — the SigNoz backend's OTLP endpoint that the `otlp_grpc/signoz` exporter dials outbound. Listed here only because AGENTS.md reserves the whole 14317–14322 block for this role; the backend container that binds it is `remote_server_setup`'s. |
| `127.0.0.1:8889/metrics` | The collector's own Prometheus scrape endpoint. Overrides the base layer's `8888` (taken on this host) and runs at `detailed` level, because the overflow alerting in Verifying delivery below is built on per-queue, per-exporter series that `normal` does not expose. |

The reference profile also scrapes `docker_stats` and a reverse proxy's admin
endpoint into `metrics/self`; which proxy is running, and its admin port, are
deployment choices owned by `remote_server_setup`.

`otlp/local` is not a lower-privilege side entrance: `otlp/public` and
`otlp/local` are wired into the same three pipelines
(`config/examples/gateway.yaml:234,238,242`), so a co-located workload posting
to 14321/14322 shares the `memory_limiter` budget, both `file_storage` WALs
and both sending queues with authenticated remote edges. Loopback reachability
is the only property distinguishing the two receivers — nothing rate-limits or
bounds the local path separately. A local flood that fills a queue therefore
back-pressures every producer behind it, edges included, for the reason given
under Verifying delivery below.

## Compose and validate

```console
otelcol-otelbox --config config/base.yaml --config <gateway-role-config>.yaml
```

`<gateway-role-config>.yaml` is `config/examples/gateway.yaml` here, or the
authoritative copy `remote_server_setup` renders
(`roles/otel_gateway/files/config.yaml`). The gateway role restates all three
`memory_limiter` keys and all three `batch` keys over the base layer's
workstation-sized values — map keys merge key by key, so an omitted key would
silently keep the edge value — and leaves `redaction/secrets` unrestated,
taking the base layer's patterns verbatim.

## The ingest allowlist

`OTELBOX_GATEWAY_TOKEN_FILE` is read by upstream's
`bearertokenauthextension`: a `bufio.Scanner` over the file, `strings.Fields`
per line, first field taken as the token, everything after it as a trailing
comment. An incoming request's bearer token is compared against every entry
with `subtle.ConstantTimeCompare`. Consequences worth holding before editing
this file:

- **One token per source, however many sources there are.** The list has no
  cap. Do not share a token between machines — each independently managed
  edge gets its own line, which is also what makes revocation possible
  without affecting the rest.
- **The file is watched and re-read on change.** Adding or revoking a token
  takes effect without restarting the gateway.
- **A whole-line comment is a live token, not a comment.** `# managed by
  Ansible` on its own line parses as `parts[0] == "#"`, which the extension
  then accepts as a valid bearer token — the literal string `#`. An empty
  line is safe (`len(parts) == 0`), so blank-line separation is fine; a
  leading `#` is not a comment marker here.
- **The token's own label carries no telemetry.** Any `name` a deployment
  attaches to a token in its source-of-truth is bookkeeping for a human, not
  something the extension attaches to a record — nothing here stamps it onto
  a resource attribute or a metric dimension. Distinguishing which edge sent
  what is the edge's job, via `resource_detection` in the edge role layer;
  the gateway does not and cannot recover source identity from the token
  alone.

## Verifying delivery

The same warning as the edge applies with one more link in the chain: a
healthy-looking gateway proves the hop from the edge arrived, and proves
nothing about either backend. Query `127.0.0.1:8889/metrics`:

- `otelcol_receiver_accepted_*` on `otlp/public` — confirms the edge's export
  actually reached the gateway (the edge-side half of this check lives in
  `docs/edge.md`). A precondition worth checking once per deployment: a
  request with no bearer token, or an unrecognised one, against `14319` or
  `14320` must return HTTP 401 — if it does not, the allowlist is not being
  enforced.
- `otelcol_exporter_send_failed_*` on **both** `otlp_grpc/signoz` and
  `otlp_grpc/clickstack` — must be checked independently; a dashboard that
  only alerts on one exporter misses the other backend failing silently.
- `otelcol_exporter_queue_size` on both exporters — each queue is sized in
  bytes against its own 10 GiB `file_storage` cap (9 GiB queue ceiling, kept
  below the cap so the queue's own limit binds first rather than a storage
  write failure). The per-backend WAL buys an independent disk budget and an
  independent blast radius for a corrupted `bbolt` file — **not** independent
  liveness. Fan-out is synchronous on the caller's goroutine
  (`internal/fanoutconsumer`), and with `block_on_overflow: true` on both
  exporters a blocked enqueue on one stalls everything queued behind it,
  whatever the pipelines look like — splitting the backends across separate
  pipelines would not decouple them either. With headroom in both queues a
  stopped backend does not touch the healthy one; once its queue fills, the
  back-pressure reaches every producer behind it, edge included. A sustained
  rise on either queue is the leading indicator of that, well before
  `send_failed` moves.

The repository's end-to-end test exercises this whole chain, including the
precondition above and a token deliberately outside the allowlist, and
asserts on the exporter-side failure metrics rather than on anything that
only proves the local half of the pipeline.
