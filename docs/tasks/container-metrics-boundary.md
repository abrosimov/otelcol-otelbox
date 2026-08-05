# Task: define a safe container-metrics boundary

## Status

Deferred. This is not a 2.1 release blocker.

## Problem

The host-agent profile has no per-container CPU, memory, network or I/O
visibility. Connecting `podman_stats` directly to a rootless Podman socket would
also hand the Collector access to a control-plane API. A read-only filesystem
mount does not turn that API into a read-only telemetry interface.

## Decision required

Select a metrics-only boundary before linking a container receiver. Candidate
families are:

1. cgroups v2 or systemd-unit aggregation without a container API;
2. a constrained proxy that exposes only the required Podman inspection calls;
3. a deployment-specific Kubernetes path using `kubeletstats`, if Kubernetes
   becomes a supported environment.

Direct access to an unfiltered Docker or Podman socket is not an acceptable
reference design.

## Admission criteria

- threat model covering socket/API authority, service-account privileges and
  tenant isolation;
- bounded series cardinality and stable container identity semantics;
- no command-line or environment credential capture;
- target-host tests for container lifecycle, daemon outage and permission
  denial;
- explicit ownership split between this binary and the consuming deployment.

Reopen when a consuming deployment needs container-level alerts that cannot be
met by host or service metrics.

