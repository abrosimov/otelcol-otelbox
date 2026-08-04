package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The marker key is matched by none of the role profiles' blocked key patterns
// and the marker value by none of their blocked values, so a record carrying one
// always survives redaction — which is what makes "the marker arrived" and "the
// secret did not" independent facts rather than one fact stated twice.
const markerKey = "otelbox.ci.marker"

// The OTLP/HTTP JSON shapes the receiver accepts, cut down to what a log record
// needs. Typed rather than a printf template so a malformed payload is a
// compile error instead of a 400 the assertions would have to explain.
type logsPayload struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

type resourceLogs struct {
	Resource  resourceValue `json:"resource"`
	ScopeLogs []scopeLogs   `json:"scopeLogs"`
}

type resourceValue struct {
	Attributes []attribute `json:"attributes"`
}

type scopeLogs struct {
	Scope      scopeValue  `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type scopeValue struct {
	Name string `json:"name"`
}

type logRecord struct {
	TimeUnixNano         string      `json:"timeUnixNano"`
	ObservedTimeUnixNano string      `json:"observedTimeUnixNano"`
	SeverityNumber       int         `json:"severityNumber"`
	SeverityText         string      `json:"severityText"`
	Body                 otlpString  `json:"body"`
	Attributes           []attribute `json:"attributes"`
}

type attribute struct {
	Key   string     `json:"key"`
	Value otlpString `json:"value"`
}

type otlpString struct {
	StringValue string `json:"stringValue"`
}

type metricsPayload struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

type resourceMetrics struct {
	Resource     resourceValue  `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type scopeMetrics struct {
	Scope   scopeValue `json:"scope"`
	Metrics []metric   `json:"metrics"`
}

type metric struct {
	Name  string `json:"name"`
	Gauge gauge  `json:"gauge"`
}

type gauge struct {
	DataPoints []numberDataPoint `json:"dataPoints"`
}

type numberDataPoint struct {
	TimeUnixNano string      `json:"timeUnixNano"`
	AsInt        string      `json:"asInt"`
	Attributes   []attribute `json:"attributes"`
}

type tracesPayload struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

type resourceSpans struct {
	Resource   resourceValue `json:"resource"`
	ScopeSpans []scopeSpans  `json:"scopeSpans"`
}

type scopeSpans struct {
	Scope scopeValue `json:"scope"`
	Spans []span     `json:"spans"`
}

type span struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	Name              string      `json:"name"`
	Kind              int         `json:"kind"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []attribute `json:"attributes"`
}

func logPayload(t *testing.T, marker, body string, extra ...attribute) []byte {
	t.Helper()

	now := strconv.FormatInt(time.Now().UnixNano(), 10)
	attributes := append([]attribute{{Key: markerKey, Value: otlpString{marker}}}, extra...)

	payload, err := json.Marshal(logsPayload{
		ResourceLogs: []resourceLogs{{
			Resource: resourceValue{Attributes: []attribute{
				{Key: "service.name", Value: otlpString{"otelbox-integration-test"}},
			}},
			ScopeLogs: []scopeLogs{{
				Scope: scopeValue{Name: "otelbox.integration"},
				LogRecords: []logRecord{{
					TimeUnixNano:         now,
					ObservedTimeUnixNano: now,
					SeverityNumber:       9,
					SeverityText:         "INFO",
					Body:                 otlpString{body},
					Attributes:           attributes,
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("could not marshal the OTLP payload for marker %s: %v", marker, err)
	}
	return payload
}

func tracePayload(t *testing.T, marker string, selected bool, extra ...attribute) []byte {
	t.Helper()

	started := time.Now()
	resourceAttributes := []attribute{
		{Key: "service.name", Value: otlpString{"otelbox-integration-test"}},
	}
	resourceAttributes = append(resourceAttributes, extra...)
	if selected {
		resourceAttributes = append(resourceAttributes, attribute{
			Key:   "otelbox.telemetry.class",
			Value: otlpString{"llm"},
		})
	}

	payload, err := json.Marshal(tracesPayload{
		ResourceSpans: []resourceSpans{{
			Resource: resourceValue{Attributes: resourceAttributes},
			ScopeSpans: []scopeSpans{{
				Scope: scopeValue{Name: "otelbox.integration"},
				Spans: []span{{
					TraceID:           randomHex(t, 16),
					SpanID:            randomHex(t, 8),
					Name:              "otelbox integration trace",
					Kind:              1,
					StartTimeUnixNano: strconv.FormatInt(started.UnixNano(), 10),
					EndTimeUnixNano:   strconv.FormatInt(started.Add(time.Millisecond).UnixNano(), 10),
					Attributes: append([]attribute{{
						Key:   markerKey,
						Value: otlpString{marker},
					}}, extra...),
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("could not marshal the OTLP trace payload for marker %s: %v", marker, err)
	}
	return payload
}

func metricPayload(t *testing.T, marker string, extra ...attribute) []byte {
	t.Helper()

	attributes := append([]attribute{{Key: markerKey, Value: otlpString{marker}}}, extra...)
	payload, err := json.Marshal(metricsPayload{
		ResourceMetrics: []resourceMetrics{{
			Resource: resourceValue{Attributes: append([]attribute{{
				Key:   "service.name",
				Value: otlpString{"otelbox-integration-test"},
			}}, extra...)},
			ScopeMetrics: []scopeMetrics{{
				Scope: scopeValue{Name: "otelbox.integration"},
				Metrics: []metric{{
					Name: "otelbox.integration.value",
					Gauge: gauge{DataPoints: []numberDataPoint{{
						TimeUnixNano: strconv.FormatInt(time.Now().UnixNano(), 10),
						AsInt:        "1",
						Attributes:   attributes,
					}}},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("could not marshal the OTLP metric payload for marker %s: %v", marker, err)
	}
	return payload
}

// Three attributes covering both redaction mechanisms in redaction/secrets, so a
// pattern list that loses either one fails here:
//
//	password                          — blocked_key_patterns, innocuous value
//	http.request.header.authorization — blocked_key_patterns and blocked_values
//	payload.note                      — blocked_values only, innocuous key
//
// The last is the one that matters most: it is a credential smuggled under a
// key nobody would think to block, which is how credentials actually reach
// telemetry.
func credentialAttributes(secret string) []attribute {
	return []attribute{
		{Key: "password", Value: otlpString{secret}},
		{Key: "http.request.header.authorization", Value: otlpString{"Bearer " + secret}},
		{Key: "payload.note", Value: otlpString{"sk-" + secret}},
	}
}

func credentialCorpus(secret string) ([]attribute, []string) {
	values := []string{
		"Basic " + secret,
		"eyJ" + secret + "." + secret + "." + secret,
		"AKIA1234567890ABCDEF",
		"ghp_1234567890abcdefghijklmnopqrstuv",
		"github_pat_1234567890abcdefghijklmnopqrstuv",
		"xoxb-1234567890-abcdefghijklmnop",
		"AIza1234567890abcdefghijklmnopqrstuv",
		"postgres://admin:" + secret + "@db.example/telemetry",
		"client_secret=" + secret,
		"-----BEGIN PRIVATE KEY-----\n" + secret + "\n-----END PRIVATE KEY-----",
	}

	attributes := credentialAttributes(secret)
	keyCases := []string{
		"token",
		"id_token",
		"x-amz-security-token",
		"client_secret",
		"private_key",
		"db.connection_string",
		"credential",
		"passwd",
		"passphrase",
		"signature",
	}
	for _, key := range keyCases {
		attributes = append(attributes, attribute{Key: key, Value: otlpString{secret}})
	}
	for i, value := range values {
		attributes = append(attributes, attribute{
			Key:   fmt.Sprintf("otelbox.test.payload.%d", i),
			Value: otlpString{value},
		})
	}

	return attributes, append([]string{secret}, values...)
}

var (
	// Separate clients rather than one, because the two answer different
	// questions: a probe that has not been answered in 5s is a listener that is
	// not up, while a send is allowed the receiver's own processing time.
	probeClient = &http.Client{Timeout: 5 * time.Second}
	sendClient  = &http.Client{Timeout: 10 * time.Second}
)

// Returns the HTTP status the collector gave, or 0 for a transport error —
// curl's `000` in the shell harness this replaced. Callers distinguish the two:
// "the listener refused us" is not "the listener answered". An empty token
// sends no authorization header, which is what the edge's loopback receiver
// expects and what the gateway's 401 precondition needs.
func postLogs(client *http.Client, port int, token string, payload []byte) (int, string, error) {
	return postOTLPJSON(client, port, "logs", token, payload)
}

func postTraces(client *http.Client, port int, token string, payload []byte) (int, string, error) {
	return postOTLPJSON(client, port, "traces", token, payload)
}

func postMetrics(client *http.Client, port int, token string, payload []byte) (int, string, error) {
	return postOTLPJSON(client, port, "metrics", token, payload)
}

func postOTLPJSON(client *http.Client, port int, signal, token string, payload []byte) (int, string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/%s", port, signal)
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, strings.TrimSpace(string(body)), nil
}

// Posts to a collector and insists on a 200. Without this check a later "the
// marker never arrived" would be ambiguous between the pipeline dropping the
// record and the test never having sent it.
func sendOrFail(t *testing.T, description string, port int, payload []byte) bool {
	t.Helper()

	status, body, err := postLogs(sendClient, port, "", payload)
	switch {
	case err != nil:
		t.Errorf("%s: the OTLP receiver on port %d did not answer: %v", description, port, err)
		return false
	case status != http.StatusOK:
		t.Errorf("%s: the OTLP receiver on port %d refused the record (HTTP %d): %s",
			description, port, status, body)
		return false
	}
	return true
}

func sendTraceOrFail(t *testing.T, description string, port int, payload []byte) bool {
	t.Helper()

	status, body, err := postTraces(sendClient, port, "", payload)
	switch {
	case err != nil:
		t.Errorf("%s: the OTLP receiver on port %d did not answer: %v", description, port, err)
		return false
	case status != http.StatusOK:
		t.Errorf("%s: the OTLP receiver on port %d refused the trace (HTTP %d): %s",
			description, port, status, body)
		return false
	}
	return true
}

func sendMetricOrFail(t *testing.T, description string, port int, payload []byte) bool {
	t.Helper()

	status, body, err := postMetrics(sendClient, port, "", payload)
	switch {
	case err != nil:
		t.Errorf("%s: the OTLP receiver on port %d did not answer: %v", description, port, err)
		return false
	case status != http.StatusOK:
		t.Errorf("%s: the OTLP receiver on port %d refused the metric (HTTP %d): %s",
			description, port, status, body)
		return false
	}
	return true
}

// An empty resourceLogs array: it reaches the receiver but carries no record,
// so a probe can never contaminate a sink the assertions read.
var emptyLogsPayload = []byte(`{"resourceLogs":[]}`)

func gatewayUnauthenticatedStatus(port int) int {
	status, _, err := postLogs(probeClient, port, "", emptyLogsPayload)
	if err != nil {
		return 0
	}
	return status
}

// A status of 0 is "no HTTP response at all" — connection refused while the
// listener is still opening. Treating it as a response would make the readiness
// poll return on its first attempt, and the precondition would then judge the
// gateway on a request that never arrived.
func gatewayResponds(port int) bool {
	return gatewayUnauthenticatedStatus(port) != 0
}

func endpointAccepts(endpoint string) bool {
	conn, err := net.DialTimeout("tcp", endpoint, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// A profile that lost `use_v2` would still answer 200 here: the v1 responder
// registers `/` on an http.ServeMux, which subtree-matches `/status`. So this
// proves the health endpoint is up, not that it is the v2 responder.
func edgeHealthy() bool {
	response, err := probeClient.Get(fmt.Sprintf("http://127.0.0.1:%d/status", edgeHealthPort))
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK
}

func sinkContains(t *testing.T, path, needle string) bool {
	t.Helper()

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("could not read the sink at %s: %v", path, err)
	}
	return bytes.Contains(raw, []byte(needle))
}

func sinkTail(path string, lines int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(no sink at %s: %v)", path, err)
	}
	const maxDiagnosticBytes = 16 * 1024
	truncated := len(raw) > maxDiagnosticBytes
	if truncated {
		raw = raw[len(raw)-maxDiagnosticBytes:]
	}
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	tail := strings.Join(all, "\n")
	if truncated {
		return fmt.Sprintf("(truncated to the last %d bytes)\n%s", maxDiagnosticBytes, tail)
	}
	return tail
}

func sinkSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func scrapeMetrics(port int) string {
	response, err := probeClient.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
	if err != nil {
		return ""
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return ""
	}
	return string(raw)
}

// True when any otelcol_exporter_send_failed_* series carries a positive value.
// Matched on the prefix rather than a full metric name on purpose: the OTel
// Prometheus exporter appends `_total` to counters and upstream has moved that
// suffix around between releases, so pinning the exact name would turn a
// cosmetic upstream change into a red build. The prefix is anchored at the line
// start, which also skips the `# HELP`/`# TYPE` lines.
func exporterReportsFailure(port int) bool {
	return exporterMetricPositive(port, "otelcol_exporter_send_failed", "")
}

func exporterMetricPositive(port int, prefix, exporter string) bool {
	for _, line := range strings.Split(scrapeMetrics(port), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if exporter != "" && !strings.Contains(line, `exporter="`+exporter+`"`) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if value, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil && value > 0 {
			return true
		}
	}
	return false
}

func exporterMetrics(port int, prefixes ...string) string {
	var kept []string
	for _, line := range strings.Split(scrapeMetrics(port), "\n") {
		for _, prefix := range prefixes {
			if strings.HasPrefix(line, prefix) {
				kept = append(kept, line)
				break
			}
		}
	}
	if len(kept) == 0 {
		return fmt.Sprintf("(nothing matching %s on port %d — endpoint unreachable or the series is absent)",
			strings.Join(prefixes, ", "), port)
	}
	return strings.Join(kept, "\n")
}
