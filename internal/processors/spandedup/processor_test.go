package spandedup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
)

var storageID = component.MustNewIDWithName("file_storage", "dedup")

type harness struct {
	processor *spanDedup
	client    *memoryClient
	reader    *sdkmetric.ManualReader
}

func startProcessor(t *testing.T, client *memoryClient, next consumer.Traces) harness {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	settings := processortest.NewNopSettings(componentType)
	settings.MeterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	config := createDefaultConfig().(*Config)
	config.Storage = storageID

	created, err := NewFactory().CreateTraces(context.Background(), settings, config, next)
	require.NoError(t, err)
	host := extensionHost{storageID: &memoryExtension{client: client}}
	require.NoError(t, created.Start(context.Background(), host))
	t.Cleanup(func() { require.NoError(t, created.Shutdown(context.Background())) })

	dedup := created.(*spanDedup)
	dedup.now = func() time.Time { return epoch }
	return harness{processor: dedup, client: client, reader: reader}
}

func (h harness) duplicatesCounted(t *testing.T) int64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	require.NoError(t, h.reader.Collect(context.Background(), &collected))
	for _, scope := range collected.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != "otelcol_processor_span_dedup_duplicate_spans" {
				continue
			}
			var total int64
			for _, point := range recorded.Data.(metricdata.Sum[int64]).DataPoints {
				processorID, _ := point.Attributes.Value("processor")
				require.Equal(t, "span_dedup", processorID.AsString())
				total += point.Value
			}
			return total
		}
	}
	return 0
}

func spanWith(traces ptrace.Traces, traceByte, spanByte byte, name string) {
	span := traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetTraceID(pcommon.TraceID{traceByte})
	span.SetSpanID(pcommon.SpanID{spanByte})
	span.SetName(name)
}

func tracesWithSpans(spans ...byte) ptrace.Traces {
	batch := ptrace.NewTraces()
	for _, spanByte := range spans {
		spanWith(batch, 1, spanByte, "span")
	}
	return batch
}

func spanIDs(batches []ptrace.Traces) []byte {
	var ids []byte
	for _, batch := range batches {
		for _, resourceSpans := range batch.ResourceSpans().All() {
			for _, scopeSpans := range resourceSpans.ScopeSpans().All() {
				for _, span := range scopeSpans.Spans().All() {
					ids = append(ids, span.SpanID()[0])
				}
			}
		}
	}
	return ids
}

func TestAcceptedSpanIsDroppedWhenSentAgain(t *testing.T) {
	sink := &consumertest.TracesSink{}
	h := startProcessor(t, newMemoryClient(), sink)

	require.NoError(t, h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1)))
	require.NoError(t, h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1, 2)))
	require.NoError(t, h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1, 2)))

	require.Equal(t, []byte{1, 2}, spanIDs(sink.AllTraces()))
	require.Len(t, sink.AllTraces(), 2, "a request whose every span was seen must not reach the next consumer")
	require.Equal(t, int64(3), h.duplicatesCounted(t))
}

func TestSpanIsNotRecordedWhenTheNextConsumerRefusesIt(t *testing.T) {
	refused := errors.New("queue is full")
	var refuse bool
	sink := &consumertest.TracesSink{}
	next, err := consumer.NewTraces(func(ctx context.Context, batch ptrace.Traces) error {
		if refuse {
			return refused
		}
		return sink.ConsumeTraces(ctx, batch)
	})
	require.NoError(t, err)
	h := startProcessor(t, newMemoryClient(), next)

	refuse = true
	require.ErrorIs(t, h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1)), refused)
	refuse = false
	require.NoError(t, h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1)))

	require.Equal(t, []byte{1}, spanIDs(sink.AllTraces()), "the retry of a refused span must pass")
	require.Zero(t, h.duplicatesCounted(t))
}

