package harness

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProducerIdentitySurvivesTheCollector holds PC-10: the identifiers and
// correlation a producer sets travel edge -> gateway -> both recipients
// unchanged. Trace, span and parent span IDs, a span link, and producer
// attributes outside the redaction key set arrive as sent. The test also pins
// the exception a producer must design around: a blocked key masks its value
// whatever its type, so an integer under a key with a `token` segment, such as
// OpenInference's llm.token_count.prompt, arrives as the string "****", while
// the GenAI convention's gen_ai.usage.input_tokens, whose segment is `tokens`,
// arrives intact.
func TestProducerIdentitySurvivesTheCollector(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")
	token := "otelbox-ci-identity-" + randomHex(t, 16)

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
		name:     "identity-gateway",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})
	edge := startCollector(t, collectorSpec{
		name:     "identity-edge",
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
	collectors := []*collector{gateway, edge}
	defer func() {
		if !t.Failed() {
			return
		}
		dumpDiagnostics(t, []diagnosticSection{
			{fmt.Sprintf("sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 5)},
			{"selected-traces backend", selectedBackend.diagnostics()},
		}, collectors...)
	}()

	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}
	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy: %v", err)
	}

	sent := newProducerSpan(t)
	status, body, err := postTraces(sendClient, edgeHTTPPort, "", sent.payload(t))
	if err != nil || status != http.StatusOK {
		t.Fatalf("the edge did not admit the producer span (status %d, error %v): %s", status, err, body)
	}

	if err := poll(deliveryTimeout, edge, "the producer span to reach the all-signal sink", func() bool {
		return sinkContains(t, sink, sent.marker)
	}); err != nil {
		t.Fatalf("the producer span never reached the all-signal recipient: %v", err)
	}
	raw := sinkTail(sink, 1_000_000)
	for _, want := range []struct{ what, text string }{
		{"trace ID", `"traceId":"` + sent.traceID + `"`},
		{"span ID", `"spanId":"` + sent.spanID + `"`},
		{"parent span ID", `"parentSpanId":"` + sent.parentSpanID + `"`},
		{"link trace ID", `"traceId":"` + sent.linkTraceID + `"`},
		{"link span ID", `"spanId":"` + sent.linkSpanID + `"`},
		{"producer event ID", `"stringValue":"` + sent.eventID + `"`},
		{"producer session ID", `"stringValue":"` + sent.sessionID + `"`},
		{"GenAI semantic-convention token count", `{"key":"gen_ai.usage.input_tokens","value":{"intValue":"42"}}`},
		{"masked integer under a token key", `{"key":"llm.token_count.prompt","value":{"stringValue":"****"}}`},
		{"masked string under a token key", `{"key":"llm.token_count.completion","value":{"stringValue":"****"}}`},
	} {
		if !strings.Contains(raw, want.text) {
			t.Errorf("the all-signal recipient did not receive the %s as %s", want.what, want.text)
		}
	}

	if err := poll(deliveryTimeout, edge, "the producer span to reach the selected-traces recipient", func() bool {
		return selectedBackend.contains(sent.marker)
	}); err != nil {
		t.Fatalf("the classified producer span never reached the selected-traces recipient: %v", err)
	}
	for _, want := range []struct{ what, hexValue string }{
		{"trace ID", sent.traceID},
		{"span ID", sent.spanID},
		{"parent span ID", sent.parentSpanID},
		{"link trace ID", sent.linkTraceID},
		{"link span ID", sent.linkSpanID},
	} {
		value, _ := hex.DecodeString(want.hexValue)
		if !selectedBackend.contains(string(value)) {
			t.Errorf("the selected-traces recipient did not receive the %s %s", want.what, want.hexValue)
		}
	}
}

// producerSpan is one classified span with the identity fields a producer
// relies on.
type producerSpan struct {
	marker       string
	traceID      string
	spanID       string
	parentSpanID string
	linkTraceID  string
	linkSpanID   string
	eventID      string
	sessionID    string
}

func newProducerSpan(t *testing.T) producerSpan {
	return producerSpan{
		marker:       "otelbox-ci-identity-" + randomHex(t, 8),
		traceID:      randomHex(t, 16),
		spanID:       randomHex(t, 8),
		parentSpanID: randomHex(t, 8),
		linkTraceID:  randomHex(t, 16),
		linkSpanID:   randomHex(t, 8),
		eventID:      "evt-" + randomHex(t, 12),
		sessionID:    "session-" + randomHex(t, 12),
	}
}

func (s producerSpan) payload(t *testing.T) []byte {
	t.Helper()

	str := func(key, value string) map[string]any {
		return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
	}
	started := time.Now()
	payload, err := json.Marshal(map[string]any{
		"resourceSpans": []any{map[string]any{
			"resource": map[string]any{"attributes": []any{
				str("service.name", "otelbox-integration-test"),
				str("otelbox.telemetry.class", "llm"),
				str("producer.session.id", s.sessionID),
			}},
			"scopeSpans": []any{map[string]any{
				"scope": map[string]any{"name": "otelbox.integration"},
				"spans": []any{map[string]any{
					"traceId":           s.traceID,
					"spanId":            s.spanID,
					"parentSpanId":      s.parentSpanID,
					"name":              "producer span",
					"kind":              2,
					"startTimeUnixNano": strconv.FormatInt(started.UnixNano(), 10),
					"endTimeUnixNano":   strconv.FormatInt(started.Add(time.Millisecond).UnixNano(), 10),
					"attributes": []any{
						str(markerKey, s.marker),
						str("producer.event.id", s.eventID),
						map[string]any{"key": "gen_ai.usage.input_tokens", "value": map[string]any{"intValue": "42"}},
						map[string]any{"key": "llm.token_count.prompt", "value": map[string]any{"intValue": "42"}},
						str("llm.token_count.completion", "17"),
					},
					"links": []any{map[string]any{"traceId": s.linkTraceID, "spanId": s.linkSpanID}},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("could not marshal the producer span: %v", err)
	}
	return payload
}
