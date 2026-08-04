// Package harness is the end-to-end test for the OCB-built otelcol-otelbox
// binary. It has no library surface: everything lives in the _test.go files
// beside this one, and this file exists so the package builds and vets like any
// other.
//
// # Why this exists
//
// The deployed edge failed *every* export for days with
//
//	Unauthenticated ... provided authorization does not match expected scheme
//	or token
//
// and nothing caught it. Every local signal stayed green throughout: the binary
// ran, launchd reported it alive, the health endpoint answered 200, the OTLP
// receiver accepted everything the applications sent it. All of those prove the
// local half of the pipeline and none of them prove delivery, so a wrong
// credential looked exactly like a healthy collector. The telemetry was simply
// gone.
//
// This harness closes that gap by standing up the real thing: processes of the
// binary under test in the edge and gateway roles, wired to each other over
// loopback across the same authenticated, TLS-verified hop the roles define. It
// asserts on what came out of the far end, which is the only signal the
// incident would have moved.
//
// The host-agent role is outside this harness. Its journald receiver is
// Linux-only and its useful assertions depend on the target host's /proc,
// journal and service-account permissions, so the consuming deployment must
// validate and smoke it on that host.
//
// # What it proves
//
// TestEdgeToGatewayDelivery:
//
//  0. Precondition — the gateway rejects unauthenticated ingest (HTTP 401). Not
//     one of the five assertions; it is the guard that keeps assertion 4
//     honest. Delete the authenticator and this fails first, so assertion 3 can
//     never pass by proving nothing.
//  1. Happy path — a log posted to the edge reaches the gateway's sink. One
//     assertion covering the whole chain: edge receiver, processors, exporter,
//     bearer-token authentication, gateway receiver, gateway exporter.
//  2. Redaction — the configured credential corpus never reaches the sink,
//     while the record carrying it does and representative ordinary values
//     remain unchanged. The gateway is loaded with a CI-only redaction bypass,
//     so this result is attributable to the edge rather than to either of two
//     identical processors.
//  3. Selected routing — an ordinary trace reaches only the ordinary recipient,
//     while a trace classified with `otelbox.telemetry.class=llm` additionally
//     reaches the OTLP/HTTP recipient. That backend accepts only protobuf with
//     the configured authorisation and protocol headers.
//  4. The regression above — an edge holding a token that is not in the
//     gateway's allowlist drops the data AND says so. Both halves are asserted:
//     the marker must be absent from the sink, and otelcol_exporter_send_failed_*
//     on the edge's own metrics endpoint must be non-zero. The second half is
//     what the incident lacked. It is sound because the gateway classifies an
//     authentication failure as permanent while the edge exporter runs
//     retry_on_failure.max_elapsed_time: 0s (retry forever on transient errors)
//     — so a non-zero send_failed means dropped, never merely delayed. It
//     carries a second precondition of its own, inside the subtest rather than
//     beside it: a certificate fault satisfies both halves exactly as a
//     rejected token does, so the assertion first handshakes with the gateway
//     against the CA the edge was handed and refuses to conclude anything if
//     the transport is at fault.
//  5. A non-empty allowlist replacement activates the new token and revokes the
//     previous one, including the final-client revocation operation.
//
// TestEdgePersistsAcceptedDataBeforeAcknowledgement: a record accepted while
// the gateway is unavailable survives an edge SIGKILL and is delivered after
// the gateway and edge start. The forced kill is the assertion's boundary: a
// graceful shutdown would flush an in-memory processor batch and could let a
// non-durable pipeline pass.
//
// TestGatewayPersistsSelectedTraceBeforeAcknowledgement applies the same
// SIGKILL boundary to the selected-traces HTTP recipient and its own WAL.
//
// TestGatewayPersistsOrdinarySignalsBeforeAcknowledgement applies that boundary
// to the signal-specific log, metric and trace gRPC WALs in one crash.
//
// TestGatewayRedaction posts logs, traces and metrics directly to the gateway
// without the bypass above. It proves the same configured corpus on the
// ordinary recipient and the additional selected-traces recipient.
//
// TestRequiredRecipientCouplingUnderQueuePressure: required gateway recipients are
// not independent under `block_on_overflow: true`. A stopped recipient with queue
// headroom leaves ingest and the healthy recipient untouched; once its queue is
// full, synchronous fan-out backpressures ingest. A full log queue does not
// block the separately stored metric and trace queues.
//
// # Usage
//
// From the repository root:
//
//	go test -C test/harness . -count=1 -timeout 15m -v \
//	    -args -otelcol-binary "$PWD/_build/otelcol-otelbox"
//
// `-C` rather than `./test/harness`, because this is a module of its own and
// the root has no go.mod: the package path form cannot resolve a module and
// fails before running anything. CI reaches the same place with
// `working-directory: test/harness` and a bare `go test .`.
//
// Exit: 0 all assertions passed, 1 anything else — a failed assertion, a
// missing -otelcol-binary, a binary without the executable bit. The shell
// harness's 64 and 66 are gone rather than ported: `go test` reports its own 1
// whatever the test binary exits with, so the distinction could not reach a
// caller. The message on stderr is what says which of the three it was.
//
// # Conventions this harness keeps
//
// Assertions are subtests, and a failing one does not abort the run: half the
// value of a failure is what the *other* assertions did, and a t.Fatalf at the
// top level would take that with it. Only a setup step that leaves nothing to
// assert on — a collector that never came up — is fatal.
//
// There are no fixed sleeps standing in for readiness. Every wait is a bounded
// poll that also watches the collector process, so one that dies on a config
// error is reported in a second with its last log lines rather than after the
// full timeout with none.
//
// # The sandbox trap
//
// This test cannot run under a command sandbox that denies the boot-time
// sysctl: the edge's `resource_detection` processor fails to start with
// "getting boot time: operation not permitted", which reads like a
// configuration fault and is not one. Nothing in config/ is wrong when that
// happens. CI runners are unaffected.
package harness
