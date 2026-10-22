---
note_id: "20261022130000"
created: 2026-10-22
type: note
subtype: agent-artifact
language: en
retention: active
note_role: artifact
artifact_kind: research
publication_status: draft
status: active
authorship: agent
assistant: Claude Code
---
# Research: Durable-admission failure matrix

## Objective

Record, for each fault the audit considered, where it is injected, what a
producer and an operator observe, which safety invariant must hold, what
recovery is expected, and which test holds it. The analysis behind each row is
in [the audit](durable-admission-audit.md) and the linked problem card.

## Output

### Finding 01: matrix

Safety invariant S: an acknowledged record remains recoverable after a
restart with durable storage preserved, or its required delivery has already
completed. Liveness L: an admitted record is eventually delivered when every
recipient becomes available, capacity suffices and no permanent delivery
error occurs. "Pass" means the test holds the invariant on the pinned
v0.162.0 build; "violated" means the test demonstrates the invariant does not
hold and pins that behaviour.

| # | Fault and injection point | Expected observable behaviour | Safety | Recovery / liveness | Test | Result | Remaining limitation | Card |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Gateway unreachable while the edge admits logs, spans and metric points over HTTP and gRPC | Edge answers 200 / gRPC OK; records wait in the edge WAL | S holds | Delivered once the gateway returns | `TestEdgePersistsAcceptedDataBeforeAcknowledgement` | Pass | Producer-leg compression not exercised | [PC-01](problem-cards/PC-01-ack-after-persistent-enqueue.md), [PC-12](problem-cards/PC-12-grpc-ingress-untested.md) |
| 2 | Required recipient unreachable while the gateway admits every signal over HTTP and gRPC | Gateway answers 200 / OK; records wait in each signal's WAL | S holds | Delivered once the recipient returns | `TestGatewayPersistsOrdinarySignalsBeforeAcknowledgement`, `TestGatewayPersistsSelectedTraceBeforeAcknowledgement` | Pass | — | [PC-01](problem-cards/PC-01-ack-after-persistent-enqueue.md) |
| 3 | SIGKILL immediately after the producer's acknowledgement (edge and gateway) | Restarted process replays the WAL | S holds | Delivered after restart | Rows 1 and 2 (each kills with SIGKILL, not SIGTERM) | Pass | — | [PC-01](problem-cards/PC-01-ack-after-persistent-enqueue.md) |
| 4 | Persistent queue full (`block_on_overflow: true`) | The request blocks and ends in a client timeout or 503; no 200 | No false success | Admission resumes when the queue drains | `TestRequiredRecipientCouplingUnderQueuePressure`, `TestGatewayMixedTraceRequestIsOneAcknowledgementUnit` | Pass | A request larger than `queue_size` blocks until its deadline on every retry (source only) | [PC-05](problem-cards/PC-05-blocked-admission-and-deadlines.md) |
| 5 | Storage reaches `file_storage` `max_size` (stand-in for disk full) | HTTP 503 "the storage extension has run out of available space"; no 200 | S holds on the drain path | All records acknowledged before the limit are delivered | `TestEdgeStorageLimitKeepsAcknowledgedRecords` | Pass | Real ENOSPC not injected: bbolt rolls back a failed commit (source). The read-while-full path of PC-13 is not refuted | [PC-04](problem-cards/PC-04-capacity-early-warning.md), [PC-13](problem-cards/PC-13-read-side-storage-failure-drops-acknowledged-item.md) |
| 6 | Crash during queue persistence | Either the whole item and its index are committed or neither is | S holds by construction | — | None; one bbolt `Update` per enqueue with `fdatasync` of data then meta (source) | Not testable deterministically | Power loss relies on the device honouring the flush | [PC-02](problem-cards/PC-02-storage-commit-and-fsync-boundary.md) |
| 7 | Crash during compaction | Original file or complete copy survives; temporary file removed at start | S holds for process crash (source) | — | None | Not proven | Rebound compaction needs a WAL of at least 100 MiB; the directory is not synced after rename, so power loss can roll the file back | [PC-06](problem-cards/PC-06-compaction-crash-safety.md) |
| 8 | Lost downstream acknowledgement (recipient received, gateway killed before the answer) | Restarted gateway sends the record again | S holds | Delivered at least once; a duplicate is expected | `TestGatewayResendsWhenRecipientAcknowledgementIsLost` | Pass | Recipients must deduplicate | [PC-11](problem-cards/PC-11-at-least-once-duplicate-windows.md) |
| 9 | Concurrent ingestion from several producers, then SIGKILL | Every request answered 200; no record overwritten | S holds | All delivered after restart | `TestEdgeConcurrentAdmissionKeepsEveryRecord` | Pass | — | [PC-01](problem-cards/PC-01-ack-after-persistent-enqueue.md) |
| 10 | Mixed classified and unclassified trace request while the selected-traces queue is full | No acknowledgement; producer retries | S holds; the all-signal branch may hold a duplicate | Classified span reaches both recipients, unclassified only the all-signal one | `TestGatewayMixedTraceRequestIsOneAcknowledgementUnit` | Pass | Branch order is unspecified | [PC-03](problem-cards/PC-03-gateway-trace-fan-out.md) |
| 11 | Recipient answers HTTP 500 once | The gateway retries on the ordinary backoff; nothing counted as failed or dropped | S holds | Delivered on the next attempt | `TestRecipientHTTP500IsRetriedUntilDelivered`; wrapper unit tests for gRPC `Internal`/`Unknown` and HTTP 5xx versus 4xx | Pass after the `retry_on_server_error` correction; violated on 2.4.2 | A deterministic server error holds the queue until the queue alert; HTTP 400/413 stay permanent | [PC-07](problem-cards/PC-07-permanent-error-after-admission.md) |
| 12 | Recipient answers partial success | "Partial success response" logged; counted as sent; no retry | S holds | Rejected records are not delivered and not counted as failed | `TestRecipientPartialSuccessCountsAsDelivered` | Characterised | Visible only in logs | [PC-08](problem-cards/PC-08-recipient-partial-success.md) |
| 13 | `memory_limiter` refuses before any processor | HTTP 503 / gRPC `Unavailable`, no `Retry-After` | No false success | Producer retries | None; heap pressure cannot be induced from outside | Source only | — | [PC-09](problem-cards/PC-09-memory-limiter-refusal.md) |
| 14 | Producer cancels or times out while the request is blocked | Producer sees an error; the request may still be persisted later | No false success; possible duplicate | Producer retry | Covered by rows 4 and 10 for the timeout; late persistence is source only | Partly proven | HTTP `write_timeout` does not cancel the handler | [PC-05](problem-cards/PC-05-blocked-admission-and-deadlines.md) |
| 15 | Second crash during restart re-enqueue of in-flight items | In-flight items lost | **S can be violated** in that window | — | None; needs a crash point inside start-up | Not proven (source hazard) | Needs a storage fault-injection seam | [PC-14](problem-cards/PC-14-restart-re-enqueue-window.md) |
| 16 | Storage write fails while dispatching an item | Item deleted with a Debug log | **S can be violated** | — | Row 5 refutes the drain path only | Not proven | Concurrent admission at the limit is timing-dependent | [PC-13](problem-cards/PC-13-read-side-storage-failure-drops-acknowledged-item.md) |
| 17 | Producer attributes and identifiers through edge and gateway | IDs, parent, links and attributes unchanged; blocked keys masked whatever the type | Not a durability fault | — | `TestProducerIdentitySurvivesTheCollector` | Pass; convention token keys exempt, other blocked keys masked | The exemption list is held to the conventions by the `redaction-conventions` workflow | [PC-10](problem-cards/PC-10-identity-and-correlation-preservation.md) |

## Sources And Limits

Each "Test" cell names a function in `test/harness/`; the workflow's "Run the
end-to-end harness" step runs all of them. Rows marked source only rest on the
upstream references in the linked card. The matrix covers edge and gateway;
the host agent deliberately drops new samples when its queue is full and is
outside the acknowledgement contract.
