package harness

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A quarter of the 2 MiB queue test/config/gateway-coupling-ci.yaml gives the
// stalled exporter, so four records fill it — and inert, because a body
// redaction rewrote would change what is enqueued and the sizing would stop
// meaning anything.
const couplingPadding = 512 * 1024

const (
	// Twelve records of 512 KiB against a 2 MiB queue: the queue is full long
	// before the loop runs out, and the bound only stops a queue that never
	// fills from polling for ever. The per-record patience is deliberately far
	// above the ~1s this path actually takes, because the loop reads a slow
	// delivery as a stall and a loaded runner must not be able to fake one.
	fillMaxRecords   = 12
	fillProbeTimeout = 15 * time.Second

	// The stalled exporter has been failing long enough to be at
	// retry_on_failure.max_interval (30s), so recovery is one backoff plus the
	// drain, and the whole point is that it does eventually clear.
	recoveryTimeout = 120 * time.Second
)

// A gateway fans out to required recipients with `block_on_overflow: true`, and
// fan-out in the collector is synchronous on the caller's goroutine
// (`internal/fanoutconsumer`). Queue headroom isolates an outage; a full queue
// sends backpressure through ingest even when another recipient is healthy. A
// runbook in a consuming repository claims the recipients are always independent;
// this test fixes the boundary in both directions.
func TestRequiredRecipientCouplingUnderQueuePressure(t *testing.T) {
	state := t.TempDir()
	healthySink := filepath.Join(state, "healthy-sink.json")
	stalledSink := filepath.Join(state, "stalled-sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")
	ingestToken := "otelbox-ci-ingest-" + randomHex(t, 16)

	// One allowlist for every client, so the token posted below is the one the
	// gateway loads — and a wrong one surfaces as the receiver's own 401 on the
	// first subtest's post rather than as a healthy recipient receiving nothing.
	writeTokenFile(t, tokenFile, ingestToken)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)

	healthyBackend := startBackend(t, state, "backend-healthy",
		healthyBackendEndpoint, healthySink, healthyBackendMetricsPort)
	if err := poll(readyTimeout, healthyBackend, "the healthy backend's OTLP listener", func() bool {
		return endpointAccepts(healthyBackendEndpoint)
	}); err != nil {
		t.Fatalf("the backend that is supposed to stay up never came up: %v", err)
	}

	// The other recipient is deliberately not started. Its exporter therefore
	// fails every export, retries for ever (max_elapsed_time: 0s) and holds
	// everything it was handed in its queue.
	gatewayEnv := map[string]string{
		"OTELBOX_INGEST_TOKEN_FILE":   tokenFile,
		"OTELBOX_STORAGE_DIR":         filepath.Join(state, "gateway-storage"),
		"OTELBOX_CI_HEALTHY_ENDPOINT": healthyBackendEndpoint,
		"OTELBOX_CI_STALLED_ENDPOINT": stalledBackendEndpoint,
		// Expansion traverses the reference profile before the overlay replaces
		// this endpoint with the healthy test double.
		"OTELBOX_ALL_SIGNALS_RECIPIENT_ENDPOINT": "127.0.0.1:34398",
	}
	selectedContract.addEnv(gatewayEnv)

	gateway := startCollector(t, collectorSpec{
		name:     "coupling-gateway",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-coupling-ci.yaml"),
		},
	})

	var stalledBackend *collector
	defer func() {
		if !t.Failed() {
			return
		}
		collectors := []*collector{gateway, healthyBackend}
		if stalledBackend != nil {
			collectors = append(collectors, stalledBackend)
		}
		dumpDiagnostics(t,
			[]diagnosticSection{
				{"gateway queue metrics", exporterMetrics(couplingGatewayMetricsPort,
					"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity",
					"otelcol_exporter_send_failed", "otelcol_exporter_enqueue_failed")},
				{fmt.Sprintf("healthy recipient sink (%d bytes)", sinkSize(healthySink)), sinkTail(healthySink, 5)},
				{fmt.Sprintf("stalled recipient sink (%d bytes)", sinkSize(stalledSink)), sinkTail(stalledSink, 5)},
			},
			collectors...)
	}()

	// An unauthenticated probe answering 401 is still an answer, and that is all
	// readiness needs: the listener is up.
	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(couplingGatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	send := func(t *testing.T, marker string) error {
		t.Helper()

		payload := logPayload(t, marker, strings.Repeat("x", couplingPadding))
		status, body, err := postLogs(sendClient, couplingGatewayHTTPPort, ingestToken, payload)
		switch {
		case err != nil:
			return fmt.Errorf("the receiver did not answer: %w", err)
		case status != http.StatusOK:
			return fmt.Errorf("the receiver refused the record (HTTP %d): %s", status, body)
		}
		return nil
	}

	headroomMarker := "otelbox-ci-headroom-" + randomHex(t, 8)
	pressureMarker := ""

	t.Run("a stopped recipient with queue headroom does not affect the healthy recipient", func(t *testing.T) {
		if err := send(t, headroomMarker); err != nil {
			t.Fatalf("could not post the first record to the gateway: %v", err)
		}
		if err := poll(deliveryTimeout, gateway, "marker "+headroomMarker+" to reach the healthy recipient", func() bool {
			return sinkContains(t, healthySink, headroomMarker)
		}); err != nil {
			t.Fatalf("marker %s never reached the healthy recipient while the other recipient was down but its queue still had headroom. Required recipients are isolated until a queue fills, so either the gateway is not fanning out at all or the healthy leg is broken: %v",
				headroomMarker, err)
		}
	})

	t.Run("a stopped recipient whose queue has filled backpressures ingest", func(t *testing.T) {
		for record := 1; record <= fillMaxRecords; record++ {
			marker := fmt.Sprintf("otelbox-ci-fill-%02d-%s", record, randomHex(t, 4))

			// A bounded post timeout is the externally visible effect of the full
			// persistent queue blocking synchronous fan-out at the front door.
			if err := send(t, marker); err != nil {
				if !gateway.alive() {
					t.Fatalf("record %d failed because the gateway exited, not because a queue applied backpressure: %v", record, err)
				}
				if !strings.Contains(err.Error(), "context deadline exceeded") &&
					!strings.Contains(err.Error(), "Client.Timeout") {
					t.Fatalf("record %d failed without the bounded client timeout that identifies receiver backpressure: %v", record, err)
				}
				t.Logf("record %d: the gateway stopped answering the post (%v) — backpressure reached ingest", record, err)
				pressureMarker = marker
				break
			}
			if err := poll(fillProbeTimeout, gateway, "marker "+marker+" to reach the healthy recipient", func() bool {
				return sinkContains(t, healthySink, marker)
			}); err != nil {
				t.Fatalf("record %d was acknowledged but marker %s did not reach the healthy recipient: %v", record, marker, err)
			}
		}

		if pressureMarker == "" {
			t.Fatalf("the gateway acknowledged all %d records while the other recipient was down: the stalled exporter's queue never applied backpressure, so this scenario proved nothing about coupling. Its queue_size in test/config/gateway-coupling-ci.yaml is what needs to be smaller, or the padding larger — do not conclude from this that the recipients are independent",
				fillMaxRecords)
		}

		if err := poll(failureTimeout, gateway, "the blocked recipient's enqueue-failure metric", func() bool {
			return exporterMetricPositive(couplingGatewayMetricsPort,
				"otelcol_exporter_enqueue_failed", "otlp_grpc/coupling_stalled")
		}); err != nil {
			t.Fatalf("ingest timed out but otlp_grpc/coupling_stalled reported no enqueue failure, so the timeout cannot be attributed to its full queue: %v\n%s", err,
				exporterMetrics(couplingGatewayMetricsPort,
					"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity",
					"otelcol_exporter_enqueue_failed"))
		}

		if !gatewayResponds(couplingGatewayHTTPPort) {
			t.Fatal("the gateway listener stopped responding even to an unauthenticated probe; this was not pipeline backpressure on an otherwise live process")
		}

		t.Logf("the full stopped-recipient queue backpressured ingest while the gateway remained live; gateway queues:\n%s",
			exporterMetrics(couplingGatewayMetricsPort,
				"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity",
				"otelcol_exporter_enqueue_failed"))
	})

	t.Run("the backlog drains and ingest recovers once the stopped recipient returns", func(t *testing.T) {
		if pressureMarker == "" {
			t.Skip("no backpressure was observed, so there is nothing to clear")
		}

		stalledBackend = startBackend(t, state, "backend-stalled",
			stalledBackendEndpoint, stalledSink, stalledBackendMetricsPort)
		if err := poll(readyTimeout, stalledBackend, "the recovered recipient's OTLP listener", func() bool {
			return endpointAccepts(stalledBackendEndpoint)
		}); err != nil {
			t.Fatalf("the recipient that was supposed to come back never did, so the stall cannot be attributed: %v", err)
		}

		if err := poll(recoveryTimeout, gateway, "the acknowledged backlog to reach the recovered backend", func() bool {
			return sinkContains(t, stalledSink, headroomMarker)
		}); err != nil {
			t.Fatalf("marker %s was acknowledged while the recipient was stopped but never reached it after recovery: %v",
				headroomMarker, err)
		}

		var recoveryMarker string
		var lastSendErr error
		if err := poll(recoveryTimeout, gateway, "ingest to accept a post after recipient recovery", func() bool {
			recoveryMarker = "otelbox-ci-recovered-" + randomHex(t, 8)
			lastSendErr = send(t, recoveryMarker)
			return lastSendErr == nil
		}); err != nil {
			t.Fatalf("the gateway did not resume accepting posts after the recipient returned: %v; last post error: %v", err, lastSendErr)
		}
		for path, name := range map[string]string{
			healthySink: "healthy recipient",
			stalledSink: "recovered recipient",
		} {
			if err := poll(deliveryTimeout, gateway, "marker "+recoveryMarker+" to reach the "+name, func() bool {
				return sinkContains(t, path, recoveryMarker)
			}); err != nil {
				t.Fatalf("marker %s did not reach the %s after recovery: %v", recoveryMarker, name, err)
			}
		}
	})
}

func startBackend(t *testing.T, stateDir, name, endpoint, sink string, metricsPort int) *collector {
	t.Helper()

	return startCollector(t, collectorSpec{
		name:     name,
		stateDir: stateDir,
		env: map[string]string{
			"OTELBOX_CI_BACKEND_ENDPOINT":     endpoint,
			"OTELBOX_CI_BACKEND_SINK":         sink,
			"OTELBOX_CI_BACKEND_METRICS_PORT": fmt.Sprintf("%d", metricsPort),
		},
		configs: []string{configPath("test", "config", "backend-ci.yaml")},
	})
}
