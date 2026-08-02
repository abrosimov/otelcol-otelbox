# Operating the edge role

This is the artefact-level contract for `otelcol-otelbox` run as **edge**: the
environment it needs, the ports it opens, how to compose and validate its
configuration, and how to prove telemetry actually left the machine. It does
not cover how the edge is installed, supervised or granted its secrets on any
particular machine — that belongs to the deploying repository and stays there.
See `config/examples/edge.yaml` for the reference profile these facts are drawn
from; it is a demonstration of the role, not a copy of any deployment.

## Required environment

| Variable | Used for |
|---|---|
| `OTELBOX_EDGE_BIND_HOST` | Address every listener in this profile binds — the `otlp` receiver and the self-metrics endpoint, and the address `prometheus/self` scrapes. Conventionally `127.0.0.1`; the ports beside it are literal. |
| `OTELBOX_EDGE_STORAGE` | Root of the `file_storage` WAL directory (`$OTELBOX_EDGE_STORAGE/gateway` plus its compaction directory). |
| `OTELBOX_EDGE_ENDPOINT` | `host:port` of the remote gateway the `otlp_grpc/gateway` exporter dials. |
| `OTELBOX_EDGE_TOKEN` | Bearer token sent as `authorization: Bearer ${OTELBOX_EDGE_TOKEN}` on every export, and on the `http_check` probe below. Must be one of the tokens the target gateway's allowlist file carries — see `docs/gateway.md`. |
| `OTELBOX_EDGE_PROBE_URL` | Full URL of the gateway's OTLP/HTTP ingest endpoint for the `http_check` probe. A URL, not the gRPC `host:port` in `OTELBOX_EDGE_ENDPOINT` — the two are deliberately separate variables. |
| `OTELBOX_EDGE_NTP_ENDPOINT` | `host:port` of the time source the `ntp` receiver queries. Which source to trust is the deployment's; the receiver refuses any interval under 30 minutes regardless. |
| `OTELBOX_EDGE_HEALTH_ENDPOINT` | `host:port` the `healthcheckv2` extension binds. Conventionally `127.0.0.1:13133`, but the bind is the supervisor's call, which is why it is a variable. |

All seven are load errors, not defaults, if unset — see AGENTS.md
"Configuration layering" for why an unset `${env:...}` reference must never be
allowed to fall through silently. Note how one surfaces: an unset variable
expands to an empty string and the failure then names the *component*, not the
variable — leaving `OTELBOX_EDGE_HEALTH_ENDPOINT` unset gives
`extensions::healthcheckv2: http endpoint required`.

## Ports and endpoints

The receivers below carry no authentication, so whoever can reach them can
write: the conventional bind is loopback, and that is the whole of the edge's
access control. The bind address is the deployment's choice and therefore a
variable; the ports are this repository's convention and stay literal.

| Endpoint | Purpose |
|---|---|
| `$OTELBOX_EDGE_BIND_HOST:4317` (gRPC) / `:4318` (HTTP) | `otlp` receiver — where local applications send telemetry. Max 32 MiB per message on both transports, matching the gateway. |
| `$OTELBOX_EDGE_HEALTH_ENDPOINT` (conventionally `127.0.0.1:13133`) | `healthcheckv2` extension, serving `/status`. **Not `/`** — that was v1's path and v2 answers it 404. Reports component lifecycle only, which is strictly less than delivery; see Verifying delivery below. |
| `$OTELBOX_EDGE_BIND_HOST:8888/metrics` | The collector's own Prometheus scrape endpoint, at `detailed` level — the role layer overrides the base layer's `normal` reader outright. Also scraped by the edge's own `prometheus/self` receiver — see below. |

The edge additionally makes three outbound observations of its own, none of
which opens a listener: `ntp` against `$OTELBOX_EDGE_NTP_ENDPOINT` every 30
minutes (the receiver refuses a shorter interval), `http_check` against
`$OTELBOX_EDGE_PROBE_URL`, and `host_metrics` reading the local kernel.

## What the edge observes about itself

`prometheus/self` scrapes the collector's own metrics endpoint back into the
`metrics/self` pipeline rather than letting `service::telemetry` push metrics out
directly. That is deliberate and load-bearing: the `service::telemetry` export
path is a separate OpenTelemetry SDK, so it bypasses `redaction/secrets`,
`batch`, the sending queue and the on-disk WAL — `otelcol_exporter_send_failed_*`
would then travel on a path with no durability, losing exactly the evidence of
the outage it describes.

**The edge runs `service::telemetry::metrics::level: detailed`**, overriding the
base layer's `normal`, and the reason is the incident. Below `detailed` the
collector's own views drop the whole otelgrpc and otelhttp meters and strip
`error_type` and `error_permanent` from `otelcol_exporter_send_failed_*` — so at
`normal` a rejected credential and an unreachable gateway are the same number.
It is not free: it adds the RPC and HTTP histogram families to what the edge
ships.

