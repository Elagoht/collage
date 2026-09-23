// Package observability defines the framework's metrics and tracing interfaces. It
// is a leaf package: it imports nothing from this module, so any package may depend
// on it without risking an import cycle. Implementations are supplied by the
// embedding application (e.g. an adapter over Prometheus or OpenTelemetry); this
// package defines only the narrow shape the framework calls through.
package observability

import (
	"context"
	"sync"
	"time"
)

// Metrics receives framework counters and timings. Implementations must be safe for
// concurrent use and must not block: the framework calls these methods on the
// request path, and a slow or blocking implementation would slow down every render.
type Metrics interface {
	// RenderDuration reports how long a full page render took, whether it was
	// served from cache, and which page it rendered.
	RenderDuration(ctx context.Context, page string, d time.Duration, cacheHit bool)
	// FragmentDuration reports how long a single fragment's data fetch took,
	// including any error it produced. A nil err means the fragment succeeded.
	FragmentDuration(ctx context.Context, page, fragment string, d time.Duration, err error)
	// CacheEvent reports a single cache operation and the key it acted on.
	CacheEvent(ctx context.Context, event CacheEvent, key string)
	// HTTPResponse reports a completed HTTP response: its status code, the request
	// path, and how long it took to produce.
	HTTPResponse(ctx context.Context, status int, path string, d time.Duration)
	// Invalidation reports a cache invalidation: the tags it was requested for and
	// how many cache keys it reached. keys counts the keys the caller resolved from
	// tags and issued a removal for. It is deliberately not "entries that were
	// live": Cache.InvalidateKey succeeds on a key holding nothing and reports no
	// distinction, so a key whose entry had already expired is counted like any
	// other, and keys is an upper bound on live entries removed.
	Invalidation(ctx context.Context, tags []string, keys int)
}

// CacheEvent identifies a single kind of cache operation reported to Metrics.
type CacheEvent string

const (
	// CacheHit means a lookup found a live entry.
	CacheHit CacheEvent = "hit"
	// CacheMiss means a lookup found no live entry.
	CacheMiss CacheEvent = "miss"
	// CacheSet means an entry was written.
	CacheSet CacheEvent = "set"
	// CacheEvict means an entry was removed because it expired or was displaced,
	// as opposed to an explicit invalidation.
	CacheEvict CacheEvent = "evict"
	// CacheInvalidate means an entry was removed by an explicit invalidation.
	CacheInvalidate CacheEvent = "invalidate"
	// CacheCoalesced means a lookup found no entry, but a render of the same key
	// was already running and this request was served by it rather than starting
	// one of its own. It is not a hit — nothing was cached when the request asked
	// — and it is not a miss that cost a render. A count that climbs is a page
	// expiring faster than it can be re-made, which is what a too-short TTL looks
	// like from the outside.
	CacheCoalesced CacheEvent = "coalesced"
)

// Timing records where a render spent its time. The render engine fills one of
// these per render and reports it through Metrics.RenderDuration.
type Timing struct {
	// Total is the full wall-clock duration of the render.
	Total time.Duration
	// Data is the time spent in fragment DataHandlers.
	Data time.Duration
	// Template is the time spent executing templates.
	Template time.Duration
	// CacheHit reports whether the render was served from cache.
	CacheHit bool
	// Fragments is the number of fragments rendered.
	Fragments int
}

// RenderDurationCall records one call to Metrics.RenderDuration.
type RenderDurationCall struct {
	// Page is the page argument the call was made with.
	Page string
	// Duration is the d argument the call was made with.
	Duration time.Duration
	// CacheHit is the cacheHit argument the call was made with.
	CacheHit bool
}

// FragmentDurationCall records one call to Metrics.FragmentDuration.
type FragmentDurationCall struct {
	// Page is the page argument the call was made with.
	Page string
	// Fragment is the fragment argument the call was made with.
	Fragment string
	// Duration is the d argument the call was made with.
	Duration time.Duration
	// Err is the err argument the call was made with.
	Err error
}

// CacheEventCall records one call to Metrics.CacheEvent.
type CacheEventCall struct {
	// Event is the event argument the call was made with.
	Event CacheEvent
	// Key is the key argument the call was made with.
	Key string
}

// HTTPResponseCall records one call to Metrics.HTTPResponse.
type HTTPResponseCall struct {
	// Status is the status argument the call was made with.
	Status int
	// Path is the path argument the call was made with.
	Path string
	// Duration is the d argument the call was made with.
	Duration time.Duration
}

