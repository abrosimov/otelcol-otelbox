package harness

import (
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// These scenarios concern what happens to a record after the gateway admitted
// it: how the selected-traces exporter treats the recipient's answer. They use
// the in-process HTTP recipient double, whose replies a test can script.

// The recipient's retry window: the profiles' first retry waits 5s, so 15s
// covers at least two attempts a retryable answer would have caused.
const recipientRetryWindow = 15 * time.Second

type recipientScenario struct {
	gateway  *collector
	spec     collectorSpec
	backend  *selectedTraceBackend
	token    string
	sink     string
	selected string
}

// startRecipientScenario starts the selected-traces double and a gateway that
// exports to it, and returns once the gateway answers.
func startRecipientScenario(t *testing.T, name string, extraEnv map[string]string) *recipientScenario {
	t.Helper()

	state := t.TempDir()
	scenario := &recipientScenario{
		sink:  filepath.Join(state, "ordinary-sink.json"),
		token: "otelbox-ci-recipient-" + randomHex(t, 16),
	}
	tokenFile := filepath.Join(state, "ingest-tokens")
	writeTokenFile(t, tokenFile, scenario.token)
	chain := mintGatewayChain(t, state)
	contract := newSelectedTraceContract(t, state, selectedTraceEndpoint)
	scenario.backend = startSelectedTraceBackend(t, contract)

	env := map[string]string{
		"OTELBOX_INGEST_TOKEN_FILE":              tokenFile,
		"OTELBOX_STORAGE_DIR":                    filepath.Join(state, "gateway-storage"),
		"OTELBOX_CI_SINK":                        scenario.sink,
		"OTELBOX_CI_GATEWAY_CERT_FILE":           chain.certFile,
		"OTELBOX_CI_GATEWAY_KEY_FILE":            chain.keyFile,
		"OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT": "127.0.0.1:34398",
	}
	contract.addEnv(env)
	for key, value := range extraEnv {
		env[key] = value
	}
	scenario.spec = collectorSpec{
		name:     name,
		stateDir: state,
		env:      env,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	}
	scenario.gateway = scenario.start(t, name)
	return scenario
}

func (s *recipientScenario) start(t *testing.T, name string) *collector {
	t.Helper()

	s.spec.name = name
	gateway := startCollector(t, s.spec)
	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}
	return gateway
}

func (s *recipientScenario) dumpOnFailure(t *testing.T, collectors ...*collector) {
	if !t.Failed() {
		return
	}
	dumpDiagnostics(t,
		[]diagnosticSection{
			{"gateway exporter metrics", exporterMetrics(gatewayMetricsPort,
				"otelcol_exporter_sent", "otelcol_exporter_send_failed",
				"otelcol_exporter_enqueue_failed", "otelcol_exporter_queue")},
			{"selected-traces backend", s.backend.diagnostics()},
		}, collectors...)
}

// postSelected admits one selected trace carrying marker and requires 200.
func (s *recipientScenario) postSelected(t *testing.T, marker string) {
	t.Helper()

	s.postSelectedPayload(t, tracePayload(t, marker, true))
}

