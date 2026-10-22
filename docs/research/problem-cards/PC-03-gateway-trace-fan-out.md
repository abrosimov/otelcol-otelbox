---
note_id: "20261022120300"
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
# PC-03: One gateway trace request is one acknowledgement unit across two recipients

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | `config/gateway.yaml` feeds one `otlp` receiver into `traces` and `traces/selected`. `docs/reference/gateway.md` says a mixed request is acknowledged only after every required pipeline has enqueued it. No test sends a mixed request into a failing branch. |
| Entity of concern | The gateway's response to one OTLP trace request whose required recipient set is the all-signal recipient and, for resources classified `llm`, the selected-traces recipient. |
| Scope | Gateway profile; traces only; HTTP and gRPC ingress; both recipients' persistent queues. |
| Claim family | Anticipated-condition claim: the response is success only when both branches returned nil; a branch that drops every resource by filter returns nil; a failure in one branch after the other persisted yields an error and, on producer retry, a duplicate in the persisted branch, never a loss. |
| Why not a ticket | "Test fan-out" does not say which outcome is acceptable when the branches disagree. |
| Acceptance probe | A mixed request whose selected branch cannot enqueue receives no success, the all-signal branch may hold a copy, and after recovery every `llm` record reaches both recipients and every unclassified record reaches only the all-signal recipient. |
| Next use | Closed for the receiving use; the duplicate it allows is PC-11's. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| The fan-out consumer calls every pipeline in turn, without early return, and joins errors with `multierr`. | `go.opentelemetry.io/collector/internal/fanoutconsumer@v0.162.0/traces.go:47-75` | supported by inspected source |
| Pipeline order is fixed at graph build but unspecified, so which branch persists first is not defined. | service v0.162.0 graph construction (gonum map iteration) | supported by inspected source |
| A filter that removes every resource returns the skip sentinel, which `processorhelper` turns into nil. | `github.com/open-telemetry/opentelemetry-collector-contrib/processor/filterprocessor@v0.162.0/internal/condition/traces.go:109-111`; `go.opentelemetry.io/collector/processor/processorhelper@v0.162.0/traces.go:62-66` | supported by inspected source |
| A full selected-traces queue blocks the whole request; the separate log, metric and trace queues stay independent. | `TestRequiredRecipientCouplingUnderQueuePressure` | proven by test |
| A mixed request is not acknowledged while the selected-traces queue cannot take it; after recovery the classified span reaches both recipients and the unclassified span only the all-signal recipient. | `TestGatewayMixedTraceRequestIsOneAcknowledgementUnit` | proven by test |

### Interpretation

The contract is all-or-error per request with duplicates allowed in the branch that succeeded. That is consistent with at-least-once delivery and with the documented coupling.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

See PC-11 for the duplicate-delivery contract.
