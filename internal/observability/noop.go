package observability

import (
	"context"
	"time"
)

// NoopMetrics is a Metrics implementation whose methods do nothing. It has a value
// receiver on every method and holds no state, so NoopMetrics{} costs nothing to
// construct or call — use it, not &NoopMetrics{}, on any hot path.
type NoopMetrics struct{}

// RenderDuration does nothing.
func (NoopMetrics) RenderDuration(ctx context.Context, page string, d time.Duration, cacheHit bool) {
}

// FragmentDuration does nothing.
func (NoopMetrics) FragmentDuration(ctx context.Context, page, fragment string, d time.Duration, err error) {
}

// CacheEvent does nothing.
func (NoopMetrics) CacheEvent(ctx context.Context, event CacheEvent, key string) {}

// HTTPResponse does nothing.
func (NoopMetrics) HTTPResponse(ctx context.Context, status int, path string, d time.Duration) {}

// Invalidation does nothing.
func (NoopMetrics) Invalidation(ctx context.Context, tags []string, keys int) {}

var _ Metrics = NoopMetrics{}

// NoopTracer is a Tracer implementation that starts spans which do nothing. It has
// a value receiver and holds no state, so NoopTracer{} costs nothing to construct or
// call.
type NoopTracer struct{}

// StartSpan returns ctx unchanged and a NoopSpan.
func (NoopTracer) StartSpan(ctx context.Context, name string) (context.Context, Span) {
	return ctx, NoopSpan{}
}

var _ Tracer = NoopTracer{}

// NoopSpan is a Span implementation whose methods do nothing. It has a value
// receiver on every method and holds no state, so NoopSpan{} costs nothing to
// construct or call.
type NoopSpan struct{}

// SetAttribute does nothing.
func (NoopSpan) SetAttribute(key, value string) {}

// RecordError does nothing.
func (NoopSpan) RecordError(err error) {}

// End does nothing.
func (NoopSpan) End() {}

var _ Span = NoopSpan{}

// MetricsOrNoop returns m if it is non-nil, and NoopMetrics{} otherwise. Callers
// use it to obtain a Metrics that is always safe to call without a nil check, such
// as when a Config's Observability.Metrics field was left nil to mean "no-op".
func MetricsOrNoop(m Metrics) Metrics {
	if m == nil {
		return NoopMetrics{}
	}
	return m
}

// TracerOrNoop returns t if it is non-nil, and NoopTracer{} otherwise. Callers use
// it to obtain a Tracer that is always safe to call without a nil check, such as
// when a Config's Observability.Tracer field was left nil to mean "no-op".
func TracerOrNoop(t Tracer) Tracer {
	if t == nil {
		return NoopTracer{}
	}
	return t
}
