package harness

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestGatewayMixedTraceRequestIsOneAcknowledgementUnit holds PC-03: one OTLP
// trace request carrying a classified and an unclassified resource is one
// acknowledgement unit across the all-signal and selected-traces recipients.
// While the selected-traces queue cannot take the classified resource, the
// request is not acknowledged, even though the all-signal branch may already
// hold it. After recovery the classified span reaches both recipients and the
// unclassified span only the all-signal recipient.
func TestGatewayMixedTraceRequestIsOneAcknowledgementUnit(t *testing.T) {
	// A 2 MiB selected-traces queue fills quickly while the recipient holds
	// every answer.
	scenario := startRecipientScenario(t, "gateway-mixed-fan-out", map[string]string{
		"OTELBOX_RECIPIENT_QUEUE_SIZE_BYTES": "2097152",
	})
	defer func() { scenario.dumpOnFailure(t, scenario.gateway) }()

	release := make(chan struct{})
	scenario.backend.setReply(func([]byte) backendReply {
		return backendReply{status: http.StatusOK, hold: release}
	})

	// Fill the queue in shrinking steps until a span the size of the mixed
	// request no longer fits, so "full" means full for that request and not
	// merely for a large one. A blocked post ends at the client's deadline.
	shortClient := &http.Client{Timeout: 5 * time.Second}
	admitted := 0
	for _, padBytes := range []int{64 * 1024, 8 * 1024, 1024, 0} {
		var extra []attribute
		if padBytes > 0 {
			extra = append(extra, attribute{Key: "otelbox.ci.padding", Value: otlpString{randomHex(t, padBytes)}})
		}
		for range 64 {
			started := time.Now()
			status, _, err := postTraces(shortClient, gatewayHTTPPort, scenario.token,
				tracePayload(t, "otelbox-ci-fill-"+randomHex(t, 4), true, extra...))
			if err != nil || status != http.StatusOK {
				t.Logf("a %d-byte padded span blocked after %s (status %d, error %v)", 2*padBytes, time.Since(started).Round(time.Millisecond), status, err)
				break
			}
			admitted++
		}
	}
	t.Logf("%d padded spans admitted before the selected-traces queue refused an unpadded one", admitted)

	classified := "otelbox-ci-mixed-llm-" + randomHex(t, 8)
	unclassified := "otelbox-ci-mixed-plain-" + randomHex(t, 8)
	payload := mixedTracePayload(t, classified, unclassified)

	status, body, err := postTraces(shortClient, gatewayHTTPPort, scenario.token, payload)
	if err == nil && status == http.StatusOK {
		t.Fatalf("the gateway acknowledged a mixed request while the selected-traces queue was full: %s", body)
	}
	t.Logf("mixed request while the selected-traces queue was full: status %d, error %v", status, err)

	scenario.backend.setReply(nil)
	close(release)

	// The producer retries; once the queue drains the same request is admitted.
	if err := poll(recoveryTimeout, scenario.gateway, "the mixed request to be acknowledged after recovery", func() bool {
		status, _, err := postTraces(sendClient, gatewayHTTPPort, scenario.token, payload)
		return err == nil && status == http.StatusOK
	}); err != nil {
		t.Fatalf("the gateway never admitted the mixed request after the recipient recovered: %v", err)
	}
	if err := poll(deliveryTimeout, scenario.gateway, "the classified span to reach the selected-traces recipient", func() bool {
		return scenario.backend.contains(classified)
	}); err != nil {
		t.Fatalf("the classified span %s never reached the selected-traces recipient: %v", classified, err)
	}
	if scenario.backend.contains(unclassified) {
		t.Fatalf("the unclassified span %s reached the selected-traces recipient", unclassified)
	}
	for _, marker := range []string{classified, unclassified} {
		if err := poll(deliveryTimeout, scenario.gateway, marker+" to reach the all-signal sink", func() bool {
			return sinkContains(t, scenario.sink, marker)
		}); err != nil {
			t.Fatalf("span %s never reached the all-signal recipient: %v", marker, err)
		}
	}
}

// mixedTracePayload is one OTLP/JSON trace request with two resources: one
// classified for the selected-traces recipient and one not.
func mixedTracePayload(t *testing.T, classified, unclassified string) []byte {
	t.Helper()

	var mixed, plain tracesPayload
	if err := json.Unmarshal(tracePayload(t, classified, true), &mixed); err != nil {
		t.Fatalf("could not decode the classified trace payload: %v", err)
	}
	if err := json.Unmarshal(tracePayload(t, unclassified, false), &plain); err != nil {
		t.Fatalf("could not decode the unclassified trace payload: %v", err)
	}
	mixed.ResourceSpans = append(mixed.ResourceSpans, plain.ResourceSpans...)
	payload, err := json.Marshal(mixed)
	if err != nil {
		t.Fatalf("could not encode the mixed trace payload: %v", err)
	}
	return payload
}
