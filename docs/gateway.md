# Operating the gateway role

The gateway authenticates one OTLP ingress, redacts accepted telemetry and
persists each signal to every eligible required recipient. Load
[the profile](../config/gateway.yaml) on its own:

```console
otelcol-otelbox --config config/gateway.yaml
```

## Recipient model

Recipient count is configuration, not a binary limit. The reference profile
demonstrates one logical all-signal gRPC recipient as three signal-specific
exporter/storage pairs, plus one additional selected-traces HTTP recipient:

| Data | Required recipients |
| --- | --- |
| Traces without a route classification | `otlp_grpc/traces` |
| Traces with resource attribute `otelbox.telemetry.class=llm` | `otlp_grpc/traces` and `otlp_http/selected_traces` |
| Metrics, including gateway self-metrics | `otlp_grpc/metrics` |
| Logs | `otlp_grpc/logs` |

To render another all-signal recipient, add a uniquely named exporter and
`file_storage` extension for logs, metrics and traces, add all three extensions
under `service.extensions`, and append the matching exporter to each pipeline.
Removing any member of that triplet is a contract change, not a harmless
refactor. Static Collector YAML cannot expand an environment variable into an
arbitrary list, so the consuming repository renders the list explicitly.

The selected-traces branch deliberately exposes only generic OTLP/HTTP,
credential-file, arbitrary-header and filter capabilities. A consuming
deployment owns the concrete backend endpoint, path, authorisation scheme,
protocol values and version; none is part of this repository's reference
contract.

The route marker only selects delivery. Producers and instrumentation own the
backend's semantic span attributes and trace context; the gateway must not
manufacture a vendor-specific observation schema from arbitrary traces.
The pinned `filter` processor and `headers_setter` extension are Alpha; their
configuration is intentionally narrow and the black-box routing/header harness
is part of the release gate.

## Required environment

| Variable | Meaning |
| --- | --- |
| `OTELBOX_STORAGE_DIR` | Private writable root for all recipient WALs and compaction files. |
| `OTELBOX_INGEST_TOKEN_FILE` | Static bearer allowlist for `bearertokenauth/ingest`. |
| `OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT` | Reference all-signal OTLP/gRPC recipient `host:port`. |
| `OTELBOX_SELECTED_TRACES_ENDPOINT` | OTLP/HTTP base URL; the exporter appends `/v1/traces`. |
| `OTELBOX_SELECTED_TRACES_AUTH_HEADER_FILE` | Complete outbound authorisation value, including its scheme. |
| `OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_NAME` | Additional protocol/header contract name. |
| `OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_VALUE` | Additional protocol/header contract value. |

Reference defaults apply only when a variable is absent:

| Variable | Default | Scope |
| --- | ---: | --- |
| `OTELBOX_BIND_HOST` | `127.0.0.1` | OTLP listeners, self-metrics reader and target. A public or container ingress must opt into another address. |
| `OTELBOX_HEALTH_ENDPOINT` | `127.0.0.1:14323` | Lifecycle health listener. |
| `OTELBOX_INGEST_MAX_CONCURRENT_STREAMS` | 16 | Per gRPC connection, not a global client limit. |
| `OTELBOX_MEMORY_LIMIT_PERCENTAGE` | 75 | Hard heap-pressure threshold relative to the cgroup limit. |
| `OTELBOX_MEMORY_SPIKE_LIMIT_PERCENTAGE` | 15 | Spike allowance subtracted from the hard threshold. |
| `OTELBOX_EXPORTER_CONSUMERS` | 2 | Concurrent workers per exporter and signal. |
| `OTELBOX_AUTH_RETRY_INTERVAL` | `1h` | Probe interval while a recipient rejects an outbound credential. |
| `OTELBOX_LOGS_STORAGE_MAX_SIZE_BYTES` | 12 GiB | Log WAL for the reference all-signal recipient. |
| `OTELBOX_LOGS_QUEUE_SIZE_BYTES` | 8 GiB | Log queue for the reference all-signal recipient. |
| `OTELBOX_METRICS_STORAGE_MAX_SIZE_BYTES` | 2 GiB | Metrics WAL for the reference all-signal recipient. |
| `OTELBOX_METRICS_QUEUE_SIZE_BYTES` | 1 GiB | Metrics queue for the reference all-signal recipient. |
| `OTELBOX_TRACES_STORAGE_MAX_SIZE_BYTES` | 12 GiB | Trace WAL for the reference all-signal recipient. |
| `OTELBOX_TRACES_QUEUE_SIZE_BYTES` | 8 GiB | Trace queue for the reference all-signal recipient. |
| `OTELBOX_RECIPIENT_STORAGE_MAX_SIZE_BYTES` | 12 GiB | Selected-traces WAL. |
| `OTELBOX_RECIPIENT_QUEUE_SIZE_BYTES` | 8 GiB | Selected-traces queue. |
| `OTELBOX_RECIPIENT_MAX_PAYLOAD_BYTES` | 3 MiB | Sender split limit, below the common 4 MiB downstream gRPC default. |

