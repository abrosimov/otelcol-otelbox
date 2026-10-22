---
note_id: "20261022120800"
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
# PC-08: A recipient's partial success is counted as delivery

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | Neither wrapper handles OTLP `partial_success`; the repository never mentions it. |
| Entity of concern | The exporter's handling of a successful OTLP response that reports rejected spans, data points or log records. |
| Scope | All network exporters of edge and gateway; gRPC and HTTP; v0.162.0 exporters. |
| Claim family | Anticipated-condition claim: rejected records in a partial-success response are logged at Warn and the request counts as sent, so they are dropped without retry. |
| Why not a ticket | Partial success is a protocol outcome, not a failure the queue can retry. |
| Acceptance probe | A recipient double answering `partial_success` with rejected items: the drop is visible only in the log and is not counted as `send_failed`. |
| Next use | P2W-ready: test with the HTTP backend double; document the obligation on recipients and producers. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| Both OTLP exporters log a partial-success response and return nil. | `go.opentelemetry.io/collector/exporter/otlpexporter@v0.162.0/otlp.go:103-110`, `:123-130`, `:143-150`; `go.opentelemetry.io/collector/exporter/otlphttpexporter@v0.162.0/otlp.go:111`, `:131`, `:152` (partial-success handlers) | supported by inspected source |
| The receiver never sets `partial_success` towards the producer. | `go.opentelemetry.io/collector/receiver/otlpreceiver@v0.162.0` (no non-test reference) | supported by inspected source |
| Records rejected by a recipient are not retried and not counted as failed. | no test | not yet proven |

### Interpretation

Records a recipient rejects are a delivery outcome outside the admission contract. They must be visible to operators; today they are visible only in logs.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Recipients and producers own the reaction to rejected records.
