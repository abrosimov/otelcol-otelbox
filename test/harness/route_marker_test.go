package harness

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The gateway routes on `otelbox.telemetry.class` and no role profile ever
// writes it: the producer does, or nobody does. docs/gateway.md describes the
// third case — a producer that cannot be changed but whose stream the local
// collector can already tell apart — and 2.3.0 links the `resource` processor,
// wired nowhere, so a deployment can render the stamp itself. This scenario is
// that arrangement standing up: without it the processor is proved only by the
// manifest check, which says that it is in the binary and nothing about whether
// stamping there reaches the route it was linked for.
func TestStampedRouteMarkerSelectsAnUnclassifiedTrace(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")

	token := "otelbox-ci-route-marker-" + randomHex(t, 16)
	stampedMarker := "otelbox-ci-stamped-trace-" + randomHex(t, 8)
	unstampedMarker := "otelbox-ci-unstamped-trace-" + randomHex(t, 8)

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

	// The reference gateway, unmodified beyond the usual CI overlay: the filter
	// this scenario is about is the profile's own, so anything that made it
	// easier to satisfy would be assuming the result.
	gateway := startCollector(t, collectorSpec{
		name:     "route-marker-gateway",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})

	edge := startCollector(t, collectorSpec{
		name:     "route-marker-edge",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_STORAGE_DIR":       filepath.Join(state, "edge-storage"),
			"OTELBOX_UPSTREAM_ENDPOINT": fmt.Sprintf("127.0.0.1:%d", gatewayGRPCPort),
			"OTELBOX_UPSTREAM_AUTH_HEADER_FILE": writeAuthHeaderFile(t,
				filepath.Join(state, "edge-auth-header"), token),
			"OTELBOX_CI_EDGE_CA_FILE": chain.caFile,
		},
		configs: []string{
			configPath("config", "edge.yaml"),
			configPath("test", "config", "edge-ci.yaml"),
			configPath("test", "config", "edge-route-marker-ci.yaml"),
		},
	})

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
				{"selected-traces backend", selectedBackend.diagnostics()},
			},
			gateway, edge)
	}()

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}
	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy — the added listener and trace pipeline are the only thing this run has that the delivery scenario does not: %v", err)
	}

	// Both payloads are built unclassified, and that is the point: the marker
	// difference between them is which listener received them, exactly as it is
	// for the producer this arrangement exists for.
	if !sendTraceOrFail(t, "the classified listener", edgeClassifiedHTTPPort,
		tracePayload(t, stampedMarker, false)) {
		return
	}
	if !sendTraceOrFail(t, "the ordinary listener", edgeHTTPPort,
		tracePayload(t, unstampedMarker, false)) {
		return
	}

	// Delivery of both to the ordinary recipient comes first. Without it the
	// negative below would pass just as well on a run that delivered nothing.
	for marker, description := range map[string]string{
		stampedMarker:   "the trace posted to the classified listener",
		unstampedMarker: "the trace posted to the ordinary listener",
	} {
		if err := poll(deliveryTimeout, edge, description+" to reach the ordinary recipient", func() bool {
			return sinkContains(t, sink, marker)
		}); err != nil {
			t.Fatalf("%s (%s) never reached the ordinary recipient, so neither routing claim below can be attributed to the marker: %v",
				description, marker, err)
		}
	}

	if err := poll(deliveryTimeout, gateway, "the stamped trace to reach the selected-traces recipient", func() bool {
		return selectedBackend.contains(stampedMarker)
	}); err != nil {
		t.Fatalf("trace %s arrived carrying no otelbox.telemetry.class and never reached the selected-traces recipient: either resource/llm did not stamp it or the gateway's filter does not read what it stamped, and a deployment following docs/gateway.md would lose the route silently: %v; %s",
			stampedMarker, err, selectedBackend.diagnostics())
	}
	if err := staysFalse(2*time.Second, gateway, "the trace posted to the ordinary listener reached the selected-traces recipient", func() bool {
		return selectedBackend.contains(unstampedMarker)
	}); err != nil {
		t.Fatalf("the stamp reached past the one pipeline that carries it, which is the failure the reference warns about: every ordinary producer on this edge is now coupled to the selected-traces recipient's queue and its outages: %v; %s",
			err, selectedBackend.diagnostics())
	}
}
