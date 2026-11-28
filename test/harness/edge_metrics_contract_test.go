package harness

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The values are distinct from every default so that a capacity series proves
// which reference it reads, and the main queue is small enough for one test to
// fill. OTELBOX_QUEUE_SIZE_BYTES cannot go below the profile's batch min_size.
const (
	contractQueueBytes     = 1024 * 1024
	contractSelfQueueBytes = 300 * 1024 * 1024
	// The record body is twice this, in hexadecimal characters.
	contractRecordHexBytes = 256 * 1024
	contractRecordBody     = 2 * contractRecordHexBytes
	// A queue that counted items, or batches, would report one for the record;
	// a queue that counted uncompressed bytes would report at least its body.
	contractMinStoredBytes = 128 * 1024
	contractRefusalRecords = 8

	contractGatewayExporter = "otlp_grpc/gateway"
	contractSelfExporter    = "otlp_grpc/gateway_self"
)

type metricSample struct {
	name   string
	labels map[string]string
	value  float64
}

func (s metricSample) labelKeys() []string {
	keys := make([]string, 0, len(s.labels))
	for key := range s.labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

var (
	metricLine      = regexp.MustCompile(`^([^{\s]+)(?:\{(.*)\})?\s+(\S+)`)
	metricLabelPair = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)="([^"]*)"`)
)

func parseMetricSamples(exposition string) []metricSample {
	var samples []metricSample
	for _, line := range strings.Split(exposition, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		match := metricLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		value, err := strconv.ParseFloat(match[3], 64)
		if err != nil {
			continue
		}
		labels := map[string]string{}
		for _, pair := range metricLabelPair.FindAllStringSubmatch(match[2], -1) {
			labels[pair[1]] = pair[2]
		}
		samples = append(samples, metricSample{name: match[1], labels: labels, value: value})
	}
	return samples
}

func edgeSamples() []metricSample {
	return parseMetricSamples(scrapeMetrics(edgeMetricsPort))
}

func samplesNamed(samples []metricSample, name string) []metricSample {
	var matched []metricSample
	for _, sample := range samples {
		if sample.name == name {
			matched = append(matched, sample)
		}
	}
	return matched
}

// findSeries returns the one series of name carrying exactly the given labels.
func findSeries(samples []metricSample, name string, labels map[string]string) (metricSample, bool) {
	for _, sample := range samplesNamed(samples, name) {
		if len(sample.labels) != len(labels) {
			continue
		}
		matches := true
		for key, value := range labels {
			if sample.labels[key] != value {
				matches = false
				break
			}
		}
		if matches {
			return sample, true
		}
	}
	return metricSample{}, false
}

func seriesLabels(dataType, exporter string) map[string]string {
	return map[string]string{"data_type": dataType, "exporter": exporter}
}

func exporterLabels(exporter string) map[string]string {
	return map[string]string{"exporter": exporter}
}

// requireLabelKeys fails when the metric is absent or any of its series carries
// a label key outside want, which is how a consumer's selector would break.
func requireLabelKeys(t *testing.T, samples []metricSample, name string, want ...string) {
	t.Helper()

	found := samplesNamed(samples, name)
	if len(found) == 0 {
		t.Errorf("%s is absent from the edge's metrics endpoint", name)
		return
	}
	slices.Sort(want)
	for _, sample := range found {
		if got := sample.labelKeys(); !slices.Equal(got, want) {
			t.Errorf("%s{%v} has label keys %v, the contract fixes %v", name, sample.labels, got, want)
		}
	}
}

var queueSeries = []struct{ dataType, exporter string }{
	{"logs", contractGatewayExporter},
	{"metrics", contractGatewayExporter},
	{"traces", contractGatewayExporter},
	{"metrics", contractSelfExporter},
}

// requireQueueGauges fails unless the queue gauges are exactly the contract's
// four series, with the contract's label keys and the configured capacities.
func requireQueueGauges(t *testing.T, samples []metricSample) {
	t.Helper()

	requireLabelKeys(t, samples, "otelcol_exporter_queue_size", "data_type", "exporter")
	requireLabelKeys(t, samples, "otelcol_exporter_queue_capacity", "data_type", "exporter")

	for _, name := range []string{"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity"} {
		var got, want []string
		for _, sample := range samplesNamed(samples, name) {
			got = append(got, sample.labels["data_type"]+"/"+sample.labels["exporter"])
		}
		for _, series := range queueSeries {
			want = append(want, series.dataType+"/"+series.exporter)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s series are %v, the contract fixes %v", name, got, want)
		}
	}

	for _, series := range queueSeries {
		labels := seriesLabels(series.dataType, series.exporter)
		capacity, ok := findSeries(samples, "otelcol_exporter_queue_capacity", labels)
		if !ok {
			continue
		}
		wantCapacity := float64(contractQueueBytes)
		if series.exporter == contractSelfExporter {
			wantCapacity = contractSelfQueueBytes
		}
		if capacity.value != wantCapacity {
			t.Errorf("otelcol_exporter_queue_capacity%v is %v, want the configured %v bytes", labels, capacity.value, wantCapacity)
		}
	}
}