`http_check` turns the incident below into a metric. Read the signal carefully:
authentication runs ahead of the OTLP handler, so a **rejected** token answers
401 and an **accepted** one answers a method error — 405, never 200. Alert on
`http.status_class` being 4xx with `http.status_code` 401. `httpcheck.error`
stays at zero throughout, because the HTTP exchange itself succeeded.

`host_metrics` runs without the `process` scraper, and muting its errors is not
the fix — see AGENTS.md "The process scraper". Cardinality is bounded inside the
receiver rather than with a `filter` processor, so the series are never built:
macOS virtual interfaces are excluded by pattern, and the filesystem scraper
takes a **strict `include_mount_points` allowlist of `/`** rather than an
exclude list. On APFS every volume in a container reports the container's total
and free space, so one mount point carries the whole capacity signal, and
`/Volumes/` mounts — disk images, external drives, network shares — are fresh
`(device, mountpoint)` pairs that vanish two scrapes later. Includes and
excludes are ANDed, so an allowlist is the only form that keeps them out; see
AGENTS.md "`host_metrics` filter semantics". The per-core `cpu` attribute on
`system.cpu.time` became opt-in upstream at v0.157.0 and is left opt-out here.

## Compose and validate

Two layers, base first:

```console
otelcol-otelbox --config config/base.yaml --config <edge-role-config>.yaml
```

`<edge-role-config>.yaml` is `config/examples/edge.yaml` here, or whatever role
layer the deployment renders. Validate a candidate configuration against the
built binary before rolling it out:

```console
OTELBOX_EDGE_BIND_HOST=127.0.0.1 \
OTELBOX_EDGE_STORAGE=/tmp/otelbox-edge \
OTELBOX_EDGE_ENDPOINT=127.0.0.1:4317 \
OTELBOX_EDGE_TOKEN=placeholder \
OTELBOX_EDGE_PROBE_URL=http://127.0.0.1:4318/v1/logs \
OTELBOX_EDGE_NTP_ENDPOINT=pool.ntp.org:123 \
OTELBOX_EDGE_HEALTH_ENDPOINT=127.0.0.1:13133 \
  otelcol-otelbox validate \
    --config config/base.yaml --config config/examples/edge.yaml
```

`validate` parses and starts nothing, which is why it works under a restricted
command sandbox where the edge itself will not start: `resource_detection` and
every `host_metrics` scraper read the boot time in `start()`, and a sandbox that
denies that `sysctl` kills the process with an error that reads like a
configuration fault.

The edge role restates no processor from the base layer — it runs
`memory_limiter`, `batch` and `redaction/secrets` at the base layer's
workstation-sized values, and adds `resource_detection` (edge-only: it stamps
this machine's identity, which the gateway must not restamp onto relayed
telemetry). It does override `service::telemetry::metrics`, for the reason above.

## Verifying delivery

**A running process, a green `healthcheckv2` and an `otlp` receiver accepting
requests prove nothing about delivery.** This repository exists because a
deployed edge failed every export for days while all three stayed green: the
process ran, launchd reported it alive, the health endpoint answered 200, and
the receiver accepted everything handed to it — none of that touches the
network hop to the gateway.

This is a property of the extension, not an oversight in how it is configured.
`healthcheckv2` aggregates component *lifecycle* events, and a rejected export
emits none — `include_permanent_errors: true` is set in the profile, and it
still cannot see this, because the permanent errors it reports on are failures
to start or stop. No setting on the health endpoint would change that. Query the
self-metrics endpoint on `8888` instead:

- `otelcol_exporter_send_failed_spans` / `_metric_points` / `_log_records` —
  non-zero means the gateway is rejecting or unreachable, and at `detailed` the
  `error_type` and `error_permanent` labels say which. During the incident above
  this counter was climbing for days; what failed was that nobody was looking at
  it and no alert was built on it. `error_permanent="true"` is the case that
  cannot be waited out: a permanent error is dropped rather than parked in the
  WAL, and `Unauthenticated` is permanent — see AGENTS.md "Component facts".
- `otelcol_exporter_enqueue_failed_*` — the WAL write itself failed (disk full
  or a corrupted `bbolt` file). `block_on_overflow: true` means a full queue
  blocks the producer rather than returning a failure, so this metric should
  stay at zero from ordinary backlog and only moves when the storage layer
  itself is unhealthy.
- `otelcol_exporter_queue_size` — current WAL backlog. Should hover near
  zero when the gateway is reachable; a sustained rise is the leading
  indicator of the failure above, well before `send_failed` moves.
- `httpcheck_status` with `http_status_code="401"` — the probe described above,
  and the only one of these that names the cause rather than the symptom. It
  moves on the *first* scrape after a credential goes stale, and it survives
  the failure mode that made the incident invisible, because it does not depend
  on anything being exported.

Confirming the other end received it means checking the gateway's own
`otelcol_receiver_accepted_*` on its self-metrics endpoint — see
`docs/gateway.md`. The repository's end-to-end test exercises this exact
path, including the case of a token outside the gateway's allowlist, and
asserts on `send_failed` rather than on anything local.
