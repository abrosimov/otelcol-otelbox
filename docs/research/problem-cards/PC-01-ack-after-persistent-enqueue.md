---
note_id: "20261022120100"
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
# PC-01: The OTLP success response follows the persistent enqueue

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | The repository asserts that an accepted record reaches the WAL before success is returned (README.md, AGENTS.md). Black-box evidence covers edge logs and gateway ingress over OTLP/HTTP only. |
| Entity of concern | The ordering, inside one edge or gateway process, between the producer-facing OTLP success response and the commit of the same request to every required persistent sending queue. |
| Scope | Collector core v0.162.0 / v1.68.0 as resolved by `builder.yaml`; `config/edge.yaml` and `config/gateway.yaml`; OTLP/HTTP and OTLP/gRPC; logs, metrics and traces. |
| Claim family | Anticipated-condition claim about the implementation: success is returned only after `ConsumeX` of every required exporter has returned nil, and that return follows a committed storage transaction. |
| Why not a ticket | "Add more durability tests" names Work, not the property. This card fixes which ordering the tests must observe. |
| Acceptance probe | For each role, protocol and signal: a record acknowledged while every downstream is down survives SIGKILL and is delivered after restart. Source references show no buffer between receiver and queue. |
| Next use | Closed for the receiving use: the failure matrix rows cite the tests. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| The OTLP/HTTP handler calls `Export` synchronously and writes 200 only after it returns nil. | `go.opentelemetry.io/collector/receiver/otlpreceiver@v0.162.0/otlphttp.go:46-57` (traces; logs and metrics at :77 and :108) | supported by inspected source |
| `Export` calls the next consumer directly and maps its error to a status. | `go.opentelemetry.io/collector/receiver/otlpreceiver@v0.162.0/internal/trace/otlp.go:33-56` | supported by inspected source |
| No processor in a role pipeline buffers: `processorhelper` tail-calls the next consumer; redaction, resource detection and filter start no goroutine and own no channel. | `go.opentelemetry.io/collector/processor/processorhelper@v0.162.0/traces.go:62-71`; the batch processor is not linked (`builder.yaml`) | supported by inspected source |
| The exporter path ends in the persistent queue `Offer`, which returns after one storage `Batch` containing the item and the advanced write index. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queuebatch/queue_batch.go:101`; `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:272-319` | supported by inspected source |
| `sending_queue.batch` batches on the consumer side of the queue, so it does not hold unpersisted data. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queuebatch/queue_batch.go:74` | supported by inspected source |
| Edge logs over HTTP survive SIGKILL immediately after 200. | `TestEdgePersistsAcceptedDataBeforeAcknowledgement` | proven by test |
| Gateway logs, metrics, traces and selected traces over HTTP survive SIGKILL immediately after 200. | `TestGatewayPersistsOrdinarySignalsBeforeAcknowledgement`, `TestGatewayPersistsSelectedTraceBeforeAcknowledgement` | proven by test |
| Edge spans and metric points over HTTP, and edge and gateway logs, spans and metric points over OTLP/gRPC, survive SIGKILL immediately after success. | `TestEdgePersistsAcceptedDataBeforeAcknowledgement`, `TestGatewayPersistsOrdinarySignalsBeforeAcknowledgement` (both extended in this audit) | proven by test |
| Two hundred records admitted concurrently by eight producers survive SIGKILL without loss or overwrite. | `TestEdgeConcurrentAdmissionKeepsEveryRecord` | proven by test |

### Interpretation

Source and tests now agree for both roles, all three signals and both protocols. The selected-traces recipient is proven for traces over HTTP, the only signal it receives.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Upstream paths are relative to the Go module cache. The durability of the committed transaction itself is PC-02.
