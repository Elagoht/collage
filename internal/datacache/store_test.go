package datacache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

// str is a string as the store holds it.
type str string

func (s str) Encode() ([]byte, error) { return json.Marshal(string(s)) }

func decodeStr(data []byte) (types.CacheValue, error) {
	var v string
	err := json.Unmarshal(data, &v)
	return str(v), err
}

func counting(calls *atomic.Int32, value string) func(context.Context) (types.CacheValue, error) {
	return func(context.Context) (types.CacheValue, error) {
		calls.Add(1)
		return str(value), nil
	}
}

func failing(err error) func(context.Context) (types.CacheValue, error) {
	return func(context.Context) (types.CacheValue, error) { return nil, err }
}

// clock is a test's time, safe to read from a refresh's goroutine while the
// test moves it.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock(s *Store) *clock {
	c := &clock{at: time.Now()}
	s.now = c.now
	return c
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func TestStore_LoadsOnceAndServesAfter(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	for range 5 {
		v, _, err := s.Load(context.Background(), "author:A", 0, nil, counting(&calls, "A"), decodeStr)
		if err != nil || v != str("A") {
			t.Fatalf("Load = %v, %v", v, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", calls.Load())
	}
}

// Concurrent loads of one key share one fetch.
func TestStore_ConcurrentLoadsShareOneFetch(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	release := make(chan struct{})
	fetch := func(context.Context) (types.CacheValue, error) {
		calls.Add(1)
		<-release
		return str("A"), nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if v, _, err := s.Load(context.Background(), "k", 0, nil, fetch, decodeStr); err != nil || v != str("A") {
				t.Errorf("Load = %v, %v", v, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", calls.Load())
	}
}

func TestStore_Expires(t *testing.T) {
	s := New(Options{})
	clk := newClock(s)
	var calls atomic.Int32
	s.Load(context.Background(), "k", time.Minute, nil, counting(&calls, "v"), decodeStr)
	clk.advance(59 * time.Second)
	s.Load(context.Background(), "k", time.Minute, nil, counting(&calls, "v"), decodeStr)
	clk.advance(2 * time.Second)
	s.Load(context.Background(), "k", time.Minute, nil, counting(&calls, "v"), decodeStr)
	s.refreshes.Wait()
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2: one, then one after the minute", calls.Load())
	}
}

func TestStore_InvalidateByTag(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(context.Background(), "author:A", 0, []string{"author:A"}, counting(&calls, "A"), decodeStr)
	s.Load(context.Background(), "author:B", 0, []string{"author:B"}, counting(&calls, "B"), decodeStr)

	if marked := s.Invalidate([]string{"author:A"}); marked != 1 {
		t.Errorf("marked = %d, want 1", marked)
	}
	s.Load(context.Background(), "author:A", 0, []string{"author:A"}, counting(&calls, "A"), decodeStr)
	s.Load(context.Background(), "author:B", 0, []string{"author:B"}, counting(&calls, "B"), decodeStr)
	s.refreshes.Wait()
	if calls.Load() != 3 {
		t.Errorf("fetches = %d, want 3: A again, B still stored", calls.Load())
	}
}

func TestStore_ErrorsAreNotStored(t *testing.T) {
	s := New(Options{})
	failure := errors.New("upstream down")
	if _, _, err := s.Load(context.Background(), "k", 0, nil, failing(failure), decodeStr); !errors.Is(err, failure) {
		t.Fatalf("Load error = %v", err)
	}
	var calls atomic.Int32
	s.Load(context.Background(), "k", 0, nil, counting(&calls, "v"), decodeStr)
	if calls.Load() != 1 {
		t.Error("a failed fetch was stored")
	}
}

func TestStore_EvictsTheLeastRecentlyUsed(t *testing.T) {
	s := New(Options{MaxEntries: 2})
	var calls atomic.Int32
	s.Load(context.Background(), "a", 0, nil, counting(&calls, "a"), decodeStr)
	s.Load(context.Background(), "b", 0, nil, counting(&calls, "b"), decodeStr)
	s.Load(context.Background(), "a", 0, nil, counting(&calls, "a"), decodeStr) // a is now the most recent
	s.Load(context.Background(), "c", 0, nil, counting(&calls, "c"), decodeStr) // evicts b
	s.Load(context.Background(), "a", 0, nil, counting(&calls, "a"), decodeStr)
	s.Load(context.Background(), "b", 0, nil, counting(&calls, "b"), decodeStr)
	if calls.Load() != 4 {
		t.Errorf("fetches = %d, want 4: a, b, c, then b again", calls.Load())
	}
}

// A fetch that panics must not leave its key behind as a fetch forever running:
// every later load of the key would wait on it, and none would fetch again.
func TestStore_APanickingFetchDoesNotHoldTheKey(t *testing.T) {
	s := New(Options{})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not reach the caller whose fetch it was")
			}
		}()
		_, _, _ = s.Load(context.Background(), "k", 0, nil, func(context.Context) (types.CacheValue, error) { panic("upstream") }, decodeStr)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, _, err := s.Load(ctx, "k", 0, nil, func(context.Context) (types.CacheValue, error) { return str("ok"), nil }, decodeStr)
	if err != nil || got != str("ok") {
		t.Fatalf("Load() after a panic = %v, %v; want a fresh fetch", got, err)
	}
}

// A waiter on a panicking fetch is told it failed, not left waiting.
func TestStore_AWaiterOnAPanickingFetchIsAnswered(t *testing.T) {
	s := New(Options{})
	release := make(chan struct{})
	go func() {
		defer func() { _ = recover() }()
		_, _, _ = s.Load(context.Background(), "k", 0, nil, func(context.Context) (types.CacheValue, error) {
			<-release
			panic("upstream")
		}, decodeStr)
	}()
	waitInflight(t, s, "k")

	done := make(chan error, 1)
	go func() {
		_, _, err := s.Load(context.Background(), "k", 0, nil, func(context.Context) (types.CacheValue, error) { return str("second"), nil }, decodeStr)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // the waiter joins the running fetch
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrFetchPanicked) {
			t.Fatalf("waiter's Load() = %v, want ErrFetchPanicked", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the waiter was never answered")
	}
}

// The caller that started a fetch going away is its own business. A waiter whose
// context is still live fetches again rather than failing with an error about a
// request that was not its own — or one reader disconnecting on purpose fails
// every reader who shared the fetch.
func TestStore_AWaiterOutlivesTheCallerThatStartedTheFetch(t *testing.T) {
	s := New(Options{})
	first, cancel := context.WithCancel(context.Background())
	go func() {
		_, _, _ = s.Load(first, "k", 0, nil, func(ctx context.Context) (types.CacheValue, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, decodeStr)
	}()
	waitInflight(t, s, "k")

	done := make(chan struct{})
	var got string
	var err error
	go func() {
		defer close(done)
		var value types.CacheValue
		value, _, err = s.Load(context.Background(), "k", 0, nil, func(context.Context) (types.CacheValue, error) { return str("mine"), nil }, decodeStr)
		if value != nil {
			got = string(value.(str))
		}
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if err != nil || got != "mine" {
		t.Fatalf("waiter's Load() = %v, %v; want its own fetch", got, err)
	}
}

func waitInflight(t *testing.T, s *Store, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, ok := s.inflight[key]
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no fetch of %q started", key)
}

var bg = context.Background()

// An invalidated value is served, with a nil error, while the fetch that
// should replace it fails — and stays served after it failed.
func TestStore_AnInvalidatedValueIsServedWhenTheRefreshFails(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})

	down := failing(errors.New("upstream down"))
	v, stale, err := s.Load(bg, "k", 0, []string{"t"}, down, decodeStr)
	if err != nil || v != str("old") || !stale {
		t.Fatalf("Load = %v, %v, %v; want the old value, stale, no error", v, stale, err)
	}
	s.refreshes.Wait()
	v, stale, err = s.Load(bg, "k", 0, []string{"t"}, down, decodeStr)
	if err != nil || v != str("old") || !stale {
		t.Fatalf("after the failed refresh Load = %v, %v, %v; want the old value kept", v, stale, err)
	}
}

// Content gone upstream goes from the site: not found never falls back.
func TestStore_NotFoundDropsTheStaleValue(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})

	gone := failing(fmt.Errorf("post 7: %w", types.ErrNotFound))
	if v, stale, err := s.Load(bg, "k", 0, nil, gone, decodeStr); err != nil || v != str("old") || !stale {
		t.Fatalf("first Load = %v, %v, %v; want the stale value while the refresh runs", v, stale, err)
	}
	s.refreshes.Wait()
	if s.Len() != 0 {
		t.Fatalf("Len = %d after not found, want 0", s.Len())
	}
	if _, _, err := s.Load(bg, "k", 0, nil, gone, decodeStr); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Load after not found = %v, want ErrNotFound", err)
	}
}

