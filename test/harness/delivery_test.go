package harness

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEdgeToGatewayDelivery(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")

	validToken := "otelbox-ci-valid-" + randomHex(t, 16)
	wrongToken := "otelbox-ci-wrong-" + randomHex(t, 16)
	secret := "otelboxsecret" + randomHex(t, 16)
	credentialCases, secretValues := credentialCorpus(secret)
	benignValues := []string{
		"task-queue-processor-1",
		"disk_read_bytes_total",
		"risk_engine_v2_scoring",
		"network_interface_eth0",
		"bookmark_service_latency",
		"api_gateway_handler_v3",
	}
	happyMarker := "otelbox-ci-happy-" + randomHex(t, 8)
	redactionMarker := "otelbox-ci-redaction-" + randomHex(t, 8)
	unauthorisedMarker := "otelbox-ci-unauthorised-" + randomHex(t, 8)

	gatewayEndpoint := fmt.Sprintf("127.0.0.1:%d", gatewayGRPCPort)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)
	selectedBackend := startSelectedTraceBackend(t, selectedContract)

	writeTokenFile(t, tokenFile, validToken)

	chain := mintGatewayChain(t, state)
	gatewayEnv := map[string]string{
		"OTELBOX_INGEST_TOKEN_FILE":    tokenFile,
		"OTELBOX_STORAGE_DIR":          filepath.Join(state, "gateway-storage"),
		"OTELBOX_CI_SINK":              sink,
		"OTELBOX_CI_GATEWAY_CERT_FILE": chain.certFile,
		"OTELBOX_CI_GATEWAY_KEY_FILE":  chain.keyFile,
		// The CI overlay removes this exporter from ordinary pipelines, but
		// environment expansion still traverses its configured endpoint.
		"OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT": "127.0.0.1:34398",
	}
	selectedContract.addEnv(gatewayEnv)

	gateway := startCollector(t, collectorSpec{
		name:     "gateway",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
			configPath("test", "config", "gateway-no-redaction-ci.yaml"),
		},
	})

	var edges []*collector
	startEdge := func(t *testing.T, name, token string) *collector {
		t.Helper()

		edge := startCollector(t, collectorSpec{
			name:     name,
			stateDir: state,
			env: map[string]string{
				// A WAL per edge run, never a shared one: a record enqueued by
				// the happy-path edge would otherwise replay under the second
				// edge's credential, and assertion 4 would report on the wrong
				// record.
				"OTELBOX_STORAGE_DIR":       filepath.Join(state, name+"-storage"),
				"OTELBOX_UPSTREAM_ENDPOINT": gatewayEndpoint,
				// A header file per edge, so the credential the second edge
				// presents is the only thing that differs between the two runs.
				"OTELBOX_UPSTREAM_AUTH_HEADER_FILE": writeAuthHeaderFile(t,
					filepath.Join(state, name+"-auth-header"), token),
				// The trust anchor for the leg the role profile keeps at
				// `insecure: false`.
				"OTELBOX_CI_EDGE_CA_FILE": chain.caFile,
			},
			configs: []string{
				configPath("config", "edge.yaml"),
				configPath("test", "config", "edge-ci.yaml"),
			},
		})
		edges = append(edges, edge)
		return edge
	}

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
			append([]*collector{gateway}, edges...)...)
	}()

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		// Nothing below can run, so this ends the test rather than carrying on.
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	// Precondition, not an assertion. Assertion 4 needs a real authentication
	// outage before it can prove recovery from one.
	t.Run("precondition: the gateway rejects unauthenticated ingest", func(t *testing.T) {
		status := gatewayUnauthenticatedStatus(gatewayHTTPPort)
		if status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated ingest returned HTTP %d, expected 401 — the gateway is not enforcing bearertokenauth/ingest, so assertion 4 below cannot prove anything", status)
		}
	})

	edge := startEdge(t, "edge-valid-token", validToken)
	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy: %v", err)
	}

	t.Run("assertion 1: a log sent to the edge reaches the gateway's sink", func(t *testing.T) {
		if !sendOrFail(t, "assertion 1", edgeHTTPPort,
			logPayload(t, happyMarker, "otelbox integration test record")) {
			return
		}
		if err := poll(deliveryTimeout, edge, "marker "+happyMarker+" to reach the sink", func() bool {
			return sinkContains(t, sink, happyMarker)
		}); err != nil {
			t.Fatalf("marker %s never reached %s — the edge accepted the record but the two-hop pipeline did not deliver it: %v",
				happyMarker, sink, err)
		}
	})

	t.Run("assertion 2: credentials are stripped without changing ordinary telemetry", func(t *testing.T) {
		attributes := append([]attribute{}, credentialCases...)
		for i, value := range benignValues {
			attributes = append(attributes, attribute{
				Key:   fmt.Sprintf("otelbox.test.benign.%d", i),
				Value: otlpString{value},
			})
		}
		payload := logPayload(t, redactionMarker, "login failed: password="+secret, attributes...)
		if !sendOrFail(t, "assertion 2", edgeHTTPPort, payload) {
			return
		}
		if err := poll(deliveryTimeout, edge, "marker "+redactionMarker+" to reach the sink", func() bool {
			return sinkContains(t, sink, redactionMarker)
		}); err != nil {
			t.Fatalf("marker %s never reached %s, so there is no delivered record to judge redaction on: %v",
				redactionMarker, sink, err)
		}
		// Deliberately the whole file, not the matching record. Redaction that
		// leaked the value into a summary attribute, a resource attribute or a
		// neighbouring batch would still be a leak.
		for _, value := range secretValues {
			if sinkContains(t, sink, value) {
				t.Errorf("credential value %q reached %s even though marker %s arrived", value, sink, redactionMarker)
			}
		}
		for _, value := range benignValues {
			if !sinkContains(t, sink, value) {
				t.Errorf("ordinary telemetry value %q was changed before reaching %s", value, sink)
			}
		}
		if !sinkContains(t, sink, "redaction.masked.count") {
			t.Error("the delivered record has no redaction.masked.count evidence")
		}

		metricMarker := "otelbox-ci-redaction-metric-" + randomHex(t, 8)
		if !sendMetricOrFail(t, "metric redaction", edgeHTTPPort,
			metricPayload(t, metricMarker, credentialCases...)) {
			return
		}
		if err := poll(deliveryTimeout, edge, "metric marker "+metricMarker+" to reach the sink", func() bool {
			return sinkContains(t, sink, metricMarker)
		}); err != nil {
			t.Fatalf("metric marker %s never reached the sink: %v", metricMarker, err)
		}
		for _, value := range secretValues {
			if sinkContains(t, sink, value) {
				t.Errorf("credential value %q survived the edge metrics pipeline", value)
			}
		}
	})

	t.Run("assertion 3: only classified traces reach the required HTTP recipient with its protocol headers", func(t *testing.T) {
		ordinaryMarker := "otelbox-ci-ordinary-trace-" + randomHex(t, 8)
		selectedMarker := "otelbox-ci-selected-trace-" + randomHex(t, 8)

		if !sendTraceOrFail(t, "ordinary trace routing", edgeHTTPPort,
			tracePayload(t, ordinaryMarker, false)) {
			return
		}
		if !sendTraceOrFail(t, "selected trace routing", edgeHTTPPort,
			tracePayload(t, selectedMarker, true, credentialCases...)) {
			return
		}

		for marker, description := range map[string]string{
			ordinaryMarker: "ordinary trace",
			selectedMarker: "selected trace",
		} {
			if err := poll(deliveryTimeout, edge, description+" to reach the ordinary recipient", func() bool {
				return sinkContains(t, sink, marker)
			}); err != nil {
				t.Fatalf("%s marker %s did not reach the ordinary recipient: %v", description, marker, err)
			}
		}
		if err := poll(deliveryTimeout, gateway, "selected trace to reach the HTTP recipient", func() bool {
			return selectedBackend.contains(selectedMarker)
		}); err != nil {
			t.Fatalf("selected trace marker %s did not reach the HTTP recipient: %v; %s",
				selectedMarker, err, selectedBackend.diagnostics())
		}
		if err := staysFalse(2*time.Second, gateway, "ordinary trace reached the selected-traces recipient", func() bool {
			return selectedBackend.contains(ordinaryMarker)
		}); err != nil {
			t.Fatalf("the traces-only route leaked an unclassified trace: %v; %s", err, selectedBackend.diagnostics())
		}
		for _, value := range secretValues {
			if sinkContains(t, sink, value) {
				t.Errorf("credential value %q survived the edge trace pipeline to the ordinary recipient", value)
			}
			if selectedBackend.contains(value) {
				t.Errorf("credential value %q survived the edge selected-trace pipeline", value)
			}
		}
		if !selectedBackend.contains("redaction.masked.count") {
			t.Error("the selected trace has no redaction.masked.count evidence")
		}
	})

	t.Run("assertion 4: an authentication outage retains data until live credential rotation", func(t *testing.T) {
		edge.stop(t)

		const edgeName = "edge-wrong-token"
		authHeaderFile := filepath.Join(state, edgeName+"-auth-header")
		wrongTokenEdge := startEdge(t, edgeName, wrongToken)
		if err := poll(readyTimeout, wrongTokenEdge, "the edge's health endpoint", edgeHealthy); err != nil {
			t.Fatalf("the edge never became healthy on the second run: %v", err)
		}
		pid := wrongTokenEdge.cmd.Process.Pid

		if err := chain.verifyServed(gatewayEndpoint); err != nil {
			t.Fatalf("the gateway's certificate does not verify against the CA the edge was handed, so the outage cannot be attributed to authentication: %v", err)
		}

		if !sendOrFail(t, "assertion 4", edgeHTTPPort,
			logPayload(t, unauthorisedMarker, "otelbox integration test record")) {
			return
		}

		if err := poll(failureTimeout, wrongTokenEdge, "the exporter to enter authentication backoff", func() bool {
			return wrongTokenEdge.firstLogLine("will retry the request after interval") != ""
		}); err != nil {
			t.Fatalf("the exporter did not enter retry backoff after the gateway rejected its credential: %v", err)
		}

		if err := staysFalse(time.Second, wrongTokenEdge, "the gateway accepted the rejected credential", func() bool {
			return sinkContains(t, sink, unauthorisedMarker)
		}); err != nil {
			t.Fatalf("marker %s reached the sink before credential rotation: %v", unauthorisedMarker, err)
		}
		if line := wrongTokenEdge.firstLogLine("dropping data"); line != "" {
			t.Fatalf("the exporter dropped the retained marker during authentication backoff:\n%s", line)
		}

		writeAuthHeaderFile(t, authHeaderFile, validToken)
		if err := poll(deliveryTimeout, wrongTokenEdge, "the retained marker to drain after credential rotation", func() bool {
			return sinkContains(t, sink, unauthorisedMarker)
		}); err != nil {
			t.Fatalf("marker %s was not delivered after live credential rotation: %v", unauthorisedMarker, err)
		}
		if !wrongTokenEdge.alive() || wrongTokenEdge.cmd.Process.Pid != pid {
			t.Fatalf("credential recovery restarted the edge; expected the original live process %d", pid)
		}
	})

	t.Run("assertion 5: replacing the last allowlisted token revokes the previous one", func(t *testing.T) {
		replacementToken := "otelbox-ci-rotated-" + randomHex(t, 16)
		writeTokenFile(t, tokenFile, replacementToken)

		if err := poll(readyTimeout, gateway, "the rotated allowlist to take effect", func() bool {
			oldStatus, _, _ := postLogs(probeClient, gatewayHTTPPort, validToken, emptyLogsPayload)
			newStatus, _, _ := postLogs(probeClient, gatewayHTTPPort, replacementToken, emptyLogsPayload)
			return oldStatus == http.StatusUnauthorized && newStatus == http.StatusOK
		}); err != nil {
			t.Fatalf("the old token stayed active or its replacement never became active: %v", err)
		}

		// Pinned v0.158 keeps the previous value on an empty-file reload. A random
		// non-client token is the explicit last-token revocation operation.
		revocationToken := "otelbox-ci-revoked-" + randomHex(t, 16)
		writeTokenFile(t, tokenFile, revocationToken)
		if err := poll(readyTimeout, gateway, "last-token revocation to take effect", func() bool {
			oldStatus, _, _ := postLogs(probeClient, gatewayHTTPPort, replacementToken, emptyLogsPayload)
			guardStatus, _, _ := postLogs(probeClient, gatewayHTTPPort, revocationToken, emptyLogsPayload)
			return oldStatus == http.StatusUnauthorized && guardStatus == http.StatusOK
		}); err != nil {
			t.Fatalf("replacing the last client token did not revoke it: %v", err)
		}
	})
}

// The profile contract is stricter than the pinned parser: one URL-safe token
// per line and no ignored comment suffix that an audit tool could interpret as
// part of the credential.
func writeTokenFile(t *testing.T, path, token string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("could not write the gateway's token allowlist to %s: %v", path, err)
	}
}

// The client credential, in the format headerssetterextension parses — and it is
// not the allowlist format above. The whole file is TrimSpace'd and nothing
// prepends a scheme, so the scheme is written out and a trailing comment would
// travel to the gateway as part of the header value.
func writeAuthHeaderFile(t *testing.T, path, token string) string {
	t.Helper()

	if err := os.WriteFile(path, []byte("Bearer "+token+"\n"), 0o600); err != nil {
		t.Fatalf("could not write the edge's authorization header file to %s: %v", path, err)
	}
	return path
}
