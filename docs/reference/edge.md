---
note_id: "20261022090000"
created: 2026-10-22
type: note
subtype: reference
language: en
retention: active
---
# Operating the edge role

The edge accepts OTLP from local applications, applies origin detection and
redaction, persists outbound data and sends it to one authenticated gateway.
Load [the profile](../../config/edge.yaml) on its own:

```console
otelcol-otelbox --config config/edge.yaml
```

## Required environment

| Variable | Meaning |
| --- | --- |
| `OTELBOX_STORAGE_DIR` | Private writable root for the gateway WAL and its compaction files. |
| `OTELBOX_UPSTREAM_ENDPOINT` | Gateway OTLP/gRPC `host:port`. |
| `OTELBOX_UPSTREAM_AUTH_HEADER_FILE` | File containing the complete header value, literally `Bearer <token>`. |

Reference variables have safe absent-variable defaults:

| Variable | Default | Constraint |
| --- | ---: | --- |
| `OTELBOX_BIND_HOST` | `127.0.0.1` | Fails closed on loopback when the deployment omits it. |
| `OTELBOX_HEALTH_ENDPOINT` | `127.0.0.1:13133` | Complete health `host:port`. |
| `OTELBOX_MEMORY_LIMIT_MIB` | 400 MiB | Fixed hard threshold for the workstation process. |
| `OTELBOX_MEMORY_SPIKE_LIMIT_MIB` | 80 MiB | Spike allowance below the hard threshold. |
| `OTELBOX_EXPORTER_CONSUMERS` | 2 | Concurrent outbound workers. |
| `OTELBOX_AUTH_RETRY_INTERVAL` | `1h` | Probe interval while the gateway rejects the outbound credential. |
| `OTELBOX_STORAGE_MAX_SIZE_BYTES` | 6 GiB | Per signal file; at least 1.5 times the queue capacity, see [sizing invariants](#sizing-invariants). |
| `OTELBOX_QUEUE_SIZE_BYTES` | 4 GiB | Per signal, in stored (zstd-compressed) bytes. Must exceed the outage budget plus the in-flight headroom and stay within storage capacity; see [sizing the queue](#sizing-the-queue). |
| `OTELBOX_SELF_STORAGE_MAX_SIZE_BYTES` | 768 MiB | File cap of the self-metrics WAL; see [capacity signals](#capacity-signals). |
| `OTELBOX_SELF_QUEUE_SIZE_BYTES` | 512 MiB | Self-metrics queue, in stored bytes; two thirds of its file cap. |
| `OTELBOX_UPSTREAM_TLS_CERT_FILE` | none | Optional client certificate presented to the gateway leg. Absent means none is offered. |
| `OTELBOX_UPSTREAM_TLS_KEY_FILE` | none | Private key for the certificate above. Supply both or neither. |
| `OTELBOX_UPSTREAM_TLS_RELOAD_INTERVAL` | `1h` | How stale the client pair may be before the next handshake re-reads it. |
| `OTELBOX_UPSTREAM_COMPRESSION` | `zstd` | Wire codec for the gateway leg. `zstd`, `gzip` and `none` are exercised; see below. |
| `OTELBOX_LANGFUSE_USER_ID` | `unknown` | Identity stamped on every native trace; see below. |
| `OTELBOX_LANGFUSE_NATIVE_TAGS` | `otel-native` | Comma-separated tags stamped on every native trace; see below. |

If `OTELBOX_STORAGE_DIR` is absent, its invalid `/dev/null/...-is-required`
sentinel makes validation fail before a root-level WAL can be selected. An
exported empty value bypasses that sentinel and every numeric default. Reject
empty values and render explicit values in production rather than relying on
the reference sizes.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `${OTELBOX_BIND_HOST}:4317` | OTLP/gRPC ingest, up to 1 MiB per message. |
| `${OTELBOX_BIND_HOST}:4318` | OTLP/HTTP ingest, up to 1 MiB per request. |
| `${OTELBOX_BIND_HOST}:8888/metrics` | Detailed Collector metrics; also scraped back through a durable queue of their own. |
| `${OTELBOX_HEALTH_ENDPOINT}/status` | Component lifecycle health. It does not prove delivery. |

The profile publishes exactly these listeners. A deployment that must route a
local producer to the gateway's selected-traces recipient adds its own
classified listener and a `resource` processor instance rather than changing
these; the linked-but-unwired component and its failure modes are described in
[stamping the route marker](gateway.md#stamping-the-route-marker-where-the-producer-cannot).

## Authentication and TLS

`headers_setter/gateway` reads the complete `authorization` value from the
header file. It trims surrounding whitespace but adds no scheme, so this is
valid:

```text
Bearer one-secret-token
```

Do not add a trailing comment: it would become part of the header. Keep the file
readable only by the account running the Collector.

The upstream exporter uses TLS verification by default. The reference profile
does not choose a CA path because trust-store ownership is deployment-specific.
The CI overlay supplies a per-run CA and verifies the gateway leaf; it never
turns verification off.

### Optional client certificate

`OTELBOX_UPSTREAM_TLS_CERT_FILE` and `OTELBOX_UPSTREAM_TLS_KEY_FILE` make the
edge present a client certificate on the gateway leg. Deployments that reach
their gateway over an SSH tunnel or a private network leave both unset and
nothing changes; the exporter offers no certificate and the bearer token remains
the only credential.

Supply them when the gateway sits behind a front end configured for mTLS. The
certificate and the token are then two independent factors: the token names a
client in the gateway's `bearertokenauth/ingest` allowlist, the certificate
proves possession of a host key to the front end, and either can be revoked
without touching the other.

Three failure modes, deliberately different:

| Situation | Where it surfaces |
| --- | --- |
| Both variables absent or empty | No failure. No client certificate is offered. |
| Exactly one of the pair supplied | `validate` fails: `TLS configuration must include both certificate and key`. |
| Both supplied, a path does not resolve | `validate` passes; the exporter fails at start with `failed to load TLS cert and key`. |

The third row is the one to plan for: a mistyped path is not a configuration
error and only appears when the process starts, so treat a failed start as the
signal rather than expecting `validate` to catch it.

Rotation needs no restart, but the mechanism differs from the header file's.
`headers_setter/gateway` is watched by fsnotify and picks up a replacement
immediately. The certificate pair is polled: `configtls` re-reads it on the
first handshake after `OTELBOX_UPSTREAM_TLS_RELOAD_INTERVAL` has elapsed, so a
replacement takes effect within that interval plus the time until the next
connection, not instantly. Leaving the variable unset keeps the reference
one hour; `configtls`'s own default of `0` would pin the process to the material
it started with, which is why this profile does not use it.

If the gateway rejects the credential, the exporter retains the accepted data
and retries at `OTELBOX_AUTH_RETRY_INTERVAL`. Replace the header file in place;
`headers_setter/gateway` supplies the new value to a later attempt without a
Collector restart. The interval is deliberately much longer than the ordinary
5–30 second transient backoff.

## Compression

The gateway leg uses `zstd`. It is chosen for this leg specifically, not as a
general preference: a gRPC client can only select a codec that is registered in
the server it dials, and that is a property of the peer's build rather than its
configuration. Both ends of this leg are `otelcol-otelbox`, so the guarantee
holds by construction. The gateway's own recipient exporters stay on `gzip`,
where the peer is a third-party backend.

Override with `OTELBOX_UPSTREAM_COMPRESSION` when the deployment terminates this
leg somewhere other than this binary, or when the transport already compresses —
`none` is a legitimate value there. An unrecognised value is refused by name at
load rather than silently ignored. Compression level is not configurable: the
persistent-queue exporter wrapper does not carry `compression_params`.

`zstd` and `gzip` are the values the harness has actually carried records under,
and `none` needs no codec at all. `snappy`, `lz4`, `zlib` and `deflate` are
accepted by the configuration loader, but loading is not delivery: a codec must
also be registered in the gRPC peer, so treat those as unproven on this leg
until a run says otherwise.

The codec changes neither sizing invariant. The sender batch's `sizer: bytes`
measures uncompressed items, the queue counts its own stored zstd bytes
whatever the wire codec is, and the gateway's `max_recv_msg_size_mib` applies to
the decompressed message, so the 1.5 MiB sender batch stays inside the 2 MiB receive
envelope regardless. It does change the exporter `timeout`, which is derived
from the compressed batch; see [the exporter timeout](#the-exporter-timeout).

## Langfuse trace-level fields on native traces

`transform/langfuse_native` stamps two span attributes on the `traces` pipeline:

| Attribute | Value |
| --- | --- |
| `langfuse.user.id` | `${OTELBOX_LANGFUSE_USER_ID:-unknown}` |
| `langfuse.trace.tags` | `${OTELBOX_LANGFUSE_NATIVE_TAGS:-otel-native}` |

Langfuse resolves a trace's user and tags from span attributes only; resource
attributes are not consulted for either, so `resource_detection` and a rendered
`resource/<name>` instance cannot supply them. A producer speaking plain
OpenTelemetry sets neither, and its own `user.id` is frequently a per-installation
identifier rather than a person, so those traces arrive untagged and under an
identity nothing else shares.

The tags value is a comma-separated string, not an OTLP array. Langfuse accepts
an array, a JSON-array string and a comma-separated string, and the string is the
only form `redaction/secrets` inspects: a slice attribute is an uncovered carrier
under `redact_all_types: false`. That is also why the processor sits between
`resource_detection` and `redaction/secrets` — what it injects passes the same
deny-list as an ingested attribute.

Both statements use OTTL `set`, which overwrites. A deployment that adds a
listener fed by a Langfuse SDK gives it its own pipeline and does **not** put this
processor on it: the SDK states both fields itself, per-observation tags included,
and `set` would discard them. `metrics`, `logs` and `metrics/self` carry no
Langfuse trace-level fields and are untouched.

`error_mode: ignore` is deliberate. A failed stamp is logged and the span
continues; `propagate` would return the error to the receiver and reject data
this role exists to retain.

## Durability

The exporter queue writes through `file_storage/gateway` and uses byte-based
capacity. Retry has no elapsed-time limit. Sender batching happens inside that
persistent queue; there is no pipeline `batch` processor that could return
success while retaining the record only in memory. The sender splits batches at
1.5 MiB, below the gateway's 2 MiB receive limit.

The harness forces this boundary: it posts a record while the gateway is down,
requires HTTP 200, kills the edge with SIGKILL, reopens the same storage and
requires the record to reach the gateway after recovery.

### When the queue is full

The queue sets `block_on_overflow: false`. A request that does not fit is
refused at once, before anything is persisted: the receiver answers gRPC
`Unavailable` or HTTP 503 without `Retry-After`. Both are retryable under the
OTLP specification, so the producer's client retries the request on its own
backoff, and each retry is admitted as soon as the queue has drained enough.
`TestEdgeRefusesAtFullQueueWithARetryableStatus` holds this.

Refusal does not make the edge responsible for the request. The producer still
holds it, and only for as long as its own retry budget lasts: a client that
gives up before the queue drains loses the batch. A full queue is therefore a
capacity failure, and the [capacity signals](#capacity-signals) exist to report
it well before it happens.

That budget belongs to the producer, and SDK defaults are short: the
OpenTelemetry Go SDK's OTLP exporter, for example, gives up on a batch after one
minute of retries, and an SDK's in-memory batch queue drops further batches once
it is full. A producer that
must survive a full edge queue for longer raises its exporter's retry time and
its batch queue size, or accepts the loss as the cost of the capacity it was
given.

Blocking was the earlier policy, and it was worse for exactly that producer. A
blocked request waited until the client's own export timeout cancelled it; the
enqueue then failed with `context canceled`, and the client, whose deadline had
already passed, dropped the batch without a retry. That is how a full queue
lost spans in practice. On the edge the producer is the only party that can
still retry, so it must be given a status it acts on promptly.

Records already in the WAL are unaffected. Restart recovery re-enqueues
in-flight items without the capacity check, so a full queue never refuses its
own replay.

### Sizing the queue

The exporter sets `storage_compression: zstd`: every item is written to the WAL as
one zstd frame, and with `sizer: bytes` both `queue_size` and the reported queue
size count the **stored** bytes an item takes, not its uncompressed size. The
queue and `file_storage` `max_size` are therefore in the same unit, and the
disk holds the uncompressed volume times the compression ratio. OTLP telemetry
commonly compresses several times over, so the same disk budget carries a
correspondingly longer outage than an uncompressed queue would.

Measure the rate from the edge's own metrics, which the profile publishes at
`detailed` level:

| Metric | Use |
| --- | --- |
| `otelcol_exporter_queue_size` for `otlp_grpc/gateway`, per signal | Stored bytes held, in the unit `queue_size` counts. Its growth during an outage is the stored rate the budget is sized from. |
| `otelcol_exporter_enqueue_size_bytes` (histogram) | **Uncompressed** bytes offered to the queue. Divide by the stored rate for the compression ratio this workload achieves. |

Render, per signal:

```text
queue_size >= peak stored bytes/second × outage seconds × safety factor
              + in-flight headroom
```

Without a measured stored rate, start from the uncompressed rate divided by a
conservative ratio and correct it from `otelcol_exporter_queue_size` once an
outage has been observed. Every signal has its own queue and WAL file, but
`OTELBOX_QUEUE_SIZE_BYTES` sets all three, so size it for the busiest signal,
usually traces. The 4 GiB default is a reference value, not a measurement.

A WAL written before compression was enabled is still read; its items are
counted at their uncompressed size until the queue drains, so the reported size
is approximate in the meantime.

### In-flight headroom

With sender batching, one reader takes items from the queue and hands them to
the batcher, which sends up to `num_consumers` batches at once. An item remains
counted against `queue_size` until every batch carrying part of it has been
sent, so part of the queue is always held by data in flight rather than free
for admission. At most:

```text
in-flight headroom ≈ (num_consumers + 2) × max(batch max_size, largest accepted request)
```

That is `num_consumers` batches being sent, the item the reader holds while it
waits for a free worker, and the batch still filling towards `min_size`. During
an outage the in-flight batches are the ones being retried, so this capacity
does not come back until the gateway does. The items are counted at their
stored size, so the bound below, in uncompressed batch bytes, overstates it; in
the reference profile it is at most (2 + 2) × 1.5 MiB = 6 MiB of the 4 GiB
queue.

### Records larger than a batch

A single span, log record or metric data point whose encoded size exceeds
`batch.max_size` cannot be split. The batcher drops it after the producer was
acknowledged and exports the rest of its request; the edge logs `Failed to split
request.` with `single span exceeds the max size limit`, or the log-record or
data-point equivalent. In the reference
profile no accepted record can reach that point, because the 1 MiB receiver
limit is below the 1.5 MiB `max_size`.

A deployment that raises the receiver limits to accept larger records, such as
LLM transcript spans up to a 128 MiB receiver limit, must move the rest of the
invariants with them:

1. raise `batch.max_size` to at least the new receiver limit, so every
   acceptable record fits a batch;
2. raise the gateway's `max_recv_msg_size_mib` above that `max_size`, with its
   `max_request_body_size` at least as large. A gateway that refuses a batch as
   too large answers `ResourceExhausted`, which stays permanent and drops the
   batch;
3. budget the larger in-flight headroom: at two consumers and a 128 MiB
   `max_size`, (2 + 2) × 128 MiB = 512 MiB of the queue, before any outage
   budget;
4. recompute [the exporter timeout](#the-exporter-timeout) for the larger
   batch.

### Sizing invariants

Check a rendered deployment against every row:

| Invariant | Reference values | Why |
| --- | --- | --- |
| Edge receiver limit (`max_recv_msg_size_mib`, `max_request_body_size`) ≤ `batch.max_size` | 1 MiB ≤ 1.5 MiB | A record above `max_size` is dropped after acknowledgement. |
| `batch.max_size` < gateway `max_recv_msg_size_mib` ≤ gateway `max_request_body_size` | 1.5 MiB < 2 MiB ≤ 2 MiB | The gateway refuses an oversized batch, permanently. |
| `queue_size` ≥ outage budget + (`num_consumers` + 2) × max(`batch.max_size`, receiver limit) | 4 GiB ≥ budget + 6 MiB | In-flight items hold queue capacity until sent. Budget and queue in stored bytes. |
| `file_storage` `max_size` ≥ 1.5 × `queue_size`, per signal | 6 GiB ≥ 1.5 × 4 GiB | Below. Both are stored bytes, so the margin covers bbolt overhead only, not a compression ratio. |
| `timeout` ≥ 3 × largest compressed batch ÷ slowest uplink | 10 s ≥ 9.4 s | A batch that cannot cross the uplink within one attempt is never delivered. |

The storage rule exists so that the queue, not the storage, is what refuses
admission. `max_size` caps the bbolt file, not the logical bytes the queue
counts: the file also carries page and B+tree overhead, a freelist, and pages
an outage's churn freed but did not return, since bbolt reclaims space only by
compaction. A queue refusal is clean and retryable. A storage refusal reaches
the read side too: the queue no longer deletes an item whose dispatch it cannot
record ([PC-13](../research/problem-cards/PC-13-read-side-storage-failure-drops-acknowledged-item.md),
resolved in [the outage review](../research/network-outage-loss-review.md)),
but it stops draining until finished items free space, so the full storage is
still a stall. 1.5 is a margin for that overhead, not a measured bound; keep it,
and keep the volume itself larger than every signal's `max_size` together.

### The forked persistent queue

The binary and the wrapper tests build exporterhelper from a fork of
[abrosimov/opentelemetry-collector](https://github.com/abrosimov/opentelemetry-collector),
through the one `replaces` entry in `builder.yaml`, whose comment carries the
re-base rule. It carries three changes to upstream v0.164.0's persistent queue:

- **Size accounting.** Upstream keeps the size of a put that failed to persist,
  and of a request dropped on read or on re-queue at start-up, counted. Over a
  long outage the leaked size fills `queue_size`, and the queue refuses data it
  has room for.
- **Dispatch durability.** Upstream deletes an item whose dispatch it cannot
  record, and moves the items in flight at start-up in two transactions, so a
  crash between them loses those items (PC-13, PC-14). The fork keeps the item
  and reads it again after a second, and moves the in-flight items in one
  transaction.
- **Stored-byte sizing.** Under `sizer: bytes`, upstream counts a request
  before encoding; the fork counts the bytes an item takes in storage. The
  exporter wrappers in `internal/exporters/` own compression: their
  `storage_compression: zstd` wraps the queue encoding, and this change is what
  makes the queue bound the compressed result. See
  [sizing the queue](#sizing-the-queue).

The fork goes once a Collector release contains all three.

### The exporter timeout

`timeout` bounds one send attempt. The exporter retries `DeadlineExceeded`, so
a timeout too short for a batch is not a loss but a stall: that batch is sent,
cut off at the deadline and sent again for ever, and once every consumer holds
such a batch the queue stops draining on that link. A longer timeout costs only
recovery time after a network change, when an attempt is held on a dead
connection until it expires.

An expired attempt cancels its request, not the connection, so without more the
next attempt goes down the same dead connection. A peer that vanished without a
reset — another Wi-Fi network, a dropped NAT or VPN mapping, a laptop that
slept — is then detected only when the kernel abandons its retransmissions,
about fifteen minutes on Linux. The exporter therefore pings every 30 s
(`keepalive`, also with no request in flight) and abandons the connection 10 s
after an unanswered ping; with keepalive set, grpc-go also sets
`TCP_USER_TIMEOUT` to the same bound. The gateway's receiver permits that rate.
A deployment that terminates this leg on another front end must permit it too:
a gRPC server's default policy allows one ping in five minutes and answers more
with GOAWAY `too_many_pings`.

```text
timeout >= 3 × largest compressed batch ÷ slowest supported uplink
```

The factor of three covers connection and TLS set-up, slow start and
retransmission on a lossy link. For the reference profile: the largest batch is
1.5 MiB uncompressed; assuming `zstd` shrinks it at least 4×, about 384 KiB
crosses the wire; on a 1 Mbit/s uplink (125 kB/s), the weak end of a mobile
tether or a congested hotel network, that takes 3.1 s, and three times that is
9.4 s. The reference 10 s therefore holds and is unchanged.

It does not hold for every rendering. With `OTELBOX_UPSTREAM_COMPRESSION=none`
and no compressing transport, the full 1.5 MiB needs a 3.8 Mbit/s uplink to
meet the rule at 10 s. A deployment that raises `batch.max_size` for large
records needs far more: a 128 MiB batch that compresses 4× takes about 270 s at
1 Mbit/s, so the rule asks for about 13 minutes.

## Capacity signals

Alert before the queue stops admitting data. The edge reports what that needs
through its own `metrics/self` pipeline:

| Signal | Metric | Suggested alert |
| --- | --- | --- |
| Queue fill | `otelcol_exporter_queue_size / otelcol_exporter_queue_capacity` for `otlp_grpc/gateway`; both are bytes under `sizer: bytes`, although they declare `{batch}` | above 0.5 |
| Refused admission | `otelcol_exporter_enqueue_failed_spans`, `_metric_points`, `_log_records` | any increase |
| Volume space | `system.filesystem.utilization` from `host_metrics/storage`, selected by the `mountpoint` that holds `OTELBOX_STORAGE_DIR` | above 0.8 |
| Silence | absence of any point from this edge | longer than a few collection intervals |

These points travel on `metrics/self`, which has its own exporter
`otlp_grpc/gateway_self` and WAL `file_storage/gateway_self`. A full main queue
therefore refuses application data without also refusing the signals that
report it; once the gateway is reachable again, the backend sees the fill and
refusal series of the outage in order. The self-metrics queue does not block,
like the host agent's, because a scrape cannot be back-pressured.

`file_storage` exports no metric for its file size or `max_size`, so volume
space and queue fill stand in for it. On macOS the WAL usually lives on the
`/System/Volumes/Data` APFS volume, not `/`.

These points travel through the very queue they warn about. While the gateway
is unreachable they wait in the WAL with everything else, so a backend sees the
queue filling only after the outage has ended, and sees the silence alert
during it, which cannot tell a laptop that is offline from one that is about to
refuse data. A deployment **must** therefore run an out-of-band watcher on the
same host: it reads the queue-fill and refused-admission series directly from
the local `:8888/metrics` endpoint and the free space of the volume holding
`OTELBOX_STORAGE_DIR`, and raises a local notification that does not depend on
the gateway. The watcher belongs to the deployment, not to this repository.

## Validate and operate

Create the header file before validation so path handling is checked too:

```console
printf 'Bearer validation-placeholder\n' > /tmp/otelbox-edge-auth-header
OTELBOX_BIND_HOST=127.0.0.1 \
OTELBOX_HEALTH_ENDPOINT=127.0.0.1:13133 \
OTELBOX_STORAGE_DIR=/tmp/otelbox-edge \
OTELBOX_UPSTREAM_ENDPOINT=127.0.0.1:14319 \
OTELBOX_UPSTREAM_AUTH_HEADER_FILE=/tmp/otelbox-edge-auth-header \
  otelcol-otelbox validate --config config/edge.yaml
```

Treat `/status` as startup health only. Delivery monitoring must include queue
size/capacity, enqueue failures and the local Collector log. Authentication
backoff logs `Exporting failed. Will retry the request after interval`; the
process and health endpoint deliberately remain green while the WAL retains
the outage. The end-to-end harness proves live credential recovery because
liveness alone cannot.
