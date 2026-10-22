---
note_id: "20261022120700"
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
# PC-07: An admitted record dropped as a permanent delivery error

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | The exporter logs "Exporting failed. Dropping data." and deletes the item when an error is classified as permanent. HTTP 500 and gRPC `Internal` or `Unknown` are classified as permanent. |
| Entity of concern | The classification, in the outer exporterhelper of each wrapper, of a recipient's response as permanent, and the deletion of the admitted item that follows. |
| Scope | All network exporters of edge and gateway; gRPC and HTTP recipients; v0.162.0 exporters and the local wrappers. |
| Claim family | Actual counterexample to eventual delivery: durable admission does not imply delivery. A recipient response classified as permanent ends delivery for that item, which violates the liveness condition "no permanent delivery error" even when the recipient's failure was transient. |
| Why not a ticket | "Retry more" does not say which responses are transient for a given recipient. |
| Acceptance probe | A recipient double that answers 500 once and then succeeds: the record is either delivered or the loss is visible in `otelcol_exporter_send_failed_*` and the log. |
| Next use | Owner decision on reclassification; the test pins today's behaviour and must change with it. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| A permanent error skips retry and the item is deleted with an Error log. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/retry_sender.go:98-100`; `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue_sender.go:57` | supported by inspected source |
| gRPC: only `Canceled`, `DeadlineExceeded`, `Aborted`, `OutOfRange`, `Unavailable`, `DataLoss`, and `ResourceExhausted` with `RetryInfo`, are retryable. | `go.opentelemetry.io/collector/exporter/otlpexporter@v0.162.0/otlp.go:173-221` | supported by inspected source |
| HTTP: only 429, 502, 503 and 504 are retryable; 500 is permanent. | `go.opentelemetry.io/collector/exporter/otlphttpexporter@v0.162.0/otlp.go:176-277` | supported by inspected source |
| The wrappers reclassify only `Unauthenticated` and `PermissionDenied` (401 and 403). | `internal/exporters/*/factory.go` `classifyError` | supported by inspected source |
| One HTTP 500 from the selected-traces recipient drops an admitted span: no retry within 15 s, `otelcol_exporter_send_failed_spans` rises and "Dropping data" is logged. | `TestRecipientHTTP500DropsAnAdmittedRecord` | proven by test |

### Interpretation

This is the main gap between durable admission and eventual delivery. Admission holds; delivery depends on recipients never answering a transient failure with 500, `Internal` or `Unknown`.

### Proposal

1. **Keep upstream classification and document it as a recipient obligation**
   - Pros: No code; matches upstream.
   - Cons: Recipients such as Langfuse can answer 500 under load.
2. **Reclassify HTTP 500 and gRPC `Internal`/`Unknown` as retryable in the wrappers, with a bounded interval**
   - Pros: Uses the existing wrapper seam.
   - Cons: A genuinely invalid payload answered with 500 can pin a queue; needs a bound and alerting.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Recipient behaviour is outside this repository; the choice is the owner's.
