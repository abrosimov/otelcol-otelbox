# Image smoke post-mortem and self-reflection

- Incident date: 2026-08-09
- Recorded: 2026-08-09 13:09:01 +04 (UTC+04:00)
- Repository: `abrosimov/otelcol-otelbox`
- Scope: local working tree only
- External effects: none; no commit, push, merge, release or registry write

## Post-mortem

### Summary

The first implementation put the OCI runtime contract into
`test/image-smoke.sh` and used a one-off shell command to build and exercise a
candidate image. That was the wrong ownership boundary. This repository already
owns reproducible contract checks in the stdlib-only `tools/ci` Go module, and a
green workflow must execute the same repository-owned check that a developer can
run locally.

The shell implementation was removed. The image contract now lives in
`otelbox-ci image smoke`, with Go tests for the rootfs contract, Docker command
graph, declared-user preservation, readiness state checks, cleanup and failure
logs. The image job installs the pinned Go 1.25.12 toolchain and invokes the same
command for the local tag and the published digest.

### What happened

At the start of the change, the proposed `test/image-smoke.sh` path was treated
as an implementation instruction without reconciling it against the established
`tools/ci` ownership model. The script then accumulated policy: tar-output
parsing, Docker lifecycle management, readiness polling, cleanup and diagnostic
logging. Local validation also used a custom cross-build command rather than a
single checked-in verification entry point.

This produced two avoidable failures during local work:

1. The first build wrote to restricted Go and Buildx caches and failed because
   the command did not use the repository's permitted cache locations.
2. The retry used the host Go 1.24.0 and hit the known linker failure before the
   build was repeated with the pinned Go 1.25.12 toolchain.

Neither failure diagnosed the packaging change. Both were evidence that the
validation path was not yet reproducible.

After moving the check into Go, I repeated the same class of mistake by wrapping
the final positive and negative checks in another multi-command shell fragment
with a cleanup function and trap. Although that fragment was not persisted, it
still bypassed the canonical interface I had just established. The user stopped
that invocation. It is not counted as validation evidence.

### Impact

- The initial patch did not meet the repository's maintainability standard.
- Shell parsing of `tar -tvf` output introduced host-tool formatting risk.
- The one-off build path mixed toolchain, packaging and runtime claims.
- A local success could not be described as the same proof CI would execute.
- The user had to stop the implementation and restate an already established
  working agreement twice.

No defective change was committed or published. `builder.yaml` remains at
2.1.0, so the automatic release path was not armed.

### Root cause

The primary cause was failure to resolve a mechanism-level request against the
repository's existing validation architecture before editing. I optimised for
the named output file instead of the required invariant: one reproducible,
reviewable and tested image-release gate.

Contributing factors were:

- treating the requested Bash filename as more authoritative than the existing
  Go-owned contract boundary;
- starting implementation before writing down the evidence chain from build to
  published digest;
- accepting an ad-hoc local command as validation instead of first creating the
  canonical repository command;
- not checking the image job's Go toolchain ownership as soon as Go execution
  moved into that job;
- confusing a non-persisted shell fragment with an acceptable verification
  method after the user had explicitly rejected ad-hoc scripts.

### Corrective changes

- Replaced the Bash smoke with `otelbox-ci image smoke`.
- Used Go's `archive/tar` reader for exact UID, GID, type and mode checks.
- Added tests for valid and invalid rootfs entries, the restricted Docker run,
  readiness before and after the probe, signal cancellation, cleanup, and
  runtime logs on failure.
- Added pinned Go 1.25.12 setup to the image job.
- Made both the pre-push local image and post-push digest checks invoke the same
  command and configuration.
- Kept binary component inventory separate from OCI runtime readiness.
- Kept the 2.1.1 version bump outside this patch until release authority is
  granted separately.

### Prevention

For future CI work in this repository:

1. Identify the owning checked-in verifier before choosing a script or workflow
   implementation.
2. Put reusable validation logic in `tools/ci`; workflow YAML only composes the
   same command around artefact production and publication.
3. Pin every toolchain in every job that executes it.
4. Keep binary, configuration, harness and OCI packaging evidence as separate
   claims.
5. Require one positive candidate and one known-negative regression check before
   calling a new gate sensitive to the defect.
6. Treat remote CI as unproved until the exact committed workflow completes;
   local checks cannot substitute for that result.
7. Do not use a multi-command shell fragment as the hand-off proof when a
   repository-owned command exists.

## Self-reflection

I took a proposed artefact name, `test/image-smoke.sh`, as the shortest path to
the requested behaviour. That was too literal and too local. The durable rule in
this repository is that CI contract logic belongs in the tested Go tool under
`tools/ci`. I should have recognised that rootfs inspection, process lifecycle,
readiness and diagnostics were contract logic, not a thin shell orchestration
layer.

The important mistake was not that Bash can never start Docker. It was that I
let Bash become the only specification of a release invariant and then validated
it with another one-off shell command. That created two weak points: the script
itself was hard to test structurally, and the local evidence did not necessarily
match the command run by CI.

I also reacted to the first build failure by repairing the command incrementally.
That exposed two facts I should have established before running it: this checkout
requires writable task-specific caches in the restricted environment, and the
collector must use Go 1.25.12. The failures were not mysterious; they followed
from bypassing the repository's prescribed verification path.

The correct sequence would have been:

1. Map each requested claim to its owner: Dockerfile for rootfs construction,
   test configuration for CA loading, `tools/ci` for verification logic, and the
   workflow for pre-push and post-push orchestration.
2. Add the Go command and its unit tests first.
3. Make the workflow invoke that command with a pinned Go toolchain.
4. Run the same command against a corrected image and a known-broken image.
5. Report local proof separately from unrun remote CI and release state.

The user's explicit mention of a shell script explains why that option was
available, but it does not excuse failing to reconcile it with the stronger,
already accepted repository rule. When a requested mechanism conflicts with a
project invariant, I must surface the conflict and preserve the invariant rather
than silently choosing the most literal implementation.

I then failed a second time by using a shell wrapper merely because it was not
going to be committed. That distinction was irrelevant: the user required a
reproducible verification path, not only a clean final file tree. A disposable
command can still produce dishonest evidence if it is materially different from
the checked-in CI path.

My operational guardrail from this incident is: before adding or running any CI
script, I will first identify the repository's existing validation command and
ask whether the logic belongs there. If it does, the workflow and local
validation must call that command directly. Any diagnostic command remains
diagnostic only and must never be promoted into a readiness claim.
