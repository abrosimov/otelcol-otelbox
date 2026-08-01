#!/usr/bin/env bash
# End-to-end integration test for the OCB-built otelcol-otelbox binary.
#
# WHY THIS EXISTS
#
# The deployed edge failed *every* export for days with
#
#     Unauthenticated ... provided authorization does not match expected scheme
#     or token
#
# and nothing caught it. Every local signal stayed green throughout: the binary
# ran, launchd reported it alive, the health endpoint answered 200, the OTLP
# receiver accepted everything the applications sent it. All of those prove the
# local half of the pipeline and none of them prove delivery, so a wrong
# credential looked exactly like a healthy collector. The telemetry was simply
# gone.
#
# This script closes that gap by standing up the real thing: two processes of
# the binary under test, in the two roles it actually serves, wired to each
# other over loopback with the same authenticated hop the deployment uses. It
# asserts on what came out of the far end, which is the only signal the incident
# would have moved.
#
# WHAT IT PROVES
#
#   0. Precondition — the gateway rejects unauthenticated ingest (HTTP 401).
#      Not one of the three assertions; it is the guard that keeps assertion 3
#      honest. Delete the authenticator and this fails first, so assertion 3 can
#      never pass by proving nothing.
#   1. Happy path — a log posted to the edge reaches the gateway's sink. One
#      assertion covering the whole chain: edge receiver, processors, exporter,
#      bearer-token authentication, gateway receiver, gateway exporter.
#   2. Redaction — credential-shaped attribute values never reach the sink,
#      while the record carrying them does. The privacy invariant the collector
#      exists for, and the one the shared base layer is shared in order to keep.
#   3. The regression above — an edge holding a token that is not in the
#      gateway's allowlist drops the data AND says so. Both halves are asserted:
#      the marker must be absent from the sink, and otelcol_exporter_send_failed_*
#      on the edge's own metrics endpoint must be non-zero. The second half is
#      what the incident lacked. It is sound because the gateway classifies an
#      authentication failure as permanent while the edge exporter runs
#      retry_on_failure.max_elapsed_time: 0s (retry forever on transient
#      errors) — so a non-zero send_failed means dropped, never merely delayed.
#
# Usage: ./test/integration-test.sh <path-to-otelcol-otelbox>
#
# Exit: 0 all assertions passed; 1 at least one failed; 64 usage; 66 bad binary.
#
# CONVENTIONS THIS SCRIPT KEEPS
#
# `set -e` is deliberately NOT used. Half the control flow here is a probe that
# is expected to fail — a poll before a listener is up, a grep for a marker that
# has not arrived yet, a curl against a port still opening. Under `set -e` the
# first of those aborts the run with no diagnosis and no cleanup ordering.
# Failure is tracked in `failures` instead and drives the exit status at the
# end, so every assertion runs and reports even after an earlier one fails.
#
# There are no fixed sleeps standing in for readiness. Every wait is a bounded
# poll that also watches the collector's PID, so a process that dies on a config
# error is reported in a second rather than after the full timeout.

set -uo pipefail

readonly USAGE_EXIT=64
readonly NOINPUT_EXIT=66

# 34xxx throughout, chosen to miss both real deployments: the workstation edge
# holds 4317/4318/13133/8888 and the server gateway holds 14317-14322/8888/8889.
# A developer running their own edge must be able to run this test.
readonly EDGE_HTTP_PORT=34318
readonly EDGE_HEALTH_PORT=34133
readonly EDGE_METRICS_PORT=34888
readonly GATEWAY_HTTP_PORT=34328
readonly GATEWAY_GRPC_PORT=34327

# Generous rather than tight. The inherited production values put a 5s batch
# timeout on each of the two hops plus the queue and sink flush intervals, so
# the happy path is ~12s of legitimate latency; anything under ~30s would be a
# flake generator on a loaded CI runner. These bound failure, not success — a
# passing run touches none of them.
readonly READY_TIMEOUT=45
readonly DELIVERY_TIMEOUT=90
readonly FAILURE_TIMEOUT=90

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR="${script_dir}"
readonly REPO_ROOT="${SCRIPT_DIR%/test}"

