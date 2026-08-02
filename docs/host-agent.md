# Operating the host-agent role

This is the artefact-level contract for `otelcol-otelbox` run as **host-agent**:
the environment it needs, the port it opens, what it is allowed to see, how to
compose and validate its configuration, and how to prove its telemetry reached
the gateway. It does not cover any service unit, system account, Docker socket
proxy or secret store — those belong to the deploying repository and stay
there. See `config/examples/host-agent.yaml` for the reference profile these
facts are drawn from; it is a demonstration of the role, not a copy of any
deployment.

The host agent is the same binary as the gateway, on the same machine, as a
plain host process rather than a container. That is the whole point of it: a
container cannot see the host's `/proc`, cannot read the journal, and cannot
reach a loopback-only admin endpoint, because inside a namespace `127.0.0.1` is
the container.

## Required environment

| Variable | Used for |
|---|---|
| `OTELBOX_HOST_AGENT_BIND_HOST` | Address the self-metrics endpoint binds, and the address `prometheus/host` scrapes it back on. Conventionally `127.0.0.1`; the port beside it is literal. |
| `OTELBOX_HOST_AGENT_AUTH_HEADER_FILE` | Path to a file holding the **complete** `authorization` header value — literally `Bearer <token>`. Read by `headers_setter/gateway`; see the format warning below. |
| `OTELBOX_HOST_AGENT_STORAGE` | Root of the `file_storage` directory holding the journald cursor (`$OTELBOX_HOST_AGENT_STORAGE/journald`). |
| `OTELBOX_HOST_AGENT_GATEWAY_ENDPOINT` | `host:port` of the gateway's `otlp` gRPC listener, conventionally `:14319`. The gateway has one receiver and one allowlist, so this agent's token is a line in the same file every edge's is. |
| `OTELBOX_HOST_AGENT_DOCKER_ENDPOINT` | Docker API endpoint for `docker_stats`. |
| `OTELBOX_HOST_AGENT_HEALTH_ENDPOINT` | `host:port` the `healthcheckv2` extension binds. Conventionally `127.0.0.1:14324` — this role shares a host with the gateway, so the two health endpoints cannot both take a default. |
| `OTELBOX_HOST_AGENT_SCRAPE_TARGET_1` / `_2` | Prometheus targets for neighbouring processes on this host. Which processes those are is the deployment's to say; the profile carries two to show the list is configuration. |
| `OTELBOX_HOST_AGENT_JOURNAL_UNIT_1` / `_2` | Journal units to follow, same reasoning. |

Every one is a load error, not a default, if unset.

## The authenticator, and the file format that comes with it

The exporter authenticates with **`headers_setter`, not `bearertokenauth`**, and
the swap is not cosmetic. `bearertokenauth`'s gRPC credential returns
`RequireTransportSecurity()` true unconditionally, so pairing it with a
plaintext leg passes `validate` and then fails the exporter's start with
`grpc: the credentials require transport level security`. `headers_setter`
returns false, which leaves the transport decision where it belongs — with the
deployment.

Three consequences, each of which fails silently:

- **No scheme is prepended.** `bearertokenauth`'s `filename:` holds a bare token
  and the extension adds `Bearer `. `value_file:` does not: the file must
  contain literally `Bearer <token>`.
- **The whole file is `TrimSpace`d**, not scanned line by line, so a trailing
  comment becomes part of the header value. The gateway's allowlist file works
  the opposite way, and the two are easy to confuse.
- **Do not chain it via `additional_auth:`** back to `bearertokenauth`.
  `RequireTransportSecurity` then delegates and returns true again.

`headers_setter` is alpha where `bearertokenauth` is beta. Both watch their file
through the same fsnotify-backed watcher, so rotation without a restart is
preserved. The gateway's receiving side keeps `bearertokenauth` and is
unaffected — the server path validates a header and never inspects the
transport.

## Ports and endpoints

| Endpoint | Purpose |
|---|---|
| `$OTELBOX_HOST_AGENT_BIND_HOST:8890/metrics` | The collector's own Prometheus scrape endpoint. 8888 belongs to the base layer's default reader and 8889 to the gateway, so a host running all three has spent both. Left at the base layer's `normal` level, unlike the other two roles — an open decision, see AGENTS.md "Still outstanding". |
| `$OTELBOX_HOST_AGENT_HEALTH_ENDPOINT` (conventionally `127.0.0.1:14324`) | `healthcheckv2`, serving `/status`. |

Those two are the only listeners this role opens. Everything else is outbound,
and every address comes from the environment: the gateway's `otlp` gRPC, a
Docker API endpoint, two neighbour scrape targets, and its own metrics endpoint
scraped back off itself.

## What it is allowed to see, and what it is not

- **A Docker API endpoint, and a read-only proxy is the point of the
  variable.** The raw socket is equivalent to host root; a proxy that answers a
  handful of read paths and refuses every write method is what the role is
  designed around. Which of the two a deployment offers, and whether the
  agent's account could open the socket directly at all, are the deployment's.
