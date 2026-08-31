package harness

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

const (
	// configtls polls rather than watching, and defaults to never reloading: the
	// pair is re-read at the first handshake after this elapses. A second keeps
	// claim 3's replacement inside this scenario's budget.
	certificateReloadInterval = "1s"

	// A rejected handshake is an ordinary connection failure, not an
	// authentication one: recovery waits on retry_on_failure's 5–30s schedule
	// and grpc's own reconnect backoff, not the 200ms retry_on_auth_failure
	// interval test/config/edge-ci.yaml compresses. Its own constant, because
	// raising the shared deliveryTimeout would slacken every other scenario.
	certificateRecoveryTimeout = 150 * time.Second

	// Claim 2 verifies the gateway's rejection with a direct TLS probe (instant
	// and deterministic), then holds this window open to confirm that data never
	// leaks through. The window must cover the sending queue's batch flush
	// (~200 ms) plus the first two retry_on_failure cycles (5 s + 10 s), with
	// margin for a slow CI runner.
	certificateRejectionWindow = 30 * time.Second

	// Claim 3's staysFalse window: the rejection was established by the logged
	// TLS fault, polled for first; this window only has to outlast the retry
	// that follows it, so that "not delivered" is a state rather than a moment.
	certificateSilenceWindow = 5 * time.Second
)