bin="${1:-}"
if [[ -z "${bin}" ]]; then
    echo "usage: $0 <path-to-otelcol-otelbox>" >&2
    exit "${USAGE_EXIT}"
fi
if [[ ! -x "${bin}" ]]; then
    echo "integration-test: '${bin}' is not an executable" >&2
    exit "${NOINPUT_EXIT}"
fi
bin="$(cd -- "$(dirname -- "${bin}")" && pwd)/$(basename -- "${bin}")"
readonly BIN="${bin}"

for required in curl od; do
    if ! command -v "${required}" >/dev/null 2>&1; then
        echo "integration-test: '${required}' is required and not on PATH" >&2
        exit "${NOINPUT_EXIT}"
    fi
done

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/otelbox-integration.XXXXXX")"
if [[ -z "${tmp_dir}" || ! -d "${tmp_dir}" ]]; then
    echo "integration-test: could not create a temporary directory" >&2
    exit "${NOINPUT_EXIT}"
fi
readonly TMP_DIR="${tmp_dir}"
readonly SINK="${TMP_DIR}/sink.json"
readonly TOKEN_FILE="${TMP_DIR}/ingest-tokens"
readonly GATEWAY_LOG="${TMP_DIR}/gateway.log"
readonly EDGE_LOG="${TMP_DIR}/edge.log"

failures=0
gateway_pid=""
edge_pid=""

# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------

say() { printf 'integration-test: %s\n' "$*"; }
pass() { printf 'integration-test: PASS — %s\n' "$*"; }

fail() {
    printf 'integration-test: FAIL — %s\n' "$*" >&2
    failures=$((failures + 1))
}

# CI logs are the only debugging surface this script has: nobody can attach to a
# runner after the fact, and the temporary directory is gone by then. Anything
# needed to diagnose a failure has to be printed before cleanup runs.
dump_diagnostics() {
    local metrics_snapshot="${1:-}"
    printf '\n===== diagnostics =====\n' >&2
    printf -- '--- gateway.log (last 60 lines) ---\n' >&2
    tail -n 60 "${GATEWAY_LOG}" >&2 2>/dev/null || echo "(no gateway log)" >&2
    printf -- '--- edge.log (last 60 lines) ---\n' >&2
    tail -n 60 "${EDGE_LOG}" >&2 2>/dev/null || echo "(no edge log)" >&2
    printf -- '--- edge exporter metrics ---\n' >&2
    printf '%s\n' "${metrics_snapshot:-(edge metrics endpoint unreachable)}" >&2
    printf -- '--- sink (%s bytes) ---\n' "$(wc -c <"${SINK}" 2>/dev/null || echo 0)" >&2
    tail -n 10 "${SINK}" >&2 2>/dev/null || echo "(no sink file)" >&2
    printf '=======================\n\n' >&2
}

# ---------------------------------------------------------------------------
# Process lifecycle
# ---------------------------------------------------------------------------

# SIGTERM first so the collector shuts its pipelines down and flushes; SIGKILL
# only if it will not go. The bounded wait matters because a stuck collector
# holding 34318 would break the *next* run rather than this one.
stop_collector() {
    local pid="$1" name="$2"
    [[ -n "${pid}" ]] || return 0
    kill -TERM "${pid}" 2>/dev/null
    local waited=0
    while kill -0 "${pid}" 2>/dev/null; do
        if ((waited >= 40)); then
            say "${name} did not stop on SIGTERM, sending SIGKILL"
            kill -KILL "${pid}" 2>/dev/null
            break
        fi
        sleep 0.25
        waited=$((waited + 1))
    done
    wait "${pid}" 2>/dev/null
}

