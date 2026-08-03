package harness

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const selectedTraceProtocolHeader = "x-otelbox-ci-protocol-version"

type selectedTraceContract struct {
	listenEndpoint string
	exportEndpoint string
	authHeader     string
	authFile       string
	protocolValue  string
}

func newSelectedTraceContract(t *testing.T, state, endpoint string) selectedTraceContract {
	t.Helper()

	authHeader := "Basic " + randomHex(t, 24)
	authFile := filepath.Join(state, "selected-traces-auth-header")
	if err := os.WriteFile(authFile, []byte(authHeader+"\n"), 0o600); err != nil {
		t.Fatalf("could not write the selected-traces authorization header file: %v", err)
	}

	return selectedTraceContract{
		listenEndpoint: endpoint,
		exportEndpoint: "http://" + endpoint + "/otel",
		authHeader:     authHeader,
		authFile:       authFile,
		protocolValue:  "ci-contract-v4",
	}
}

func (c selectedTraceContract) addEnv(env map[string]string) {
	env["OTELBOX_SELECTED_TRACES_ENDPOINT"] = c.exportEndpoint
	env["OTELBOX_SELECTED_TRACES_AUTH_HEADER_FILE"] = c.authFile
	env["OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_NAME"] = selectedTraceProtocolHeader
	env["OTELBOX_SELECTED_TRACES_PROTOCOL_HEADER_VALUE"] = c.protocolValue
}

type selectedTraceBackend struct {
	contract selectedTraceContract
	server   *http.Server
	listener net.Listener

	mu       sync.Mutex
	bodies   [][]byte
	failures []string
	serveErr error
}

func startSelectedTraceBackend(t *testing.T, contract selectedTraceContract) *selectedTraceBackend {
	t.Helper()

	listener, err := net.Listen("tcp", contract.listenEndpoint)
	if err != nil {
		t.Fatalf("could not listen for the selected-traces backend on %s: %v", contract.listenEndpoint, err)
	}

	backend := &selectedTraceBackend{contract: contract, listener: listener}
	mux := http.NewServeMux()
	mux.HandleFunc("/otel/v1/traces", backend.receive)
	backend.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		err := backend.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			backend.mu.Lock()
			backend.serveErr = err
			backend.mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := backend.server.Shutdown(ctx); err != nil {
			t.Errorf("could not stop the selected-traces backend: %v", err)
		}
	})
	return backend
}

func (b *selectedTraceBackend) receive(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		b.reject(response, fmt.Sprintf("method %s", request.Method), http.StatusMethodNotAllowed)
		return
	}
	if request.Header.Get("Authorization") != b.contract.authHeader {
		b.reject(response, "authorization header mismatch", http.StatusUnauthorized)
		return
	}
	if request.Header.Get(selectedTraceProtocolHeader) != b.contract.protocolValue {
		b.reject(response, "protocol header mismatch", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/x-protobuf") {
		b.reject(response, "content type is not OTLP protobuf", http.StatusUnsupportedMediaType)
		return
	}

	reader := io.Reader(request.Body)
	if request.Header.Get("Content-Encoding") == "gzip" {
		compressed, err := gzip.NewReader(request.Body)
		if err != nil {
			b.reject(response, "invalid gzip body", http.StatusBadRequest)
			return
		}
		defer compressed.Close()
		reader = compressed
	}

	const maxBody = 8 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(reader, maxBody+1))
	if err != nil {
		b.reject(response, "could not read request body", http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		b.reject(response, "request body exceeded test limit", http.StatusRequestEntityTooLarge)
		return
	}

	b.mu.Lock()
	b.bodies = append(b.bodies, body)
	b.mu.Unlock()

	response.Header().Set("Content-Type", "application/x-protobuf")
	response.WriteHeader(http.StatusOK)
}

func (b *selectedTraceBackend) reject(response http.ResponseWriter, failure string, status int) {
	b.mu.Lock()
	b.failures = append(b.failures, failure)
	b.mu.Unlock()
	http.Error(response, failure, status)
}

func (b *selectedTraceBackend) contains(marker string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, body := range b.bodies {
		if strings.Contains(string(body), marker) {
			return true
		}
	}
	return false
}

func (b *selectedTraceBackend) diagnostics() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return fmt.Sprintf("requests=%d failures=%v serve_error=%v", len(b.bodies), b.failures, b.serveErr)
}
