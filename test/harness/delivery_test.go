package harness

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestEdgeToGatewayDelivery(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")

	validToken := "otelbox-ci-valid-" + randomHex(t, 16)
	wrongToken := "otelbox-ci-wrong-" + randomHex(t, 16)
	secret := "otelboxsecret" + randomHex(t, 16)
	happyMarker := "otelbox-ci-happy-" + randomHex(t, 8)
	redactionMarker := "otelbox-ci-redaction-" + randomHex(t, 8)
	unauthorisedMarker := "otelbox-ci-unauthorised-" + randomHex(t, 8)

	gatewayEndpoint := fmt.Sprintf("127.0.0.1:%d", gatewayGRPCPort)

	writeTokenFile(t, tokenFile, validToken)

	chain := mintGatewayChain(t, state)

	gateway := startCollector(t, collectorSpec{
		name:     "gateway",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_GATEWAY_TOKEN_FILE":   tokenFile,
			"OTELBOX_GATEWAY_STORAGE":      filepath.Join(state, "gateway-storage"),
			"OTELBOX_CI_SINK":              sink,
			"OTELBOX_CI_GATEWAY_CERT_FILE": chain.certFile,
			"OTELBOX_CI_GATEWAY_KEY_FILE":  chain.keyFile,
			// The CI overlay drops all four from every pipeline, so none is
			// reached — but expansion runs over the merged map, and an unset
			// variable fails the load as a validation error naming the
			// component rather than the variable.
			"OTELBOX_GATEWAY_DOCKER_ENDPOINT":    "unix:///var/run/docker.sock",
			"OTELBOX_GATEWAY_BACKEND_1_ENDPOINT": "127.0.0.1:34398",
			"OTELBOX_GATEWAY_BACKEND_2_ENDPOINT": "127.0.0.1:34399",
			"OTELBOX_GATEWAY_BACKEND_2_TOKEN":    "unused-in-ci",
		},
		configs: []string{
			configPath("config", "base.yaml"),
			configPath("config", "examples", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})

	var edges []*collector
	startEdge := func(name, token string) *collector {
		edge := startCollector(t, collectorSpec{
			name:     name,
			stateDir: state,
			env: map[string]string{
				// A WAL per edge run, never a shared one: a record enqueued by
				// the happy-path edge would otherwise replay under the second
				// edge's credential, and assertion 3 would report on the wrong
				// record.
				"OTELBOX_EDGE_STORAGE":  filepath.Join(state, name+"-storage"),
				"OTELBOX_EDGE_ENDPOINT": gatewayEndpoint,
				"OTELBOX_EDGE_TOKEN":    token,
				// The trust anchor for the leg the role layer keeps at
				// `insecure: false`.
				"OTELBOX_CI_EDGE_CA_FILE": chain.caFile,
				// The ntp receiver survives the CI overlay in the config but
				// not in any pipeline, so this is expanded and never dialled.
				"OTELBOX_EDGE_NTP_ENDPOINT": "127.0.0.1:34123",
			},
			configs: []string{
				configPath("config", "base.yaml"),
				configPath("config", "examples", "edge.yaml"),
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
		// Nothing below can run, so this ends the test rather than carrying on.
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	// Precondition, not an assertion. Assertion 3 claims that a wrong token
	// stops delivery; that claim is worthless unless the gateway is checking
	// tokens at all. Remove `auth:` from the receiver, or the extension from the
	// config, and this returns 200 and the run fails here — which is the point.
	t.Run("precondition: the gateway rejects unauthenticated ingest", func(t *testing.T) {
		status := gatewayUnauthenticatedStatus(gatewayHTTPPort)
		if status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated ingest returned HTTP %d, expected 401 — the gateway is not enforcing bearertokenauth/ingest, so assertion 3 below cannot prove anything", status)
		}
	})

	edge := startEdge("edge-valid-token", validToken)
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

	t.Run("assertion 2: credential-shaped attribute values are stripped before the sink", func(t *testing.T) {
		payload := logPayload(t, redactionMarker, "otelbox integration test record",
			credentialAttributes(secret)...)
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
		if sinkContains(t, sink, secret) {
			t.Fatalf("the secret value reached %s — redaction/secrets did not strip it. The record arrived (%s), so the pipeline ran; the pattern list in config/base.yaml is what failed",
				sink, redactionMarker)
		}
	})

	t.Run("assertion 3: an edge with a token outside the gateway's allowlist drops data, visibly", func(t *testing.T) {
		edge.stop(t)

		wrongTokenEdge := startEdge("edge-wrong-token", wrongToken)
		if err := poll(readyTimeout, wrongTokenEdge, "the edge's health endpoint", edgeHealthy); err != nil {
			t.Fatalf("the edge never became healthy on the second run: %v", err)
		}
		// Health is green here and stays green for the rest of the run. That is
		// the incident in one line: the endpoint launchd and every dashboard
		// watched answered 200 the entire time telemetry was being dropped.

		// This assertion's own precondition, inside rather than beside it: a
		// sibling subtest could only make the run red, not stop this one
		// reporting a certificate fault as a rejected token. A bad SAN, an
		// expired leaf or a CA mismatch satisfies both halves below exactly as
		// an unlisted token does, so the transport has to be known good — and
		// known good here, at the moment the export is attempted.
		if err := chain.verifyServed(gatewayEndpoint); err != nil {
			t.Fatalf("the gateway's certificate does not verify against the CA the edge was handed, so a dropped record below would be a TLS fault and this assertion would say nothing about the allowlist: %v", err)
		}

		if !sendOrFail(t, "assertion 3", edgeHTTPPort,
			logPayload(t, unauthorisedMarker, "otelbox integration test record")) {
			return
		}

		if err := poll(failureTimeout, wrongTokenEdge, "otelcol_exporter_send_failed_* to go non-zero on the edge", func() bool {
			return exporterReportsFailure(edgeMetricsPort)
		}); err != nil {
			// The failure mode worth naming: send_failed stuck at zero means
			// either the export succeeded (the token check is gone) or nothing
			// was ever attempted. Both make the negative case vacuous, which is
			// worse than a red build.
			t.Errorf("otelcol_exporter_send_failed_* stayed at zero on port %d. Either the gateway accepted a token that is not in its allowlist, or the edge never attempted the export — in both cases the drop this test exists to catch would be invisible again: %v",
				edgeMetricsPort, err)
			if sinkContains(t, sink, unauthorisedMarker) {
				t.Errorf("and marker %s did reach the sink — the gateway accepted an unknown token", unauthorisedMarker)
			}
			return
		}

		// Ordered deliberately: only once the exporter has recorded a permanent
		// failure is "absent from the sink" a decided fact rather than a record
		// still in flight.
		if sinkContains(t, sink, unauthorisedMarker) {
			t.Fatalf("marker %s reached %s despite the edge holding a token outside the gateway's allowlist — bearertokenauth/ingest is not rejecting it",
				unauthorisedMarker, sink)
		}

		t.Logf("the drop is visible in the edge's own metrics:\n%s",
			exporterMetrics(edgeMetricsPort, "otelcol_exporter_send_failed"))
		// Supporting evidence only. The wording of the upstream error is not a
		// contract, so a change to it must not fail the build — but when it is
		// there it is the exact string from the incident, and logging it makes
		// the CI output self-explanatory.
		if line := wrongTokenEdge.firstLogLine("unauthenticated", "does not match expected scheme or token"); line != "" {
			t.Logf("the edge log carries the incident's error, as expected:\n%s", line)
		}
	})
}

// The allowlist, in the format bearertokenauthextension parses: one token per
// line, first whitespace-delimited field is the token, the rest is a comment.
// The trailing comment is not decoration — it is the format assertion. A change
// upstream that stopped ignoring it would break this test rather than a server.
func writeTokenFile(t *testing.T, path, token string) {
	t.Helper()

	line := token + "  otelbox integration test — the only credential this gateway accepts\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("could not write the gateway's token allowlist to %s: %v", path, err)
	}
}
