package harness

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

func TestGatewayRedaction(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")
	token := "otelbox-ci-gateway-redaction-" + randomHex(t, 16)
	secret := "otelboxgatewaysecret" + randomHex(t, 16)
	credentialCases, secretValues := credentialCorpus(secret)

	writeTokenFile(t, tokenFile, token)
	chain := mintGatewayChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)
	selectedBackend := startSelectedTraceBackend(t, selectedContract)

	gatewayEnv := map[string]string{
		"OTELBOX_INGEST_TOKEN_FILE":              tokenFile,
		"OTELBOX_STORAGE_DIR":                    filepath.Join(state, "gateway-storage"),
		"OTELBOX_CI_SINK":                        sink,
		"OTELBOX_CI_GATEWAY_CERT_FILE":           chain.certFile,
		"OTELBOX_CI_GATEWAY_KEY_FILE":            chain.keyFile,
		"OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT": "127.0.0.1:34398",
	}
	selectedContract.addEnv(gatewayEnv)

	gateway := startCollector(t, collectorSpec{
		name:     "gateway-redaction",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})

	defer func() {
		if !t.Failed() {
			return
		}
		dumpDiagnostics(t,
			[]diagnosticSection{
				{fmt.Sprintf("sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 10)},
				{"selected-traces backend", selectedBackend.diagnostics()},
			},
			gateway)
	}()

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	send := func(signal, marker string, payload []byte) bool {
		t.Helper()

		status, body, err := postOTLPJSON(sendClient, gatewayHTTPPort, signal, token, payload)
		switch {
		case err != nil:
			t.Errorf("gateway %s redaction case %s did not receive an HTTP response: %v", signal, marker, err)
			return false
		case status != http.StatusOK:
			t.Errorf("gateway %s redaction case %s returned HTTP %d: %s", signal, marker, status, body)
			return false
		}
		return true
	}

	logMarker := "otelbox-ci-gateway-redaction-log-" + randomHex(t, 8)
	traceMarker := "otelbox-ci-gateway-redaction-trace-" + randomHex(t, 8)
	metricMarker := "otelbox-ci-gateway-redaction-metric-" + randomHex(t, 8)
	if !send("logs", logMarker,
		logPayload(t, logMarker, "login failed: password="+secret, credentialCases...)) {
		return
	}
	if !send("traces", traceMarker,
		tracePayload(t, traceMarker, true, credentialCases...)) {
		return
	}
	if !send("metrics", metricMarker,
		metricPayload(t, metricMarker, credentialCases...)) {
		return
	}

	for _, marker := range []string{logMarker, traceMarker, metricMarker} {
		if err := poll(deliveryTimeout, gateway, "marker "+marker+" to reach the ordinary recipient", func() bool {
			return sinkContains(t, sink, marker)
		}); err != nil {
			t.Errorf("marker %s never reached the ordinary recipient: %v", marker, err)
		}
	}
	if err := poll(deliveryTimeout, gateway, "the selected trace to reach its HTTP recipient", func() bool {
		return selectedBackend.contains(traceMarker)
	}); err != nil {
		t.Fatalf("selected trace marker %s never reached its HTTP recipient: %v", traceMarker, err)
	}

	for _, value := range secretValues {
		if sinkContains(t, sink, value) {
			t.Errorf("credential value %q survived gateway redaction to the ordinary recipient", value)
		}
		if selectedBackend.contains(value) {
			t.Errorf("credential value %q survived gateway redaction to the selected-traces recipient", value)
		}
	}
	if !sinkContains(t, sink, "redaction.masked.count") {
		t.Error("ordinary recipient records have no redaction.masked.count evidence")
	}
	if !selectedBackend.contains("redaction.masked.count") {
		t.Error("selected trace has no redaction.masked.count evidence")
	}
}
