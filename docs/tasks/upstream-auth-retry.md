# Task: upstream authentication-failure retry policy

## Status

Deferred external contribution. This is not a 2.1 release blocker.

## Problem

Standard OTLP exporters classify gRPC `Unauthenticated` and
`PermissionDenied`, and HTTP 401 and 403, as permanent failures. This is correct
for a static credential but loses WAL-backed data during an operational
credential rotation.

`otelcol-otelbox` provides an opt-in long retry interval and rereads the watched
credential before the next attempt. Keeping that behaviour only downstream
creates a permanent maintenance obligation.

## Proposed upstream shape

- opt-in and disabled by default;
- limited to the four authentication/authorisation responses;
- requires ordinary retry to be enabled;
- uses a separately configured interval so an invalid credential cannot create
  a tight retry loop;
- leaves malformed payload and other permanent errors unchanged.

## Evidence to attach

- unit tests for enabled, disabled and invalid configuration;
- live credential-file replacement without process restart;
- persistent-queue retention during rejection;
- rationale distinguishing operational credential rotation from retrying every
  permanent response.

Open an upstream issue or proposal only as an explicit external action.

