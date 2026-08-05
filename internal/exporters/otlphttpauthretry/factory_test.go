package otlphttpauthretry

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
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
			err := classifyError(permanent, authRetry)

			require.Error(t, err)
			require.False(t, consumererror.IsPermanent(err))
			require.ErrorContains(t, err, "Throttle (2h0m0s)")
		})
	}

	permanent := consumererror.NewPermanent(status.Error(codes.InvalidArgument, "bad payload"))
	require.Equal(t, permanent, classifyError(permanent, authRetry))
	require.Equal(t, permanent, classifyError(permanent, AuthRetryConfig{}))
	plain := errors.New("plain")
	require.Same(t, plain, classifyError(plain, authRetry))
	require.NoError(t, classifyError(nil, authRetry))
}
