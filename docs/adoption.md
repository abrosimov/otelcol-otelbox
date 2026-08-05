# Adopting `otelcol-otelbox` 2.0

Version 2.0 is a configuration-contract break. Upgrade the binary and each
rendered role configuration as one change; a 1.x configuration is not a safe
fallback for the 2.0 binary, and a 2.0 profile is not intended for a 1.x
binary.

This repository publishes reference profiles. `devbox-setup` and
`remote_server_setup` continue to own the configurations they render, their
secrets, services, storage paths and network topology.

## Breaking changes

| 1.x contract | 2.0 contract |
| --- | --- |
| Load `base.yaml`, then a role layer. | Load exactly one self-contained `config/<role>.yaml`. |
| Reference profiles live under `config/examples/`. | Published profiles are `config/edge.yaml`, `config/gateway.yaml` and `config/host-agent.yaml`. |
| Shared behaviour is inherited by merge convention. | Marked shared regions are copied into all profiles and checked byte for byte. |
| Machine-specific environment names are role-prefixed. | All roles use the same generic names such as `OTELBOX_BIND_HOST`, `OTELBOX_STORAGE_DIR` and `OTELBOX_UPSTREAM_ENDPOINT`. |
| Edge credentials may be assembled from a token value. | Edge and host agent read a file containing the complete `Bearer <token>` header value. |
| Gateway may have separate local and remote OTLP receivers. | One authenticated `otlp` receiver listens on ports 14319 and 14320. |
| `health_check` may still be configured. | Only `healthcheckv2` is linked and `/status` is the health path. |
| Pipeline `batch` may precede the persistent exporter. | The processor is not linked; sender batching happens inside the exporter queue after durable enqueue. |
| Host-agent export may be in memory and time-limited. | It has a persistent byte-sized queue and indefinite transient retry. |
| Host-agent container statistics may use Docker or Podman sockets. | The reference first cut omits container statistics until a constrained rootless telemetry boundary is defined. |
| Gateway fan-out may be described as two fixed backends. | Recipients are an explicitly rendered N-element set; each required recipient owns an exporter and WAL per signal. |
| Gateway recipients may all use OTLP/gRPC. | The reference also proves a selected-traces OTLP/HTTP recipient with arbitrary headers. |

The old `config/base.yaml` and `config/examples/` files are deliberately absent
from the release archive and Homebrew installation. Keeping local copies under
those names makes a stale startup command look plausible; remove them from the
managed destination during migration.

## Canonical component names

Use the v0.158 canonical spelling in rendered configuration:

| Old spelling | 2.0 spelling |
| --- | --- |
| OTLP exporter `otlp/...` | `otlp_grpc/...` |
| OTLP/HTTP exporter `otlphttp/...` | `otlp_http/...` |
| `resourcedetection/...` | `resource_detection/...` |
| `hostmetrics` | `host_metrics` |
| `health_check` | `healthcheckv2` |

The OTLP receiver remains `otlp`; do not rename it to `otlp_grpc`.

## Environment migration

Use the profile itself as the authoritative inventory. Typical mappings from
the previous role-prefixed names are:

| Previous name | 2.0 name |
| --- | --- |
| `OTELBOX_EDGE_BIND_HOST`, `OTELBOX_GATEWAY_BIND_HOST`, `OTELBOX_HOST_AGENT_BIND_HOST` | `OTELBOX_BIND_HOST` |
| `OTELBOX_EDGE_STORAGE`, `OTELBOX_GATEWAY_STORAGE`, `OTELBOX_HOST_AGENT_STORAGE` | `OTELBOX_STORAGE_DIR` |
| `OTELBOX_EDGE_HEALTH_ENDPOINT`, `OTELBOX_GATEWAY_HEALTH_ENDPOINT`, `OTELBOX_HOST_AGENT_HEALTH_ENDPOINT` | `OTELBOX_HEALTH_ENDPOINT` |
| `OTELBOX_EDGE_ENDPOINT`, `OTELBOX_HOST_AGENT_GATEWAY_ENDPOINT` | `OTELBOX_UPSTREAM_ENDPOINT` |
| Edge token value, `OTELBOX_HOST_AGENT_AUTH_HEADER_FILE` | `OTELBOX_UPSTREAM_AUTH_HEADER_FILE` containing the full header value |
| `OTELBOX_GATEWAY_TOKEN_FILE` | `OTELBOX_INGEST_TOKEN_FILE` |
| `OTELBOX_HOST_AGENT_SCRAPE_TARGET_1`, `_2` | `OTELBOX_SCRAPE_TARGET_1`, `_2` |
| `OTELBOX_HOST_AGENT_JOURNAL_UNIT_1`, `_2` | `OTELBOX_JOURNAL_UNIT_1`, `_2` |

