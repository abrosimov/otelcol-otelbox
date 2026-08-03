package harness

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

func TestEdgePersistsAcceptedDataBeforeAcknowledgement(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")
	edgeStorage := filepath.Join(state, "edge-storage")

	token := "otelbox-ci-durable-" + randomHex(t, 16)
	marker := "otelbox-ci-durable-" + randomHex(t, 8)
	gatewayEndpoint := fmt.Sprintf("127.0.0.1:%d", gatewayGRPCPort)

	writeTokenFile(t, tokenFile, token)
	authHeaderFile := writeAuthHeaderFile(t, filepath.Join(state, "edge-auth-header"), token)
	chain := mintGatewayChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)

	edgeSpec := collectorSpec{
		name:     "edge-before-crash",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_STORAGE_DIR":               edgeStorage,
			"OTELBOX_UPSTREAM_ENDPOINT":         gatewayEndpoint,
			"OTELBOX_UPSTREAM_AUTH_HEADER_FILE": authHeaderFile,
			"OTELBOX_CI_EDGE_CA_FILE":           chain.caFile,
		},
		configs: []string{
			configPath("config", "edge.yaml"),
			configPath("test", "config", "edge-ci.yaml"),
		},
	}

	edge := startCollector(t, edgeSpec)
	collectors := []*collector{edge}
	defer func() {
		if !t.Failed() {
			return
		}
		dumpDiagnostics(t,
			[]diagnosticSection{
				{"edge exporter metrics", exporterMetrics(edgeMetricsPort,
					"otelcol_exporter_sent", "otelcol_exporter_send_failed",
					"otelcol_exporter_enqueue_failed", "otelcol_exporter_queue")},
				{fmt.Sprintf("sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 10)},
			}, collectors...)
	}()

	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy: %v", err)
	}
	if !sendOrFail(t, "durability assertion", edgeHTTPPort,
		logPayload(t, marker, "record acknowledged before a forced restart")) {
		return
	}

	// A graceful stop flushes processor memory and cannot distinguish an
	// acknowledgement from a durable enqueue.
	edge.crash(t)

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
		name:     "gateway-after-crash",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})
	collectors = append(collectors, gateway)

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	edgeSpec.name = "edge-after-crash"
	restartedEdge := startCollector(t, edgeSpec)
	collectors = append(collectors, restartedEdge)
	if err := poll(readyTimeout, restartedEdge, "the restarted edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the restarted edge never became healthy: %v", err)
	}
	if err := poll(deliveryTimeout, restartedEdge, "persisted marker "+marker+" to reach the sink", func() bool {
		return sinkContains(t, sink, marker)
	}); err != nil {
		t.Fatalf("marker %s was acknowledged before the crash but did not survive it: %v", marker, err)
	}
}

func TestGatewayPersistsSelectedTraceBeforeAcknowledgement(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "ordinary-sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")
	storage := filepath.Join(state, "gateway-storage")
	token := "otelbox-ci-selected-durable-" + randomHex(t, 16)
	marker := "otelbox-ci-selected-durable-" + randomHex(t, 8)

	writeTokenFile(t, tokenFile, token)
	chain := mintGatewayChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)
	gatewayEnv := map[string]string{
		"OTELBOX_INGEST_TOKEN_FILE":              tokenFile,
		"OTELBOX_STORAGE_DIR":                    storage,
		"OTELBOX_CI_SINK":                        sink,
		"OTELBOX_CI_GATEWAY_CERT_FILE":           chain.certFile,
		"OTELBOX_CI_GATEWAY_KEY_FILE":            chain.keyFile,
		"OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT": "127.0.0.1:34398",
	}
	selectedContract.addEnv(gatewayEnv)
	gatewaySpec := collectorSpec{
		name:     "selected-gateway-before-crash",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	}

	gateway := startCollector(t, gatewaySpec)
	collectors := []*collector{gateway}
	var selectedBackend *selectedTraceBackend
	defer func() {
		if !t.Failed() {
			return
		}
		sections := []diagnosticSection{
			{"gateway exporter metrics", exporterMetrics(gatewayMetricsPort,
				"otelcol_exporter_sent", "otelcol_exporter_send_failed",
				"otelcol_exporter_enqueue_failed", "otelcol_exporter_queue")},
			{fmt.Sprintf("ordinary sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 10)},
		}
		if selectedBackend != nil {
			sections = append(sections, diagnosticSection{"selected-traces backend", selectedBackend.diagnostics()})
		}
		dumpDiagnostics(t, sections, collectors...)
	}()

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	status, body, err := postTraces(sendClient, gatewayHTTPPort, token, tracePayload(t, marker, true))
	if err != nil {
		t.Fatalf("the gateway did not answer the selected trace: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("the gateway refused the selected trace before its recipient returned (HTTP %d): %s", status, body)
	}
	if err := poll(deliveryTimeout, gateway, "selected trace to reach the ordinary recipient", func() bool {
		return sinkContains(t, sink, marker)
	}); err != nil {
		t.Fatalf("the selected trace did not reach the ordinary required recipient before the crash: %v", err)
	}

	gateway.crash(t)
	selectedBackend = startSelectedTraceBackend(t, selectedContract)

	gatewaySpec.name = "selected-gateway-after-crash"
	restarted := startCollector(t, gatewaySpec)
	collectors = append(collectors, restarted)
	if err := poll(readyTimeout, restarted, "the restarted gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the restarted gateway never accepted a connection: %v", err)
	}
	if err := poll(deliveryTimeout, restarted, "persisted selected trace to reach its HTTP recipient", func() bool {
		return selectedBackend.contains(marker)
	}); err != nil {
		t.Fatalf("selected trace %s was acknowledged before the crash but did not survive it: %v; %s",
			marker, err, selectedBackend.diagnostics())
	}
}
