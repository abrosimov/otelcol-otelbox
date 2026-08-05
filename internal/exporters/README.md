# OTLP authentication-retry wrappers

The two modules in this directory wrap the unmodified standard OTLP gRPC and
HTTP exporters. They preserve the canonical component types `otlp_grpc` and
`otlp_http` and the existing profile configuration, including
`retry_on_auth_failure`.

Each wrapper creates two layers:

```text
outer exporterhelper: persistent queue, timeout and retry
  -> standard upstream exporter: transport only
```

The inner exporter's queue and ordinary retry are disabled, so an accepted item
has one persistence boundary and one retry state machine. Its exporterhelper
telemetry providers are no-op to prevent duplicate counters under the same
component ID; the outer helper publishes the supported exporter metrics. Start
and shutdown are delegated to the inner exporter so transport clients retain
their standard lifecycle.

The standard exporters still identify authentication responses as permanent.
The wrapper unwraps that result and converts only gRPC `Unauthenticated` and
`PermissionDenied`—including the HTTP 401 and 403 statuses mapped to those
codes—into a throttled retry. Every other permanent response remains permanent.

`UPSTREAM_VERSION`, the wrapper `go.mod` files and every component pin in
`builder.yaml` form one dependency set. `otelbox-ci manifest resolve` rejects a
mixed set. An upgrade must move all of them together and run the full harness;
unit tests alone do not prove credential-file rereading or WAL recovery.

