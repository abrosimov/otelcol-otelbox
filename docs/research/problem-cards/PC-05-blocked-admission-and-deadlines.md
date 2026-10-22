---
note_id: "20261022120500"
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
# PC-05: A blocked enqueue ends in an error, never in a late success

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | `docs/reference/gateway.md` says an item larger than the queue blocks until its context expires. The interaction of HTTP server timeouts, client deadlines and the blocking queue is not otherwise stated. |
| Entity of concern | One OTLP request held in `persistentQueue.Offer` by `block_on_overflow: true`, and the producer-visible outcome when its context ends. |
| Scope | Edge and gateway; HTTP and gRPC; queue full, request larger than `queue_size`, client cancellation, server `write_timeout`. |
| Claim family | Anticipated-condition claim: a request whose context ends while blocked returns an error and persists nothing; a request larger than `queue_size` can never be admitted and fails only by deadline. |
| Why not a ticket | "Handle timeouts" does not distinguish a lost request from one that is persisted after the producer stopped waiting. |
| Acceptance probe | A request that cannot fit gets no success within a finite deadline, and the collector keeps serving other requests. |
| Next use | P2W-ready: test a request above an overlay's small `queue_size` and below the receiver's size limit. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| A blocked `Offer` returns `ctx.Err()` when the caller's context ends, before any size or storage change. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:276-284`; `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/cond.go:43-63` | supported by inspected source |
| A request larger than capacity is not rejected up front; it waits until its context ends. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:276-284` | supported by inspected source |
| The receiver passes the request context unchanged; the HTTP server's `write_timeout` does not cancel it. | net/http server behaviour | assumed from external contract |
| A full queue makes ingest time out while the process stays healthy. | `TestRequiredRecipientCouplingUnderQueuePressure` | proven by test |

### Interpretation

No path turns a blocked request into a false success. A request that outlives its producer may still be persisted later, which is a duplicate under PC-11. A producer without a deadline can wait indefinitely on an oversized request.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Producers must set a deadline; the future Go engine must not send a request larger than the edge `queue_size`.
