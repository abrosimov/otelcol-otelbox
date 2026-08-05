# Roadmap

This roadmap separates the supported product contract from experiments and
deployment-owned controls. A version number changes only after the corresponding
capability and its evidence are present in this repository.

## Version 2.1 target

Version 2.1 improves maintainability and deployment extensibility without
changing the three-role configuration model or weakening persistence before
acknowledgement.

The target state is:

- contract checks run through a tested, standard-library Go tool instead of
  repository-owned Bash programs;
- Collector, Contrib, OCB and local exporter adaptation dependencies form one
  reviewed version set and cannot drift independently;
- the auth-failure retry adaptation is reduced to the smallest maintainable
  layer that preserves the existing YAML and delivery contract;
- deployments can add organisation-specific redaction after the canonical
  credential floor without editing that floor;
- release evidence covers every supported profile and states clearly which
  host-agent behaviour still requires a target-host acceptance test.

The following contracts do not change in 2.1:

- one binary and one self-contained profile per `edge`, `gateway` or
  `host-agent` process;
- no `batch` processor before a persistent exporter queue;
- bounded, at-least-once delivery with authentication failures retained in the
  WAL;
- no embedded deployment manager, secret store, ingress proxy or service
  supervisor;
- no container control socket exposed merely to obtain statistics.

## Delivery sequence

| Phase | Outcome | Gate | Status |
| --- | --- | --- | --- |
| 1. Go contract tooling | Shared-region, manifest and built-binary checks are implemented and unit-tested in `tools/ci`. | Tool unit tests, `go vet`, and equivalent checks against the current profiles and binary pass. | Implemented and verified locally. |
| 2. Exporter adaptation | Thin wrappers own persistence and retry around unmodified standard upstream exporters. | Unit tests plus credential-rotation, HTTP/gRPC auth, WAL and SIGKILL harness cases pass. | Implemented; the full harness passed locally. |
| 3. Dependency set | All Collector and Contrib components move together to `v0.158.0`; the corresponding stable Collector modules move to `v1.64.0`. | No mixed version set, the OCB build succeeds, profiles validate and the full harness passes. | Implemented and verified locally. |
| 4. Extension and assurance contract | The redaction extension point and host-agent evidence boundary are documented and CI exercises all repository-owned checks. | Documentation, static checks, Linux profile validation and release artefact checks agree. | Implemented; Linux execution awaits CI. |
| 5. Version 2.1 candidate | `dist.version` is `2.1.0` after the implementation gates are present. | CI must pass before the default-branch workflow may publish. | Candidate prepared; not published. |

The dependency set is fixed for the 2.1 cycle. A later upstream release does
not move this target automatically.

## Deferred design work

The following work is intentionally outside the 2.1 release boundary:

- [container metrics privilege boundary](tasks/container-metrics-boundary.md);
- [strategic trace sampling](tasks/strategic-trace-sampling.md);
- [tenant-aware ingress rate limiting](tasks/tenant-rate-limiting.md);
- [upstream authentication-retry contribution](tasks/upstream-auth-retry.md).

These are not hidden release blockers. Each task records the condition that
would make it worth scheduling and the evidence required before it can alter a
supported profile.

## Release evidence

Version 2.1 is ready for publication only when:

1. the Go contract tool and every Go module pass tests, formatting and vetting;
2. both native binaries match the manifest and report version `2.1.0`;
3. all three profiles validate on Linux and the edge profile validates on its
   native macOS target;
4. the full black-box harness passes, including live credential recovery and
   persistence across forced termination;
5. image, configuration bundle, checksum and formula checks pass;
6. the remaining target-host host-agent smoke is stated as deployment
   acceptance evidence rather than misreported as repository CI coverage.
