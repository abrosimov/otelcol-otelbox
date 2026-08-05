# Task: define tenant-aware ingress rate limiting

## Status

Deferred. This is not a 2.1 release blocker.

## Problem

`memory_limiter`, request-size limits and connection concurrency protect global
resources but do not implement tenant fairness. The current bearer allowlist
authenticates tokens without producing a stable tenant identity suitable for a
quota key.

## Required design

A Collector-local limiter would need an authenticated token-to-tenant mapping,
per-tenant budgets, bounded cardinality, `Retry-After` behaviour and an HA state
model. Without those pieces it would be a global overload switch presented as
tenant isolation.

The current supported boundary remains:

```text
ingress proxy or network identity -> authenticated Collector -> memory limiter -> durable backpressure
```

## Admission criteria

- a real multi-tenant requirement and abuse model;
- quota identity that does not expose credentials in telemetry;
- local versus shared bucket decision for multiple gateway replicas;
- fairness, eviction and retry tests under concurrent load;
- clear ownership relative to the ingress proxy.

Reopen if the gateway becomes a multi-tenant service rather than one telemetry
estate's authenticated ingress.

