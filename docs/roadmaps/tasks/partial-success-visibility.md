---
note_id: "20261022160000"
created: 2026-10-22
type: note
subtype: roadmap-item
language: en
retention: active
status: planned
priority: medium
---
# Task: make recipient partial-success rejections visible

## Status

Planned after 2.5.0. The finding is
[PC-08](../../research/problem-cards/PC-08-recipient-partial-success.md).

## Problem

A recipient that accepts part of an OTLP request answers success with
`partial_success.rejected_*` set. The pinned exporters log that at Warn and
count the request as sent, so the rejected records leave the queue and appear
in no metric.

The OTLP specification makes the rejection terminal: the client must not retry
a request whose `partial_success` is populated. The rest of the request is
already accepted, the response names only a count, not the rejected items, and
a server reports a transient failure as an error for the whole request, so a
partial rejection means those items will never be accepted. Retrying would
duplicate the accepted part and loop on the rejected one. The transport can
therefore neither recover nor retry these records; it can only make them
visible.

## Proposed shape

A local client middleware extension, built like the exporter wrappers and
attached to an exporter through the Collector's `middlewares` setting:

- HTTP first (`extensionmiddleware.HTTPClient`): wrap the round tripper, read a
  2xx `Export*ServiceResponse` body, restore it for the exporter, and record
  the rejected count and message;
- gRPC after a prototype (`extensionmiddleware.GRPCClient`): a unary client
  interceptor on the export reply, provided the pinned pdata reply type can be
  read there;
- one counter per recipient and signal, for example
  `otelbox_exporter_partial_success_rejected_{spans,metric_points,log_records}`,
  exported through each role's `metrics/self` so a deployment can alert;
- delivery behaviour unchanged; a minor version.

## Division of responsibility for accounted telemetry

Agent billing and Langfuse cost data must not rely on the transport for
completeness, because no transport can undo a recipient's rejection:

| Layer | Owns |
| --- | --- |
| Producer engine | A ledger of events with stable identity, validation before sending, and reconciliation against the recipient that replays missing events under the same identity. |
| otelcol-otelbox | Durable admission, at-least-once delivery and the rejection signal above, which tells the engine when to reconcile. |
| Recipient | Idempotent acceptance by identity, so a replay cannot double-count. Whether Langfuse provides it is recorded below. |

## Langfuse idempotency

Read in the Langfuse source at `langfuse/langfuse@4d851c2` (2026-10-22):

- **Identity.** The OTLP route stores the raw spans and queues them; a worker
  converts each span to an observation whose id is the hex span ID
  (`OtelIngestionProcessor.ts`, `parseId(span.spanId)`). A resent span gets the
  same id.
- **Duplicates.** Both write paths use ClickHouse `ReplacingMergeTree` keyed by
  that id, so an identical copy collapses to one observation; reads use
  `LIMIT 1 BY` until merges run. Cost and usage are computed per record and
  billing counts distinct event ids, so a duplicate is not double-counted.
  A replay under the same span ID is therefore safe.
- **Differing copies.** The legacy path merges a later copy into the existing
  observation; the direct path, chosen by newer SDK headers, replaces the whole
  row and the copy processed last wins, so attributes missing from it are lost.
  A replay must therefore resend the complete span, never a partial update. A
  missing start or end timestamp is replaced by the receive time, which gives
  each copy a different key: the producer must always set both.
- **No partial success.** The endpoint never sets `partial_success`. It rejects
  a malformed batch whole with 400, answers 403 when ingestion is suspended for
  usage, 413 above its body limit, 429 with `Retry-After` for its rate limit,
  503 when its object store throttles and 500 for other publishing failures.
- **Loss after acknowledgement.** Spans can still be dropped after a 200, in
  the asynchronous worker: schema failures, per-event write errors, masking
  failures and ClickHouse writes that exhaust their retries are only logged.

For Langfuse the middleware above would therefore report nothing: the
recipient-side loss there is asynchronous and invisible to any OTLP client.
Only the producer engine's reconciliation against Langfuse's API, replaying
complete spans under their original span IDs, closes it. The middleware stays
worth building for recipients that do return partial success. The 403 suspension
is retried on the long authentication interval, so a usage stop holds data in
the WAL rather than dropping it.

## Evidence to attach

- unit tests for HTTP and gRPC bodies with and without rejections, and for a
  body the exporter still reads unchanged;
- a harness scenario using the selected-traces recipient double's
  partial-success reply, requiring the counter at the gateway;
- the existing `TestRecipientPartialSuccessCountsAsDelivered` kept as the
  guard that delivery behaviour did not change.
