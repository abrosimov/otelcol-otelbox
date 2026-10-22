---
note_id: "20261022121400"
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
# PC-14: Restart re-enqueue is not atomic

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | At start, the queue reads in-flight items, deletes them and re-writes them in separate transactions. |
| Entity of concern | `persistentQueue.enqueueNotDispatchedReqs` between its delete transaction and the `putInternal` calls that re-add the items. |
| Scope | exporterhelper v0.162.0; a crash during collector start with items that were dispatched before the previous crash. |
| Claim family | Anticipated-condition claim: a second crash inside that window loses the in-flight items. |
| Why not a ticket | The window is upstream and narrow; the useful result is its classification. |
| Acceptance probe | Source references; a classification of the window as not deterministically testable with the current harness. |
| Next use | Characterise; record as an upstream issue candidate. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| Retrieve, delete and re-put are separate storage transactions. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:448-499` | supported by inspected source |
| A crash between delete and re-put loses the items. | no test; needs a crash point inside start-up | not yet proven |

### Interpretation

The window spans one start-up, after a previous crash, and only the items that were in flight. It is a real but narrow exception to the safety property.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

A deterministic test would need a fault-injection seam inside the storage client.
