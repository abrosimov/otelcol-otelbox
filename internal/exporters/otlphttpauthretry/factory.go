package otlphttpauthretry

import (
	"context"
	"regexp"

	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	upstream "go.opentelemetry.io/collector/exporter/otlphttpexporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

var componentType = component.MustNewType("otlp_http")

func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		componentType,
		createDefaultConfig,
		exporter.WithTraces(createTraces, component.StabilityLevelStable),
		exporter.WithMetrics(createMetrics, component.StabilityLevelStable),
		exporter.WithLogs(createLogs, component.StabilityLevelStable),
	)
}

func createDefaultConfig() component.Config {
	config := upstream.NewFactory().CreateDefaultConfig().(*upstream.Config)
	return &Config{Config: *config}
}

func createTraces(ctx context.Context, settings exporter.Settings, config component.Config) (exporter.Traces, error) {
	wrapperConfig := config.(*Config)
	inner, err := upstream.NewFactory().CreateTraces(ctx, innerSettings(settings), innerConfig(wrapperConfig))
	if err != nil {
		return nil, err
	}
	return exporterhelper.NewTraces(
		ctx,
		settings,
		config,
		func(ctx context.Context, traces ptrace.Traces) error {
			return classifyError(inner.ConsumeTraces(ctx, traces), wrapperConfig.AuthRetry, wrapperConfig.ServerErrorRetry)
		},
		exporterhelper.WithCapabilities(consumer.Capabilities{MutatesData: false}),
		exporterhelper.WithTimeout(exporterhelper.TimeoutConfig{Timeout: 0}),
		exporterhelper.WithRetry(wrapperConfig.RetryConfig),
		exporterhelper.WithQueue(wrapperConfig.QueueConfig),
		exporterhelper.WithStart(inner.Start),
		exporterhelper.WithShutdown(inner.Shutdown),
	)
}

func createMetrics(ctx context.Context, settings exporter.Settings, config component.Config) (exporter.Metrics, error) {
	wrapperConfig := config.(*Config)
	inner, err := upstream.NewFactory().CreateMetrics(ctx, innerSettings(settings), innerConfig(wrapperConfig))
	if err != nil {
		return nil, err
	}
	return exporterhelper.NewMetrics(
		ctx,
		settings,
		config,
		func(ctx context.Context, metrics pmetric.Metrics) error {
			return classifyError(inner.ConsumeMetrics(ctx, metrics), wrapperConfig.AuthRetry, wrapperConfig.ServerErrorRetry)
		},
		exporterhelper.WithCapabilities(consumer.Capabilities{MutatesData: false}),
		exporterhelper.WithTimeout(exporterhelper.TimeoutConfig{Timeout: 0}),
		exporterhelper.WithRetry(wrapperConfig.RetryConfig),
		exporterhelper.WithQueue(wrapperConfig.QueueConfig),
		exporterhelper.WithStart(inner.Start),
		exporterhelper.WithShutdown(inner.Shutdown),
	)
}

func createLogs(ctx context.Context, settings exporter.Settings, config component.Config) (exporter.Logs, error) {
	wrapperConfig := config.(*Config)
	inner, err := upstream.NewFactory().CreateLogs(ctx, innerSettings(settings), innerConfig(wrapperConfig))
	if err != nil {
		return nil, err
	}
	return exporterhelper.NewLogs(
		ctx,
		settings,
		config,
		func(ctx context.Context, logs plog.Logs) error {
			return classifyError(inner.ConsumeLogs(ctx, logs), wrapperConfig.AuthRetry, wrapperConfig.ServerErrorRetry)
		},
		exporterhelper.WithCapabilities(consumer.Capabilities{MutatesData: false}),
		exporterhelper.WithTimeout(exporterhelper.TimeoutConfig{Timeout: 0}),
		exporterhelper.WithRetry(wrapperConfig.RetryConfig),
		exporterhelper.WithQueue(wrapperConfig.QueueConfig),
		exporterhelper.WithStart(inner.Start),
		exporterhelper.WithShutdown(inner.Shutdown),
	)
}

func innerConfig(config *Config) *upstream.Config {
	inner := config.Config
	inner.RetryConfig.Enabled = false
	inner.QueueConfig = configoptional.None[exporterhelper.QueueBatchConfig]()
	return &inner
}

func innerSettings(settings exporter.Settings) exporter.Settings {
	// Only the outer helper reports exporter metrics, avoiding duplicate series.
	settings.MeterProvider = noop.NewMeterProvider()
	settings.TracerProvider = tracenoop.NewTracerProvider()
	return settings
}

// The upstream exporter maps every HTTP status it does not name to gRPC
// `Unknown` and keeps the status code only in this message, which
// TestClassifyErrorReadsTheUpstreamMessage holds to the pinned release.
var serverErrorStatus = regexp.MustCompile(`responded with HTTP Status Code (5[0-9][0-9])\b`)

// classifyError reclassifies the upstream exporter's permanent errors that
// this wrapper retries: authentication failures (401, 403) after a long
// throttle, and HTTP 5xx server errors through the ordinary retry backoff.
// Every other error is returned unchanged.
func classifyError(err error, authRetry AuthRetryConfig, serverRetry ServerErrorRetryConfig) error {
	if err == nil {
		return err
	}
	grpcStatus, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch grpcStatus.Code() {
	case codes.Unauthenticated, codes.PermissionDenied:
		if authRetry.Enabled {
			return exporterhelper.NewThrottleRetry(grpcStatus.Err(), authRetry.Interval)
		}
	case codes.Unknown:
		if serverRetry.Enabled && serverErrorStatus.MatchString(grpcStatus.Message()) {
			return grpcStatus.Err()
		}
	}
	return err
}