// Past its ttl a value is stale, not gone.
func TestStore_AnExpiredValueIsServedWhenTheRefreshFails(t *testing.T) {
	s := New(Options{})
	clk := newClock(s)
	var calls atomic.Int32
	s.Load(bg, "k", time.Minute, nil, counting(&calls, "old"), decodeStr)
	clk.advance(2 * time.Minute)
	v, stale, err := s.Load(bg, "k", time.Minute, nil, failing(errors.New("down")), decodeStr)
	if err != nil || v != str("old") || !stale {
		t.Fatalf("Load = %v, %v, %v; want the expired value, stale", v, stale, err)
	}
}

// A stale hit does not wait for the upstream; the refresh lands for the next.
func TestStore_AStaleHitReturnsAtOnceAndTheRefreshLands(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})

	release := make(chan struct{})
	slow := func(context.Context) (types.CacheValue, error) { <-release; return str("new"), nil }
	got := make(chan types.CacheValue, 1)
	go func() {
		v, _, _ := s.Load(bg, "k", 0, []string{"t"}, slow, decodeStr)
		got <- v
	}()
	select {
	case v := <-got:
		if v != str("old") {
			t.Fatalf("stale hit = %v, want old", v)
		}
	case <-time.After(time.Second):
		t.Fatal("a stale hit waited for the upstream")
	}
	close(release)
	s.refreshes.Wait()
	if v, stale, err := s.Load(bg, "k", 0, nil, failing(errors.New("unused")), decodeStr); v != str("new") || stale || err != nil {
		t.Fatalf("after the refresh Load = %v, %v, %v; want new, fresh", v, stale, err)
	}
}

