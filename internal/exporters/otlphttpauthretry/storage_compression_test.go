package otlphttpauthretry

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter/exportertest"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

func TestStorageCompressionValidation(t *testing.T) {
	for _, valid := range []StorageCompression{"", StorageCompressionNone, StorageCompressionZstd} {
		require.NoError(t, valid.Validate())
	}
	require.EqualError(t, StorageCompression("gzip").Validate(), `unsupported storage compression "gzip"`)

	config := NewFactory().CreateDefaultConfig().(*Config)
	config.ClientConfig.Endpoint = "http://localhost:4318"
	config.StorageCompression = StorageCompressionZstd
	require.ErrorContains(t, config.Validate(), `"storage_compression" requires "sending_queue.storage"`)
	storageID := component.MustNewID("file_storage")
	config.QueueConfig.Get().StorageID = &storageID
	require.NoError(t, config.Validate())
}

func TestDecompressStoredItemPassesThroughWhatIsNotAFrame(t *testing.T) {
	plain := []byte{0x0D, 1, 0, 0, 0}
	require.Equal(t, plain, decompressStoredItem(plain))
	notAFrame := append(append([]byte{}, zstdFrameMagic...), 1, 2, 3)
	require.Equal(t, notAFrame, decompressStoredItem(notAFrame))
	payload := bytes.Repeat([]byte("otlp payload "), 1000)
	frame := zstdEncoder.EncodeAll(payload, nil)
	require.Less(t, len(frame), len(payload)/10)
	require.Equal(t, payload, decompressStoredItem(frame))
}

// The queue stores what the wrapped encoding writes, and a queue written with
// compression is still delivered after compression is turned off.
func TestPersistentQueueStoresZstdFramesAndReadsThemBack(t *testing.T) {
	store := newMemoryStorage()
	storageID := component.MustNewID("file_storage")
	host := storageHost{storageID: store}

	compressing := NewFactory().CreateDefaultConfig().(*Config)
	compressing.ClientConfig.Endpoint = "http://127.0.0.1:1"
	compressing.QueueConfig.Get().StorageID = &storageID
	compressing.RetryConfig.InitialInterval = 10 * time.Millisecond
	compressing.StorageCompression = StorageCompressionZstd

	settings := exportertest.NewNopSettings(componentType)
	exporter, err := NewFactory().CreateTraces(context.Background(), settings, compressing)
	require.NoError(t, err)
	require.NoError(t, exporter.Start(context.Background(), host))
	require.NoError(t, exporter.ConsumeTraces(context.Background(), probeTraces("compressed-probe")))
	require.Eventually(t, store.holdsZstdFrame, 5*time.Second, 10*time.Millisecond, "the queue item was not stored as a zstd frame")
	require.NoError(t, exporter.Shutdown(context.Background()))

	received := &capturingTraceServer{}
	server := httptest.NewServer(received)
	defer server.Close()

	plain := NewFactory().CreateDefaultConfig().(*Config)
	plain.ClientConfig.Endpoint = server.URL
	plain.QueueConfig.Get().StorageID = &storageID
	reopened, err := NewFactory().CreateTraces(context.Background(), settings, plain)
	require.NoError(t, err)
	require.NoError(t, reopened.Start(context.Background(), host))
	defer func() { require.NoError(t, reopened.Shutdown(context.Background())) }()
	require.Eventually(t, func() bool { return received.has("compressed-probe") }, 10*time.Second, 10*time.Millisecond,
		"the compressed item was not delivered after compression was turned off")
}

func probeTraces(name string) ptrace.Traces {
	traces := ptrace.NewTraces()
	traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName(name)
	return traces
}

type capturingTraceServer struct {
	mu    sync.Mutex
	names []string
}

func (server *capturingTraceServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var body io.Reader = request.Body
	if request.Header.Get("Content-Encoding") == "gzip" {
		unzipped, err := gzip.NewReader(request.Body)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		body = unzipped
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	exportRequest := ptraceotlp.NewExportRequest()
	if err := exportRequest.UnmarshalProto(raw); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	server.mu.Lock()
	for _, resourceSpans := range exportRequest.Traces().ResourceSpans().All() {
		for _, scopeSpans := range resourceSpans.ScopeSpans().All() {
			for _, span := range scopeSpans.Spans().All() {
				server.names = append(server.names, span.Name())
			}
		}
	}
	server.mu.Unlock()
	response, _ := ptraceotlp.NewExportResponse().MarshalProto()
	writer.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = writer.Write(response)
}

func (server *capturingTraceServer) has(name string) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	for _, received := range server.names {
		if received == name {
			return true
		}
	}
	return false
}

type storageHost map[component.ID]component.Component

func (host storageHost) GetExtensions() map[component.ID]component.Component { return host }

// memoryStorage is a storage extension whose one client outlives the
// exporters using it, as a file would.
type memoryStorage struct {
	component.StartFunc
	component.ShutdownFunc
	mu     sync.Mutex
	values map[string][]byte
}

func newMemoryStorage() *memoryStorage { return &memoryStorage{values: map[string][]byte{}} }

func (store *memoryStorage) GetClient(context.Context, component.Kind, component.ID, string) (storage.Client, error) {
	return memoryClient{store}, nil
}

func (store *memoryStorage) holdsZstdFrame() bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, value := range store.values {
		if key != "qmv0" && bytes.HasPrefix(value, zstdFrameMagic) {
			return true
		}
	}
	return false
}

type memoryClient struct{ store *memoryStorage }

func (client memoryClient) Get(ctx context.Context, key string) ([]byte, error) {
	operation := storage.GetOperation(key)
	err := client.Batch(ctx, operation)
	return operation.Value, err
}

func (client memoryClient) Set(ctx context.Context, key string, value []byte) error {
	return client.Batch(ctx, storage.SetOperation(key, value))
}

func (client memoryClient) Delete(ctx context.Context, key string) error {
	return client.Batch(ctx, storage.DeleteOperation(key))
}

func (client memoryClient) Batch(_ context.Context, operations ...*storage.Operation) error {
	client.store.mu.Lock()
	defer client.store.mu.Unlock()
	for _, operation := range operations {
		switch operation.Type {
		case storage.Get:
			operation.Value = bytes.Clone(client.store.values[operation.Key])
		case storage.Set:
			client.store.values[operation.Key] = bytes.Clone(operation.Value)
		case storage.Delete:
			delete(client.store.values, operation.Key)
		}
	}
	return nil
}

func (memoryClient) Close(context.Context) error { return nil }
