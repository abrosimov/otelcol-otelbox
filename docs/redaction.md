# Credential redaction boundary

`redaction/secrets` is defence in depth against credentials accidentally
entering telemetry. It is not a general PII scrubber and it does not make an
arbitrary payload safe. Producers remain responsible for not recording secrets;
the collector masks common mistakes that fit the bounded contract below.

The same processor block is byte-identical in all three role profiles and is
the last attribute-changing processor before export. A matching key masks its
whole value. A matching value pattern masks only the matching substring. The
replacement is `****`; values are never hashed into a stable identifier.

## Covered carriers

| Signal | Inspected | Not inspected |
| --- | --- | --- |
| Traces | Resource, instrumentation-scope, span and span-event attributes. | Span and event names, status messages, trace state and span-link attributes. |
| Logs | Resource, instrumentation-scope and record attributes; scalar bodies by value; map and slice bodies recursively by map key and scalar leaf value. | Metadata fields outside attributes and body. An arbitrary secret that matches no value pattern remains ordinary free text. |
| Metrics | Resource, instrumentation-scope and datapoint attributes. | Metric name, description and unit, and exemplar filtered attributes. |

Attribute value scanning is deliberately string-only. A sensitive key is still
masked whatever its value type, but an otherwise innocuous map or slice
attribute is not recursively searched. Enabling `redact_all_types` would turn a
whole structured value into a string when one nested match fires, silently
changing the downstream schema. Producers should not capture aggregate header
maps or request objects; emit approved individual attributes instead.

## Credential classes

Key patterns recognise separator-delimited authentication, authorisation,
cookie, token, secret, credential, password, signature, DSN, API/private-key and
connection-string names. Value patterns recognise Basic and Bearer credentials,
`sk`/`rk`/`pk` tokens, JWTs, common AWS, GitHub, Slack and Google token prefixes,
URLs with user information, common `name=value` secret assignments and PEM
private-key blocks.

The list is intentionally not an entropy detector. Unknown vendor formats,
encoded or encrypted payloads, misspelled keys and secrets split across fields
can pass. Broad fragments such as `api` are not blocked: they corrupt ordinary
values such as `api_gateway_handler_v3`. Add a credential format only with both
a positive leak case and representative benign preservation cases.

## Evidence and operation

`summary: info` adds integer `redaction.masked.count` attributes when masking
occurs. It never emits masked key names. The count is record-local evidence that
the processor fired; it is not a Collector-wide counter and its absence is not
proof that the pipeline is safe.

The black-box harness proves edge redaction with gateway redaction disabled,
then proves gateway redaction independently. Both paths cover logs, traces and
metrics; the trace case covers the additional selected-traces recipient. The
harness also checks scalar log bodies, the configured credential corpus and
ordinary values that previously produced false positives. It does not execute
host-agent collection, although the shared-block check and profile validation
prove that the same configuration is wired there.

Consuming deployments must:

- prevent secrets entering span/event names, metric identity fields, span
  links, exemplars and structured attributes;
- source-sanitise application and journal free text instead of relying on the
  deny-list to understand arbitrary prose;
- preserve the processor as the final attribute-changing stage in every
  exported pipeline;
- run a unique far-end leak marker for every eligible recipient after changing
  patterns, pipeline order or processor version;
- treat redaction evidence, exporter failure metrics and far-end delivery as
  separate signals. None substitutes for another.

Changing the processor configuration requires editing every shared copy and
running the Go shared-region check plus the full harness. A new claimed carrier
is not covered until a test observes the exact shipped path.

## Deployment-specific patterns

The canonical `redaction/secrets` rules are a safety floor owned by this
repository. A consuming deployment must not edit that block to add an internal
credential format, because doing so creates a permanent merge conflict with
upstream profile updates.

Instead, the deployment may add a second processor to its fully rendered
profile:

```yaml
processors:
  redaction/deployment:
    allow_all_keys: true
    redact_all_types: false
    summary: info
    blocked_values:
      - '^MY_CORP_TOKEN_[A-Z0-9]+$'
```

Place `redaction/deployment` immediately after `redaction/secrets` in every
applicable pipeline and before any exporter. The canonical processor must stay
present and first: deployment patterns extend the floor rather than replace it.
Do not use multi-file Collector merge behaviour as the extension contract; the
consuming repository owns one complete rendered profile and its validation.

Every deployment pattern needs a positive leak marker and benign preservation
cases at the far end. Deployment patterns must not match attributes used by a
later routing or eligibility processor; test those attributes explicitly.