The old fixed backend endpoint inputs have no one-to-one replacement. The
reference uses `OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT` for one logical
all-signal recipient represented by `otlp_grpc/{logs,metrics,traces}` and the
matching storage triplet. Deployments render a uniquely named triplet for every
actual recipient.

The reference queue/storage budgets are signal-specific:
`OTELBOX_{LOGS,METRICS,TRACES}_{QUEUE_SIZE,STORAGE_MAX_SIZE}_BYTES`. The generic
`OTELBOX_RECIPIENT_{QUEUE_SIZE,STORAGE_MAX_SIZE}_BYTES` variables apply only to
the selected-traces recipient.

The selected-traces recipient additionally requires
`OTELBOX_SELECTED_TRACES_ENDPOINT`,
`OTELBOX_SELECTED_TRACES_AUTH_HEADER_FILE`,
`OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_NAME` and
`OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_VALUE`. Concrete vendor paths,
credentials and protocol versions belong to the consuming repository.

Removed example-only inputs such as edge probe/NTP values, gateway Docker
inputs, per-backend tokens and the local gateway token file have no direct
replacement in the published profiles. If a deployment still needs one of
those capabilities, add it deliberately to that repository's rendered
configuration and its own validation rather than restoring the 1.x layer.

Numeric capacity inputs use byte units and have defaults only when the variable
is absent. An exported empty value becomes a zero value and can mean unlimited,
no splitting or invalid configuration depending on the component. Render
explicit production budgets. `OTELBOX_STORAGE_DIR` instead defaults to an
invalid `/dev/null/...-is-required` sentinel so an absent storage root fails
validation; reject exported-empty storage values because they bypass it.

Exporter IDs are part of persistent queue identity. Renaming `otlp/gateway` or
an ordinal backend ID to a named `otlp_grpc/<recipient>` instance does not
migrate an existing bbolt backlog. Drain and verify the old exporter queue
before cutover, preserve the old WAL directory for rollback, and start the new
ID with a new empty directory. Do not treat process health as proof that the old
queue was replayed.

## Migration order

1. Update the gateway rendering in `remote_server_setup` first. Collapse its
   ingest receivers, adopt the shared allowlist and canonical names, allocate
   one WAL directory per required recipient and signal, and add the transport,
   selected routing and header settings required by the real recipients.
2. Validate that rendered gateway configuration with the 2.0 binary before
   replacing the running service. Confirm unauthenticated ingest returns 401.
3. Update the edge installation and launchd rendering in `devbox-setup`. Keep
   Homebrew out of managed workstations: the playbook-owned binary and launchd
   agent must refer to the same release asset.
4. Update `remote_server_setup`'s host-agent rendering. Run it as a host process,
   allocate outbound WAL capacity and preserve a separate journald cursor
   directory. Keep container statistics out until the rootless boundary is
   designed and tested.
5. Remove legacy base/example files and old environment entries from managed
   destinations only after the new service command points at one role file.

Gateway-first does not mean switching clients to it without a compatibility
plan. If the receiver ports or TLS termination change, keep the old listener
available until each client is ready, or coordinate a short atomic cutover.

## Acceptance checklist

For each consuming repository:

- render a complete file and run `otelcol-otelbox validate --config <file>`;
- compare its component names, pipeline order, redaction and queue settings with
  the matching published profile;
- check owner, mode, free space and compaction headroom for every storage path;
- check the ingest allowlist and client header files with real file ownership,
  without printing credentials;
- verify TLS from the client's network namespace to the endpoint it actually
  dials;
- require unauthenticated gateway ingest to return 401;
- send a unique marker through the complete path and observe it at every
  eligible required recipient;
- alert on exporter send/enqueue failures and queue utilisation; do not use
  `/status` as a delivery assertion;
- on the host agent, run a real startup smoke test as the service account so
  `/proc`, boot-time detection and `journalctl` permissions are covered.

Before publishing or deploying, run this repository's smoke check, shared-block
check and full harness against the exact binary to be distributed. The harness
must run outside a sandbox that denies the boot-time sysctl.

## Rollback

Preserve the previous binary, rendered configuration and service arguments as a
single rollback unit. Roll back all three together. Reusing a 1.x base layer
with a 2.0 binary, or keeping a 2.0 role file while restoring a 1.x binary,
creates a mixed contract that has not been tested.

WAL directories contain version-sensitive operational state. Do not delete
them during rollback. Stop the process, preserve the directories and inspect
startup/replay behaviour with the selected binary before deciding whether any
state migration is required.