# Runs on every exit path — success, assertion failure, and interrupt — so a
# cancelled CI job leaves neither a collector holding a port nor a temporary
# directory behind. Diagnostics are printed while the files still exist.
cleanup() {
    local metrics_snapshot=""
    # Scraped before anything is stopped. The counters live in the collector's
    # own process, so SIGTERM takes the endpoint and the evidence with it — the
    # one number worth having in a failing CI log is the first thing lost.
    if ((failures > 0)); then
        metrics_snapshot="$(scrape "${EDGE_METRICS_PORT}" \
            | grep -E '^otelcol_exporter_(sent|send_failed|enqueue_failed|queue)')"
    fi
    stop_collector "${edge_pid}" edge
    stop_collector "${gateway_pid}" gateway
    if ((failures > 0)); then
        dump_diagnostics "${metrics_snapshot}"
    fi
    rm -rf -- "${TMP_DIR}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# ---------------------------------------------------------------------------
# Probes
# ---------------------------------------------------------------------------

random_hex() { od -An -tx1 -N"${1:-8}" /dev/urandom | tr -d ' \n'; }

scrape() { curl -fsS --max-time 5 "http://127.0.0.1:${1}/metrics" 2>/dev/null; }

sink_contains() { [[ -s "${SINK}" ]] && grep -qF -- "$1" "${SINK}"; }

edge_healthy() {
    curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:${EDGE_HEALTH_PORT}/" 2>/dev/null
}

# Echoes the HTTP status the gateway gives an unauthenticated OTLP post, or
# nothing when the listener is not answering yet. The payload is an empty
# resourceLogs array: it reaches the receiver but carries no record, so a probe
# can never contaminate the sink the assertions read.
gateway_unauthenticated_status() {
    curl -sS --max-time 5 -o /dev/null -w '%{http_code}' \
        -X POST -H 'Content-Type: application/json' \
        -d '{"resourceLogs":[]}' \
        "http://127.0.0.1:${GATEWAY_HTTP_PORT}/v1/logs" 2>/dev/null
}

# `000` is curl's placeholder for "no HTTP response at all" — connection refused
# while the listener is still opening. Treating it as a response makes the
# readiness poll return on its first attempt, and the precondition below then
# judges the gateway on a request that never arrived.
gateway_responds() {
    local code
    code="$(gateway_unauthenticated_status)"
    [[ -n "${code}" && "${code}" != "000" ]]
}

# True when any otelcol_exporter_send_failed_* series carries a positive value.
# Matched on the prefix rather than a full metric name on purpose: the OTel
# Prometheus exporter appends `_total` to counters and upstream has moved that
# suffix around between releases, so pinning the exact name would turn a
# cosmetic upstream change into a red build. `index($0, p) == 1` anchors at the
# line start, which also skips the `# HELP`/`# TYPE` lines.
exporter_reports_failure() {
    scrape "${EDGE_METRICS_PORT}" | awk '
        index($0, "otelcol_exporter_send_failed") == 1 && $NF + 0 > 0 { found = 1 }
        END { exit found ? 0 : 1 }
    '
}

# Bounded poll. Watches the collector PID as well as the predicate so a process
# that died on a bad config is reported immediately, with the reason, instead of
# after the full timeout with none.
poll() {
    local timeout="$1" watch_pid="$2" what="$3"
    shift 3
    local deadline=$(($(date +%s) + timeout))
    while :; do
        if "$@"; then
            return 0
        fi
        if [[ -n "${watch_pid}" ]] && ! kill -0 "${watch_pid}" 2>/dev/null; then
            printf 'integration-test: collector (pid %s) exited while waiting for %s\n' \
                "${watch_pid}" "${what}" >&2
            return 1
        fi
        if (($(date +%s) >= deadline)); then
            printf 'integration-test: timed out after %ss waiting for %s\n' \
                "${timeout}" "${what}" >&2
            return 1
        fi
        sleep 0.5
    done
}

# ---------------------------------------------------------------------------
# Payloads
# ---------------------------------------------------------------------------

# A single OTLP/HTTP JSON log record carrying `otelbox.ci.marker`. The marker
# key is checked against none of the base layer's blocked patterns and the
# marker value against none of its blocked values, so the record itself always
# survives redaction — which is what makes "the marker arrived" and "the secret
# did not" independent facts rather than one fact stated twice.
otlp_log_payload() {
    local marker="$1" extra_attributes="${2:-}" now
    now="$(date +%s)"
    printf '{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"otelbox-integration-test"}}]},"scopeLogs":[{"scope":{"name":"otelbox.integration"},"logRecords":[{"timeUnixNano":"%s000000000","observedTimeUnixNano":"%s000000000","severityNumber":9,"severityText":"INFO","body":{"stringValue":"otelbox integration test record"},"attributes":[{"key":"otelbox.ci.marker","value":{"stringValue":"%s"}}%s]}]}]}]}' \
        "${now}" "${now}" "${marker}" "${extra_attributes}"
}

# Three attributes covering both redaction mechanisms in config/base.yaml, so a
# pattern list that loses either one fails here:
#   password                          — blocked_key_patterns, innocuous value
#   http.request.header.authorization — blocked_key_patterns and blocked_values
#   payload.note                      — blocked_values only, innocuous key
# The last is the one that matters most: it is a credential smuggled under a key
# nobody would think to block, which is how credentials actually reach telemetry.
credential_attributes() {
    local secret="$1"
    printf ',{"key":"password","value":{"stringValue":"%s"}}' "${secret}"
    printf ',{"key":"http.request.header.authorization","value":{"stringValue":"Bearer %s"}}' "${secret}"
    printf ',{"key":"payload.note","value":{"stringValue":"sk-%s"}}' "${secret}"
}

# Posts to the edge and insists on a 2xx. Without this check a later "the marker
# never arrived" would be ambiguous between the pipeline dropping the record and
# the test never having sent it.
send_to_edge() {
    local description="$1" payload="$2" code
    code="$(curl -sS --max-time 10 -o "${TMP_DIR}/last-send.out" -w '%{http_code}' \
        -X POST -H 'Content-Type: application/json' --data-binary "${payload}" \
        "http://127.0.0.1:${EDGE_HTTP_PORT}/v1/logs" 2>"${TMP_DIR}/last-send.err")"
    if [[ "${code}" != "200" ]]; then
        fail "${description}: the edge receiver refused the record (HTTP '${code}')"
        printf 'integration-test: curl stderr: %s\n' "$(cat "${TMP_DIR}/last-send.err")" >&2
        printf 'integration-test: response body: %s\n' "$(cat "${TMP_DIR}/last-send.out")" >&2
        return 1
    fi
    return 0
}

# ---------------------------------------------------------------------------
# Collector startup
# ---------------------------------------------------------------------------

start_gateway() {
    OTELBOX_GATEWAY_TOKEN_FILE="${TOKEN_FILE}" \
    OTELBOX_GATEWAY_STORAGE="${TMP_DIR}/gateway-storage" \
    OTELBOX_CI_SINK="${SINK}" \
    CLICKSTACK_INGESTION_API_KEY="unused-in-ci" \
        "${BIN}" \
        --config "${REPO_ROOT}/config/base.yaml" \
        --config "${REPO_ROOT}/config/examples/gateway.yaml" \
        --config "${SCRIPT_DIR}/config/gateway-ci.yaml" \
        >"${GATEWAY_LOG}" 2>&1 &
    gateway_pid=$!
}

# `storage_subdir` is a distinct WAL per edge run. Sharing one would let a
# record enqueued by the happy-path edge replay under the second edge's
# credential, which would make assertion 3 report on the wrong record.
start_edge() {
    local token="$1" storage_subdir="$2"
    OTELBOX_EDGE_STORAGE="${TMP_DIR}/${storage_subdir}" \
    OTELBOX_EDGE_ENDPOINT="127.0.0.1:${GATEWAY_GRPC_PORT}" \
    OTELBOX_EDGE_TOKEN="${token}" \
        "${BIN}" \
        --config "${REPO_ROOT}/config/base.yaml" \
        --config "${REPO_ROOT}/config/examples/edge.yaml" \
        --config "${SCRIPT_DIR}/config/edge-ci.yaml" \
        >>"${EDGE_LOG}" 2>&1 &
    edge_pid=$!
}

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

# Freshly generated every run. A checked-in token would still be a credential in
# a repository, and a fixed one could match a value left over from an earlier
# run and let a broken build pass.
valid_token="otelbox-ci-valid-$(random_hex 16)"
wrong_token="otelbox-ci-wrong-$(random_hex 16)"
secret="otelboxsecret$(random_hex 16)"
marker_happy="otelbox-ci-happy-$(random_hex 8)"
marker_redaction="otelbox-ci-redaction-$(random_hex 8)"
marker_unauthorised="otelbox-ci-unauthorised-$(random_hex 8)"
readonly VALID_TOKEN="${valid_token}"
readonly WRONG_TOKEN="${wrong_token}"
readonly SECRET="${secret}"
readonly MARKER_HAPPY="${marker_happy}"
readonly MARKER_REDACTION="${marker_redaction}"
readonly MARKER_UNAUTHORISED="${marker_unauthorised}"

# The allowlist, in the format bearertokenauthextension parses: one token per
# line, first whitespace-delimited field is the token, the rest is a comment.
# The trailing comment is not decoration — it is the format assertion. A change
# upstream that stopped ignoring it would break this test rather than a server.
printf '%s  otelbox integration test — the only credential this gateway accepts\n' \
    "${VALID_TOKEN}" >"${TOKEN_FILE}"
chmod 0600 "${TOKEN_FILE}"

say "binary          ${BIN}"
say "temporary state ${TMP_DIR} (removed on exit)"

# ---------------------------------------------------------------------------
# Bring the pipeline up
# ---------------------------------------------------------------------------

say "starting the gateway (OTLP gRPC ${GATEWAY_GRPC_PORT}, HTTP ${GATEWAY_HTTP_PORT}) …"
start_gateway
if ! poll "${READY_TIMEOUT}" "${gateway_pid}" "the gateway's OTLP/HTTP listener" gateway_responds; then
    # Nothing downstream can run, so this exits rather than carrying on. The
    # diagnostics come from the EXIT trap — printing them here as well would
    # duplicate sixty lines of collector log in the CI output.
    fail "the gateway never accepted a connection"
    exit 1
fi

# Precondition, not an assertion. Assertion 3 claims that a wrong token stops
# delivery; that claim is worthless unless the gateway is checking tokens at
# all. Remove `auth:` from the receiver, or the extension from the config, and
# this returns 200 and the run fails here — which is the point.
say "checking that the gateway rejects unauthenticated ingest …"
unauthenticated_status="$(gateway_unauthenticated_status)"
if [[ "${unauthenticated_status}" == "401" ]]; then
    pass "precondition: unauthenticated ingest rejected with HTTP 401"
else
    fail "precondition: unauthenticated ingest returned HTTP '${unauthenticated_status}', expected 401 — the gateway is not enforcing bearertokenauth/ingest, so assertion 3 below cannot prove anything"
fi

say "starting the edge with a valid token (OTLP/HTTP ${EDGE_HTTP_PORT}) …"
start_edge "${VALID_TOKEN}" edge-storage-valid
if ! poll "${READY_TIMEOUT}" "${edge_pid}" "the edge's health endpoint" edge_healthy; then
    fail "the edge never became healthy"
    exit 1
fi

# ---------------------------------------------------------------------------
# Assertion 1 — happy path
# ---------------------------------------------------------------------------

say "assertion 1: a log sent to the edge reaches the gateway's sink"
if send_to_edge "assertion 1" "$(otlp_log_payload "${MARKER_HAPPY}")"; then
    if poll "${DELIVERY_TIMEOUT}" "${edge_pid}" \
        "marker ${MARKER_HAPPY} to reach the sink" sink_contains "${MARKER_HAPPY}"; then
        pass "assertion 1: edge → authenticated gateway → sink delivered ${MARKER_HAPPY}"
    else
        fail "assertion 1: marker ${MARKER_HAPPY} never reached ${SINK} within ${DELIVERY_TIMEOUT}s — the edge accepted the record but the two-hop pipeline did not deliver it"
    fi
fi

# ---------------------------------------------------------------------------
# Assertion 2 — redaction
# ---------------------------------------------------------------------------

say "assertion 2: credential-shaped attribute values are stripped before the sink"
if send_to_edge "assertion 2" \
    "$(otlp_log_payload "${MARKER_REDACTION}" "$(credential_attributes "${SECRET}")")"; then
    if poll "${DELIVERY_TIMEOUT}" "${edge_pid}" \
        "marker ${MARKER_REDACTION} to reach the sink" sink_contains "${MARKER_REDACTION}"; then
        # Deliberately the whole file, not the matching record. Redaction that
        # leaked the value into a summary attribute, a resource attribute or a
        # neighbouring batch would still be a leak.
        if sink_contains "${SECRET}"; then
            fail "assertion 2: the secret value reached ${SINK} — redaction/secrets did not strip it. The record arrived (${MARKER_REDACTION}), so the pipeline ran; the pattern list in config/base.yaml is what failed"
        else
            pass "assertion 2: record delivered with every credential-shaped value stripped"
        fi
    else
        fail "assertion 2: marker ${MARKER_REDACTION} never reached ${SINK} within ${DELIVERY_TIMEOUT}s — cannot judge redaction on a record that was not delivered"
    fi
fi

# ---------------------------------------------------------------------------
# Assertion 3 — the incident
# ---------------------------------------------------------------------------

say "assertion 3: an edge with a token outside the gateway's allowlist drops data, visibly"
stop_collector "${edge_pid}" edge
edge_pid=""

say "restarting the edge with a token the gateway does not know …"
start_edge "${WRONG_TOKEN}" edge-storage-wrong
if ! poll "${READY_TIMEOUT}" "${edge_pid}" "the edge's health endpoint" edge_healthy; then
    fail "assertion 3: the edge never became healthy on the second run"
else
    # Health is green here and stays green for the rest of the run. That is the
    # incident in one line: the endpoint launchd and every dashboard watched
    # answered 200 the entire time telemetry was being dropped.
    if send_to_edge "assertion 3" "$(otlp_log_payload "${MARKER_UNAUTHORISED}")"; then
        if poll "${FAILURE_TIMEOUT}" "${edge_pid}" \
            "otelcol_exporter_send_failed_* to go non-zero on the edge" exporter_reports_failure; then
            # Ordered deliberately: only once the exporter has recorded a
            # permanent failure is "absent from the sink" a decided fact rather
            # than a record still in flight.
            if sink_contains "${MARKER_UNAUTHORISED}"; then
                fail "assertion 3: marker ${MARKER_UNAUTHORISED} reached ${SINK} despite the edge holding a token outside the gateway's allowlist — bearertokenauth/ingest is not rejecting it"
            else
                pass "assertion 3: data dropped at the gateway and the drop is visible in the edge's own metrics"
                printf 'integration-test:   %s\n' \
                    "$(scrape "${EDGE_METRICS_PORT}" | grep '^otelcol_exporter_send_failed' | head -n 3)"
                # Supporting evidence only. The wording of the upstream error is
                # not a contract, so a change to it must not fail the build —
                # but when it is there it is the exact string from the incident,
                # and printing it makes the CI log self-explanatory.
                if grep -qi 'unauthenticated\|does not match expected scheme or token' "${EDGE_LOG}"; then
                    say "  edge log carries the incident's error, as expected:"
                    grep -i -m 1 'unauthenticated\|does not match expected scheme or token' "${EDGE_LOG}" \
                        | sed 's/^/integration-test:   /'
                fi
            fi
        else
            # The failure mode worth naming: send_failed stuck at zero means
            # either the export succeeded (the token check is gone) or nothing
            # was ever attempted. Both make the negative case vacuous, which is
            # worse than a red build.
            fail "assertion 3: otelcol_exporter_send_failed_* stayed at zero for ${FAILURE_TIMEOUT}s on port ${EDGE_METRICS_PORT}. Either the gateway accepted a token that is not in its allowlist, or the edge never attempted the export — in both cases the drop this test exists to catch would be invisible again"
            if sink_contains "${MARKER_UNAUTHORISED}"; then
                fail "assertion 3: and marker ${MARKER_UNAUTHORISED} did reach the sink — the gateway accepted an unknown token"
            fi
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Verdict
# ---------------------------------------------------------------------------

if ((failures > 0)); then
    printf 'integration-test: FAILED — %d check(s) did not pass\n' "${failures}" >&2
    exit 1
fi

say "OK — all three assertions passed"