The gateway profile assumes a cgroup memory limit. Set that limit in the
deployment and set `GOMEMLIMIT` below it; otherwise a percentage of host memory
is not a useful process ceiling. The Collector limiter observes Go heap, not RSS
or memory-mapped WAL pages.

## Capacity and payload envelopes

Queue capacity is an outage budget, not a round number to copy blindly. For
each recipient and signal, estimate:

```text
queue bytes >= peak encoded bytes/second * required outage seconds * safety factor
storage max_size >= queue bytes * 1.5
```

A safety factor between 1.25 and 2 covers burstiness and estimation error. The
reference reserves 8/12 GiB for logs, 1/2 GiB for metrics and 8/12 GiB for
traces; the larger metrics ratio leaves additional room for database overhead.
Gateway rates are aggregate across all edges and local agents; reusing an
edge-sized budget without multiplying the measured ingress rate is incorrect.

The reference topology has four bbolt files: three for the all-signal recipient
and one for selected traces. Their configured caps total 38 GiB before
filesystem and compaction headroom. A deployment with N all-signal recipients
has `3N + 1` files and a default cap of `26 GiB * N + 12 GiB` while the
selected-traces branch remains present. Size and alert from the rendered set.

The default request ladder prevents a single accepted record from becoming
unsendable after acknowledgement:

```text
edge ingress 1 MiB -> edge batch 1.5 MiB -> gateway ingress 2 MiB
-> recipient batch 3 MiB -> downstream receiver at least 4 MiB
```

Compression does not increase receiver headroom: limits apply to the decoded
message as well. A deployment that changes one rung must prove the entire
ladder, especially the final receiver limit. Every rendered recipient queue
must also exceed the gateway's 2 MiB accepted-request envelope: a single item
larger than its persistent byte queue cannot be enqueued and blocks the request
until its context expires.

## Authentication and transport

The ingest allowlist contract is one URL-safe bare token per line, with no
whitespace or comments. Give every client a distinct token. Although pinned
v0.158 ignores text after the first whitespace, relying on that parser quirk
makes file audits ambiguous.

An empty-file reload is rejected and the previous tokens remain active. Rotate
by replacing the file with a non-empty complete allowlist. To revoke the final
client, replace it with a freshly generated non-client revocation token; do not
truncate the file. The harness proves that replacement activates the new token
and rejects the previous one.

Inbound TLS termination belongs to the deployment. A bearer token must never
cross an unencrypted network. All outbound exporters retain secure defaults;
deliberate private plaintext must be stated in the rendered configuration.

Outbound gRPC `Unauthenticated`/`PermissionDenied` and HTTP 401/403 responses
retain the affected request in its recipient WAL and retry at
`OTELBOX_AUTH_RETRY_INTERVAL`. Replacing a watched outbound header file takes
effect without restarting the gateway. This is at-least-once delivery: a lost
success response may produce a duplicate at the recipient.

## Required delivery and coupling

Every network exporter has its own persistent byte queue, infinite transient
retry and `block_on_overflow: true`. An eligible record is acknowledged after it
has been enqueued to every required pipeline. A recipient outage is isolated while
its queue has headroom; after it fills, synchronous fan-out backpressures ingest.

The selected HTTP recipient couples only classified trace requests. Ordinary
traces, metrics and logs never enter its WAL. If one OTLP request contains both
classified and unclassified trace resources, the request is one acknowledgement
unit and can block on the selected recipient. The coupling and SIGKILL harness
tests cover the queue-headroom, overflow and replay boundaries.

## Endpoints and validation

| Endpoint | Purpose |
| --- | --- |
| `${OTELBOX_BIND_HOST}:14319` | Authenticated OTLP/gRPC ingest, up to 2 MiB per message. |
| `${OTELBOX_BIND_HOST}:14320` | Authenticated OTLP/HTTP ingest, up to 2 MiB per request; 10 s header, 30 s read/write and 1 min idle timeouts. |
| `${OTELBOX_BIND_HOST}:8889/metrics` | Detailed Collector metrics and self-scrape target. |
| `${OTELBOX_HEALTH_ENDPOINT}/status` | Lifecycle health only; full queues and rejected exports do not make it unhealthy. |

```console
OTELBOX_STORAGE_DIR=/tmp/otelbox-gateway \
OTELBOX_INGEST_TOKEN_FILE=/tmp/otelbox-ingest-tokens \
OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT=127.0.0.1:14317 \
OTELBOX_SELECTED_TRACES_ENDPOINT=https://selected.example/otel \
OTELBOX_SELECTED_TRACES_AUTH_HEADER_FILE=/tmp/selected-auth-header \
OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_NAME=x-protocol-version \
OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_VALUE=4 \
  otelcol-otelbox validate --config config/gateway.yaml
```

Monitor send/enqueue failures and queue size/capacity per exporter and signal.
Require an unauthenticated OTLP/HTTP probe to return 401 and send far-end markers
to every eligible recipient; `/status` proves neither access control nor
delivery.
