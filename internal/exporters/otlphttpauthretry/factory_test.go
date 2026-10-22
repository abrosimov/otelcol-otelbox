package otlphttpauthretry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.opentelemetry.io/collector/exporter/exportertest"
	upstream "go.opentelemetry.io/collector/exporter/otlphttpexporter"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestDefaultConfigUsesCanonicalTypeAndUpstreamDefaults(t *testing.T) {
	factory := NewFactory()
	config := factory.CreateDefaultConfig().(*Config)

	require.Equal(t, "otlp_http", factory.Type().String())
	require.True(t, config.RetryConfig.Enabled)
	require.True(t, config.QueueConfig.HasValue())
	config.ClientConfig.Endpoint = "https://localhost:4318"
	require.NoError(t, config.Validate())
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{
			name: "retry disabled",
			configure: func(config *Config) {
				config.RetryConfig.Enabled = false
				config.AuthRetry = AuthRetryConfig{Enabled: true, Interval: time.Hour}
			},
			wantError: "requires",
		},
		{
			name: "interval missing",
			configure: func(config *Config) {
				config.AuthRetry.Enabled = true
			},
			wantError: "must be positive",
		},
		{
			name: "enabled",
			configure: func(config *Config) {
				config.AuthRetry = AuthRetryConfig{Enabled: true, Interval: time.Hour}
			},
		},
		{
			name: "server error retry without retry",
			configure: func(config *Config) {
				config.RetryConfig.Enabled = false
				config.ServerErrorRetry.Enabled = true
			},
			wantError: `"retry_on_server_error" requires`,
		},
		{
			name: "server error retry enabled",
			configure: func(config *Config) {
				config.ServerErrorRetry.Enabled = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := NewFactory().CreateDefaultConfig().(*Config)
			config.ClientConfig.Endpoint = "https://localhost:4318"
			test.configure(config)

			err := config.Validate()
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestInnerConfigDisablesDuplicateQueueAndRetry(t *testing.T) {
	config := NewFactory().CreateDefaultConfig().(*Config)
	inner := innerConfig(config)

	require.False(t, inner.RetryConfig.Enabled)
	require.Equal(t, configoptional.None[exporterhelper.QueueBatchConfig](), inner.QueueConfig)
	require.True(t, config.RetryConfig.Enabled)
	require.True(t, config.QueueConfig.HasValue())
	require.Equal(t, config.ClientConfig.Timeout, inner.ClientConfig.Timeout)
}

func TestClassifyError(t *testing.T) {
	authRetry := AuthRetryConfig{Enabled: true, Interval: 2 * time.Hour}
	for _, code := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			permanent := consumererror.NewPermanent(status.Error(code, "credential rejected"))
			err := classifyError(permanent, authRetry, ServerErrorRetryConfig{})

			require.Error(t, err)
			require.False(t, consumererror.IsPermanent(err))
			require.ErrorContains(t, err, "Throttle (2h0m0s)")
		})
	}

	permanent := consumererror.NewPermanent(status.Error(codes.InvalidArgument, "bad payload"))
	require.Equal(t, permanent, classifyError(permanent, authRetry, ServerErrorRetryConfig{}))
	require.Equal(t, permanent, classifyError(permanent, AuthRetryConfig{}, ServerErrorRetryConfig{Enabled: true}))
	plain := errors.New("plain")
	require.Same(t, plain, classifyError(plain, authRetry, ServerErrorRetryConfig{Enabled: true}))
	require.NoError(t, classifyError(nil, authRetry, ServerErrorRetryConfig{Enabled: true}))
}

// TestClassifyErrorReadsTheUpstreamMessage drives the pinned upstream exporter
// against real responses, so a change to the message the wrapper reads fails
// here rather than silently turning server errors permanent again.
func TestClassifyErrorReadsTheUpstreamMessage(t *testing.T) {
	serverRetry := ServerErrorRetryConfig{Enabled: true}
	tests := []struct {
		status    int
		retryable bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusNotImplemented, true},
		{http.StatusHTTPVersionNotSupported, true},
		{http.StatusRequestEntityTooLarge, false},
		{http.StatusConflict, false},
		{http.StatusBadRequest, false},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
			}))
			defer server.Close()

			config := NewFactory().CreateDefaultConfig().(*Config)
			config.ClientConfig.Endpoint = server.URL
			inner, err := upstream.NewFactory().CreateTraces(context.Background(),
				exportertest.NewNopSettings(upstream.NewFactory().Type()), innerConfig(config))
			require.NoError(t, err)
			require.NoError(t, inner.Start(context.Background(), componenttest.NewNopHost()))
			defer func() { require.NoError(t, inner.Shutdown(context.Background())) }()

			traces := ptrace.NewTraces()
			traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("probe")
			exportErr := inner.ConsumeTraces(context.Background(), traces)
			require.Error(t, exportErr)
			require.True(t, consumererror.IsPermanent(exportErr), "upstream must still call HTTP %d permanent", test.status)

			classified := classifyError(exportErr, AuthRetryConfig{}, serverRetry)
			require.Equal(t, test.retryable, !consumererror.IsPermanent(classified))
			require.Equal(t, exportErr, classifyError(exportErr, AuthRetryConfig{}, ServerErrorRetryConfig{}))
		})
	}
}
