package harness

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

const (
	concurrentProducers          = 8
	concurrentRecordsPerProducer = 25
)

// TestEdgeConcurrentAdmissionKeepsEveryRecord admits records from several
// producers at once while the gateway is down, kills the edge, and requires
// every acknowledged record after restart: concurrent enqueues into one WAL
// neither overwrite nor lose one another.
func TestEdgeConcurrentAdmissionKeepsEveryRecord(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")
	token := "otelbox-ci-concurrent-" + randomHex(t, 16)
	run := "otelbox-ci-concurrent-" + randomHex(t, 6)

	writeTokenFile(t, tokenFile, token)
	authHeaderFile := writeAuthHeaderFile(t, filepath.Join(state, "edge-auth-header"), token)
	chain := mintGatewayChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)

	edgeSpec := collectorSpec{
		name:     "edge-concurrent-before-crash",
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
	}
	edge := startCollector(t, edgeSpec)
	collectors := []*collector{edge}
	defer func() {
		if !t.Failed() {
			return
		}
		dumpDiagnostics(t, []diagnosticSection{
			{fmt.Sprintf("sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 3)},
		}, collectors...)
	}()
	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy: %v", err)
	}

	// Payloads are built before the workers start: logPayload reports through
	// t, which must not be called from those goroutines after a failure.
	type record struct {
		marker  string
		payload []byte
	}
	batches := make([][]record, concurrentProducers)
	for p := range batches {
		for i := range concurrentRecordsPerProducer {
			marker := fmt.Sprintf("%s-p%02d-r%03d", run, p, i)
			batches[p] = append(batches[p], record{marker, logPayload(t, marker, "concurrent record")})
		}
	}

	var (
		mu           sync.Mutex
		acknowledged []string
		refusals     []string
		wg           sync.WaitGroup
	)
	for _, batch := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, r := range batch {
				status, body, err := postLogs(sendClient, edgeHTTPPort, "", r.payload)
				mu.Lock()
				if err == nil && status == http.StatusOK {
					acknowledged = append(acknowledged, r.marker)
				} else {
					refusals = append(refusals, fmt.Sprintf("%s: status %d, error %v, body %s", r.marker, status, err, body))
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(refusals) > 0 {
		t.Fatalf("the edge refused %d of %d concurrent records with headroom in its queue; first: %s",
			len(refusals), concurrentProducers*concurrentRecordsPerProducer, refusals[0])
	}
	edge.crash(t)

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
		name:     "gateway-after-concurrent-crash",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})
	collectors = append(collectors, gateway)
	if err := poll(readyTimeout, gateway, "the gateway's OTLP/HTTP listener", func() bool {
		return gatewayResponds(gatewayHTTPPort)
	}); err != nil {
		t.Fatalf("the gateway never accepted a connection: %v", err)
	}

	edgeSpec.name = "edge-concurrent-after-crash"
	restarted := startCollector(t, edgeSpec)
	collectors = append(collectors, restarted)
	var missing []string
	if err := poll(deliveryTimeout, restarted, "every concurrently acknowledged record to reach the sink", func() bool {
		missing = sinkMissing(t, sink, acknowledged)
		return len(missing) == 0
	}); err != nil {
		t.Fatalf("%d of %d concurrently acknowledged records did not survive the crash (first missing: %s): %v",
			len(missing), len(acknowledged), firstOf(missing), err)
	}
}
