package harness

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// Eight records of 512 KiB against a 4 MiB queue with the gateway down: the
// queue is full long before the limit, while the WAL's default cap is far away,
// so the queue's overflow policy is what answers.
const (
	overflowQueueBytes = 4 * 1024 * 1024
	overflowBodyBytes  = 512 * 1024
	overflowMaxRecords = 64
	// Far below sendClient's own timeout, which is where a blocking queue would
	// have left the request.
	overflowRefusalDeadline = 3 * time.Second
	grpcStatusUnavailable   = 14
)

// TestEdgeRefusesAtFullQueueWithARetryableStatus fills the edge queue while the
// gateway is down and requires the first request that does not fit to be
// refused at once with a status an OTLP client retries: HTTP 503 and gRPC
// Unavailable. Every record acknowledged before that must be delivered once the
// gateway returns, and a request refused while full must be admitted after the
// queue drains.
func TestEdgeRefusesAtFullQueueWithARetryableStatus(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")

	token := "otelbox-ci-overflow-" + randomHex(t, 16)
	runID := "otelbox-ci-overflow-" + randomHex(t, 6)

	writeTokenFile(t, tokenFile, token)
	authHeaderFile := writeAuthHeaderFile(t, filepath.Join(state, "edge-auth-header"), token)
	chain := mintGatewayChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)

	edge := startCollector(t, collectorSpec{
		name:     "edge-at-full-queue",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_STORAGE_DIR":               filepath.Join(state, "edge-storage"),
			"OTELBOX_QUEUE_SIZE_BYTES":          fmt.Sprint(overflowQueueBytes),
			"OTELBOX_UPSTREAM_ENDPOINT":         fmt.Sprintf("127.0.0.1:%d", gatewayGRPCPort),
			"OTELBOX_UPSTREAM_AUTH_HEADER_FILE": authHeaderFile,
			"OTELBOX_CI_EDGE_CA_FILE":           chain.caFile,
		},
		configs: []string{
			configPath("config", "edge.yaml"),
			configPath("test", "config", "edge-ci.yaml"),
		},
	})
	collectors := []*collector{edge}
	var acknowledged []string
	defer func() {
		if !t.Failed() {
			return
		}
		dumpDiagnostics(t,
			[]diagnosticSection{
				{"edge exporter metrics", exporterMetrics(edgeMetricsPort,
					"otelcol_exporter_sent", "otelcol_exporter_send_failed",
					"otelcol_exporter_enqueue_failed", "otelcol_exporter_queue")},
				{fmt.Sprintf("sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 3)},
			}, collectors...)
	}()

	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy: %v", err)
	}

	refusedMarker := ""
	for i := range overflowMaxRecords {
		marker := fmt.Sprintf("%s-%04d", runID, i)
		started := time.Now()
		status, body, err := postLogs(sendClient, edgeHTTPPort, "",
			logPayload(t, marker, randomHex(t, overflowBodyBytes/2)))
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("record %d: the edge did not answer within the client timeout, so a full queue held the request instead of refusing it: %v", i, err)
		}
		if status == http.StatusOK {
			acknowledged = append(acknowledged, marker)
			continue
		}
		if status != http.StatusServiceUnavailable {
			t.Fatalf("record %d refused with HTTP %d, which an OTLP client does not retry; want 503: %s", i, status, body)
		}
		if elapsed > overflowRefusalDeadline {
			t.Fatalf("record %d was refused only after %s; a full queue must refuse at once, not hold the request towards the client's own timeout", i, elapsed)
		}
		refusedMarker = marker
		t.Logf("%d records acknowledged; record %d refused with HTTP 503 after %s: %s", len(acknowledged), i, elapsed, body)
		break
	}
	if refusedMarker == "" {
		t.Fatalf("the edge acknowledged %d records of %d KiB without refusing one; a %d MiB queue cannot hold them",
			len(acknowledged), overflowBodyBytes/1024, overflowQueueBytes/(1024*1024))
	}
	if len(acknowledged) == 0 {
		t.Fatal("the edge refused the first record, so nothing exercises a full queue")
	}

	t.Run("gRPC refusal is Unavailable", func(t *testing.T) {
		started := time.Now()
		status, detail, err := grpcExport(edgeGRPCPort, grpcLogsMethod, "",
			grpcLogsMessage(runID+"-grpc", randomHex(t, overflowBodyBytes/2)))
		elapsed := time.Since(started)
		switch {
		case err != nil:
			t.Errorf("the OTLP/gRPC receiver did not answer while the queue was full: %v", err)
		case status != grpcStatusUnavailable:
			t.Errorf("grpc-status %d while the queue was full, want %d (Unavailable): %s", status, grpcStatusUnavailable, detail)
		case elapsed > overflowRefusalDeadline:
			t.Errorf("gRPC refusal took %s; a full queue must refuse at once", elapsed)
		}
	})

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
		name:     "gateway-after-overflow",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})
	collectors = append(collectors, gateway)

	var missing []string
	if err := poll(recoveryTimeout, edge, "every acknowledged record to reach the sink", func() bool {
		missing = sinkMissing(t, sink, acknowledged)
		return len(missing) == 0
	}); err != nil {
		t.Fatalf("%d of %d acknowledged records never reached the sink after the gateway returned (first missing: %s): %v",
			len(missing), len(acknowledged), firstOf(missing), err)
	}

	t.Run("the refused request is admitted once the queue drains", func(t *testing.T) {
		status, body, err := postLogs(sendClient, edgeHTTPPort, "",
			logPayload(t, refusedMarker, randomHex(t, overflowBodyBytes/2)))
		if err != nil || status != http.StatusOK {
			t.Fatalf("the retried request was not admitted after the queue drained (HTTP %d, %v): %s", status, err, body)
		}
		if err := poll(recoveryTimeout, edge, "the retried record to reach the sink", func() bool {
			return len(sinkMissing(t, sink, []string{refusedMarker})) == 0
		}); err != nil {
			t.Errorf("the retried record never reached the sink: %v", err)
		}
	})
}
