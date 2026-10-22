package harness

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The edge's WAL is capped well below its queue so the storage limit, not the
// queue, is what stops admission. file_storage refuses a max_size below its
// default 100 MiB compaction rebound threshold, which the profile does not
// expose, so 128 MiB is close to the smallest cap the edge profile accepts.
const (
	storageLimitBytes      = 128 * 1024 * 1024
	storageLimitBodyBytes  = 512 * 1024
	storageLimitMaxRecords = 600
)

// TestEdgeStorageLimitKeepsAcknowledgedRecords fills the edge WAL to its
// file_storage max_size while the gateway is down, then requires two things:
// admission fails closed once storage is full, and every record acknowledged
// before that is delivered once the gateway returns. It reproduces or refutes
// PC-13 (docs/research/problem-cards), where a failed metadata write while
// dispatching deletes an acknowledged item.
func TestEdgeStorageLimitKeepsAcknowledgedRecords(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")

	token := "otelbox-ci-limit-" + randomHex(t, 16)
	runID := "otelbox-ci-limit-" + randomHex(t, 6)

	writeTokenFile(t, tokenFile, token)
	authHeaderFile := writeAuthHeaderFile(t, filepath.Join(state, "edge-auth-header"), token)
	chain := mintGatewayChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)

	edge := startCollector(t, collectorSpec{
		name:     "edge-at-storage-limit",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_STORAGE_DIR":               filepath.Join(state, "edge-storage"),
			"OTELBOX_STORAGE_MAX_SIZE_BYTES":    fmt.Sprint(storageLimitBytes),
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

	// Admit records until the edge refuses one. Every 200 is an acknowledgement
	// the safety property must honour; the first refusal is the fail-closed
	// boundary.
	refusal := ""
	for i := range storageLimitMaxRecords {
		marker := fmt.Sprintf("%s-%04d", runID, i)
		status, body, err := postLogs(sendClient, edgeHTTPPort, "",
			logPayload(t, marker, randomHex(t, storageLimitBodyBytes/2)))
		if err != nil {
			t.Fatalf("record %d: the edge did not answer: %v", i, err)
		}
		if status == http.StatusOK {
			acknowledged = append(acknowledged, marker)
			continue
		}
		refusal = fmt.Sprintf("record %d refused with HTTP %d: %s", i, status, body)
		break
	}
	if refusal == "" {
		t.Fatalf("the edge acknowledged %d records of %d KiB without refusing one; a %d MiB WAL cannot hold them, so the storage limit did not stop admission",
			len(acknowledged), storageLimitBodyBytes/1024, storageLimitBytes/(1024*1024))
	}
	if len(acknowledged) == 0 {
		t.Fatalf("the edge refused the first record, so nothing exercises the limit: %s", refusal)
	}
	t.Logf("%d records acknowledged before the storage limit; %s", len(acknowledged), refusal)

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
		name:     "gateway-after-limit",
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
}

// sinkMissing reads the sink once and returns the markers it does not contain.
func sinkMissing(t *testing.T, path string, markers []string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return markers
	}
	if err != nil {
		t.Fatalf("could not read the sink at %s: %v", path, err)
	}
	var missing []string
	for _, marker := range markers {
		// The quote stops marker -0001 matching inside marker -00012.
		if !bytes.Contains(raw, []byte(marker+`"`)) {
			missing = append(missing, marker)
		}
	}
	return missing
}

func firstOf(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values[:1], "")
}
