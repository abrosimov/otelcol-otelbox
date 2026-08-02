# Operating the edge role

The edge accepts OTLP from local applications, applies origin detection and
redaction, persists outbound data and sends it to one authenticated gateway.
Load [the profile](../config/edge.yaml) on its own:

```console
otelcol-otelbox --config config/edge.yaml
```

## Required environment

| Variable | Meaning |
| --- | --- |
| `OTELBOX_BIND_HOST` | Address used by the OTLP listeners, self-metrics reader and self-scrape target. Conventionally `127.0.0.1` on a host process. |
| `OTELBOX_HEALTH_ENDPOINT` | Complete `host:port` for `healthcheckv2`, conventionally `127.0.0.1:13133`. |
| `OTELBOX_STORAGE_DIR` | Private writable root for the gateway WAL and its compaction files. |
| `OTELBOX_UPSTREAM_ENDPOINT` | Gateway OTLP/gRPC `host:port`. |
| `OTELBOX_UPSTREAM_AUTH_HEADER_FILE` | File containing the complete header value, literally `Bearer <token>`. |

Optional capacity variables have safe absent-variable defaults:

| Variable | Default | Constraint |
| --- | ---: | --- |
| `OTELBOX_STORAGE_MAX_SIZE_BYTES` | 5 GiB | Must be larger than the queue and leave filesystem headroom for compaction. |
| `OTELBOX_QUEUE_SIZE_BYTES` | 4 GiB | Must fit within storage capacity. |

An exported empty value does not select the default. Render explicit values in
production rather than relying on these reference sizes.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `${OTELBOX_BIND_HOST}:4317` | OTLP/gRPC ingest, up to 32 MiB per message. |
| `${OTELBOX_BIND_HOST}:4318` | OTLP/HTTP ingest, up to 32 MiB per request. |
| `${OTELBOX_BIND_HOST}:8888/metrics` | Detailed Collector metrics; also scraped back through the durable telemetry pipeline. |
| `${OTELBOX_HEALTH_ENDPOINT}/status` | Component lifecycle health. It does not prove delivery. |

## Authentication and TLS

`headers_setter/gateway` reads the complete `authorization` value from the
header file. It trims surrounding whitespace but adds no scheme, so this is
valid:

```text
Bearer one-secret-token
```

Do not add a trailing comment: it would become part of the header. Keep the file
readable only by the account running the Collector.

The upstream exporter uses TLS verification by default. The reference profile
does not choose a CA path because trust-store ownership is deployment-specific.
The CI overlay supplies a per-run CA and verifies the gateway leaf; it never
turns verification off.

## Durability

The exporter queue writes through `file_storage/gateway`, uses byte-based
capacity and blocks on overflow. Retry has no elapsed-time limit. Sender
batching happens inside that persistent queue; there is no pipeline `batch`
processor that could return success while retaining the record only in memory.

The harness forces this boundary: it posts a record while the gateway is down,
requires HTTP 200, kills the edge with SIGKILL, reopens the same storage and
requires the record to reach the gateway after recovery.

## Validate and operate

Create the header file before validation so path handling is checked too:

```console
printf 'Bearer validation-placeholder\n' > /tmp/otelbox-edge-auth-header
OTELBOX_BIND_HOST=127.0.0.1 \
OTELBOX_HEALTH_ENDPOINT=127.0.0.1:13133 \
OTELBOX_STORAGE_DIR=/tmp/otelbox-edge \
OTELBOX_UPSTREAM_ENDPOINT=127.0.0.1:14319 \
OTELBOX_UPSTREAM_AUTH_HEADER_FILE=/tmp/otelbox-edge-auth-header \
  otelcol-otelbox validate --config config/edge.yaml
```

Treat `/status` as startup health only. Delivery monitoring must include
`otelcol_exporter_send_failed_*`, `otelcol_exporter_enqueue_failed_*` and queue
size/capacity metrics from port 8888. A wrong gateway token leaves the process
and health endpoint green; the end-to-end harness exists because that failure
previously caused silent loss.
