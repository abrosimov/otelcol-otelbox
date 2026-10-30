package spandedup

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
)

var componentType = component.MustNewType("span_dedup")

const (
	defaultTTL        = 72 * time.Hour
	defaultMaxEntries = 500_000
)

func NewFactory() processor.Factory {
	return processor.NewFactory(
		componentType,
		createDefaultConfig,
		processor.WithTraces(createTraces, component.StabilityLevelDevelopment),
	)
}

func createDefaultConfig() component.Config {
	return &Config{TTL: defaultTTL, MaxEntries: defaultMaxEntries}
}

func createTraces(_ context.Context, settings processor.Settings, config component.Config, next consumer.Traces) (processor.Traces, error) {
	return newSpanDedup(settings, config.(*Config), next)
}
