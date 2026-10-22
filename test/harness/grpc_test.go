package harness

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// OTLP/gRPC without grpc-go: the harness stays a standard-library module, so a
// unary call is written out by hand. gRPC is HTTP/2 POST to /<service>/<method>
// with a five-byte frame header (compressed flag, big-endian length) around
// one protobuf message; the outcome is the grpc-status trailer, or a header
// when the server answers trailers-only. Go's net/http speaks HTTP/2 without
// TLS once it is asked to.

const (
	grpcLogsMethod    = "/opentelemetry.proto.collector.logs.v1.LogsService/Export"
	grpcTracesMethod  = "/opentelemetry.proto.collector.trace.v1.TraceService/Export"
	grpcMetricsMethod = "/opentelemetry.proto.collector.metrics.v1.MetricsService/Export"

	grpcStatusOK              = 0
	grpcStatusUnauthenticated = 16
)

var grpcClient = func() *http.Client {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Protocols: protocols},
	}
}()

// grpcExport makes one unary OTLP export call and returns the grpc-status code
// and message. A transport failure is an error; a non-OK status is not.
func grpcExport(port int, method, token string, message []byte) (int, string, error) {
	frame := make([]byte, 5, 5+len(message))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(message)))
	frame = append(frame, message...)

	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", port, method), bytes.NewReader(frame))
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Content-Type", "application/grpc")
	request.Header.Set("TE", "trailers")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := grpcClient.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return 0, "", err
	}
	if response.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("gRPC transport answered HTTP %d", response.StatusCode)
	}

	code := response.Trailer.Get("Grpc-Status")
	detail := response.Trailer.Get("Grpc-Message")
	if code == "" {
		code = response.Header.Get("Grpc-Status")
		detail = response.Header.Get("Grpc-Message")
	}
	status, err := strconv.Atoi(code)
	if err != nil {
		return 0, "", fmt.Errorf("gRPC response carried no grpc-status (%q)", code)
	}
	return status, detail, nil
}

// grpcSendOrFail exports one message and insists on status OK.
func grpcSendOrFail(t *testing.T, description string, port int, method, token string, message []byte) bool {
	t.Helper()

	status, detail, err := grpcExport(port, method, token, message)
	switch {
	case err != nil:
		t.Errorf("%s: the OTLP/gRPC receiver on port %d did not answer: %v", description, port, err)
		return false
	case status != grpcStatusOK:
		t.Errorf("%s: the OTLP/gRPC receiver on port %d refused the export (grpc-status %d): %s",
			description, port, status, detail)
		return false
	}
	return true
}

// A minimal protobuf writer: enough wire format for the OTLP messages below.
type protoMessage []byte

func (m protoMessage) tag(field, wireType int) protoMessage {
	return binary.AppendUvarint(m, uint64(field<<3|wireType))
}

func (m protoMessage) bytesField(field int, value []byte) protoMessage {
	m = m.tag(field, 2)
	m = binary.AppendUvarint(m, uint64(len(value)))
	return append(m, value...)
}

func (m protoMessage) stringField(field int, value string) protoMessage {
	return m.bytesField(field, []byte(value))
}

func (m protoMessage) varintField(field int, value uint64) protoMessage {
	return binary.AppendUvarint(m.tag(field, 0), value)
}

func (m protoMessage) fixed64Field(field int, value uint64) protoMessage {
	return binary.LittleEndian.AppendUint64(m.tag(field, 1), value)
}

func protoKeyValue(key, value string) []byte {
	anyValue := protoMessage(nil).stringField(1, value)
	return protoMessage(nil).stringField(1, key).bytesField(2, anyValue)
}

func protoResource() []byte {
	return protoMessage(nil).bytesField(1, protoKeyValue("service.name", "otelbox-integration-test"))
}

func protoScope() []byte {
	return protoMessage(nil).stringField(1, "otelbox.integration")
}

// grpcLogsMessage is an ExportLogsServiceRequest with one record carrying
// marker as its otelbox.ci.marker attribute.
func grpcLogsMessage(marker, body string) []byte {
	now := uint64(time.Now().UnixNano())
	record := protoMessage(nil).
		fixed64Field(1, now).
		varintField(2, 9).
		stringField(3, "INFO").
		bytesField(5, protoMessage(nil).stringField(1, body)).
		bytesField(6, protoKeyValue(markerKey, marker)).
		fixed64Field(11, now)
	scopeLogs := protoMessage(nil).bytesField(1, protoScope()).bytesField(2, record)
	resourceLogs := protoMessage(nil).bytesField(1, protoResource()).bytesField(2, scopeLogs)
	return protoMessage(nil).bytesField(1, resourceLogs)
}

// grpcTraceMessage is an ExportTraceServiceRequest with one server span
// carrying marker as its otelbox.ci.marker attribute.
func grpcTraceMessage(t *testing.T, marker string) []byte {
	t.Helper()

	traceID := make([]byte, 16)
	spanID := make([]byte, 8)
	if _, err := rand.Read(traceID); err != nil {
		t.Fatalf("could not generate a trace ID: %v", err)
	}
	if _, err := rand.Read(spanID); err != nil {
		t.Fatalf("could not generate a span ID: %v", err)
	}
	ended := uint64(time.Now().UnixNano())
	span := protoMessage(nil).
		bytesField(1, traceID).
		bytesField(2, spanID).
		stringField(5, "otelbox-integration-span").
		varintField(6, 2).
		fixed64Field(7, ended-uint64(time.Millisecond)).
		fixed64Field(8, ended).
		bytesField(9, protoKeyValue(markerKey, marker))
	scopeSpans := protoMessage(nil).bytesField(1, protoScope()).bytesField(2, span)
	resourceSpans := protoMessage(nil).bytesField(1, protoResource()).bytesField(2, scopeSpans)
	return protoMessage(nil).bytesField(1, resourceSpans)
}

// grpcMetricsMessage is an ExportMetricsServiceRequest with one gauge point
// carrying marker as its otelbox.ci.marker attribute.
func grpcMetricsMessage(marker string) []byte {
	point := protoMessage(nil).
		fixed64Field(3, uint64(time.Now().UnixNano())).
		fixed64Field(4, math.Float64bits(1)).
		bytesField(7, protoKeyValue(markerKey, marker))
	gauge := protoMessage(nil).bytesField(1, point)
	metric := protoMessage(nil).stringField(1, "otelbox.ci.gauge").bytesField(5, gauge)
	scopeMetrics := protoMessage(nil).bytesField(1, protoScope()).bytesField(2, metric)
	resourceMetrics := protoMessage(nil).bytesField(1, protoResource()).bytesField(2, scopeMetrics)
	return protoMessage(nil).bytesField(1, resourceMetrics)
}
