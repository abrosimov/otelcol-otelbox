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
- **Loss after acknowledgement.** The 200 follows the upload of the raw batch
  to the event bucket (`LANGFUSE_S3_EVENT_UPLOAD_BUCKET`); an asynchronous
  worker then converts it. Records can still fail to reach ClickHouse there:

  | Case | Configurable | Signal |
  | --- | --- | --- |
  | An observation fails the ingestion schema | No; the schema is fixed | Metric `langfuse.ingestion.otel.observation_parse_failure` and one sample warning per batch |
  | Building or buffering one event record throws | No; a deterministic conversion failure | Error log naming the span ID |
  | The ingestion-masking callback fails | Yes. Masking is an enterprise feature that runs only when `LANGFUSE_INGESTION_MASKING_CALLBACK_URL` is set; it fails open by default and drops the batch only when `LANGFUSE_INGESTION_MASKING_CALLBACK_FAIL_CLOSED=true` | Warning naming the object key; masking metrics |
  | A ClickHouse insert fails on every attempt | Attempt count only, `LANGFUSE_INGESTION_CLICKHOUSE_MAX_ATTEMPTS` (default 3) | Metric `langfuse.queue.clickhouse_writer.rows_dropped`; dropped IDs logged |
  | The whole job fails, for example Redis, Postgres or the object store unavailable | Not by environment; the queue makes 6 attempts with exponential backoff from 5 s | The job stays in the queue's failed set |

  Redaction is this repository's, so a deployment that leaves the masking
  callback unset has no masking case. Because the raw batch stays in the event
  bucket, an operator of a self-hosted Langfuse can replay a failed batch with
  `worker/src/scripts/replayIngestionEventsV2`, subject to that bucket's
  retention.

For Langfuse the middleware above would therefore report nothing: the
recipient-side loss there is asynchronous and invisible to any OTLP client.
Two routes close it: the Langfuse operator alerting on the signals above and
replaying from the event bucket, or the producer engine reconciling against
Langfuse's API and replaying complete spans under their original span IDs. Only
the second is available without operating Langfuse. The middleware stays worth
building for recipients that do return partial success. The 403 suspension is
retried on the long authentication interval, so a usage stop holds data in the
WAL rather than dropping it.

## Evidence to attach

- unit tests for HTTP and gRPC bodies with and without rejections, and for a
  body the exporter still reads unchanged;
- a harness scenario using the selected-traces recipient double's
  partial-success reply, requiring the counter at the gateway;
- the existing `TestRecipientPartialSuccessCountsAsDelivered` kept as the
  guard that delivery behaviour did not change.
