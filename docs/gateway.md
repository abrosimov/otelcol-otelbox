# Operating the gateway role

This is the artefact-level contract for `otelcol-otelbox` run as **gateway**:
the environment it needs, the ports it opens, the shape of its ingest
allowlist, how to compose and validate its configuration, and how to prove
telemetry reached both backends. It does not cover any supervisor, ingress,
secret store or backend bootstrap — those belong to the deploying repository
and stay there. See `config/examples/gateway.yaml` for the reference profile
these facts are drawn from; it is a demonstration of the role, not a copy of
any deployment.

## Required environment

| Variable | Used for |
|---|---|
| `OTELBOX_GATEWAY_BIND_HOST` | Address every listener in this profile binds — the OTLP receiver and the self-metrics endpoint, and the address `prometheus/self` scrapes. Loopback or wildcard depending on whether the role runs in a container; the ports beside it are literal. |
| `OTELBOX_GATEWAY_TOKEN_FILE` | Path to the `bearertokenauth/ingest` allowlist file, holding one token per client — remote edges and the co-located host agent alike (see below). |
| `OTELBOX_GATEWAY_STORAGE` | Root of the per-backend `file_storage` WALs (`$OTELBOX_GATEWAY_STORAGE/backend_{1,2}` plus their compaction directories). |
| `OTELBOX_GATEWAY_DOCKER_ENDPOINT` | Docker API endpoint for `docker_stats` — a socket path or a read-only TCP proxy. Which one is the deployment's decision; the socket itself is equivalent to host root. |
| `OTELBOX_GATEWAY_HEALTH_ENDPOINT` | `host:port` the `healthcheckv2` extension binds. Conventionally `0.0.0.0:14323`: the image is `FROM scratch`, so a probe cannot run inside the container and has to reach it from outside. |
| `OTELBOX_GATEWAY_BACKEND_1_ENDPOINT` | `host:port` the first backend exporter dials. |
| `OTELBOX_GATEWAY_BACKEND_2_ENDPOINT` | `host:port` the second backend exporter dials. |
| `OTELBOX_GATEWAY_BACKEND_2_TOKEN` | Sent verbatim as the `authorization` header on the second backend exporter — no scheme is prepended, because some backends want a bare key rather than `Bearer <key>`. |

Every one is a load error, not a default, if unset.

## Ports and endpoints

| Endpoint | Purpose |
|---|---|
| `$OTELBOX_GATEWAY_BIND_HOST:14319` (gRPC) / `:14320` (HTTP) | `otlp` — authenticated ingest, for remote edges and host-local producers alike. Max 32 MiB per message on both transports. |
| `$OTELBOX_GATEWAY_BIND_HOST:8889/metrics` | The collector's own Prometheus scrape endpoint. Overrides the base layer's `8888` and runs at `detailed` level, because the overflow alerting in Verifying delivery below is built on per-queue, per-exporter series that `normal` does not expose. |
| `$OTELBOX_GATEWAY_HEALTH_ENDPOINT` (conventionally `:14323`) | `healthcheckv2`, serving `/status`. Unauthenticated, which is why the profile leaves the `/config` dump disabled — it would serve the merged configuration with the allowlist path and the second backend's token expanded. |

**The bind address is not the access control.** Inside a container namespace
`127.0.0.1` is the container, so a loopback bind says nothing about who can reach
the socket — it depends entirely on how the role is deployed, which this profile
cannot know, which is why it is a variable while the ports beside it are not.
Every request is checked against the allowlist regardless.

**One receiver, one allowlist.** An earlier profile carried a second receiver on
14321/14322 with an allowlist of its own for host-local producers; those ports
are now free. A deployment that genuinely needs two independent credential sets
still needs two receivers — `configauth.Config` holds one `AuthenticatorID`, not
a list — but this profile no longer demonstrates it, because the second
credential set bought separation of revocation and nothing else: both receivers
already fed the same pipelines, the same WALs and the same queues. That sharing
survives the collapse: a host-local flood that fills a queue back-pressures every
edge behind it, for the reason under Verifying delivery below.

The backends' own OTLP ports are not in this table. They are dialled outbound,
they belong to whatever binds them, and the profile names them only through
`OTELBOX_GATEWAY_BACKEND_{1,2}_ENDPOINT`.

**Message size, and where the limits bind.** 32 MiB on gRPC
(`max_recv_msg_size_mib: 32`) and the same number of bytes on HTTP
(`max_request_body_size: 33554432`), stated rather than left to `confighttp`'s
20 MiB default, which would silently make HTTP the tighter of the two. Both bind
the **uncompressed** size, so `compression: gzip` on a sender buys no headroom.
The two transports fail differently: on gRPC the check runs before
authentication, so any client that can open a socket can trip it and the answer
is `RESOURCE_EXHAUSTED`; on HTTP authentication runs first and the answer is a
400, indistinguishable from a malformed payload. Keep every sender's
`batch.max_size` below these — a record larger than its sender's maximum is
dropped by the sender, not split.

The profile also scrapes `docker_stats` into `metrics/self`. Whether a container
can reach a Docker API endpoint at all is a deployment question, which is why
the endpoint is a variable and why the host-agent role exists — see
`docs/host-agent.md`.

## Compose and validate

```console
otelcol-otelbox --config config/base.yaml --config <gateway-role-config>.yaml
```

`<gateway-role-config>.yaml` is `config/examples/gateway.yaml` here, or whatever
role layer the deployment renders. The gateway role restates all three
`memory_limiter` keys and all three `batch` keys over the base layer's
workstation-sized values — map keys merge key by key, so an omitted key would
silently keep the edge value — and leaves `redaction/secrets` unrestated,
taking the base layer's patterns verbatim.

## The ingest allowlist

`OTELBOX_GATEWAY_TOKEN_FILE` is read by upstream's `bearertokenauthextension`: a
`bufio.Scanner` over the file, `strings.Fields` per line, first field taken as
the token, everything after it as a trailing comment. An incoming request's
bearer token is compared against every entry with `subtle.ConstantTimeCompare`.
Consequences worth holding before editing this file:

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
nothing about either backend. Query the self-metrics endpoint on `8889`:

- `otelcol_receiver_accepted_*` on `otlp` — confirms the edge's export actually
  reached the gateway (the edge-side half of this check lives in
  `docs/edge.md`). A precondition worth checking once per deployment: a
  request with no bearer token, or an unrecognised one, against `14319` or
  `14320` must return HTTP 401 — if it does not, the allowlist is not being
  enforced. Note what the sibling counter does **not** cover:
  `otelcol_receiver_refused_*` means the consumer chain returned an error, so an
  oversized message — rejected before the handler runs — moves nothing in
  `otelcol_receiver_*` at all. See AGENTS.md "Component facts".
- `otelcol_exporter_send_failed_*` on **every** backend exporter
  (`otlp_grpc/backend_1`, `otlp_grpc/backend_2`, and any further one a
  deployment adds) — each must be checked independently; a dashboard that only
  alerts on one misses the other backend failing silently.
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
