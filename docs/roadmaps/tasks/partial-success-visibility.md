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

Pending the source investigation recorded with this task.

## Evidence to attach

- unit tests for HTTP and gRPC bodies with and without rejections, and for a
  body the exporter still reads unchanged;
- a harness scenario using the selected-traces recipient double's
  partial-success reply, requiring the counter at the gateway;
- the existing `TestRecipientPartialSuccessCountsAsDelivered` kept as the
  guard that delivery behaviour did not change.