func edgeSeriesValue(name string, labels map[string]string) (float64, bool) {
	sample, ok := findSeries(edgeSamples(), name, labels)
	return sample.value, ok
}

// TestEdgeSelfTelemetryContract holds docs/reference/edge-status-metrics.md to
// the real binary: the series, label keys and units that desktop consumers
// read from the edge's Prometheus endpoint.
func TestEdgeSelfTelemetryContract(t *testing.T) {
	state := t.TempDir()
	sink := filepath.Join(state, "sink.json")
	tokenFile := filepath.Join(state, "ingest-tokens")

	token := "otelbox-ci-metrics-" + randomHex(t, 16)
	runID := "otelbox-ci-metrics-" + randomHex(t, 6)
	deliveredMarker := runID + "-delivered"

	writeTokenFile(t, tokenFile, token)
	authHeaderFile := writeAuthHeaderFile(t, filepath.Join(state, "edge-auth-header"), token)
	chain := mintGatewayChain(t, state)
	selectedContract := newSelectedTraceContract(t, state, selectedTraceEndpoint)

	edge := startCollector(t, collectorSpec{
		name:     "edge-metrics-contract",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_STORAGE_DIR":               filepath.Join(state, "edge-storage"),
			"OTELBOX_QUEUE_SIZE_BYTES":          fmt.Sprint(contractQueueBytes),
			"OTELBOX_SELF_QUEUE_SIZE_BYTES":     fmt.Sprint(contractSelfQueueBytes),
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
	defer func() {
		if !t.Failed() {
			return
		}
		dumpDiagnostics(t,
			[]diagnosticSection{
				{"edge exporter metrics", exporterMetrics(edgeMetricsPort, "otelcol_exporter_")},
				{fmt.Sprintf("sink (%d bytes)", sinkSize(sink)), sinkTail(sink, 3)},
			}, collectors...)
	}()

	if err := poll(readyTimeout, edge, "the edge's health endpoint", edgeHealthy); err != nil {
		t.Fatalf("the edge never became healthy: %v", err)
	}

	t.Run("queue gauges exist before any data is sent", func(t *testing.T) {
		samples := edgeSamples()
		requireQueueGauges(t, samples)
		// The self-metrics queue is excluded: the edge scrapes itself into it
		// within seconds of starting.
		for _, series := range queueSeries[:3] {
			labels := seriesLabels(series.dataType, series.exporter)
			if size, ok := findSeries(samples, "otelcol_exporter_queue_size", labels); ok && size.value != 0 {
				t.Errorf("otelcol_exporter_queue_size%v is %v on a fresh edge, want 0", labels, size.value)
			}
		}
	})

	t.Run("queue gauges exist for every signal and exporter, in bytes", func(t *testing.T) {
		if !sendOrFail(t, "delivered record", edgeHTTPPort, logPayload(t, deliveredMarker, "x")) {
			return
		}
		sendTraceOrFail(t, "trace", edgeHTTPPort, tracePayload(t, runID+"-trace", false))
		sendMetricOrFail(t, "metric", edgeHTTPPort, metricPayload(t, runID+"-metric"))
		if !sendOrFail(t, "large record", edgeHTTPPort,
			logPayload(t, runID+"-large", randomHex(t, contractRecordHexBytes))) {
			return
		}

		logsQueue := seriesLabels("logs", contractGatewayExporter)
		if err := poll(readyTimeout, edge, "the logs queue to report stored bytes", func() bool {
			size, ok := edgeSeriesValue("otelcol_exporter_queue_size", logsQueue)
			return ok && size >= contractMinStoredBytes
		}); err != nil {
			t.Fatalf("otelcol_exporter_queue_size%v never reached %d, so it does not count stored bytes: %v",
				logsQueue, contractMinStoredBytes, err)
		}

		samples := edgeSamples()
		requireQueueGauges(t, samples)

		size, _ := findSeries(samples, "otelcol_exporter_queue_size", logsQueue)
		if size.value >= contractRecordBody {
			t.Errorf("otelcol_exporter_queue_size%v is %v for a record whose body alone is %d bytes, so it does not count compressed WAL bytes",
				logsQueue, size.value, contractRecordBody)
		}
		for _, series := range queueSeries {
			labels := seriesLabels(series.dataType, series.exporter)
			size, _ := findSeries(samples, "otelcol_exporter_queue_size", labels)
			capacity, _ := findSeries(samples, "otelcol_exporter_queue_capacity", labels)
			if size.value < 0 || size.value > capacity.value {
				t.Errorf("otelcol_exporter_queue_size%v is %v, outside 0..capacity %v", labels, size.value, capacity.value)
			}
		}
	})

	t.Run("a refused record increments the enqueue-failed counter", func(t *testing.T) {
		refused := false
		for i := range contractRefusalRecords {
			status, body, err := postLogs(sendClient, edgeHTTPPort, "",
				logPayload(t, fmt.Sprintf("%s-fill-%d", runID, i), randomHex(t, contractRecordHexBytes)))
			if err != nil {
				t.Fatalf("record %d: the edge did not answer: %v", i, err)
			}
			if status == http.StatusServiceUnavailable {
				refused = true
				break
			}
			if status != http.StatusOK {
				t.Fatalf("record %d: HTTP %d: %s", i, status, body)
			}
		}
		if !refused {
			t.Fatalf("%d records of %d KiB did not fill a %d MiB queue", contractRefusalRecords,
				contractRecordHexBytes*2/1024, contractQueueBytes/(1024*1024))
		}

		gateway := exporterLabels(contractGatewayExporter)
		if err := poll(readyTimeout, edge, "the enqueue-failed counter", func() bool {
			value, ok := edgeSeriesValue("otelcol_exporter_enqueue_failed_log_records", gateway)
			return ok && value > 0
		}); err != nil {
			t.Fatalf("a refused log record left otelcol_exporter_enqueue_failed_log_records%v at zero or absent: %v", gateway, err)
		}
		requireLabelKeys(t, edgeSamples(), "otelcol_exporter_enqueue_failed_log_records", "exporter")
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
		name:     "gateway-after-metrics-contract",
		stateDir: state,
		env:      gatewayEnv,
		configs: []string{
			configPath("config", "gateway.yaml"),
			configPath("test", "config", "gateway-ci.yaml"),
		},
	})

	collectors = append(collectors, gateway)

	t.Run("delivery raises the sent counters per signal and drains the queues", func(t *testing.T) {
		if err := poll(recoveryTimeout, gateway, "the delivered record to reach the sink", func() bool {
			return sinkContains(t, sink, deliveredMarker)
		}); err != nil {
			t.Fatalf("nothing reached the sink, so no sent counter can move: %v", err)
		}

		sent := []struct {
			name     string
			exporter string
		}{
			{"otelcol_exporter_sent_log_records", contractGatewayExporter},
			{"otelcol_exporter_sent_spans", contractGatewayExporter},
			{"otelcol_exporter_sent_metric_points", contractGatewayExporter},
			{"otelcol_exporter_sent_metric_points", contractSelfExporter},
		}
		if err := poll(recoveryTimeout, edge, "every sent counter to be positive", func() bool {
			for _, series := range sent {
				if value, ok := edgeSeriesValue(series.name, exporterLabels(series.exporter)); !ok || value <= 0 {
					return false
				}
			}
			return true
		}); err != nil {
			t.Errorf("a sent counter stayed zero or absent after delivery (%v): %v", sent, err)
		}

		samples := edgeSamples()
		for _, name := range []string{
			"otelcol_exporter_sent_log_records",
			"otelcol_exporter_sent_spans",
			"otelcol_exporter_sent_metric_points",
		} {
			requireLabelKeys(t, samples, name, "exporter")
		}

		if err := poll(recoveryTimeout, edge, "the main queues to drain to zero", func() bool {
			for _, series := range queueSeries[:3] {
				size, ok := edgeSeriesValue("otelcol_exporter_queue_size", seriesLabels(series.dataType, series.exporter))
				if !ok || size != 0 {
					return false
				}
			}
			return true
		}); err != nil {
			t.Errorf("otelcol_exporter_queue_size did not return to 0 once the gateway accepted the data: %v", err)
		}
	})
}
