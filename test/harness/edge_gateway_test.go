package harness

import (
	"fmt"
	"path/filepath"
	"testing"
)

// edgeAndGateway is an edge delivering to a gateway whose all-signal pipelines
// write to a file sink and whose selected traces go to the in-process HTTP
// recipient double.
type edgeAndGateway struct {
	edge            *collector
	gateway         *collector
	sink            string
	selectedBackend *selectedTraceBackend
}

// startEdgeAndGateway starts both collectors and returns once each answers.
// On failure it dumps both logs, the sink tail and the recipient's state.
func startEdgeAndGateway(t *testing.T, name string) edgeAndGateway {
	t.Helper()

	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")
	token := "otelbox-ci-" + name + "-" + randomHex(t, 16)

	writeTokenFile(t, tokenFile, token)
	authHeaderFile := writeAuthHeaderFile(t, filepath.Join(state, "edge-auth-header"), token)
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
		name:     name + "-gateway",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})
	edge := startCollector(t, collectorSpec{
		name:     name + "-edge",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_STORAGE_DIR":               filepath.Join(state, "edge-storage"),
			"OTELBOX_UPSTREAM_ENDPOINT":         fmt.Sprintf("127.0.0.1:%d", gatewayGRPCPort),
			"OTELBOX_UPSTREAM_AUTH_HEADER_FILE": authHeaderFile,
			"OTELBOX_CI_EDGE_CA_FILE":           chain.caFile,
		},
		configs: []string{
			configPath("config", "edge.yaml"),
			configPath("test", "config", "edge-ci.yaml"),
		},
	})
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		dumpDiagnostics(t, []diagnosticSection{
			{fmt.Sprintf("sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 5)},
			{"selected-traces backend", selectedBackend.diagnostics()},
		}, gateway, edge)
	})

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}
	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy: %v", err)
	}
	return edgeAndGateway{edge: edge, gateway: gateway, sink: sink, selectedBackend: selectedBackend}
}