func (s *recipientScenario) postSelectedPayload(t *testing.T, payload []byte) {
	t.Helper()

	status, body, err := postTraces(sendClient, gatewayHTTPPort, s.token, payload)
	if err != nil {
		t.Fatalf("the gateway did not answer the selected trace: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("the gateway refused the selected trace (HTTP %d): %s", status, body)
	}
}

// TestGatewayResendsWhenRecipientAcknowledgementIsLost is the lost-ACK window
// of PC-11: the recipient has the record but the gateway dies before it sees
// the answer. Delivery is at least once, so the restarted gateway must send it
// again; a second copy is the expected outcome, not a defect.
func TestGatewayResendsWhenRecipientAcknowledgementIsLost(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-before-lost-ack", nil)
	collectors := []*collector{scenario.gateway}
	defer func() { scenario.dumpOnFailure(t, collectors...) }()

	marker := "otelbox-ci-lost-ack-" + randomHex(t, 8)
	release := make(chan struct{})
	scenario.backend.setReply(func([]byte) backendReply {
		return backendReply{status: http.StatusOK, hold: release}
	})
	scenario.postSelected(t, marker)

	if err := poll(deliveryTimeout, scenario.gateway, "the recipient to receive "+marker, func() bool {
		return scenario.backend.count(marker) >= 1
	}); err != nil {
		t.Fatalf("the recipient never received the admitted trace: %v", err)
	}
	// The recipient holds its answer, so the exporter's request is still open:
	// the kill lands after receipt and before the acknowledgement.
	scenario.gateway.crash(t)
	scenario.backend.setReply(nil)
	close(release)

	restarted := scenario.start(t, "gateway-after-lost-ack")
	collectors = append(collectors, restarted)
	if err := poll(deliveryTimeout, restarted, "the restarted gateway to send "+marker+" again", func() bool {
		return scenario.backend.count(marker) >= 2
	}); err != nil {
		t.Fatalf("the recipient received %s once and the gateway died before the acknowledgement, but the restarted gateway did not send it again: %v",
			marker, err)
	}
}

// TestRecipientHTTP500IsRetriedUntilDelivered holds the PC-07 correction: the
// pinned otlphttp exporter calls HTTP 500 permanent, which dropped an admitted
// record on one transient failure. retry_on_server_error turns it back into an
// ordinary retry, so the record is sent again on the backoff and delivered,
// and nothing is counted as failed or logged as dropped.
func TestRecipientHTTP500IsRetriedUntilDelivered(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-recipient-500", nil)
	defer func() { scenario.dumpOnFailure(t, scenario.gateway) }()

	marker := "otelbox-ci-recipient-500-" + randomHex(t, 8)
	var answered atomic.Bool
	scenario.backend.setReply(func([]byte) backendReply {
		if answered.Swap(true) {
			return backendReply{status: http.StatusOK}
		}
		return backendReply{status: http.StatusInternalServerError, body: []byte("ci transient failure")}
	})
	scenario.postSelected(t, marker)

	// The first retry waits initial_interval (5s) plus jitter.
	if err := poll(deliveryTimeout, scenario.gateway, "the gateway to send "+marker+" again after HTTP 500", func() bool {
		return scenario.backend.count(marker) >= 2
	}); err != nil {
		t.Fatalf("the recipient answered HTTP 500 once and the gateway never sent the admitted trace again: %v", err)
	}
	if exporterMetricPositive(gatewayMetricsPort, "otelcol_exporter_send_failed_spans", "otlp_http/selected_traces") {
		t.Fatalf("the retried trace was counted as a failed send")
	}
	if line := scenario.gateway.firstLogLine("Dropping data"); line != "" {
		t.Fatalf("the gateway dropped data after a transient HTTP 500: %s", line)
	}
}

// TestRecipientPartialSuccessCountsAsDelivered pins PC-08: a partial-success
// response is logged and counted as sent, and the rejected span is not
// retried.
func TestRecipientPartialSuccessCountsAsDelivered(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-recipient-partial", nil)
	defer func() { scenario.dumpOnFailure(t, scenario.gateway) }()

	// ExportTraceServiceResponse{partial_success: {rejected_spans: 1, error_message}}.
	partial := protoMessage(nil).bytesField(1,
		protoMessage(nil).varintField(1, 1).stringField(2, "ci rejected one span"))
	scenario.backend.setReply(func([]byte) backendReply {
		return backendReply{status: http.StatusOK, body: partial}
	})

	marker := "otelbox-ci-recipient-partial-" + randomHex(t, 8)
	scenario.postSelected(t, marker)
	if err := poll(deliveryTimeout, scenario.gateway, "the recipient to receive "+marker, func() bool {
		return scenario.backend.count(marker) >= 1
	}); err != nil {
		t.Fatalf("the recipient never received the admitted trace: %v", err)
	}
	if err := poll(failureTimeout, scenario.gateway, "the partial-success warning", func() bool {
		return scenario.gateway.firstLogLine("Partial success response") != ""
	}); err != nil {
		t.Fatalf("the gateway did not log the partial success: %v", err)
	}
	if err := staysFalse(recipientRetryWindow, scenario.gateway, "a retry of "+marker, func() bool {
		return scenario.backend.count(marker) >= 2
	}); err != nil {
		t.Fatalf("a partial success is now retried (%v); PC-08 and the failure matrix must be updated", err)
	}
	if exporterMetricPositive(gatewayMetricsPort, "otelcol_exporter_send_failed_spans", "otlp_http/selected_traces") {
		t.Fatalf("a partial success is now counted as a failed send; PC-08 and the failure matrix must be updated")
	}
}

// Longer than any ordinary retry the profiles allow: the first waits the 5s
// initial interval with the default 0.5 jitter, so at most 7.5s. A resend
// sooner than this is the backoff ignoring the recipient's Retry-After.
const recipientRetryAfter = 15 * time.Second

// TestRecipientRetryAfterIsHonoured pins what the pinned otlphttp exporter and
// the outer retry sender already do: HTTP 429 and 503 with Retry-After are a
// throttled retry, resent no sooner than the delay, delivered after it and
// neither counted as failed nor dropped. Every attempt, retries included,
// must carry the configured protocol header.
func TestRecipientRetryAfterIsHonoured(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-recipient-retry-after", nil)
	defer func() { scenario.dumpOnFailure(t, scenario.gateway) }()

	throttles := []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}
	retryAfter := strconv.Itoa(int(recipientRetryAfter / time.Second))
	var attempts atomic.Int32
	scenario.backend.setReply(func([]byte) backendReply {
		attempt := int(attempts.Add(1))
		if attempt > len(throttles) {
			return backendReply{status: http.StatusOK}
		}
		return backendReply{status: throttles[attempt-1], retryAfter: retryAfter}
	})

	marker := "otelbox-ci-recipient-retry-after-" + randomHex(t, 8)
	scenario.postSelected(t, marker)
	if err := poll(deliveryTimeout, scenario.gateway, "the gateway to deliver "+marker+" after two throttled answers", func() bool {
		return scenario.backend.count(marker) > len(throttles)
	}); err != nil {
		t.Fatalf("the recipient throttled the admitted trace twice and the gateway never delivered it: %v", err)
	}

	arrivals := scenario.backend.arrivalsOf(marker)
	for i, status := range throttles {
		if gap := arrivals[i+1].Sub(arrivals[i]); gap < recipientRetryAfter {
			t.Errorf("the gateway resent %s %s after HTTP %d with Retry-After: %s", marker, gap.Round(time.Millisecond), status, retryAfter)
		}
	}
	if exporterMetricPositive(gatewayMetricsPort, "otelcol_exporter_send_failed_spans", "otlp_http/selected_traces") {
		t.Errorf("a throttled and later delivered trace was counted as a failed send")
	}
	if line := scenario.gateway.firstLogLine("Dropping data"); line != "" {
		t.Errorf("the gateway dropped data after a throttled answer: %s", line)
	}
	headers := scenario.backend.protocolHeadersSeen()
	for i, value := range headers {
		if value != scenario.backend.contract.protocolValue {
			t.Errorf("request %d of %d reached the recipient with %s=%q, want %q",
				i+1, len(headers), selectedTraceProtocolHeader, value, scenario.backend.contract.protocolValue)
		}
	}
}

