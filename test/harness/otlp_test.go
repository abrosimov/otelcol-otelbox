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

// The marker key is matched by none of the base layer's blocked key patterns
// and the marker value by none of its blocked values, so a record carrying one
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

// Three attributes covering both redaction mechanisms in config/base.yaml, so a
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

var (
	// Separate clients rather than one, because the two answer different
	// questions: a probe that has not been answered in 5s is a listener that is
	// not up, while a send is allowed the receiver's own processing time.
	probeClient = &http.Client{Timeout: 5 * time.Second}
	sendClient  = &http.Client{Timeout: 10 * time.Second}
)

// Returns the HTTP status the collector gave, or 0 for a transport error —
// curl's `000` in the shell harness this replaced. Callers distinguish the two:
// "the listener refused us" is not "the listener answered".
func postLogs(client *http.Client, port int, payload []byte) (int, string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/logs", port)
	response, err := client.Post(url, "application/json", bytes.NewReader(payload))
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

	status, body, err := postLogs(sendClient, port, payload)
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

// An empty resourceLogs array: it reaches the receiver but carries no record,
// so a probe can never contaminate a sink the assertions read.
var emptyLogsPayload = []byte(`{"resourceLogs":[]}`)

func gatewayUnauthenticatedStatus(port int) int {
	status, _, err := postLogs(probeClient, port, emptyLogsPayload)
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

func edgeHealthy() bool {
	response, err := probeClient.Get(fmt.Sprintf("http://127.0.0.1:%d/", edgeHealthPort))
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
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
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
	for _, line := range strings.Split(scrapeMetrics(port), "\n") {
		if !strings.HasPrefix(line, "otelcol_exporter_send_failed") {
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
