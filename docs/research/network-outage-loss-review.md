---
note_id: "20261112220000"
created: 2026-11-12
language: en
type: note
subtype: agent-artifact
retention: active
note_role: artifact
artifact_kind: research
publication_status: published
published_at: 2026-11-12
status: active
authorship: agent
assistant: Claude Code
source_refs:
  - "github.com/abrosimov/opentelemetry-collector@e29a212a5ddb exporter/exporterhelper/internal/queue/persistent_queue.go"
  - "go.opentelemetry.io/collector/exporter/otlpexporter@v0.164.0 factory.go"
  - "google.golang.org/grpc@v1.85.0 internal/transport/http2_client.go"
related:
  - "problem-cards/PC-03-gateway-trace-fan-out.md"
  - "problem-cards/PC-11-at-least-once-duplicate-windows.md"
  - "problem-cards/PC-13-read-side-storage-failure-drops-acknowledged-item.md"
  - "problem-cards/PC-14-restart-re-enqueue-window.md"
---
# Research — Loss and duplication across a network outage of minutes to hours

## Objective

Establish where an edge or gateway loses or multiplies telemetry while a
network leg is down for minutes or hours, and record the mitigation each path
received. A useful answer separates paths that lose an acknowledged record from
paths that only delay or duplicate it.

## Output

### Finding 01 — An acknowledged record survives the outage itself

Retry has no deadline, server errors are retried, an item leaves the WAL only
after its export ends, and a shutdown during a retry keeps it. Loss during an
outage happens only at the edges of that guarantee: a full queue (Finding 05),
a full storage (Finding 02) and a restart window (Finding 03).

### Evidence

- **Claim:** `max_elapsed_time: 0s` disables the retry deadline.
- **Proof:** forked `exporterhelper` `internal/retry_sender.go`, `Send`: the
  deadline is set only when `MaxElapsedTime > 0`.
- **What this proves:** no elapsed time ends delivery of an admitted item.

### Finding 02 — A full storage no longer deletes an accepted item

Upstream deleted an item whose dispatch metadata could not be written
([PC-13](problem-cards/PC-13-read-side-storage-failure-drops-acknowledged-item.md)).
The fork rolls the read back and reads the item again after one second. A
storage at its cap now stalls the queue until finished items free space; it no
longer loses data.

### Evidence

- **Claim:** a failed dispatch leaves the item at the read index.
- **Proof:** fork `persistent_queue.go`, `getNextItem` and
  `waitAfterDispatchFailure`; `TestPersistentQueue_FailedDispatchKeepsItem`,
  `TestPersistentQueue_FailedDispatchKeepsItemAcrossRestart`.
- **What this proves:** the read-side counterexample of PC-13 is closed in the
  linked binary. Upstream is unchanged until the fork's change is accepted.

### Finding 03 — The start-up move of in-flight items is one transaction

Upstream deleted and re-added in-flight items in separate transactions
([PC-14](problem-cards/PC-14-restart-re-enqueue-window.md)). The fork copies
their stored values, deletes the old keys and writes the metadata in one batch;
a failure leaves them for the next start.

### Evidence

- **Claim:** the move is atomic.
- **Proof:** fork `persistent_queue.go`, `enqueueNotDispatchedReqs`;
  `TestPersistentQueue_FailedRequeueOnStartKeepsItems`.
- **What this proves:** a crash during start-up no longer loses in-flight items.

### Finding 04 — Without keepalive a silent break holds a dead connection

The pinned OTLP gRPC exporter disables keepalive by default, and grpc-go sets
`TCP_USER_TIMEOUT` only when keepalive is configured. An attempt's timeout
cancels the request but not the connection, so after a break without a reset
every retry used the dead connection until the kernel abandoned it, about
fifteen minutes on Linux. No data was lost; the outage lasted longer than the
network's. Edge and host agent now ping every 30 s and the gateway receiver
permits it.

### Evidence

- **Claim:** keepalive is off by default and governs `TCP_USER_TIMEOUT`.
- **Proof:** `otlpexporter@v0.164.0/factory.go:47`
  (`Keepalive = configoptional.None`); `grpc@v1.85.0`
  `internal/transport/http2_client.go:286` (`if kp.Time != infinity`).
- **What this proves:** the mechanism. Not proven by a test: the harness has no
  network namespace in which to drop packets silently. This is an evidence gap.

### Finding 05 — Queue capacity bounds the outage; compression stretches it

A full edge queue refuses with a retryable status, and the producer's SDK
budget decides what is lost. The queue counted uncompressed bytes against a
disk that held them uncompressed. With `storage_compression: zstd` the queue
counts and stores compressed bytes, so the same disk budget holds the outage
volume divided by the compression ratio. The edge and gateway self metrics now
use a queue of their own, so the signals reporting a full queue are not
refused with it.

### Evidence

- **Claim:** under `zstd`, `queue_size` counts stored bytes.
- **Proof:** fork `persistent_queue.go`, `bytesSizeOf`;
  `TestPersistentQueue_CompressedBytesSizerCountsStoredBytes`.
- **What this proves:** capacity and `max_size` share one unit. The ratio for a
  given workload is not measured here.

### Finding 06 — A selected-recipient outage multiplied ordinary traces

While the selected-traces queue is full, a mixed request may be accepted by the
all-signal branch and then fail at its producer's deadline
([PC-03](problem-cards/PC-03-gateway-trace-fan-out.md)). An edge re-sends its
two in-flight batches about every 40 s, so the all-signal trace WAL received
about 2 × 1.5 MiB per 40 s per edge, some 270 MiB an hour, for the whole outage.
`span_dedup/traces`, held in memory and refreshed on every repeat, removes the
copies.

### Evidence

- **Claim:** each copy after the first is dropped.
- **Proof:** `TestGatewayRetriedMixedRequestReachesTheOrdinaryRecipientOnce`;
  `TestRequestReSentDuringFreshTrafficReachesTheNextConsumerOnce`.
- **What this proves:** the amplification is gone for traces. The rate figure
  is derived from the profile's batch size, consumers and backoff, not measured.

### Interpretation

- Only Finding 05 still loses data, and only once the capacity a deployment
  bought is exhausted and the producer gives up. The rest now delay or
  deduplicate.
- Logs and metrics have one recipient pipeline each, so fan-out does not copy
  them and no content-hash deduplication is needed.

### Proposal

1. **Measure the compression ratio per workload** from
   `otelcol_exporter_enqueue_size_bytes` against `otelcol_exporter_queue_size`
   and resize from it.
   - Pros: the budget becomes a measurement.
   - Cons: needs one observed outage.
2. **Prove keepalive with a packet-drop test** in a network namespace.
   - Pros: closes the evidence gap of Finding 04.
   - Cons: needs privileges the harness does not have.

## Sources And Limits

- Source locators are given with each claim; the fork commit is named in
  `builder.yaml`.
- The 270 MiB an hour figure and the fifteen-minute retransmission timeout are
  derived, not measured.
- The fork's changes are not yet in an upstream release.