// TestRecipientRetryAfterWaitSurvivesARestart stops the gateway gracefully
// while its exporter waits out a Retry-After far longer than the test. The
// retry sender aborts the wait with a shutdown error, which keeps the item in
// the WAL, and the restarted gateway sends it again at once: the remaining
// delay is not persisted.
func TestRecipientRetryAfterWaitSurvivesARestart(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-before-retry-after-restart", nil)
	collectors := []*collector{scenario.gateway}
	defer func() { scenario.dumpOnFailure(t, collectors...) }()

	scenario.backend.setReply(func([]byte) backendReply {
		return backendReply{status: http.StatusServiceUnavailable, retryAfter: "3600"}
	})
	marker := "otelbox-ci-retry-after-restart-" + randomHex(t, 8)
	scenario.postSelected(t, marker)
	if err := poll(deliveryTimeout, scenario.gateway, "the recipient to receive "+marker, func() bool {
		return scenario.backend.count(marker) >= 1
	}); err != nil {
		t.Fatalf("the recipient never received the admitted trace: %v", err)
	}
	if err := staysFalse(recipientRetryWindow, scenario.gateway, "a resend of "+marker+" inside its Retry-After", func() bool {
		return scenario.backend.count(marker) >= 2
	}); err != nil {
		t.Fatalf("the gateway ignored Retry-After: 3600: %v", err)
	}

	stopping := time.Now()
	scenario.gateway.stop(t)
	if elapsed := time.Since(stopping); elapsed >= stopTimeout {
		t.Fatalf("the gateway needed %s and SIGKILL to stop during a Retry-After wait; a graceful stop must abort the wait", elapsed.Round(time.Second))
	}
	// Upstream logs "Dropping data" for the interrupted wait although the WAL
	// keeps the item, so delivery after the restart is the only evidence.
	scenario.backend.setReply(nil)
	restarted := scenario.start(t, "gateway-after-retry-after-restart")
	collectors = append(collectors, restarted)
	if err := poll(deliveryTimeout, restarted, "the restarted gateway to deliver "+marker, func() bool {
		return scenario.backend.count(marker) >= 2
	}); err != nil {
		t.Fatalf("the trace waiting out Retry-After did not survive a gateway restart: %v", err)
	}
}
