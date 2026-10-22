---
note_id: "20261022121000"
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
# PC-10: Producer identity and correlation survive the collector unchanged

## Objective

Record one problem-side claim of the durable-admission audit as an FPF C.22.2
ProblemCard in its thin form, with the evidence that currently supports it.

## Output

### Finding 01: problem card

| Field | Value |
| --- | --- |
| Signal | A future Go engine will rely on OTLP trace and span identifiers, parent links and its own attributes. The collector redacts by key and value pattern and adds resource attributes on the edge. |
| Entity of concern | The fields a producer sets for identity and correlation in one OTLP span or log record, compared at ingress and at the recipient. |
| Scope | Edge then gateway to both recipients; traces and logs; `redaction/secrets`, `resource_detection` with `override: false`, `filter/selected_traces`. |
| Claim family | Anticipated-condition claim: trace ID, span ID, parent span ID, links, and attributes that match no redaction pattern arrive byte-identical; the collector adds only generic resource attributes and redaction summary counts. |
| Why not a ticket | "Support the new engine" does not name the fields that must survive. |
| Acceptance probe | An end-to-end test that compares identifiers, links and a set of producer attributes, including token-count integers, at the recipient. |
| Next use | P2W-ready: harness test; document which attribute keys the redaction floor would mask. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| Redaction masks string values of keys whose segment is `token`, `auth`, `secret` and others; `redact_all_types: false` leaves integers intact. | `config/*.yaml` `redaction/secrets` (SHARED) | supported by inspected source |
| `resource_detection` does not override existing resource attributes. | `config/edge.yaml` `override: false` | supported by inspected source |
| Identifiers, parent links and span links arrive unchanged at both recipients. | no test | not yet proven |

### Interpretation

A string attribute such as `llm.token_count.prompt` would be masked if a producer sent it as a string. Producers must keep identifiers out of the redaction key set.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Redaction carriers are canonical in `docs/reference/redaction.md`.
