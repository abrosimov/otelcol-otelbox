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
| Next use | Closed by the owner's decision: exempt the convention token keys through `ignored_keys` and check the list against the conventions in CI. |

### Evidence

| Claim | Proof | Status |
| --- | --- | --- |
| On 2.4.2 a blocked key masked its value whatever its type: an integer under `llm.token_count.prompt` arrived as the string `****`; `redact_all_types: false` only stops value patterns from scanning non-strings. | `TestProducerIdentitySurvivesTheCollector` | proven by test |
| `gen_ai.usage.input_tokens` (GenAI semantic conventions) arrives intact, because its segment is `tokens`, not `token`. | `TestProducerIdentitySurvivesTheCollector` | proven by test |
| Redaction adds `redaction.masked.count` to a record it changed; edge and gateway each add to it. | `TestProducerIdentitySurvivesTheCollector` sink output | proven by test |
| `resource_detection` does not override existing resource attributes. | `config/edge.yaml` `override: false` | supported by inspected source |
| Of the current conventions, OpenInference `llm.token_count.*` and `decision.token_count.*`, GenAI `gen_ai.token.modality` and the deprecated `gen_ai.token.type` match the blocked-key patterns; OpenInference `message_content.signature` and `tool_call.reasoning_signature` do too. Langfuse reads `llm.token_count.*` for usage. | OpenInference Python semantic conventions v0.1.41; semantic-conventions-genai `9ffc7e0`; semantic-conventions v1.44.0 deprecated GenAI registry; Langfuse OpenTelemetry docs | supported by inspected source |
| `ignored_keys` now exempts the thirteen token keys exactly; the two signatures stay masked as recorded exceptions; an integer and a string under `llm.token_count.*` and `gen_ai.token.type` arrive intact, and an integer under an unexempted blocked key is still masked. | `config/*.yaml` `redaction/secrets`; `tools/ci/redaction-exceptions.tsv`; `TestProducerIdentitySurvivesTheCollector` | proven by test |
| `otelbox-ci redaction check` fails when a current convention adds a key the patterns would mask, or drops one the list exempts. | `tools/ci/internal/contract/redaction_test.go`; `.github/workflows/redaction-conventions.yml` | proven by test |
| Trace, span, parent and link IDs and producer attributes outside the key set arrive unchanged at both recipients. | `TestProducerIdentitySurvivesTheCollector` | proven by test |

### Interpretation

Identity, correlation and convention token usage now survive. An ignored key is not value-scanned, so the exemptions are exact names of numeric or enumerated attributes, never patterns; any other key with a blocked segment is still masked whatever its type.

## Sources And Limits

Upstream paths are module paths in the Go module cache at the versions
`builder.yaml` resolves. Status values follow the audit's evidence scale:
proven by test, supported by inspected source, assumed from external
contract, not yet proven.

Redaction carriers are canonical in `docs/reference/redaction.md`.