// A fetch that started before an invalidation is kept, but as stale.
func TestStore_AFetchOverlappingAnInvalidationIsStoredStale(t *testing.T) {
	s := New(Options{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Load(bg, "k", 0, []string{"t"}, func(context.Context) (types.CacheValue, error) {
			<-release
			return str("old"), nil
		}, decodeStr)
	}()
	waitInflight(t, s, "k")
	s.Invalidate([]string{"t"})
	close(release)
	<-done
	v, stale, _ := s.Load(bg, "k", 0, nil, failing(errors.New("down")), decodeStr)
	if v != str("old") || !stale {
		t.Fatalf("Load = %v, stale=%v; want old, stale", v, stale)
	}
}

// Many readers of one stale key cost one upstream call.
func TestStore_ConcurrentStaleHitsShareOneRefresh(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	release := make(chan struct{})
	fetch := func(context.Context) (types.CacheValue, error) { calls.Add(1); <-release; return str("new"), nil }
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if v, stale, err := s.Load(bg, "k", 0, []string{"t"}, fetch, decodeStr); v != str("old") || !stale || err != nil {
				t.Errorf("Load = %v, %v, %v", v, stale, err)
			}
		})
	}
	wg.Wait()
	close(release)
	s.refreshes.Wait()
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2: the first, then one refresh", calls.Load())
	}
}

