# Operating the host-agent role

The host agent collects the machine's own metrics, selected journal units,
neighbouring Prometheus targets and its own Collector metrics.
It redacts and persists them before sending to the gateway. Load
[the profile](../config/host-agent.yaml) on its own:

```console
otelcol-otelbox --config config/host-agent.yaml
```

Run this role as a plain host process. The `journald` receiver executes
`journalctl`; the published `scratch` image contains neither that binary nor a
shell and cannot run this receiver.

## Required environment

| Variable | Meaning |
| --- | --- |
| `OTELBOX_STORAGE_DIR` | Private writable root for the outbound WAL, compaction files and journald cursor. |
| `OTELBOX_UPSTREAM_ENDPOINT` | Gateway OTLP/gRPC `host:port`. |
| `OTELBOX_UPSTREAM_AUTH_HEADER_FILE` | File containing the complete value `Bearer <token>`. |
| `OTELBOX_SCRAPE_TARGET_1`, `OTELBOX_SCRAPE_TARGET_2` | Prometheus `host:port` targets for neighbouring services. |
| `OTELBOX_JOURNAL_UNIT_1`, `OTELBOX_JOURNAL_UNIT_2` | Exact journal units to follow. Do not include the host agent's own unit. |

Reference defaults are `OTELBOX_BIND_HOST=127.0.0.1`,
`OTELBOX_HEALTH_ENDPOINT=127.0.0.1:14324`, `OTELBOX_MEMORY_LIMIT_MIB=400`,
`OTELBOX_MEMORY_SPIKE_LIMIT_MIB=80`, `OTELBOX_EXPORTER_CONSUMERS=2`,
`OTELBOX_STORAGE_MAX_SIZE_BYTES=6442450944` and
`OTELBOX_QUEUE_SIZE_BYTES=4294967296`. The role exports metrics and logs, so
reserve both signal files, compaction headroom and the independent journald
cursor. Render production queue capacity from measured peak rate and required
outage duration.

If `OTELBOX_STORAGE_DIR` is absent, its invalid `/dev/null/...-is-required`
sentinel makes validation fail. An exported empty value bypasses that sentinel
and is forbidden. The journald storage has no byte cap deliberately: it holds
one small cursor rather than a telemetry backlog; the storage budget variable
applies to each outbound signal WAL only.

## Local visibility and privileges

Container statistics are deliberately absent from the first rootless Podman
profile. `podman_stats` needs the Podman service API, and a raw user socket is a
control-plane capability rather than a read-only telemetry source. Add
container-level collection only after the consuming repository defines and
tests a constrained API proxy or a cgroup/systemd-unit alternative.

The journal allowlist is deliberately finite: exporting the whole journal would
move kernel, authentication and unrelated service records through a pipeline
built for selected operational telemetry. The receiver cursor is stored under
`file_storage/journald`, separate from the outbound queue.

The `process` scraper is not enabled. An unprivileged process cannot read the
important `/proc/<pid>` files for other users, while PID, parent PID and command
line create unbounded series and may expose credentials. Muting permission
errors would produce a healthy-looking but incomplete scraper. Any future
process aggregation requires an explicit privilege, cardinality and
single-writer design.

## Authentication, TLS and durability

The header file is the same complete-value format used by the edge:

```text
Bearer one-host-agent-token
```

`headers_setter/gateway` adds no scheme and a trailing comment would be sent as
part of the credential. Outbound TLS verification remains at the OTLP
exporter's secure default. A deployment whose private gateway endpoint is
deliberately plaintext must state `tls.insecure: true` in its rendered profile;
this repository does not infer that from co-location.

The outbound queue persists through `file_storage/gateway`, batches after
enqueue and retries transient failure indefinitely. Unlike edge and gateway,
it does not block when full. Scrape receivers cannot be back-pressured: waiting
would merely miss every collection cycle during the wait. The WAL therefore
retains a bounded outage and rejects new samples at capacity. Monitor capacity
well before that boundary.

The sender splits at 1.5 MiB, below the gateway's 2 MiB receive envelope.

## Endpoints and validation

The role publishes detailed Collector metrics at
`${OTELBOX_BIND_HOST}:8890/metrics` and health at
`${OTELBOX_HEALTH_ENDPOINT}/status`. Health does not prove gateway delivery.

```console
printf 'Bearer validation-placeholder\n' > /tmp/otelbox-host-agent-auth-header
OTELBOX_BIND_HOST=127.0.0.1 \
OTELBOX_HEALTH_ENDPOINT=127.0.0.1:14324 \
OTELBOX_STORAGE_DIR=/tmp/otelbox-host-agent \
OTELBOX_UPSTREAM_ENDPOINT=127.0.0.1:14319 \
OTELBOX_UPSTREAM_AUTH_HEADER_FILE=/tmp/otelbox-host-agent-auth-header \
OTELBOX_SCRAPE_TARGET_1=127.0.0.1:19001 \
OTELBOX_SCRAPE_TARGET_2=127.0.0.1:19002 \
OTELBOX_JOURNAL_UNIT_1=ssh.service \
OTELBOX_JOURNAL_UNIT_2=cron.service \
  otelcol-otelbox validate --config config/host-agent.yaml
```

Run even `validate` for this profile on Linux: component construction rejects
the journald receiver on other operating systems, although it does not execute
`journalctl`. A runtime smoke test must then be performed on the intended host
with the intended service account. Command sandboxes that deny the boot-time
sysctl make `resource_detection/system` fail at startup even when the
configuration is valid.
