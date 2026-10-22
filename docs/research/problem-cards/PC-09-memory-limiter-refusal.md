---
note_id: "20261022120900"
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
# PC-09: A memory-limiter refusal is a retryable rejection before admission

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | `memory_limiter` is first in every pipeline; no test drives a refusal, and the documentation does not state the producer-visible status. |
| Entity of concern | The producer-visible response when `memory_limiter` refuses a request before any processor or exporter sees it. |
| Scope | Edge and gateway; HTTP and gRPC. |
| Claim family | Anticipated-condition claim: a refused request is not admitted and the producer receives 503 (`Unavailable`) without `Retry-After`, not 429. |
| Why not a ticket | The refusal is correct behaviour; the question is whether producers interpret it correctly. |
| Acceptance probe | Source references; producers treat 503 as retryable. |
| Next use | Characterise; a deterministic refusal test needs a heap-pressure seam that the harness lacks, so it stays a documented limitation. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| The processor returns `ErrDataRefused`, a plain error. | `go.opentelemetry.io/collector/internal/memorylimiter@v0.162.0/memorylimiter.go:35` | supported by inspected source |
| A non-permanent error without a gRPC status maps to `Unavailable`, which HTTP maps to 503. | `go.opentelemetry.io/collector/receiver/otlpreceiver@v0.162.0/internal/errors/errors.go:15-58` | supported by inspected source |
| A refused request is never partially admitted. | memory_limiter precedes every fan-out branch | supported by inspected source |

### Interpretation

No false success. The future producer must treat 503 as retryable with its own backoff because no `Retry-After` is sent.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Heap pressure cannot be induced deterministically from outside the process.
