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
| `OTELBOX_STORAGE_DIR` | Private writable root for the gateway WAL and its compaction files. |
| `OTELBOX_UPSTREAM_ENDPOINT` | Gateway OTLP/gRPC `host:port`. |
| `OTELBOX_UPSTREAM_AUTH_HEADER_FILE` | File containing the complete header value, literally `Bearer <token>`. |

Reference variables have safe absent-variable defaults:

| Variable | Default | Constraint |
| --- | ---: | --- |
| `OTELBOX_BIND_HOST` | `127.0.0.1` | Fails closed on loopback when the deployment omits it. |
| `OTELBOX_HEALTH_ENDPOINT` | `127.0.0.1:13133` | Complete health `host:port`. |
| `OTELBOX_MEMORY_LIMIT_MIB` | 400 MiB | Fixed hard threshold for the workstation process. |
| `OTELBOX_MEMORY_SPIKE_LIMIT_MIB` | 80 MiB | Spike allowance below the hard threshold. |
| `OTELBOX_EXPORTER_CONSUMERS` | 2 | Concurrent outbound workers. |
| `OTELBOX_AUTH_RETRY_INTERVAL` | `1h` | Probe interval while the gateway rejects the outbound credential. |
| `OTELBOX_STORAGE_MAX_SIZE_BYTES` | 6 GiB | Per signal file; 1.5 times the queue capacity. |
| `OTELBOX_QUEUE_SIZE_BYTES` | 4 GiB | Must exceed the 1 MiB accepted-request envelope and fit within storage capacity. |
| `OTELBOX_UPSTREAM_TLS_CERT_FILE` | none | Optional client certificate presented to the gateway leg. Absent means none is offered. |
| `OTELBOX_UPSTREAM_TLS_KEY_FILE` | none | Private key for the certificate above. Supply both or neither. |
| `OTELBOX_UPSTREAM_TLS_RELOAD_INTERVAL` | `1h` | How stale the client pair may be before the next handshake re-reads it. |
| `OTELBOX_UPSTREAM_COMPRESSION` | `zstd` | Wire codec for the gateway leg. `zstd`, `gzip` and `none` are exercised; see below. |

If `OTELBOX_STORAGE_DIR` is absent, its invalid `/dev/null/...-is-required`
sentinel makes validation fail before a root-level WAL can be selected. An
exported empty value bypasses that sentinel and every numeric default. Reject
empty values and render explicit values in production rather than relying on
the reference sizes.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `${OTELBOX_BIND_HOST}:4317` | OTLP/gRPC ingest, up to 1 MiB per message. |
| `${OTELBOX_BIND_HOST}:4318` | OTLP/HTTP ingest, up to 1 MiB per request. |
| `${OTELBOX_BIND_HOST}:8888/metrics` | Detailed Collector metrics; also scraped back through the durable telemetry pipeline. |
| `${OTELBOX_HEALTH_ENDPOINT}/status` | Component lifecycle health. It does not prove delivery. |

The profile publishes exactly these listeners. A deployment that must route a
local producer to the gateway's selected-traces recipient adds its own
classified listener and a `resource` processor instance rather than changing
these; the linked-but-unwired component and its failure modes are described in
[stamping the route marker](gateway.md#stamping-the-route-marker-where-the-producer-cannot).

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

### Optional client certificate

`OTELBOX_UPSTREAM_TLS_CERT_FILE` and `OTELBOX_UPSTREAM_TLS_KEY_FILE` make the
edge present a client certificate on the gateway leg. Deployments that reach
their gateway over an SSH tunnel or a private network leave both unset and
nothing changes; the exporter offers no certificate and the bearer token remains
the only credential.

Supply them when the gateway sits behind a front end configured for mTLS. The
certificate and the token are then two independent factors: the token names a
client in the gateway's `bearertokenauth/ingest` allowlist, the certificate
proves possession of a host key to the front end, and either can be revoked
without touching the other.

Three failure modes, deliberately different:

| Situation | Where it surfaces |
| --- | --- |
| Both variables absent or empty | No failure. No client certificate is offered. |
| Exactly one of the pair supplied | `validate` fails: `TLS configuration must include both certificate and key`. |
| Both supplied, a path does not resolve | `validate` passes; the exporter fails at start with `failed to load TLS cert and key`. |

The third row is the one to plan for: a mistyped path is not a configuration
error and only appears when the process starts, so treat a failed start as the
signal rather than expecting `validate` to catch it.

Rotation needs no restart, but the mechanism differs from the header file's.
`headers_setter/gateway` is watched by fsnotify and picks up a replacement
immediately. The certificate pair is polled: `configtls` re-reads it on the
first handshake after `OTELBOX_UPSTREAM_TLS_RELOAD_INTERVAL` has elapsed, so a
replacement takes effect within that interval plus the time until the next
connection, not instantly. Leaving the variable unset keeps the reference
one hour; `configtls`'s own default of `0` would pin the process to the material
it started with, which is why this profile does not use it.

If the gateway rejects the credential, the exporter retains the accepted data
and retries at `OTELBOX_AUTH_RETRY_INTERVAL`. Replace the header file in place;
`headers_setter/gateway` supplies the new value to a later attempt without a
Collector restart. The interval is deliberately much longer than the ordinary
5–30 second transient backoff.

## Compression

The gateway leg uses `zstd`. It is chosen for this leg specifically, not as a
general preference: a gRPC client can only select a codec that is registered in
the server it dials, and that is a property of the peer's build rather than its
configuration. Both ends of this leg are `otelcol-otelbox`, so the guarantee
holds by construction. The gateway's own recipient exporters stay on `gzip`,
where the peer is a third-party backend.

Override with `OTELBOX_UPSTREAM_COMPRESSION` when the deployment terminates this
leg somewhere other than this binary, or when the transport already compresses —
`none` is a legitimate value there. An unrecognised value is refused by name at
load rather than silently ignored. Compression level is not configurable: the
persistent-queue exporter wrapper does not carry `compression_params`.

`zstd` and `gzip` are the values the harness has actually carried records under,
and `none` needs no codec at all. `snappy`, `lz4`, `zlib` and `deflate` are
accepted by the configuration loader, but loading is not delivery: a codec must
also be registered in the gRPC peer, so treat those as unproven on this leg
until a run says otherwise.

The codec changes neither sizing invariant. `sizer: bytes` measures uncompressed
queue items, and the gateway's `max_recv_msg_size_mib` applies to the
decompressed message, so the 1.5 MiB sender batch stays inside the 2 MiB receive
envelope regardless.

## Durability

The exporter queue writes through `file_storage/gateway`, uses byte-based
capacity and blocks on overflow. Retry has no elapsed-time limit. Sender
batching happens inside that persistent queue; there is no pipeline `batch`
processor that could return success while retaining the record only in memory.
The sender splits batches at 1.5 MiB, below the gateway's 2 MiB receive limit.

The 4 GiB queue is a reference outage budget. Production should render
`peak encoded bytes/second * outage seconds * safety factor` and retain the
1.5 storage-to-queue ratio rather than copying the default unmeasured.

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

Treat `/status` as startup health only. Delivery monitoring must include queue
size/capacity, enqueue failures and the local Collector log. Authentication
backoff logs `Exporting failed. Will retry the request after interval`; the
process and health endpoint deliberately remain green while the WAL retains
the outage. The end-to-end harness proves live credential recovery because
liveness alone cannot.
