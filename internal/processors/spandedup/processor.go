package spandedup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
)

const scopeName = "github.com/abrosimov/otelcol-otelbox/processor/spandedup"

type spanDedup struct {
	config     *Config
	settings   processor.Settings
	next       consumer.Traces
	duplicates metric.Int64Counter
	attributes metric.MeasurementOption
	now        func() time.Time

	mu     sync.Mutex
	client storage.Client
	seen   *seenSet
}

func newSpanDedup(settings processor.Settings, config *Config, next consumer.Traces) (*spanDedup, error) {
	duplicates, err := settings.MeterProvider.Meter(scopeName).Int64Counter(
		"otelcol_processor_span_dedup_duplicate_spans",
		metric.WithDescription("Spans dropped because the next consumer had already accepted a span with the same trace and span ID."),
		metric.WithUnit("{span}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating the duplicate span counter: %w", err)
	}
	return &spanDedup{
		config:     config,
		settings:   settings,
		next:       next,
		duplicates: duplicates,
		attributes: metric.WithAttributeSet(attribute.NewSet(attribute.String("processor", settings.ID.String()))),
		now:        time.Now,
	}, nil
}

func (p *spanDedup) Start(ctx context.Context, host component.Host) error {
	extension, ok := host.GetExtensions()[p.config.Storage]
	if !ok {
		return fmt.Errorf("storage extension %s is not configured", p.config.Storage)
	}
	storageExtension, ok := extension.(storage.Extension)
	if !ok {
		return fmt.Errorf("extension %s is not a storage extension", p.config.Storage)
	}
	client, err := storageExtension.GetClient(ctx, component.KindProcessor, p.settings.ID, "")
	if err != nil {
		return fmt.Errorf("opening storage %s: %w", p.config.Storage, err)
	}
	seen, err := loadSeenSet(ctx, client, p.config.TTL, p.config.MaxEntries, p.settings.Logger)
	if err != nil {
		return errors.Join(err, client.Close(ctx))
	}
	p.mu.Lock()
	p.client = client
	p.seen = seen
	p.mu.Unlock()
	p.settings.Logger.Info("Loaded the span deduplication index", zap.Int("spans", len(seen.index)))
	return nil
}

func (p *spanDedup) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		return nil
	}
	if err := p.client.Close(ctx); err != nil {
		return fmt.Errorf("closing storage %s: %w", p.config.Storage, err)
	}
	return nil
}

func (p *spanDedup) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

// A span is recorded only after the next consumer accepted it, so a failed
// attempt is never remembered and its retry passes. Concurrent requests that
// carry the same span may both pass; that is a duplicate, never a loss.
func (p *spanDedup) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	fresh, duplicates := p.removeSeen(traces)
	if duplicates > 0 {
		p.duplicates.Add(ctx, int64(duplicates), p.attributes)
	}
	if traces.SpanCount() == 0 {
		return nil
	}
	if err := p.next.ConsumeTraces(ctx, traces); err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.seen.record(ctx, fresh, p.now()); err != nil {
		p.settings.Logger.Error("Could not record accepted spans; the request fails so that its producer retries",
			zap.Int("spans", len(fresh)), zap.Error(err))
		return err
	}
	return nil
}

func (p *spanDedup) removeSeen(traces ptrace.Traces) ([]spanKey, int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var fresh []spanKey
	inRequest := make(map[spanKey]struct{})
	duplicates := 0
	traces.ResourceSpans().RemoveIf(func(resourceSpans ptrace.ResourceSpans) bool {
		resourceSpans.ScopeSpans().RemoveIf(func(scopeSpans ptrace.ScopeSpans) bool {
			scopeSpans.Spans().RemoveIf(func(span ptrace.Span) bool {
				key, ok := keyOf(span)
				if !ok {
					return false
				}
				if _, repeated := inRequest[key]; repeated || p.seen.contains(key) {
					duplicates++
					return true
				}
				inRequest[key] = struct{}{}
				fresh = append(fresh, key)
				return false
			})
			return scopeSpans.Spans().Len() == 0
		})
		return resourceSpans.ScopeSpans().Len() == 0
	})
	return fresh, duplicates
}
