package harness

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProducerIdentitySurvivesTheCollector holds PC-10: the identifiers and
// correlation a producer sets travel edge -> gateway -> both recipients
// unchanged. Trace, span and parent span IDs, a span link, and producer
// attributes outside the redaction key set arrive as sent. Token usage keys
// of the OpenInference and GenAI conventions arrive intact, including those
// with a `token` segment that redaction exempts through `ignored_keys`. A
// blocked key that no convention exempts still masks its value whatever its
// type: an integer under producer.auth.token arrives as the string "****".
func TestProducerIdentitySurvivesTheCollector(t *testing.T) {
	pair := startEdgeAndGateway(t, "identity")
	edge, sink, selectedBackend := pair.edge, pair.sink, pair.selectedBackend

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
		{"OpenInference integer token count", `{"key":"llm.token_count.prompt","value":{"intValue":"42"}}`},
		{"OpenInference token count sent as a string", `{"key":"llm.token_count.completion","value":{"stringValue":"17"}}`},
		{"deprecated GenAI token type", `{"key":"gen_ai.token.type","value":{"stringValue":"input"}}`},
		{"masked integer under a blocked key", `{"key":"producer.auth.token","value":{"stringValue":"****"}}`},
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
						str("gen_ai.token.type", "input"),
						map[string]any{"key": "producer.auth.token", "value": map[string]any{"intValue": "7"}},
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
