package harness

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// The gateway's OTLP/HTTP listener sets neither a read nor a write timeout.
// With a read timeout, a body that arrives more slowly than the timeout is
// answered with HTTP 400, which an OTLP SDK treats as permanent and drops; with
// a write timeout, a request whose enqueue waits longer than the timeout is
// persisted but never acknowledged, so the producer sends it again. These two
// tests hold both boundaries past the 30s the profile used to set.
const pastFormerTimeout = 35 * time.Second

// TestGatewayHTTPIngressWaitsForASlowBody sends one trace whose body takes
// longer than the former read timeout to arrive, and requires HTTP 200 and
// delivery.
func TestGatewayHTTPIngressWaitsForASlowBody(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-slow-body", nil)
	defer func() { scenario.dumpOnFailure(t, scenario.gateway) }()

	marker := "otelbox-ci-slow-body-" + randomHex(t, 8)
	payload := tracePayload(t, marker, false)
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/traces", gatewayHTTPPort),
		&trickleReader{data: payload, chunks: 8, pause: pastFormerTimeout / 7})
	if err != nil {
		t.Fatalf("could not build the request: %v", err)
	}
	request.ContentLength = int64(len(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+scenario.token)

	started := time.Now()
	response, err := (&http.Client{Timeout: 2 * pastFormerTimeout}).Do(request)
	if err != nil {
		t.Fatalf("the slow request failed after %s: %v", time.Since(started).Round(time.Second), err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a body that took %s to arrive was answered with HTTP %d: %s",
			time.Since(started).Round(time.Second), response.StatusCode, body)
	}
	if err := poll(deliveryTimeout, scenario.gateway, marker+" to reach the all-signal sink", func() bool {
		return sinkContains(t, scenario.sink, marker)
	}); err != nil {
		t.Fatalf("the slowly sent span never reached the all-signal recipient: %v", err)
	}
}

// TestGatewayHTTPIngressAcknowledgesALateEnqueue holds a mixed request on a
// full selected-traces queue for longer than the former write timeout, then
// lets the queue drain, and requires the waiting producer to receive HTTP 200
// and the all-signal recipient one copy.
func TestGatewayHTTPIngressAcknowledgesALateEnqueue(t *testing.T) {
	scenario := startRecipientScenario(t, "gateway-late-enqueue", map[string]string{
		"OTELBOX_RECIPIENT_QUEUE_SIZE_BYTES": "2097152",
	})
	defer func() { scenario.dumpOnFailure(t, scenario.gateway) }()

	release := make(chan struct{})
	scenario.backend.setReply(func([]byte) backendReply {
		return backendReply{status: http.StatusOK, hold: release}
	})
	fillSelectedQueue(t, scenario, &http.Client{Timeout: 2 * time.Second})

	classified := "otelbox-ci-late-llm-" + randomHex(t, 8)
	unclassified := "otelbox-ci-late-plain-" + randomHex(t, 8)
	payload := mixedTracePayload(t, classified, unclassified)

	type outcome struct {
		status int
		body   string
		err    error
	}
	result := make(chan outcome, 1)
	started := time.Now()
	go func() {
		status, body, err := postTraces(&http.Client{Timeout: 4 * pastFormerTimeout}, gatewayHTTPPort, scenario.token, payload)
		result <- outcome{status, body, err}
	}()

	select {
	case early := <-result:
		t.Fatalf("the request returned after %s, before the selected-traces queue had room: status %d, error %v",
			time.Since(started).Round(time.Second), early.status, early.err)
	case <-time.After(pastFormerTimeout):
	}
	scenario.backend.setReply(nil)
	close(release)

	got := <-result
	if got.err != nil || got.status != http.StatusOK {
		t.Fatalf("a request held %s on a full queue was not acknowledged once it was persisted: status %d, body %q, error %v",
			time.Since(started).Round(time.Second), got.status, got.body, got.err)
	}
	if err := poll(deliveryTimeout, scenario.gateway, unclassified+" to reach the all-signal sink", func() bool {
		return sinkContains(t, scenario.sink, unclassified)
	}); err != nil {
		t.Fatalf("the acknowledged span never reached the all-signal recipient: %v", err)
	}
	if copies := sinkCount(t, scenario.sink, unclassified); copies != 1 {
		t.Fatalf("the all-signal recipient received %d copies of an acknowledged span; want 1", copies)
	}
}

// trickleReader returns data in equal chunks with a pause before every chunk
// but the first, so the whole body takes (chunks-1) × pause to arrive.
type trickleReader struct {
	data   []byte
	chunks int
	pause  time.Duration
	sent   int
}

func (reader *trickleReader) Read(buffer []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	if reader.sent > 0 {
		time.Sleep(reader.pause)
	}
	size := max(1, len(reader.data)/max(1, reader.chunks-reader.sent))
	size = min(size, len(buffer))
	n := copy(buffer, reader.data[:size])
	reader.data = reader.data[n:]
	reader.sent++
	return n, nil
}

var _ io.Reader = (*trickleReader)(nil)
