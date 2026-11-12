package otlpauthretry

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.opentelemetry.io/collector/exporter/exportertest"
	upstream "go.opentelemetry.io/collector/exporter/otlpexporter"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

func TestDefaultConfigUsesCanonicalTypeAndUpstreamDefaults(t *testing.T) {
	factory := NewFactory()
	config := factory.CreateDefaultConfig().(*Config)

	require.Equal(t, "otlp_grpc", factory.Type().String())
	require.True(t, config.RetryConfig.Enabled)
	require.True(t, config.QueueConfig.HasValue())
	config.ClientConfig.Endpoint = "localhost:4317"
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
			config.ClientConfig.Endpoint = "localhost:4317"
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

func TestInnerConfigDisablesDuplicateQueueRetryAndTimeout(t *testing.T) {
	config := NewFactory().CreateDefaultConfig().(*Config)
	inner := innerConfig(config)

	require.Zero(t, inner.TimeoutConfig.Timeout)
	require.False(t, inner.RetryConfig.Enabled)
	require.Equal(t, configoptional.None[exporterhelper.QueueBatchConfig](), inner.QueueConfig)
	require.True(t, config.RetryConfig.Enabled)
	require.True(t, config.QueueConfig.HasValue())
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

func TestClassifyServerError(t *testing.T) {
	serverRetry := ServerErrorRetryConfig{Enabled: true}
	for _, code := range []codes.Code{codes.Internal, codes.Unknown} {
		t.Run(code.String(), func(t *testing.T) {
			permanent := consumererror.NewPermanent(status.Error(code, "recipient failed"))

			retried := classifyError(permanent, AuthRetryConfig{}, serverRetry)
			require.Error(t, retried)
			require.False(t, consumererror.IsPermanent(retried))
			require.NotContains(t, retried.Error(), "Throttle")

			require.Equal(t, permanent, classifyError(permanent, AuthRetryConfig{}, ServerErrorRetryConfig{}))
		})
	}
	invalid := consumererror.NewPermanent(status.Error(codes.InvalidArgument, "bad payload"))
	require.Equal(t, invalid, classifyError(invalid, AuthRetryConfig{}, serverRetry))
}

func TestClassifyResourceExhausted(t *testing.T) {
	serverRetry := ServerErrorRetryConfig{Enabled: true}

	overloaded := consumererror.NewPermanent(status.Error(codes.ResourceExhausted, "server is overloaded"))
	retried := classifyError(overloaded, AuthRetryConfig{}, serverRetry)
	require.Error(t, retried)
	require.False(t, consumererror.IsPermanent(retried))
	require.NotContains(t, retried.Error(), "Throttle")
	require.Equal(t, overloaded, classifyError(overloaded, AuthRetryConfig{}, ServerErrorRetryConfig{}))

	throttled := exporterhelper.NewThrottleRetry(status.Error(codes.ResourceExhausted, "slow down"), 5*time.Second)
	require.Equal(t, throttled, classifyError(throttled, AuthRetryConfig{}, serverRetry))

	for _, message := range []string{
		"grpc: received message larger than max (3145728 vs. 2097152)",
		"grpc: received message after decompression larger than max 2097152",
		"grpc: message after decompression larger than max (3145728 vs. 2097152)",
		"trying to send message larger than max (3145728 vs. 2097152)",
		"grpc: message too large (3145728 bytes)",
	} {
		t.Run(message, func(t *testing.T) {
			oversized := consumererror.NewPermanent(status.Error(codes.ResourceExhausted, message))
			require.Equal(t, oversized, classifyError(oversized, AuthRetryConfig{}, serverRetry))
		})
	}
}

type refusingTraceServer struct {
	ptraceotlp.UnimplementedGRPCServer
	refusal error
}

func (server *refusingTraceServer) Export(context.Context, ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
	return ptraceotlp.NewExportResponse(), server.refusal
}

func TestClassifyResourceExhaustedReadsTheUpstreamStatus(t *testing.T) {
	serverRetry := ServerErrorRetryConfig{Enabled: true}
	tests := []struct {
		name       string
		refusal    error
		maxRecv    int
		spanName   string
		retryable  bool
		wantDetail string
	}{
		{
			name:       "recipient sheds load",
			refusal:    status.Error(codes.ResourceExhausted, "server is overloaded"),
			maxRecv:    4 << 20,
			spanName:   "probe",
			retryable:  true,
			wantDetail: "server is overloaded",
		},
		{
			name:       "batch above the recipient's receive limit",
			maxRecv:    1024,
			spanName:   strings.Repeat("x", 64<<10),
			retryable:  false,
			wantDetail: "larger than max",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			server := grpc.NewServer(grpc.MaxRecvMsgSize(test.maxRecv))
			ptraceotlp.RegisterGRPCServer(server, &refusingTraceServer{refusal: test.refusal})
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()

			config := NewFactory().CreateDefaultConfig().(*Config)
			config.ClientConfig.Endpoint = listener.Addr().String()
			config.ClientConfig.TLS.Insecure = true
			inner, err := upstream.NewFactory().CreateTraces(context.Background(),
				exportertest.NewNopSettings(upstream.NewFactory().Type()), innerConfig(config))
			require.NoError(t, err)
			require.NoError(t, inner.Start(context.Background(), componenttest.NewNopHost()))
			defer func() { require.NoError(t, inner.Shutdown(context.Background())) }()

			traces := ptrace.NewTraces()
			traces.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName(test.spanName)
			exportErr := inner.ConsumeTraces(context.Background(), traces)
			require.Error(t, exportErr)
			require.Equal(t, codes.ResourceExhausted, status.Code(exportErr))
			require.ErrorContains(t, exportErr, test.wantDetail)
			require.True(t, consumererror.IsPermanent(exportErr), "upstream must still call ResourceExhausted without RetryInfo permanent")

			classified := classifyError(exportErr, AuthRetryConfig{}, serverRetry)
			require.Equal(t, test.retryable, !consumererror.IsPermanent(classified))
			require.Equal(t, exportErr, classifyError(exportErr, AuthRetryConfig{}, ServerErrorRetryConfig{}))
		})
	}
}
