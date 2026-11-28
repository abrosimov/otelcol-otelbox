---
note_id: "20261128150000"
created: 2026-11-28
type: note
subtype: reference
language: en
retention: active
---
# Edge self-telemetry contract

Desktop consumers outside this repository read the edge's own Prometheus
endpoint: a status-bar item that shows queue fill and drain, and an out-of-band
watcher that alerts on queue fill, a stalled drain and a WAL volume with too
little space. This note fixes what they may rely on. Everything else on the
endpoint is an implementation detail of the pinned Collector release and may
change with it.

`TestEdgeSelfTelemetryContract` in `test/harness/edge_metrics_contract_test.go`
starts the built binary with [the edge profile](../../config/edge.yaml), sends
data through a real gateway and fails when a series, a label key or a unit named
here changes. A change to this note and a change to that test go together.

The test composes the profile with the CI overlay `test/config/edge-ci.yaml`,
which replaces the profile's `service.telemetry.metrics.readers` list wholesale
to move the endpoint to a test port. The test therefore does not load the
profile's own Prometheus reader block: its host and port are not covered, and
any change to that block beyond host and port must be mirrored in the overlay
or the test will not see it.

The series below were read from the binary, not from component names. The
upstream `# HELP` text is not reliable: it describes the queue gauges as "in
batches" while the values are bytes (see [Units](#units)).

## Endpoint

| Item | Value |
| --- | --- |
| URL | `http://${OTELBOX_BIND_HOST:-127.0.0.1}:8888/metrics` |
| Format | Prometheus text exposition |
| Metric level | `detailed`, set in the shared `service.telemetry.metrics` region of every profile |
| Scope | One edge process; no `job`, `instance` or `otel_scope_*` label is added |

The port is a repository convention and the address follows the bind host the
receivers use. A deployment that moves either owns the consumers' address.

Counter names carry no `_total` suffix and no metric carries a unit suffix.
Values are floats and large ones use exponent notation (`1.073741824e+09`), so a
consumer parses them as floating point.

## Series consumers may rely on

Exporter identity is the `exporter` label. The edge has two network exporters,
both OTLP/gRPC to the one gateway, each with its own persistent queue:

| `exporter` | Carries | Queue capacity reference |
| --- | --- | --- |
| `otlp_grpc/gateway` | Application logs, metrics and traces | `OTELBOX_QUEUE_SIZE_BYTES`, per signal |
| `otlp_grpc/gateway_self` | The edge's own metrics (`metrics/self`) | `OTELBOX_SELF_QUEUE_SIZE_BYTES` |

The edge has no separate Langfuse or selected-traces pipeline. Native traces
take the `traces` pipeline and leave through `otlp_grpc/gateway` with
`data_type="traces"`; selected-trace routing exists only on the gateway.

### Queue gauges

Present from process start, before any data flows.

| Metric | Type | Labels | Series |
| --- | --- | --- | --- |
| `otelcol_exporter_queue_size` | gauge | `data_type`, `exporter` | exactly the four below |
| `otelcol_exporter_queue_capacity` | gauge | `data_type`, `exporter` | exactly the four below |

| `data_type` | `exporter` |
| --- | --- |
| `logs` | `otlp_grpc/gateway` |
| `metrics` | `otlp_grpc/gateway` |
| `traces` | `otlp_grpc/gateway` |
| `metrics` | `otlp_grpc/gateway_self` |

`data_type` alone does not identify a queue: `metrics` appears under both
exporters. A consumer keys a queue on the pair, and takes fill as
`queue_size / queue_capacity` for the same label set.

### Sent counters

Counters of items accepted by the gateway since the process started. Each has
the single label `exporter`; the signal is in the metric name.

| Metric | Series |
| --- | --- |
| `otelcol_exporter_sent_log_records` | `exporter="otlp_grpc/gateway"` |
| `otelcol_exporter_sent_spans` | `exporter="otlp_grpc/gateway"` |
| `otelcol_exporter_sent_metric_points` | `exporter="otlp_grpc/gateway"` and `exporter="otlp_grpc/gateway_self"` |

A consumer that sums `otelcol_exporter_sent_metric_points` over both exporters
sees the self-metrics exporter's progress as well; select by `exporter` to
follow application metrics alone. A counter series may be absent until its
first event; read absence as zero.

### Refused admission counter

| Metric | Labels | Meaning |
| --- | --- | --- |
| `otelcol_exporter_enqueue_failed_log_records` | `exporter` | Log records the full queue refused (gRPC `Unavailable`, HTTP 503) |

May be absent until the first refusal; read absence as zero.
`otelcol_exporter_enqueue_failed_spans` and
`otelcol_exporter_enqueue_failed_metric_points` belong to the same family and
[docs/reference/edge.md](edge.md#capacity-signals) names them, but the harness
exercises only the log one.

## Units

- `otelcol_exporter_queue_size` and `otelcol_exporter_queue_capacity` are
  bytes. The profile sets `sizer: bytes` with `storage_compression: zstd`, so a
  queue item counts at the bytes it occupies in the WAL (compressed), and
  `queue_capacity` is the configured `OTELBOX_QUEUE_SIZE_BYTES`
  (default 4294967296) or `OTELBOX_SELF_QUEUE_SIZE_BYTES` (default 536870912)
  exactly. The test holds the compressed unit: one record with a 512 KiB body
  of random hexadecimal raises `queue_size` to at least 128 KiB and below
  512 KiB, the size of the body alone.
- The sent and enqueue-failed counters count items of their signal (log
  records, spans, metric points), not bytes and not requests.
- The three application `queue_size` series are 0 on a fresh edge and fall to 0
  when their queue has drained; the self-metrics queue holds data within seconds
  of start.
  Counters count from process start, so a decrease between two scrapes means
  the edge restarted.

## Not part of the contract

- Every other series on the endpoint, including `otelcol_exporter_enqueue_size*`,
  `otelcol_exporter_queue_batch_send_size*`, `otelcol_exporter_in_flight_requests`,
  receiver, processor, scraper and process series, and `target_info`.
- `# HELP` and `# TYPE` text, and the `[Alpha]` or `[Development]` stability
  markers in it.
- `otelcol_exporter_send_failed_*`. The upstream exporter defines them, but the
  edge emitted none during an unreachable-gateway outage or while the gateway
  rejected its credential. Do not alert on their absence, and do not treat their
  presence as defined here.
- `otelcol_exporter_sent_profile_samples`. The binary defines it; the edge has
  no profiles pipeline, so it never appears. A consumer that maps a signal to
  it finds nothing, correctly.
- Series ordering, the set of histogram buckets, and any `exporter` value other
  than the two above.
- Disk space. `file_storage` exports no size metric, so the WAL volume is read
  from the filesystem; see [capacity signals](edge.md#capacity-signals).

## How a change is caught

The harness test fails when:

- a queue series, or its `data_type` / `exporter` label keys, is missing, extra
  or renamed;
- a queue series is missing on a freshly started edge, before any data is sent;
- `queue_capacity` stops equalling the configured byte size, or `queue_size`
  stops counting bytes, or counts them uncompressed;
- a sent counter or the enqueue-failed counter is missing, or carries a label
  key other than `exporter`;
- the main queues do not drain to zero after the gateway returns.

It runs in the `integration-test` job of `.github/workflows/otelcol-otelbox.yml`
with the rest of the harness, against the Linux binary that workflow builds, and
its formatting and vet checks run in the `static-check` job with the other
harness files.
