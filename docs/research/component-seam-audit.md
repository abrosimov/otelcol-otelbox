---
note_id: "20261116120000"
created: 2026-11-16
language: en
type: note
subtype: agent-artifact
retention: active
note_role: artifact
artifact_kind: research
publication_status: published
published_at: 2026-11-16
status: active
authorship: agent
assistant: Claude Code
source_refs:
  - "go.opentelemetry.io/collector/exporter/exporterhelper (fork, abrosimov/opentelemetry-collector@a63a99179e69) internal/queue, internal/queuebatch"
  - "go.opentelemetry.io/collector/receiver/otlpreceiver@v0.164.0 internal/trace/otlphttp.go, errors.go"
  - "go.opentelemetry.io/collector/exporter/otlphttpexporter@v0.164.0 otlp.go"
  - "github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.164.1 client.go, extension.go"
  - "go.etcd.io/bbolt@v1.5.0 db.go, node.go, tx.go"
related:
  - "network-outage-loss-review.md"
  - "problem-cards/PC-03-gateway-trace-fan-out.md"
  - "problem-cards/PC-07-permanent-error-after-admission.md"
  - "problem-cards/PC-13-read-side-storage-failure-drops-acknowledged-item.md"
---
# Research — Loss at the seams between Collector components

## Objective

Find where components that are each correct alone lose, stall or multiply an
acknowledged record when composed as the three role profiles compose them:
receiver, processors, fan-out, persistent queue, batcher, retry, timeout, the
exporter wrappers, file_storage and bbolt. Four independent read-only audits
covered the exporter side, the ingress side, the queue–storage seam and the
viability of a WAL owned by the wrappers.

## Output

### Finding 01 — The WAL, batcher, retry and shutdown chain holds

An item leaves the WAL only when every part of it reached a final outcome, a
merged or split item gets exactly one done callback, and a graceful shutdown
keeps every unfinished item. No ingress path acknowledges before persistence;
every transient refusal reaches the producer as gRPC `Unavailable` or HTTP 503.

### Evidence

- **Claim:** one done per item after all parts; shutdown keeps the item.
- **Proof:** `internal/queuebatch/partition_batcher.go` (`refCountDone`,
  `multiDone`), `retry_sender.go` (`NewShutdownErr`), `persistent_queue.go`
  `onDone`; overlay tests split-then-shutdown, permanent-part-plus-shutdown,
  held tail at shutdown and shutdown inside an attempt, each followed by a
  restart on the same storage.
- **What this proves:** the core admission and deletion contract under the
  configured sizes.

### Finding 02 — Seams that lost, stalled or multiplied data

| # | Seam | Outcome | Status |
|---|---|---|---|
| 1 | Queue metadata key shares bbolt leaves with large items; every read and delete rewrites a multi-MiB leaf, which a fragmented file at `max_size` cannot place. Reads stall; a failed delete leaves a delivered item orphaned. | STALL, DUPLICATE | Margin documented: `max_size` ≥ `queue_size` + 256 MiB. Structural fix open. |
| 2 | file_storage keeps a closed database after a failed reopen, and `GetClient` returns a client whose start-up compaction failed; start-up compaction needs free space equal to the live WAL. | STALL | Open. |
| 3 | A permanent error on a merged batch deletes every item merged into it. | LOSS | Open: a WAL that retries a failed merged batch item by item would confine it. |
| 4 | The upstream HTTP exporter calls every 4xx but 429 permanent; gRPC `NotFound`, `FailedPrecondition`, `Unimplemented` likewise. | LOSS | Fixed: `retry_on_client_error`; `TestRecipientRouteRefusalIsRetriedUntilDelivered`. |
| 5 | Gateway OTLP/HTTP `read_timeout` answers a slow body with HTTP 400; `write_timeout` drops the 200 of a late enqueue. | LOSS, DUPLICATE | Fixed: both zero; `TestGatewayHTTPIngressWaitsForASlowBody`, `TestGatewayHTTPIngressAcknowledgesALateEnqueue`. |
| 6 | An item that does not decode is deleted at Debug level and its size stays counted. | LOSS on corruption | Open. |
| 7 | One queue reader feeds `num_consumers` workers; that many batches failing retryably stall the signal's whole WAL. | STALL | Open, by design today. |
| 8 | A recipient that stores a request but answers after `timeout` is retried without bound. | DUPLICATE | Open. |
| 9 | Fan-out joins branch errors, so one permanent branch error turns the whole request into HTTP 500 / `Internal`. No branch returns one today. | latent LOSS | Open. |

### Evidence

- **Claim (1):** the stall reproduces at small caps and not at the defaults.
- **Proof:** a simulation of the fork's put, read and delete transactions on
  bbolt v1.5.0 with filestorage's options stalled at `max_size` 96 and 128 MiB
  (ratio 1.5, items up to 1.5 MiB), orphaned eight delivered items at 192 MiB,
  and stayed clean at 768 MiB; bbolt `db.go` `allocate`, `node.go` `splitTwo`.
- **Claim (4, 5):** each failed before its fix and passes after it.
- **Proof:** the three tests named above, run against the built binary with
  the fix removed and restored.
- **What this proves:** 4 and 5 are closed; 1 is mitigated by configuration,
  not removed.

### Interpretation

- Every loss found sits where one component's error classification or
  resource assumption is applied to another's data: a batch outcome to items
  that did not cause it, a status to a route rather than a payload, a server
  timeout to a body still arriving, a metadata write to a leaf full of items.
- Items 1, 3, 6 and 7 live inside the persistent queue and batcher. They can
  be fixed in the fork or in a WAL the wrappers own; the latter audit found
  that design viable with caveats on shutdown ordering, item size against the
  in-memory queue and the loss of exporterhelper's queue telemetry.

### Proposal

1. **Fix 1, 3, 6 and 7 in the WAL the wrappers would own.**
   - Pros: no fork; metadata kept apart from items; item-wise retry after a
     failed merged batch; quarantine instead of silent deletion.
   - Cons: the wrappers take over WAL code, telemetry and on-disk format.
2. **Fix them in the fork.**
   - Pros: exporterhelper's telemetry stays meaningful.
   - Cons: four more fork commits, each re-based on every Collector release.

## Sources And Limits

- Locators are given with each claim; the audits read the module sources the
  built binary links.
- The 256 MiB margin is the slack the simulation found safe, not a bound.
- Findings 2, 6, 8 and 9 rest on source reading, not on a reproduction.