- **Selected journal units, never the whole journal.** The whole journal would
  carry every authentication attempt and every kernel message off this host
  through a pipeline built for application telemetry. The agent's own unit must
  not be in the list: if the supervisor captures its stdout into the journal,
  reading its own unit back makes every send produce the log line that produces
  the next send.
- **No `process` scraper.** `processes` (aggregate counts from `/proc/stat`,
  readable by any account) is enabled; `process` (per-process CPU, memory and
  I/O) is not, and muting its errors is not the fix. See AGENTS.md
  "The `process` scraper" for both halves of the argument — the ptrace
  access-mode check that makes an unprivileged agent's data near-empty, and the
  unbounded per-PID cardinality that makes the naive workaround worse than the
  omission.

## The journald cursor

`journald` execs the `journalctl` binary and tracks its position with a cursor
it persists through a storage extension. **Without one it gets a no-op
persister whose `Get` returns nil**, so no cursor is ever kept and every
`journalctl` respawn either loses records or replays the whole journal. The
reference profile therefore declares a `file_storage/journald` extension and
names it in the receiver's `storage:` key.

That is a cursor, not a sending queue, and the two decisions are independent.
The exporter's queue is in memory on purpose: this is the one hop in the system
that cannot lose data to a network, and the host metrics the agent would replay
after a restart are already stale, because they describe a machine as it was
minutes ago. Durability for telemetry that matters is the gateway's job.

## The sending queue's batcher is stated, not defaulted

The queue is sized in items and the batcher in bytes, as on the edge. The
batcher is written out rather than left to the default because **omitting
`batch:` entirely leaves no batcher at all** — the default `BatchConfig` is only
applied when the key is present — so the outgoing payload would be unbounded in
bytes, and a large scrape could exceed the gateway's receive limit. A record
above `max_size` is dropped by the sender rather than split
(`one … size is greater than max size, dropping items`), so the invariant to
keep is `batch.max_size` < the gateway's `max_recv_msg_size_mib` ≤ its
`max_request_body_size`: 8 MiB against 32 MiB here.

## Compose and validate

```console
otelcol-otelbox --config config/base.yaml --config <host-agent-role-config>.yaml
```

```console
OTELBOX_HOST_AGENT_BIND_HOST=127.0.0.1 \
OTELBOX_HOST_AGENT_STORAGE=/tmp/otelbox-host-agent \
OTELBOX_HOST_AGENT_AUTH_HEADER_FILE=/tmp/otelbox-host-agent.header \
OTELBOX_HOST_AGENT_GATEWAY_ENDPOINT=127.0.0.1:14319 \
OTELBOX_HOST_AGENT_DOCKER_ENDPOINT=tcp://127.0.0.1:2375 \
OTELBOX_HOST_AGENT_HEALTH_ENDPOINT=127.0.0.1:14324 \
OTELBOX_HOST_AGENT_SCRAPE_TARGET_1=127.0.0.1:19001 \
OTELBOX_HOST_AGENT_SCRAPE_TARGET_2=127.0.0.1:19002 \
OTELBOX_HOST_AGENT_JOURNAL_UNIT_1=ssh.service \
OTELBOX_HOST_AGENT_JOURNAL_UNIT_2=cron.service \
  otelcol-otelbox validate \
    --config config/base.yaml --config config/examples/host-agent.yaml
```

The role restates nothing from the base layer — it runs `memory_limiter`,
`batch` and `redaction/secrets` at the base layer's values, and adds
`resource_detection/system`. That processor is not optional here: the
`host_metrics` receiver sets no resource attributes of its own, so without it
every host metric arrives at both backends with no statement of which machine
produced it.

`validate` parses and starts nothing, which is why it works under a restricted
command sandbox where the agent itself will not start: `resource_detection` and
every `host_metrics` scraper read the boot time in `start()`.

## Verifying delivery

Query the self-metrics endpoint on `8890`. The same warning as the other two
roles applies:
a running unit, a scraping receiver and a green `healthcheckv2` prove the local
half and nothing about delivery — the health extension reports component
lifecycle, and a rejected or dropped export is not one.

- `otelcol_exporter_send_failed_*` — non-zero means the gateway is rejecting or
  unreachable.
- `otelcol_exporter_enqueue_failed_*` — **this one is expected to move under
  load, unlike the other roles.** The queue is in memory with
  `block_on_overflow: false`, so overflow is a drop rather than backpressure —
  deliberately, because blocking would stall the scrapers and turn one outage
  into missing metrics for the recovery period as well.
- `otelcol_receiver_accepted_*` on the gateway's own `8889` endpoint — the
  other end of the same hop, which is what actually confirms arrival. The
  gateway has one receiver now, so this agent's traffic is not distinguishable
  there from an edge's; tell them apart by the resource attributes
  `resource_detection/system` stamps, not by which port they arrived on.
