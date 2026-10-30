package harness

import (
	"net/http"
	"testing"
	"time"
)

const duplicateSpansMetric = "otelcol_processor_span_dedup_duplicate_spans"

// TestSelectedTraceDuplicateIsDroppedAcrossRestart replays one classified span
// verbatim, same trace and span ID, after the selected-traces recipient
// received it: once to the running gateway and once to a gateway restarted
// after SIGKILL on the same storage. Neither replay may reach the recipient,
// each must be counted, and a new span must still pass.
func TestSelectedTraceDuplicateIsDroppedAcrossRestart(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-dedup-before-restart", nil)
	collectors := []*collector{scenario.gateway}
	defer func() { scenario.dumpOnFailure(t, collectors...) }()

	marker := "otelbox-ci-dedup-" + randomHex(t, 8)
	payload := tracePayload(t, marker, true)
	scenario.postSelectedPayload(t, payload)
	if err := poll(deliveryTimeout, scenario.gateway, "the recipient to receive "+marker, func() bool {
		return scenario.backend.count(marker) >= 1
	}); err != nil {
		t.Fatalf("the recipient never received the first copy: %v", err)
	}

	requireReplayDropped := func(t *testing.T, gateway *collector, when string) {
		t.Helper()

		scenario.postSelectedPayload(t, payload)
		if err := poll(deliveryTimeout, gateway, "the replay to be counted as a duplicate "+when, func() bool {
			return exporterMetricPositive(gatewayMetricsPort, duplicateSpansMetric, "")
		}); err != nil {
			t.Fatalf("the gateway did not count the replayed span as a duplicate %s: %v", when, err)
		}
		if err := staysFalse(2*time.Second, gateway, "the replayed span reached the recipient "+when, func() bool {
			return scenario.backend.count(marker) >= 2
		}); err != nil {
			t.Fatalf("the selected-traces recipient received a second copy %s: %v", when, err)
		}
	}

	requireReplayDropped(t, scenario.gateway, "while the gateway runs")

	// SIGKILL rather than a graceful stop: the span must have been recorded
	// when the exporter accepted it, not flushed at shutdown.
	scenario.gateway.crash(t)
	restarted := scenario.start(t, "gateway-dedup-after-restart")
	collectors = append(collectors, restarted)
	requireReplayDropped(t, restarted, "after a restart")

	fresh := "otelbox-ci-dedup-fresh-" + randomHex(t, 8)
	scenario.postSelected(t, fresh)
	if err := poll(deliveryTimeout, restarted, "a new span to reach the recipient", func() bool {
		return scenario.backend.count(fresh) >= 1
	}); err != nil {
		t.Fatalf("a span the gateway had never seen did not reach the recipient after the restart: %v", err)
	}
}

// TestSelectedTraceRefusedByAFullQueueIsNotRemembered holds the invariant that
// makes deduplication safe: a span is remembered only after the exporter queue
// accepted it. The queue is full, so the first attempt is refused; the
// producer's retry of the identical span must then be admitted and delivered.
// Remembering the refused attempt would drop that retry and lose the span.
func TestSelectedTraceRefusedByAFullQueueIsNotRemembered(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-dedup-refused", map[string]string{
		"OTELBOX_RECIPIENT_QUEUE_SIZE_BYTES": "2097152",
	})
	defer func() { scenario.dumpOnFailure(t, scenario.gateway) }()

	release := make(chan struct{})
	scenario.backend.setReply(func([]byte) backendReply {
		return backendReply{status: http.StatusOK, hold: release}
	})
	shortClient := &http.Client{Timeout: 5 * time.Second}
	fillSelectedQueue(t, scenario, shortClient)

	marker := "otelbox-ci-dedup-refused-" + randomHex(t, 8)
	payload := tracePayload(t, marker, true)
	status, body, err := postTraces(shortClient, gatewayHTTPPort, scenario.token, payload)
	if err == nil && status == http.StatusOK {
		t.Fatalf("the gateway acknowledged a selected span while its queue was full: %s", body)
	}
	t.Logf("selected span while the queue was full: status %d, error %v", status, err)

	scenario.backend.setReply(nil)
	close(release)

	if err := poll(recoveryTimeout, scenario.gateway, "the retried span to be acknowledged", func() bool {
		status, _, err := postTraces(sendClient, gatewayHTTPPort, scenario.token, payload)
		return err == nil && status == http.StatusOK
	}); err != nil {
		t.Fatalf("the gateway never admitted the retried span after the queue drained: %v", err)
	}
	if err := poll(deliveryTimeout, scenario.gateway, "the retried span to reach the recipient", func() bool {
		return scenario.backend.contains(marker)
	}); err != nil {
		t.Fatalf("span %s was refused once and its acknowledged retry never reached the recipient: %v", marker, err)
	}
}