// The edge's client certificate is optional and empty by default, which is how
// every other scenario here runs. This one puts a gateway behind a client CA —
// the front end a deployment terminating mTLS would stand there — and asks
// whether the certificate is load-bearing: delivery with a leaf the gateway
// trusts, silence without one at all, and recovery after the rejected pair is
// replaced under the running process.
func TestUpstreamClientCertificateIsRequiredAndReplaceable(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")

	token := "otelbox-ci-mtls-" + randomHex(t, 16)
	trustedMarker := "otelbox-ci-mtls-trusted-" + randomHex(t, 8)
	uncertifiedMarker := "otelbox-ci-mtls-uncertified-" + randomHex(t, 8)
	retainedMarker := "otelbox-ci-mtls-retained-" + randomHex(t, 8)

	gatewayEndpoint := fmt.Sprintf("127.0.0.1:%d", gatewayGRPCPort)
	writeTokenFile(t, tokenFile, token)

	server := mintGatewayChain(t, state)
	client := mintClientChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)
	gatewayEnv := map[string]string{
		"OTELBOX_INGEST_TOKEN_FILE":              tokenFile,
		"OTELBOX_STORAGE_DIR":                    filepath.Join(state, "gateway-storage"),
		"OTELBOX_CI_SINK":                        sink,
		"OTELBOX_CI_GATEWAY_CERT_FILE":           server.certFile,
		"OTELBOX_CI_GATEWAY_KEY_FILE":            server.keyFile,
		"OTELBOX_CI_GATEWAY_CLIENT_CA_FILE":      client.caFile,
		"OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT": "127.0.0.1:34398",
	}
	selectedContract.addEnv(gatewayEnv)

	gateway := startCollector(t, collectorSpec{
		name:     "mtls-gateway",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
			configPath("test", "config", "gateway-mtls-ci.yaml"),
		},
	})

	var edges []*collector
	startEdge := func(t *testing.T, name string, offersCertificate bool) *collector {
		t.Helper()

		// One edge at a time: they share the CI overlay's listeners, so a
		// predecessor still holding 34318 would turn the next claim's failure
		// into a port collision.
		if len(edges) > 0 {
			edges[len(edges)-1].stop(t)
		}

		env := map[string]string{
			"OTELBOX_STORAGE_DIR":       filepath.Join(state, name+"-storage"),
			"OTELBOX_UPSTREAM_ENDPOINT": gatewayEndpoint,
			"OTELBOX_UPSTREAM_AUTH_HEADER_FILE": writeAuthHeaderFile(t,
				filepath.Join(state, name+"-auth-header"), token),
			"OTELBOX_CI_EDGE_CA_FILE": server.caFile,
		}
		if offersCertificate {
			// The production variables, not a CI alias: an edge-side overlay
			// would prove that overlay rather than the shipped contract.
			env["OTELBOX_UPSTREAM_TLS_CERT_FILE"] = client.certFile
			env["OTELBOX_UPSTREAM_TLS_KEY_FILE"] = client.keyFile
			env["OTELBOX_UPSTREAM_TLS_RELOAD_INTERVAL"] = certificateReloadInterval
		}

		edge := startCollector(t, collectorSpec{
			name:     name,
			stateDir: state,
			env:      env,
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
			},
			append([]*collector{gateway}, edges...)...)
	}()

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	t.Run("claim 1: a leaf signed by the gateway's client CA delivers end to end", func(t *testing.T) {
		client.install(t, client.trusted)

		edge := startEdge(t, "edge-trusted-certificate", true)
		if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
			t.Fatalf("the edge never became healthy: %v", err)
		}
		if !sendOrFail(t, "claim 1", edgeHTTPPort,
			logPayload(t, trustedMarker, "record carried under a client certificate")) {
			return
		}
		if err := poll(deliveryTimeout, edge, "marker "+trustedMarker+" to reach the sink", func() bool {
			return sinkContains(t, sink, trustedMarker)
		}); err != nil {
			t.Fatalf("marker %s never reached %s while the edge held a leaf the gateway's client CA signed: %v",
				trustedMarker, sink, err)
		}
	})

	t.Run("claim 2: an edge offering no client certificate never delivers", func(t *testing.T) {
		// Confirm the gateway rejects a TLS handshake without a client
		// certificate. A direct probe from the test: instant, deterministic,
		// and independent of gRPC's reconnect backoff timing that made the
		// earlier log-based poll flaky on slow CI runners.
		if err := server.verifyRejectsUncertified(gatewayEndpoint); err != nil {
			t.Fatalf("precondition: %v", err)
		}

		edge := startEdge(t, "edge-no-certificate", false)
		if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
			t.Fatalf("the edge never became healthy without a client certificate, which is the profile's default and must still start: %v", err)
		}
		if !sendOrFail(t, "claim 2", edgeHTTPPort,
			logPayload(t, uncertifiedMarker, "record carried without a client certificate")) {
			return
		}

		// The probe above proved the gateway requires a client certificate.
		// Hold the window open long enough for the sending queue to attempt
		// delivery and for a couple of retry cycles to confirm that data
		// never leaks through.
		if err := staysFalse(certificateRejectionWindow, edge, "the uncertified marker reached the sink", func() bool {
			return sinkContains(t, sink, uncertifiedMarker)
		}); err != nil {
			t.Fatalf("marker %s reached %s from an edge that offered no client certificate, which makes the certificate the other two claims supply decorative rather than load-bearing: %v",
				uncertifiedMarker, sink, err)
		}
	})

	t.Run("claim 3: replacing a rejected pair in place drains the retained marker", func(t *testing.T) {
		client.install(t, client.untrusted)

		edge := startEdge(t, "edge-untrusted-certificate", true)
		if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
			t.Fatalf("the edge never became healthy on the untrusted pair: %v", err)
		}
		pid := edge.cmd.Process.Pid

		if !sendOrFail(t, "claim 3", edgeHTTPPort,
			logPayload(t, retainedMarker, "record retained through a rejected client certificate")) {
			return
		}
		if err := poll(failureTimeout, edge, "the exporter to report a rejected handshake", func() bool {
			return edge.tlsFault() != ""
		}); err != nil {
			t.Fatalf("the edge's leaf is signed by an authority the gateway was never given, so its handshake should have been refused; the edge's log reports no TLS fault at all: %v", err)
		}
		if err := staysFalse(certificateSilenceWindow, edge, "the retained marker reached the sink", func() bool {
			return sinkContains(t, sink, retainedMarker)
		}); err != nil {
			t.Fatalf("marker %s reached %s while the edge held a leaf from an untrusted authority: %v",
				retainedMarker, sink, err)
		}
		if line := edge.firstLogLine("dropping data"); line != "" {
			t.Fatalf("the exporter dropped the retained marker while its certificate was being rejected:\n%s", line)
		}

		client.install(t, client.trusted)
		if err := poll(certificateRecoveryTimeout, edge, "the retained marker to drain after the certificate was replaced", func() bool {
			return sinkContains(t, sink, retainedMarker)
		}); err != nil {
			t.Fatalf("marker %s was not delivered after the certificate and key files were replaced in place: %v", retainedMarker, err)
		}
		if !edge.alive() || edge.cmd.Process.Pid != pid {
			t.Fatalf("certificate replacement restarted the edge; expected the original live process %d", pid)
		}
	})
}