// InvalidationCall records one call to Metrics.Invalidation.
type InvalidationCall struct {
	// Tags is the tags argument the call was made with.
	Tags []string
	// Keys is the keys argument the call was made with.
	Keys int
}

// MetricsSnapshot is a point-in-time, independent copy of everything a
// RecordingMetrics has recorded. It shares no memory with the RecordingMetrics it
// was taken from, so a caller may read it freely even while other goroutines
// continue recording.
type MetricsSnapshot struct {
	// RenderDurations holds every RenderDuration call, in call order.
	RenderDurations []RenderDurationCall
	// FragmentDurations holds every FragmentDuration call, in call order.
	FragmentDurations []FragmentDurationCall
	// CacheEvents holds every CacheEvent call, in call order.
	CacheEvents []CacheEventCall
	// HTTPResponses holds every HTTPResponse call, in call order.
	HTTPResponses []HTTPResponseCall
	// Invalidations holds every Invalidation call, in call order.
	Invalidations []InvalidationCall
}

// RecordingMetrics is a Metrics implementation that records every call it receives,
// for tests to assert against. It is safe for concurrent use: every method is
// guarded by a mutex, so it may be exercised from many goroutines at once, such as
// concurrent requests in an HTTP handler test. Its fields are unexported
// deliberately, so a caller cannot read live state out from under a concurrent
// writer — use Snapshot instead, which returns a deep copy that is safe to read
// however long the caller holds onto it.
type RecordingMetrics struct {
	mu                sync.Mutex
	renderDurations   []RenderDurationCall
	fragmentDurations []FragmentDurationCall
	cacheEvents       []CacheEventCall
	httpResponses     []HTTPResponseCall
	invalidations     []InvalidationCall
}

// NewRecordingMetrics returns a ready-to-use RecordingMetrics with nothing
// recorded yet.
func NewRecordingMetrics() *RecordingMetrics {
	return &RecordingMetrics{}
}

// RenderDuration records the call.
func (m *RecordingMetrics) RenderDuration(_ context.Context, page string, d time.Duration, cacheHit bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renderDurations = append(m.renderDurations, RenderDurationCall{Page: page, Duration: d, CacheHit: cacheHit})
}

// FragmentDuration records the call.
func (m *RecordingMetrics) FragmentDuration(_ context.Context, page, fragment string, d time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fragmentDurations = append(m.fragmentDurations, FragmentDurationCall{Page: page, Fragment: fragment, Duration: d, Err: err})
}

// CacheEvent records the call.
func (m *RecordingMetrics) CacheEvent(_ context.Context, event CacheEvent, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cacheEvents = append(m.cacheEvents, CacheEventCall{Event: event, Key: key})
}

// HTTPResponse records the call.
func (m *RecordingMetrics) HTTPResponse(_ context.Context, status int, path string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpResponses = append(m.httpResponses, HTTPResponseCall{Status: status, Path: path, Duration: d})
}

// Invalidation records the call. Tags is copied so a caller mutating its slice
// after the call cannot affect what was recorded.
func (m *RecordingMetrics) Invalidation(_ context.Context, tags []string, keys int) {
	copied := make([]string, len(tags))
	copy(copied, tags)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidations = append(m.invalidations, InvalidationCall{Tags: copied, Keys: keys})
}

// Snapshot returns a deep copy of everything recorded so far. The returned
// MetricsSnapshot shares no backing arrays with m, so it remains valid and stable
// even if other goroutines go on to record further calls against m.
func (m *RecordingMetrics) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot := MetricsSnapshot{
		RenderDurations:   make([]RenderDurationCall, len(m.renderDurations)),
		FragmentDurations: make([]FragmentDurationCall, len(m.fragmentDurations)),
		CacheEvents:       make([]CacheEventCall, len(m.cacheEvents)),
		HTTPResponses:     make([]HTTPResponseCall, len(m.httpResponses)),
		Invalidations:     make([]InvalidationCall, len(m.invalidations)),
	}

	copy(snapshot.RenderDurations, m.renderDurations)
	copy(snapshot.FragmentDurations, m.fragmentDurations)
	copy(snapshot.CacheEvents, m.cacheEvents)
	copy(snapshot.HTTPResponses, m.httpResponses)

	for i, inv := range m.invalidations {
		tags := make([]string, len(inv.Tags))
		copy(tags, inv.Tags)
		snapshot.Invalidations[i] = InvalidationCall{Tags: tags, Keys: inv.Keys}
	}

	return snapshot
}

var _ Metrics = (*RecordingMetrics)(nil)
