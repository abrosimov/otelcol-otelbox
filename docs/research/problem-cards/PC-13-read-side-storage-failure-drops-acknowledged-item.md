---
note_id: "20261022121300"
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
# PC-13: A storage failure while dispatching deletes an acknowledged item

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | Reading an item writes the queue metadata in the same transaction. When that write fails, the queue logs at Debug and deletes the item. |
| Entity of concern | `persistentQueue.getNextItem` when its `Batch(Set metadata, Get item)` returns an error, for example `ErrStorageFull` at `max_size`. |
| Scope | exporterhelper v0.162.0 persistent queue over filestorage; any role; storage at or near `max_size`, or a transient write error. |
| Claim family | Actual counterexample candidate to the safety property: an acknowledged item can become unrecoverable while durable storage is preserved. |
| Why not a ticket | The defect is upstream; the useful result is a reproduction and the operating condition that avoids it. |
| Acceptance probe | A reproduction that fills a WAL to its `max_size` with acknowledged records and then shows whether every record is delivered after the downstream returns. |
| Next use | Characterise; propose a storage-client fault-injection seam or an upstream fix that skips the item without deleting it. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| The read index advances before the storage call, so a failed read skips the item. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:357-363` | supported by inspected source |
| On a failed read the item is removed through `itemDispatchingFinish`, logged at Debug. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:372-381`, `:502-541` | supported by inspected source |
| The metadata write can need a new page, which fails with `ErrMaxSizeReached` at `max_size`. | `go.etcd.io/bbolt@v1.5.0/db.go:1164-1220`; `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:345-350` | supported by inspected source |
| Filling the edge WAL to `max_size` with the gateway down and then draining it loses no acknowledged record: dispatch waits on the first in-flight item, and deletes free space before later reads. | `TestEdgeStorageLimitKeepsAcknowledgedRecords` | proven by test |
| A read fails while new admissions take the space a delete just freed, and the item is lost. | no test; needs concurrent ingestion at the limit, which is timing-dependent | not yet proven |

### Interpretation

The drain path is safe in practice; the contended path remains a source-level hazard. While it is unrefuted, the safety property holds only while storage writes succeed, which makes PC-04's early warning a safety requirement, not only an operational convenience.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

No correction is proposed while no failing reproduction exists.
