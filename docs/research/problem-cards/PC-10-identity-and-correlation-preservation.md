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
| Next use | Characterised; the key-set obligation goes into the producer contract in the audit. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| A blocked key masks its value whatever its type: an integer under `llm.token_count.prompt` arrives as the string `****`; `redact_all_types: false` only stops value patterns from scanning non-strings. | `TestProducerIdentitySurvivesTheCollector` | proven by test |
| `gen_ai.usage.input_tokens` (GenAI semantic conventions) arrives intact, because its segment is `tokens`, not `token`. | `TestProducerIdentitySurvivesTheCollector` | proven by test |
| Redaction adds `redaction.masked.count` to a record it changed; edge and gateway each add to it. | `TestProducerIdentitySurvivesTheCollector` sink output | proven by test |
| `resource_detection` does not override existing resource attributes. | `config/edge.yaml` `override: false` | supported by inspected source |
| Trace, span, parent and link IDs and producer attributes outside the key set arrive unchanged at both recipients. | `TestProducerIdentitySurvivesTheCollector` | proven by test |

### Interpretation

Identity and correlation survive. Token usage in the OpenInference convention does not: the credential floor turns it into `****`, which a recipient such as Langfuse cannot read. A producer must use the GenAI convention or another key outside the blocked segments; widening the floor's exceptions is a redaction-contract change, not a transport one.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Redaction carriers are canonical in `docs/reference/redaction.md`.
