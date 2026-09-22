package observability

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestMetricsOrNoop_Nil verifies that MetricsOrNoop never hands back nil, and that
// the value it returns is actually usable (calling it does not panic).
func TestMetricsOrNoop_Nil(t *testing.T) {
	m := MetricsOrNoop(nil)
	if m == nil {
		t.Fatal("MetricsOrNoop(nil) = nil, want a usable Metrics")
	}
	m.RenderDuration(context.Background(), "home", time.Second, true)
	m.FragmentDuration(context.Background(), "home", "hero", time.Millisecond, nil)
	m.CacheEvent(context.Background(), CacheHit, "key")
	m.HTTPResponse(context.Background(), 200, "/", time.Second)
	m.Invalidation(context.Background(), []string{"a"}, 1)
}

// TestMetricsOrNoop_NonNil verifies that a non-nil Metrics is passed through
// unchanged rather than being replaced with the no-op.
func TestMetricsOrNoop_NonNil(t *testing.T) {
	rm := NewRecordingMetrics()
	got := MetricsOrNoop(rm)
	if got != Metrics(rm) {
		t.Fatal("MetricsOrNoop(non-nil) did not return the given Metrics unchanged")
	}
}

// TestTracerOrNoop_Nil verifies that TracerOrNoop never hands back nil, and that a
// span started from the returned Tracer is itself usable.
func TestTracerOrNoop_Nil(t *testing.T) {
	tr := TracerOrNoop(nil)
	if tr == nil {
		t.Fatal("TracerOrNoop(nil) = nil, want a usable Tracer")
	}
	ctx, span := tr.StartSpan(context.Background(), "op")
	if ctx == nil {
		t.Error("StartSpan returned a nil context")
	}
	if span == nil {
		t.Fatal("StartSpan returned a nil Span")
	}
	span.SetAttribute("k", "v")
	span.RecordError(errors.New("boom"))
	span.End()
}

// TestTracerOrNoop_NonNil verifies that a non-nil Tracer is passed through
// unchanged rather than being replaced with the no-op.
type stubTracer struct{}

func (stubTracer) StartSpan(ctx context.Context, name string) (context.Context, Span) {
	return ctx, NoopSpan{}
}

func TestTracerOrNoop_NonNil(t *testing.T) {
	var st stubTracer
	got := TracerOrNoop(st)
	if got != Tracer(st) {
		t.Fatal("TracerOrNoop(non-nil) did not return the given Tracer unchanged")
	}
}

// TestNoopTracer_StartSpan_PreservesContext verifies that NoopTracer.StartSpan
// returns the same context it was given, not a fresh, empty one — a caller reading
// values it previously stored in ctx via other means must still find them.
func TestNoopTracer_StartSpan_PreservesContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "value")

	got, span := NoopTracer{}.StartSpan(ctx, "op")

	if got.Value(key{}) != "value" {
		t.Error("NoopTracer.StartSpan did not preserve the input context's values")
	}
	if _, ok := span.(NoopSpan); !ok {
		t.Errorf("NoopTracer.StartSpan returned a %T, want NoopSpan", span)
	}
}

// TestRecordingMetrics_RecordsAccurately verifies that every Metrics method call is
// captured by Snapshot with the exact arguments it was called with, in call order.
func TestRecordingMetrics_RecordsAccurately(t *testing.T) {
	rm := NewRecordingMetrics()
	ctx := context.Background()
	fragErr := errors.New("fetch failed")

	rm.RenderDuration(ctx, "home", 10*time.Millisecond, true)
	rm.RenderDuration(ctx, "about", 20*time.Millisecond, false)
	rm.FragmentDuration(ctx, "home", "hero", 5*time.Millisecond, nil)
	rm.FragmentDuration(ctx, "home", "footer", 3*time.Millisecond, fragErr)
	rm.CacheEvent(ctx, CacheHit, "key-1")
	rm.CacheEvent(ctx, CacheMiss, "key-2")
	rm.HTTPResponse(ctx, 200, "/home", 15*time.Millisecond)
	rm.Invalidation(ctx, []string{"tag-a", "tag-b"}, 4)

	snap := rm.Snapshot()

	wantRenders := []RenderDurationCall{
		{Page: "home", Duration: 10 * time.Millisecond, CacheHit: true},
		{Page: "about", Duration: 20 * time.Millisecond, CacheHit: false},
	}
	if len(snap.RenderDurations) != len(wantRenders) {
		t.Fatalf("RenderDurations = %d entries, want %d", len(snap.RenderDurations), len(wantRenders))
	}
	for i, want := range wantRenders {
		if snap.RenderDurations[i] != want {
			t.Errorf("RenderDurations[%d] = %+v, want %+v", i, snap.RenderDurations[i], want)
		}
	}

	if len(snap.FragmentDurations) != 2 {
		t.Fatalf("FragmentDurations = %d entries, want 2", len(snap.FragmentDurations))
	}
	if snap.FragmentDurations[0].Fragment != "hero" || snap.FragmentDurations[0].Err != nil {
		t.Errorf("FragmentDurations[0] = %+v, want fragment=hero err=nil", snap.FragmentDurations[0])
	}
	if snap.FragmentDurations[1].Fragment != "footer" || !errors.Is(snap.FragmentDurations[1].Err, fragErr) {
		t.Errorf("FragmentDurations[1] = %+v, want fragment=footer err=%v", snap.FragmentDurations[1], fragErr)
	}

	wantCacheEvents := []CacheEventCall{
		{Event: CacheHit, Key: "key-1"},
		{Event: CacheMiss, Key: "key-2"},
	}
	for i, want := range wantCacheEvents {
		if snap.CacheEvents[i] != want {
			t.Errorf("CacheEvents[%d] = %+v, want %+v", i, snap.CacheEvents[i], want)
		}
	}

	if len(snap.HTTPResponses) != 1 || snap.HTTPResponses[0].Status != 200 || snap.HTTPResponses[0].Path != "/home" {
		t.Errorf("HTTPResponses = %+v, want one entry status=200 path=/home", snap.HTTPResponses)
	}

	if len(snap.Invalidations) != 1 {
		t.Fatalf("Invalidations = %d entries, want 1", len(snap.Invalidations))
	}
	if snap.Invalidations[0].Keys != 4 {
		t.Errorf("Invalidations[0].Keys = %d, want 4", snap.Invalidations[0].Keys)
	}
	wantTags := []string{"tag-a", "tag-b"}
	if len(snap.Invalidations[0].Tags) != len(wantTags) {
		t.Fatalf("Invalidations[0].Tags = %v, want %v", snap.Invalidations[0].Tags, wantTags)
	}
	for i, tag := range wantTags {
		if snap.Invalidations[0].Tags[i] != tag {
			t.Errorf("Invalidations[0].Tags[%d] = %q, want %q", i, snap.Invalidations[0].Tags[i], tag)
		}
	}
}

