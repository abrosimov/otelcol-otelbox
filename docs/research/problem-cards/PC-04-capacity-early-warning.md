---
note_id: "20261022120400"
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
# PC-04: Approaching storage capacity is visible before it stops admission

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | The owner's position: the system must not plan to operate at the storage limit; it must signal before a configured threshold. `file_storage` exposes no metrics, and the queue size metrics declare a misleading unit. |
| Entity of concern | The set of measurements an operator can alert on before a role's WAL stops admitting data: queue fill against `queue_size`, bbolt file size against `max_size`, and free space on the WAL volume. |
| Scope | Edge on macOS and Linux, gateway on Linux, host agent on Linux; self-telemetry routed through each role's own exporter. |
| Claim family | Method-availability claim: which signals exist now, which are missing, and where each would come from. |
| Why not a ticket | "Add disk alerts" omits which quantity reaches its limit first and how its measurement reaches the backend while the WAL is filling. |
| Acceptance probe | For each role and platform a named metric, its unit and source, a threshold below the hard limit, and a staleness rule for when the node's telemetry stops arriving. |
| Next use | Compare: put the macOS options to the owner; keep the hard-limit safety claim below as a separate test row. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| `otelcol_exporter_queue_size` and `otelcol_exporter_queue_capacity` exist; under `sizer: bytes` they report bytes although they declare `{batch}`. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:126-149` | supported by inspected source |
| filestorage exports no metric for file size, free pages, `max_size` hits or compaction; it only logs. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0` (no metrics in `internal/metadata`) | supported by inspected source |
| The Linux host agent reports `system.filesystem.usage` per mount point. | `config/host-agent.yaml` `host_metrics.filesystem` | supported by inspected source |
| Edge self-metrics leave through the same `otlp_grpc/gateway` queue whose fill they report. | `config/edge.yaml` `metrics/self` pipeline | supported by inspected source |
| At `max_size`, `Offer` returns `storage.ErrStorageFull` and the producer gets an error, not success. | `github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/filestorage@v0.162.0/client.go:345-350`; `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:308-313` | supported by inspected source |
| After a failed write the queue does not roll back its in-memory size counters, so free capacity is understated until the queue fully drains. | `go.opentelemetry.io/collector/exporter/exporterhelper@v0.162.0/internal/queue/persistent_queue.go:286-313` | supported by inspected source |
| Admission fails closed at `max_size` without losing earlier acknowledged records. | no test; see PC-13 | not yet proven |

### Interpretation

Queue fill is measurable; storage file size and volume space are not measurable from the collector itself, and on macOS no role measures them today. Because the warning travels through the queue it describes, the threshold must fire early and the backend must alert on absent data.

### Proposal

1. **Add the `filesystem` scraper to the edge profile, scoped to the WAL volume**
   - Pros: One role covers macOS; minor version; no new component.
   - Cons: Changes the edge role contract; the edge gains host-level telemetry.
2. **A macOS host agent with `macos_unified_logging` instead of `journald`**
   - Pros: Keeps roles separated; the receiver exists in contrib v0.162.0.
   - Cons: Alpha stability; no persisted cursor, so restart re-reads up to `max_log_age`; needs a fourth profile variant and macOS validation in CI.
3. **A macOS host agent without log collection**
   - Pros: Smallest change that brings host metrics to macOS.
   - Cons: Still a fourth profile variant; host logs remain uncollected on macOS.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Option choice is the owner's. The hard-limit safety claim depends on PC-13.
