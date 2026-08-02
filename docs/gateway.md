# Operating the gateway role

The gateway authenticates OTLP clients, redacts accepted telemetry and fans
each signal out to two backend exporters with separate WALs. Load
[the profile](../config/gateway.yaml) on its own:

```console
otelcol-otelbox --config config/gateway.yaml
```

## Required environment

| Variable | Meaning |
| --- | --- |
| `OTELBOX_BIND_HOST` | Address used by both OTLP listeners, self-metrics reader and self-scrape target. Choose loopback or wildcard from the actual network namespace. |
| `OTELBOX_HEALTH_ENDPOINT` | Complete `host:port` for `healthcheckv2`. A container deployment commonly needs a reachable non-loopback address. |
| `OTELBOX_INGEST_TOKEN_FILE` | Allowlist file for `bearertokenauth/ingest`. |
| `OTELBOX_STORAGE_DIR` | Private writable root for both backend WALs and compaction files. |
| `OTELBOX_BACKEND_1_ENDPOINT` | First backend OTLP/gRPC `host:port`. |
| `OTELBOX_BACKEND_2_ENDPOINT` | Second backend OTLP/gRPC `host:port`. |

Optional numeric variables carry defaults only when absent:

| Variable | Default | Scope |
| --- | ---: | --- |
| `OTELBOX_INGEST_MAX_CONCURRENT_STREAMS` | 16 | Per gRPC connection. Zero means unlimited and must not be used accidentally. |
| `OTELBOX_STORAGE_MAX_SIZE_BYTES` | 10 GiB | Per signal file, per backend. |
| `OTELBOX_QUEUE_SIZE_BYTES` | 9 GiB | Per signal queue, per backend. Keep below the storage cap. |
| `OTELBOX_BACKEND_MAX_PAYLOAD_BYTES` | 3 MiB | Sender batch split limit. Set from each backend's accepted request size. |

There are three storage files per backend—traces, metrics and logs. At default
caps the theoretical file allocation is 60 GiB before compaction headroom. Size
the filesystem from that multiplication, not from one `max_size` value.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `${OTELBOX_BIND_HOST}:14319` | Authenticated OTLP/gRPC ingest, up to 32 MiB per message. |
| `${OTELBOX_BIND_HOST}:14320` | Authenticated OTLP/HTTP ingest, up to 32 MiB per request. |
| `${OTELBOX_BIND_HOST}:8889/metrics` | Detailed Collector metrics and the self-scrape target. |
| `${OTELBOX_HEALTH_ENDPOINT}/status` | Lifecycle health only; full queues and backend rejection do not make it unhealthy. |

The unauthenticated configuration dump is disabled.

## Ingest credentials and transport

The allowlist contains one bare token per line. The first whitespace-delimited
field is the token and the rest is an optional comment:

```text
edge-token-1  workstation edge
host-token-1  server host agent
```

Clients send `Authorization: Bearer <token>`. Give each client its own token so
one can be revoked independently.

The role profile does not decide where inbound TLS terminates. If the Collector
terminates it, the deployment must add certificate and key settings to both
OTLP protocols. If a trusted ingress terminates it, bind the Collector only on
that private path. A bearer token must never cross an unencrypted network. The
edge exporter keeps certificate verification enabled, and the integration test
configures gateway TLS directly.

Backend exporters also use TLS verification by default and carry no credential:
authentication terminates at this gateway in the demonstrated topology. A
deployment that needs backend authentication or deliberate plaintext must add
those settings to its rendered profile.

## Queue behaviour and coupling

Each backend has its own `file_storage` directory, byte-sized queue and infinite
transient retry. Sender batching occurs after persistent enqueue. Full queues
block rather than discard because OTLP clients can retry.

Separate queues do not make fan-out fully independent. The Collector invokes
fan-out consumers synchronously: a stopped backend leaves ingest and its
neighbour alone while it has queue headroom, but once its queue fills and
blocks, backpressure reaches the receiver. Depending on exporter order, the
healthy exporter may accept the current record before the client request
blocks; the next request still cannot proceed normally. The harness proves both
sides of this boundary. Alert on queue utilisation early enough that overflow
is an incident response trigger, not the first symptom operators see.

## Validate and operate

```console
printf 'validation-placeholder  gateway validation only\n' > /tmp/otelbox-ingest-tokens
OTELBOX_BIND_HOST=0.0.0.0 \
OTELBOX_HEALTH_ENDPOINT=0.0.0.0:14323 \
OTELBOX_INGEST_TOKEN_FILE=/tmp/otelbox-ingest-tokens \
OTELBOX_STORAGE_DIR=/tmp/otelbox-gateway \
OTELBOX_BACKEND_1_ENDPOINT=127.0.0.1:14317 \
OTELBOX_BACKEND_2_ENDPOINT=127.0.0.1:24317 \
  otelcol-otelbox validate --config config/gateway.yaml
```

Monitor exporter send/enqueue failures and queue size/capacity per exporter and
signal. Probe unauthenticated ingest and require HTTP 401 as an access-control
precondition; `/status` alone says nothing about either authentication or
delivery.