// TestRecordingMetrics_SnapshotIsIndependentCopy verifies that Snapshot returns a
// deep copy: mutating the slices inside a returned snapshot, or the slice the
// caller passed to Invalidation, must never affect the RecordingMetrics' recorded
// state or a previously taken snapshot.
func TestRecordingMetrics_SnapshotIsIndependentCopy(t *testing.T) {
	rm := NewRecordingMetrics()
	ctx := context.Background()

	tags := []string{"tag-a"}
	rm.Invalidation(ctx, tags, 1)
	tags[0] = "mutated-after-call" // must not affect what was recorded

	first := rm.Snapshot()
	if first.Invalidations[0].Tags[0] != "tag-a" {
		t.Fatalf("recorded tag = %q, want %q (mutating caller's slice after the call must not leak in)", first.Invalidations[0].Tags[0], "tag-a")
	}

	first.RenderDurations = append(first.RenderDurations, RenderDurationCall{Page: "injected"})
	first.Invalidations[0].Tags[0] = "mutated-after-snapshot"

	rm.RenderDuration(ctx, "home", time.Second, true)
	second := rm.Snapshot()

	if len(second.RenderDurations) != 1 {
		t.Fatalf("second snapshot RenderDurations = %d entries, want 1 (mutating first snapshot leaked into live state)", len(second.RenderDurations))
	}
	if second.Invalidations[0].Tags[0] != "tag-a" {
		t.Fatalf("second snapshot tag = %q, want %q (mutating first snapshot's tag leaked into live state)", second.Invalidations[0].Tags[0], "tag-a")
	}
}

// TestRecordingMetrics_ConcurrentUse exercises RecordingMetrics from many
// goroutines at once, covering every Metrics method, and must be run with -race.
// It asserts real behaviour: every call landed in the snapshot exactly once, none
// were lost or duplicated by a data race.
func TestRecordingMetrics_ConcurrentUse(t *testing.T) {
	rm := NewRecordingMetrics()
	ctx := context.Background()
	const n = 200

	var wg sync.WaitGroup
	wg.Add(n * 5)
	for i := 0; i < n; i++ {
		go func() { defer wg.Done(); rm.RenderDuration(ctx, "page", time.Millisecond, false) }()
		go func() { defer wg.Done(); rm.FragmentDuration(ctx, "page", "frag", time.Millisecond, nil) }()
		go func() { defer wg.Done(); rm.CacheEvent(ctx, CacheSet, "key") }()
		go func() { defer wg.Done(); rm.HTTPResponse(ctx, 200, "/", time.Millisecond) }()
		go func() { defer wg.Done(); rm.Invalidation(ctx, []string{"tag"}, 1) }()
	}
	wg.Wait()

	snap := rm.Snapshot()
	if len(snap.RenderDurations) != n {
		t.Errorf("RenderDurations = %d, want %d", len(snap.RenderDurations), n)
	}
	if len(snap.FragmentDurations) != n {
		t.Errorf("FragmentDurations = %d, want %d", len(snap.FragmentDurations), n)
	}
	if len(snap.CacheEvents) != n {
		t.Errorf("CacheEvents = %d, want %d", len(snap.CacheEvents), n)
	}
	if len(snap.HTTPResponses) != n {
		t.Errorf("HTTPResponses = %d, want %d", len(snap.HTTPResponses), n)
	}
	if len(snap.Invalidations) != n {
		t.Errorf("Invalidations = %d, want %d", len(snap.Invalidations), n)
	}
}

// TestCacheEvent_Constants verifies the string values of the CacheEvent constants,
// since CacheEvent is a string-backed type an adapter (e.g. to Prometheus label
// values) may serialize directly.
func TestCacheEvent_Constants(t *testing.T) {
	tests := []struct {
		event CacheEvent
		want  string
	}{
		{CacheHit, "hit"},
		{CacheMiss, "miss"},
		{CacheSet, "set"},
		{CacheEvict, "evict"},
		{CacheInvalidate, "invalidate"},
	}
	for _, tt := range tests {
		if string(tt.event) != tt.want {
			t.Errorf("CacheEvent = %q, want %q", string(tt.event), tt.want)
		}
	}
}

// Compile-time assertions that every implementation satisfies its interface.
var (
	_ Metrics = NoopMetrics{}
	_ Tracer  = NoopTracer{}
	_ Span    = NoopSpan{}
	_ Metrics = (*RecordingMetrics)(nil)
)
