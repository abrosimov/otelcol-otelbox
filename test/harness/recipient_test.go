package harness

import (
	"net/http"
	"path/filepath"
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

	status, body, err := postTraces(sendClient, gatewayHTTPPort, s.token, tracePayload(t, marker, true))
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

// TestRecipientHTTP500DropsAnAdmittedRecord pins the PC-07 counterexample to
// eventual delivery: the pinned otlphttp exporter classifies HTTP 500 as
// permanent, so one transient 500 ends delivery of an admitted record. The
// loss is visible in otelcol_exporter_send_failed and the "Dropping data"
// log line. If the classification changes, this test must change with it.
func TestRecipientHTTP500DropsAnAdmittedRecord(t *testing.T) {
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

	if err := poll(deliveryTimeout, scenario.gateway, "the recipient to receive "+marker, func() bool {
		return scenario.backend.count(marker) >= 1
	}); err != nil {
		t.Fatalf("the recipient never received the admitted trace: %v", err)
	}
	if err := staysFalse(recipientRetryWindow, scenario.gateway, "a retry of "+marker, func() bool {
		return scenario.backend.count(marker) >= 2
	}); err != nil {
		t.Fatalf("HTTP 500 is no longer treated as permanent (%v); PC-07 and the failure matrix must be updated", err)
	}
	if !exporterMetricPositive(gatewayMetricsPort, "otelcol_exporter_send_failed_spans", "otlp_http/selected_traces") {
		t.Fatalf("the dropped trace is not counted in otelcol_exporter_send_failed_spans, so the loss is invisible in metrics")
	}
	if line := scenario.gateway.firstLogLine("Dropping data"); line == "" {
		t.Fatalf("the gateway dropped the trace without logging \"Dropping data\"")
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
