---
note_id: "20261022121100"
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
# PC-11: Where an admitted record can be delivered twice

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | The repository states at-least-once delivery; the windows that produce duplicates are not enumerated or tested. |
| Entity of concern | The set of crash and failure points at which an admitted record is re-sent after a recipient already accepted it. |
| Scope | Edge and gateway exporters; process crash, shutdown, fan-out partial failure, producer retry. |
| Claim family | Anticipated-condition claim: duplicates arise after a recipient accepts a request but before the item is deleted, on fan-out partial failure, and when a blocked request is persisted after its producer retried; no window loses an item through these paths. |
| Why not a ticket | Exactly-once is out of scope; the useful result is the list of windows and the idempotency obligation they place on recipients. |
| Acceptance probe | A test where the recipient accepts and the edge is killed before deletion, showing a re-send and no loss. |
| Next use | Characterised; recipient idempotency is a producer and recipient obligation in the audit. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| The item is deleted only after export returns. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:394-426` | supported by inspected source |
| Dispatched items are re-enqueued at restart. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:151-160`, `:448-499` | supported by inspected source |
| No delivery ordering is guaranteed after restart. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:448-499` | supported by inspected source |
| A record the recipient received before the gateway died, without seeing the answer, is sent again by the restarted gateway. | `TestGatewayResendsWhenRecipientAcknowledgementIsLost` | proven by test |

### Interpretation

Recipients must deduplicate by a producer-supplied identity, such as span ID or a log record attribute.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

See PC-03 and PC-05 for the fan-out and late-persist windows.