func TestStorageFailureAfterAcceptanceIsRetryable(t *testing.T) {
	sink := &consumertest.TracesSink{}
	client := newMemoryClient()
	h := startProcessor(t, client, sink)
	storageDown := errors.New("disk unavailable")
	client.failBatches(storageDown)

	err := h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1))

	require.ErrorIs(t, err, storageDown)
	require.False(t, consumererror.IsPermanent(err))
	require.Equal(t, []byte{1}, spanIDs(sink.AllTraces()))

	client.failBatches(nil)
	require.NoError(t, h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1, 2)))
	require.Equal(t, []byte{1, 2}, spanIDs(sink.AllTraces()), "the producer's retry must not enqueue the accepted span twice")

	reloaded := loadForTest(t, client.reopen(), defaultTTL, defaultMaxEntries)
	require.True(t, reloaded.contains(testKeyFor(1, 1)))
	require.True(t, reloaded.contains(testKeyFor(1, 2)))
}

func TestRepeatedSpanWithinOneRequestIsSentOnce(t *testing.T) {
	sink := &consumertest.TracesSink{}
	h := startProcessor(t, newMemoryClient(), sink)

	require.NoError(t, h.processor.ConsumeTraces(context.Background(), tracesWithSpans(1, 1)))

	require.Equal(t, []byte{1}, spanIDs(sink.AllTraces()))
	require.Equal(t, int64(1), h.duplicatesCounted(t))
}

func TestSpanWithoutIdentityIsNeverDropped(t *testing.T) {
	sink := &consumertest.TracesSink{}
	h := startProcessor(t, newMemoryClient(), sink)
	anonymous := func() ptrace.Traces {
		batch := ptrace.NewTraces()
		spanWith(batch, 0, 0, "anonymous")
		return batch
	}

	require.NoError(t, h.processor.ConsumeTraces(context.Background(), anonymous()))
	require.NoError(t, h.processor.ConsumeTraces(context.Background(), anonymous()))

	require.Len(t, sink.AllTraces(), 2)
	require.Empty(t, h.processor.seen.index)
}

func TestSeenSpansSurviveARestart(t *testing.T) {
	client := newMemoryClient()
	first := startProcessor(t, client, &consumertest.TracesSink{})
	require.NoError(t, first.processor.ConsumeTraces(context.Background(), tracesWithSpans(1)))
	require.NoError(t, first.processor.Shutdown(context.Background()))

	sink := &consumertest.TracesSink{}
	restarted := startProcessor(t, client.reopen(), sink)
	require.NoError(t, restarted.processor.ConsumeTraces(context.Background(), tracesWithSpans(1, 2)))

	require.Equal(t, []byte{2}, spanIDs(sink.AllTraces()))
}

func TestStartRequiresAReadableStorageExtension(t *testing.T) {
	config := createDefaultConfig().(*Config)
	config.Storage = storageID
	settings := processortest.NewNopSettings(componentType)
	broken := newMemoryClient()
	broken.getErr = errors.New("disk unavailable")

	tests := []struct {
		name      string
		host      extensionHost
		wantError string
	}{
		{name: "missing", host: extensionHost{}, wantError: "is not configured"},
		{name: "not storage", host: extensionHost{storageID: componentWithoutStorage{}}, wantError: "is not a storage extension"},
		{name: "cannot open", host: extensionHost{storageID: &memoryExtension{openErr: errors.New("locked")}}, wantError: "locked"},
		{name: "cannot read", host: extensionHost{storageID: &memoryExtension{client: broken}}, wantError: "disk unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			created, err := NewFactory().CreateTraces(context.Background(), settings, config, &consumertest.TracesSink{})
			require.NoError(t, err)

			require.ErrorContains(t, created.Start(context.Background(), test.host), test.wantError)
		})
	}
	require.True(t, broken.closed, "a client opened for a failed start must be closed")
}

type componentWithoutStorage struct {
	component.StartFunc
	component.ShutdownFunc
}

func testKeyFor(traceByte, spanByte byte) spanKey {
	var key spanKey
	key[0] = traceByte
	key[16] = spanByte
	return key
}