// The request that found the value stale may be long gone; the refresh is not
// its to cancel.
func TestStore_ARefreshOutlivesTheRequestThatStartedIt(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	ctx, cancel := context.WithCancel(bg)
	s.Load(ctx, "k", 0, []string{"t"}, func(ctx context.Context) (types.CacheValue, error) {
		time.Sleep(20 * time.Millisecond)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return str("new"), nil
	}, decodeStr)
	cancel()
	s.refreshes.Wait()
	if v, stale, _ := s.Load(bg, "k", 0, nil, failing(errors.New("unused")), decodeStr); v != str("new") || stale {
		t.Fatalf("Load = %v, stale=%v; want the refresh's value", v, stale)
	}
}

// A refresh that panics runs on its own goroutine: it must not take the process
// down, and must not hold the key.
func TestStore_APanickingRefreshKeepsTheStaleValue(t *testing.T) {
	s := New(Options{})
	clk := newClock(s)
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	s.Load(bg, "k", 0, nil, func(context.Context) (types.CacheValue, error) { panic("upstream") }, decodeStr)
	s.refreshes.Wait()
	clk.advance(time.Second) // past the failed refresh's cooldown
	v, stale, err := s.Load(bg, "k", 0, nil, counting(&calls, "new"), decodeStr)
	if v != str("old") || !stale || err != nil {
		t.Fatalf("Load = %v, %v, %v; want old, stale", v, stale, err)
	}
	s.refreshes.Wait()
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2: the key was held after the panic", calls.Load())
	}
}

// Purge is the hard delete: gone, and fetched again.
func TestStore_PurgeRemoves(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
	if n := s.Purge([]string{"t"}); n != 1 || s.Len() != 0 {
		t.Fatalf("Purge = %d, Len = %d; want 1, 0", n, s.Len())
	}
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2", calls.Load())
	}
}

// A fetch that started before a purge stores nothing: the application asked for
// the value to be gone, not for an older copy of it.
func TestStore_AFetchOverlappingAPurgeIsNotStored(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	release := make(chan struct{})
	s.Load(bg, "k", 0, []string{"t"}, func(context.Context) (types.CacheValue, error) { <-release; return str("new"), nil }, decodeStr)
	s.Purge([]string{"t"})
	close(release)
	s.refreshes.Wait()
	if s.Len() != 0 {
		t.Errorf("Len = %d; a refresh resurrected a purged value", s.Len())
	}
}

// A static export never takes stale data: it waits for the upstream, and an
// upstream failure is its failure.
func TestStore_FreshDataIsNeverStale(t *testing.T) {
	s := New(Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	fresh := types.WithFreshData(bg)
	if _, stale, err := s.Load(fresh, "k", 0, nil, failing(errors.New("down")), decodeStr); err == nil || stale {
		t.Fatalf("fresh Load = stale %v, err %v; want the upstream's error", stale, err)
	}
	if v, stale, err := s.Load(fresh, "k", 0, nil, counting(&calls, "new"), decodeStr); v != str("new") || stale || err != nil {
		t.Fatalf("fresh Load = %v, %v, %v; want new", v, stale, err)
	}
}

// Every stale serve is counted.
func TestStore_StaleServesAreReported(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	s := New(Options{Metrics: metrics})
	var calls atomic.Int32
	s.Load(bg, "author\x00T", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	s.Load(bg, "author\x00T", 0, nil, failing(errors.New("down")), decodeStr)
	s.refreshes.Wait()
	stale := 0
	for _, event := range metrics.Snapshot().CacheEvents {
		if event.Event == observability.CacheStale {
			stale++
			if event.Key != "author" {
				t.Errorf("event key = %q, want the key's name", event.Key)
			}
		}
	}
	if stale != 1 {
		t.Errorf("CacheStale events = %d, want 1", stale)
	}
}

// A failed refresh is not retried by every request that serves the stale
// value: the key cools down, 1s after one failure and twice as long after each
// further one, up to a minute. A success ends it; fresh data ignores it.
func TestStore_AFailedRefreshCoolsDown(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	s := New(Options{Metrics: metrics})
	clk := newClock(s)
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})

	var attempts atomic.Int32
	down := func(context.Context) (types.CacheValue, error) {
		attempts.Add(1)
		return nil, errors.New("down")
	}
	load := func() {
		t.Helper()
		if v, stale, err := s.Load(bg, "k", 0, []string{"t"}, down, decodeStr); v != str("old") || !stale || err != nil {
			t.Fatalf("Load = %v, %v, %v; want the stale value", v, stale, err)
		}
		s.refreshes.Wait()
	}
	load()
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
	for i, delay := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60} {
		delay *= time.Second
		load()
		clk.advance(delay - time.Millisecond)
		load()
		if got := attempts.Load(); got != int32(i+1) {
			t.Fatalf("failure %d: attempts = %d within the %v cooldown, want %d", i+1, got, delay, i+1)
		}
		clk.advance(time.Millisecond)
		load()
		if got := attempts.Load(); got != int32(i+2) {
			t.Fatalf("failure %d: attempts = %d after the %v cooldown, want %d", i+1, got, delay, i+2)
		}
	}
	stale := 0
	for _, event := range metrics.Snapshot().CacheEvents {
		if event.Event == observability.CacheStale {
			stale++
		}
	}
	if want := 1 + 8*3; stale != want {
		t.Errorf("CacheStale events = %d, want %d: one per stale serve, cooling or not", stale, want)
	}

	// Fresh data waits on the upstream whatever the cooldown says.
	if _, _, err := s.Load(types.WithFreshData(bg), "k", 0, []string{"t"}, down, decodeStr); err == nil {
		t.Error("a fresh-data load in the cooldown did not reach the upstream")
	}
	if got := attempts.Load(); got != 10 {
		t.Fatalf("attempts = %d after a fresh-data load, want 10", got)
	}

	// A success replaces the entry, and with it the cooldown.
	clk.advance(time.Minute)
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "new"), decodeStr)
	s.refreshes.Wait()
	s.Invalidate([]string{"t"})
	if v, stale, _ := s.Load(bg, "k", 0, []string{"t"}, down, decodeStr); v != str("new") || !stale {
		t.Fatalf("Load = %v, stale=%v; want the refreshed value, invalidated", v, stale)
	}
	s.refreshes.Wait()
	if got := attempts.Load(); got != 11 {
		t.Errorf("attempts = %d after a success, want 11: the cooldown outlived it", got)
	}
}

// The logs say what happened: a failure while a value is still served says so,
// a failure for a value purged meanwhile does not claim to serve it, and the
// first success after failures says how many there were.
func TestStore_RefreshLogs(t *testing.T) {
	var logs strings.Builder
	var logsMu sync.Mutex
	s := New(Options{Logger: slog.New(slog.NewTextHandler(lockedWriter{&logsMu, &logs}, &slog.HandlerOptions{Level: slog.LevelInfo}))})
	clk := newClock(s)
	read := func() string {
		logsMu.Lock()
		defer logsMu.Unlock()
		out := logs.String()
		logs.Reset()
		return out
	}
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	for range 2 {
		s.Load(bg, "k", 0, nil, failing(errors.New("down")), decodeStr)
		s.refreshes.Wait()
		clk.advance(time.Minute)
	}
	if out := read(); strings.Count(out, "serving stale") != 2 {
		t.Errorf("want two failure warnings, got:\n%s", out)
	}
	s.Load(bg, "k", 0, nil, counting(&calls, "new"), decodeStr)
	s.refreshes.Wait()
	if out := read(); !strings.Contains(out, "refreshed after failures") || !strings.Contains(out, "failures=2") {
		t.Errorf("want one recovery line with failures=2, got:\n%s", out)
	}

	s.Invalidate([]string{"t"})
	gate := make(chan struct{})
	s.Load(bg, "k", 0, nil, func(context.Context) (types.CacheValue, error) {
		<-gate
		return nil, errors.New("down")
	}, decodeStr)
	s.Purge([]string{"t"})
	close(gate)
	s.refreshes.Wait()
	if out := read(); strings.Contains(out, "serving stale") {
		t.Errorf("a refresh of a purged value claimed to serve it stale:\n%s", out)
	}
}
