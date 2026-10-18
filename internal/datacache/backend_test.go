package datacache

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/types"
)

// shared returns a store keeping its values in backend, closed when the test ends.
func shared(t *testing.T, backend Backend, opts Options) *Store {
	t.Helper()
	opts.Backend = backend
	s := New(opts)
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

// markStale is what an invalidation on any instance does to the backend.
func markStale(t *testing.T, backend *cache.SharedMemory, tag string) {
	t.Helper()
	if _, err := backend.DropTags(bg, []string{tag}, false); err != nil {
		t.Fatal(err)
	}
}

// backendHolds returns what backend stores under key, decoded.
func backendHolds(t *testing.T, backend *cache.SharedMemory, key string) (value str, stale, found bool) {
	t.Helper()
	raw, stale, found, err := backend.GetData(bg, key)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return "", false, false
	}
	decoded, err := decodeStr(raw)
	if err != nil {
		t.Fatal(err)
	}
	return decoded.(str), stale, true
}

// One instance's invalidation is every instance's: s2 sees the value s1 stored
// as stale, serves it while it refreshes, and s1 then reads what s2 fetched.
func TestBackend_TwoStoresSeeOneInvalidation(t *testing.T) {
	backend := cache.NewSharedMemory()
	s1 := shared(t, backend, Options{})
	s2 := shared(t, backend, Options{})
	var calls atomic.Int32

	if v, stale, err := s1.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v1"), decodeStr); err != nil || v != str("v1") || stale {
		t.Fatalf("s1.Load = %v, %v, %v; want v1, fresh", v, stale, err)
	}
	markStale(t, backend, "t")

	v, stale, err := s2.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v2"), decodeStr)
	if err != nil || v != str("v1") || !stale {
		t.Fatalf("s2.Load = %v, %v, %v; want v1, stale", v, stale, err)
	}
	if err := s2.Close(bg); err != nil {
		t.Fatal(err)
	}
	v, stale, err = s1.Load(bg, "k", 0, []string{"t"}, never(t), decodeStr)
	if err != nil || v != str("v2") || stale {
		t.Fatalf("s1.Load after s2's refresh = %v, %v, %v; want v2, fresh", v, stale, err)
	}
	if s1.Len() != 0 || s2.Len() != 0 {
		t.Errorf("Len = %d, %d; want 0: nothing is kept in process", s1.Len(), s2.Len())
	}
}

// A fetch an invalidation on another instance overtook is stored stale, from the
// backend's epoch, not the store's own: the next load serves it and refreshes.
func TestBackend_OverlappedFetchStoredStale(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{})
	started, release := make(chan struct{}), make(chan struct{})
	slow := func(context.Context) (types.CacheValue, error) {
		close(started)
		<-release
		return str("old"), nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if v, _, err := s.Load(bg, "k", 0, []string{"t"}, slow, decodeStr); err != nil || v != str("old") {
			t.Errorf("Load = %v, %v; want the fetched value", v, err)
		}
	}()
	<-started
	markStale(t, backend, "t")
	close(release)
	<-done

	if v, stale, found := backendHolds(t, backend, "k"); !found || v != "old" || !stale {
		t.Fatalf("backend holds %q, stale %v, found %v; want old, stored stale", v, stale, found)
	}
	var calls atomic.Int32
	v, stale, err := s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "new"), decodeStr)
	if err != nil || v != str("old") || !stale {
		t.Fatalf("Load = %v, %v, %v; want old, stale", v, stale, err)
	}
	s.refreshes.Wait()
	if v, stale, _ := backendHolds(t, backend, "k"); v != "new" || stale {
		t.Errorf("after the refresh the backend holds %q, stale %v; want new, fresh", v, stale)
	}
}

// Content gone upstream is gone from the backend, for every instance.
func TestBackend_NotFoundDeletes(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v1"), decodeStr)
	markStale(t, backend, "t")

	gone := failing(types.ErrNotFound)
	if v, stale, err := s.Load(bg, "k", 0, []string{"t"}, gone, decodeStr); v != str("v1") || !stale || err != nil {
		t.Fatalf("Load = %v, %v, %v; want v1 served stale while it refreshes", v, stale, err)
	}
	s.refreshes.Wait()
	if backend.Data() != 0 {
		t.Fatalf("backend holds %d values; want the not-found one deleted", backend.Data())
	}
	if _, _, err := s.Load(bg, "k", 0, []string{"t"}, gone, decodeStr); !errors.Is(err, types.ErrNotFound) {
		t.Errorf("Load = %v; want ErrNotFound", err)
	}

	// A load that wants fresh data deletes a stale value the same way.
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v1"), decodeStr)
	markStale(t, backend, "t")
	if _, _, err := s.Load(types.WithFreshData(bg), "k", 0, []string{"t"}, gone, decodeStr); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("fresh-data Load = %v; want ErrNotFound", err)
	}
	if backend.Data() != 0 {
		t.Errorf("backend holds %d values; want the not-found one deleted", backend.Data())
	}
}

// flaky is a backend whose reads, writes or epoch fail on demand.
type flaky struct {
	*cache.SharedMemory
	failGet, failSet, failEpoch atomic.Bool
	sets                        atomic.Int32
}

var errBackendDown = errors.New("backend down")

func (f *flaky) Epoch(ctx context.Context) (uint64, error) {
	if f.failEpoch.Load() {
		return 0, errBackendDown
	}
	return f.SharedMemory.Epoch(ctx)
}

func (f *flaky) GetData(ctx context.Context, key string) ([]byte, bool, bool, error) {
	if f.failGet.Load() {
		return nil, false, false, errBackendDown
	}
	return f.SharedMemory.GetData(ctx, key)
}

func (f *flaky) SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (cache.WriteOutcome, error) {
	f.sets.Add(1)
	if f.failSet.Load() {
		return cache.WriteSkipped, errBackendDown
	}
	return f.SharedMemory.SetData(ctx, key, value, ttl, tags, since)
}

// A backend that fails costs the cache, never the page: a failed read fetches,
// a failed write still returns what was fetched, and a failed epoch read stores
// nothing, since without it an overlapping invalidation could not be told.
func TestBackend_ErrorsAreMisses(t *testing.T) {
	backend := &flaky{SharedMemory: cache.NewSharedMemory()}
	s := shared(t, backend, Options{Logger: slog.New(slog.DiscardHandler)})
	backend.failGet.Store(true)
	backend.failSet.Store(true)
	var calls atomic.Int32
	for range 2 {
		v, stale, err := s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
		if err != nil || v != str("v") || stale {
			t.Fatalf("Load = %v, %v, %v; want the fetched value, no error", v, stale, err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2: every failed read is a miss", calls.Load())
	}

	backend.failGet.Store(false)
	backend.failSet.Store(false)
	backend.failEpoch.Store(true)
	sets := backend.sets.Load()
	if v, _, err := s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr); err != nil || v != str("v") {
		t.Fatalf("Load = %v, %v; want the fetched value", v, err)
	}
	if backend.sets.Load() != sets || backend.Data() != 0 {
		t.Errorf("with no epoch the value was written; want it returned, not stored")
	}
}

// withUnexported is a value JSON would not bring back as it was.
type withUnexported struct{ secret string }

func (withUnexported) Encode() ([]byte, error) {
	return nil, errors.New("unexported field secret")
}

// A value that cannot be encoded is not cached at all: keeping it in process
// would bring back each instance's own copy. It is logged once per type.
func TestBackend_UnencodableNotShared(t *testing.T) {
	backend := cache.NewSharedMemory()
	var logs strings.Builder
	var logsMu sync.Mutex
	s := shared(t, backend, Options{Logger: slog.New(slog.NewTextHandler(lockedWriter{&logsMu, &logs}, nil))})
	var calls atomic.Int32
	fetch := func(context.Context) (types.CacheValue, error) {
		calls.Add(1)
		return withUnexported{"s"}, nil
	}
	for range 2 {
		if v, _, err := s.Load(bg, "k\x00T", 0, nil, fetch, decodeStr); err != nil || v != (withUnexported{"s"}) {
			t.Fatalf("Load = %v, %v; want the fetched value", v, err)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2", calls.Load())
	}
	if backend.Data() != 0 {
		t.Errorf("backend holds %d values, want none", backend.Data())
	}
	logsMu.Lock()
	defer logsMu.Unlock()
	if n := strings.Count(logs.String(), "data cache value not shared, it cannot be encoded"); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, logs.String())
	}
}

// A value whose refresh failed cools down in process, as in memory, and an
// invalidation ends the cooldown; a purge forgets it.
func TestBackend_AFailedRefreshCoolsDown(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{Logger: slog.New(slog.DiscardHandler)})
	clk := newClock(s)
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v1"), decodeStr)
	markStale(t, backend, "t")

	var attempts atomic.Int32
	down := func(context.Context) (types.CacheValue, error) {
		attempts.Add(1)
		return nil, errors.New("upstream down")
	}
	load := func() {
		t.Helper()
		if v, stale, err := s.Load(bg, "k", 0, []string{"t"}, down, decodeStr); v != str("v1") || !stale || err != nil {
			t.Fatalf("Load = %v, %v, %v; want v1 stale", v, stale, err)
		}
		s.refreshes.Wait()
	}
	load()
	load()
	if attempts.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1 while cooling down", attempts.Load())
	}
	clk.advance(retryBackoffMin)
	load()
	if attempts.Load() != 2 {
		t.Fatalf("refreshes = %d, want 2 after the cooldown", attempts.Load())
	}
	if n := s.Invalidate([]string{"t"}); n != 0 {
		t.Errorf("Invalidate = %d, want 0: the backend holds the values", n)
	}
	load()
	if attempts.Load() != 3 {
		t.Fatalf("refreshes = %d, want 3: an invalidation ends the cooldown", attempts.Load())
	}
	s.Purge([]string{"t"})
	s.mu.Lock()
	cooling := len(s.cooling)
	s.mu.Unlock()
	if cooling != 0 {
		t.Errorf("cooldowns after a purge = %d, want 0", cooling)
	}
	if backend.Data() != 1 {
		t.Errorf("backend holds %d values; Purge must leave the backend to dropTags", backend.Data())
	}
}

// A stored value that no longer decodes is deleted and fetched again.
func TestBackend_AValueThatNoLongerDecodesIsAMiss(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{Logger: slog.New(slog.DiscardHandler)})
	if _, err := backend.SetData(bg, "k", []byte("not json"), 0, nil, 0); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if v, stale, err := s.Load(bg, "k", 0, nil, counting(&calls, "v"), decodeStr); err != nil || v != str("v") || stale {
		t.Fatalf("Load = %v, %v, %v; want the fetched value", v, stale, err)
	}
	if v, _, _ := backendHolds(t, backend, "k"); v != "v" {
		t.Errorf("backend holds %q, want the fetched value in place of the broken one", v)
	}
}

// A reader who leaves once the value is fetched does not cost the write: the
// value is as good as it was, for the next reader on any instance.
func TestBackend_AReaderWhoLeftStillWritesTheValue(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{})
	ctx, cancel := context.WithCancel(bg)
	fetch := func(context.Context) (types.CacheValue, error) {
		cancel()
		return str("v"), nil
	}
	if v, _, err := s.Load(ctx, "k", 0, nil, fetch, decodeStr); err != nil || v != str("v") {
		t.Fatalf("Load = %v, %v; want the fetched value", v, err)
	}
	if v, _, found := backendHolds(t, backend, "k"); !found || v != "v" {
		t.Errorf("backend holds %q, found %v; want the value written", v, found)
	}
}

// A backend that is down fails on every load. It is reported once, then once
// per backendQuiet with how many failures went unreported, and its recovery once.
func TestBackend_AnUnavailableBackendIsReportedOnce(t *testing.T) {
	backend := &flaky{SharedMemory: cache.NewSharedMemory()}
	var logs strings.Builder
	var logsMu sync.Mutex
	s := shared(t, backend, Options{Logger: slog.New(slog.NewTextHandler(lockedWriter{&logsMu, &logs}, nil))})
	clk := newClock(s)
	count := func(msg string) int {
		logsMu.Lock()
		defer logsMu.Unlock()
		return strings.Count(logs.String(), msg)
	}
	backend.failGet.Store(true)
	backend.failEpoch.Store(true)
	backend.failSet.Store(true)
	var calls atomic.Int32
	for range 1000 {
		if v, _, err := s.Load(bg, "k", 0, nil, counting(&calls, "v"), decodeStr); err != nil || v != str("v") {
			t.Fatalf("Load = %v, %v; want the fetched value", v, err)
		}
	}
	if n := count("data cache unavailable"); n != 1 {
		t.Fatalf("warned %d times for 1000 failing loads, want 1:\n%s", n, logs.String())
	}
	if n := count("still unavailable"); n != 0 {
		t.Fatalf("repeated the warning %d times within the quiet period, want 0", n)
	}

	clk.advance(backendQuiet)
	s.Load(bg, "k", 0, nil, counting(&calls, "v"), decodeStr)
	if n := count("still unavailable"); n != 1 {
		t.Fatalf("after the quiet period warned %d times, want 1", n)
	}
	if n := count("suppressed=1999"); n != 1 {
		t.Errorf("the repeated warning does not count the 1999 unreported failures:\n%s", logs.String())
	}

	backend.failGet.Store(false)
	backend.failEpoch.Store(false)
	backend.failSet.Store(false)
	for range 3 {
		s.Load(bg, "k", 0, nil, counting(&calls, "v"), decodeStr)
	}
	if n := count("available again"); n != 1 {
		t.Errorf("logged the recovery %d times, want 1:\n%s", n, logs.String())
	}
}

// blocking is a backend whose chosen calls hang until their ctx ends: a
// stalled connection.
type blocking struct {
	*cache.SharedMemory
	reads, writes bool
}

func (b *blocking) Epoch(ctx context.Context) (uint64, error) {
	if b.reads {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return b.SharedMemory.Epoch(ctx)
}

func (b *blocking) GetData(ctx context.Context, key string) ([]byte, bool, bool, error) {
	if b.reads {
		<-ctx.Done()
		return nil, false, false, ctx.Err()
	}
	return b.SharedMemory.GetData(ctx, key)
}

func (b *blocking) SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (cache.WriteOutcome, error) {
	if b.writes {
		<-ctx.Done()
		return cache.WriteSkipped, ctx.Err()
	}
	return b.SharedMemory.SetData(ctx, key, value, ttl, tags, since)
}

// A stalled backend costs a load BackendTimeout per call, not the request's
// whole deadline: the page gets the fetched value.
func TestBackend_AStalledBackendDoesNotHoldTheLoad(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend *blocking
		within  time.Duration
	}{
		// The read and the epoch time out; with no epoch nothing is written.
		{"reads", &blocking{SharedMemory: cache.NewSharedMemory(), reads: true}, 2*BackendTimeout + 250*time.Millisecond},
		{"writes", &blocking{SharedMemory: cache.NewSharedMemory(), writes: true}, BackendTimeout + 250*time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := shared(t, tc.backend, Options{Logger: slog.New(slog.DiscardHandler)})
			ctx, cancel := context.WithTimeout(bg, time.Minute)
			defer cancel()
			began := time.Now()
			var calls atomic.Int32
			v, _, err := s.Load(ctx, "k", 0, nil, counting(&calls, "v"), decodeStr)
			if err != nil || v != str("v") {
				t.Fatalf("Load = %v, %v; want the fetched value", v, err)
			}
			if took := time.Since(began); took > tc.within {
				t.Errorf("Load took %v, want within %v", took, tc.within)
			}
		})
	}
}

// Cooldowns are bounded like entries: past MaxEntries the map stays at it,
// giving up a cooldown already over first and else the oldest.
func TestBackend_CooldownsAreBounded(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{MaxEntries: 3, Logger: slog.New(slog.DiscardHandler)})
	clk := newClock(s)
	fail := failing(errors.New("upstream down"))
	cool := func(key string) {
		t.Helper()
		if _, err := backend.SetData(bg, key, []byte(`"v"`), 0, []string{"t"}, 0); err != nil {
			t.Fatal(err)
		}
		markStale(t, backend, "t")
		if _, stale, _ := s.Load(bg, key, 0, []string{"t"}, fail, decodeStr); !stale {
			t.Fatalf("Load %s not stale", key)
		}
		s.refreshes.Wait()
	}
	cooling := func() map[string]bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		keys := make(map[string]bool)
		for k := range s.cooling {
			keys[k] = true
		}
		return keys
	}
	for _, k := range []string{"k0", "k1", "k2", "k3", "k4"} {
		cool(k)
	}
	if got := cooling(); len(got) != 3 || !got["k2"] || !got["k3"] || !got["k4"] {
		t.Fatalf("cooling %v; want the newest three", got)
	}

	// Every cooldown is over; k2, the oldest, fails again and cools down anew.
	clk.advance(retryBackoffMin)
	cool("k2")
	cool("k5")
	if got := cooling(); len(got) != 3 || !got["k2"] || !got["k5"] {
		t.Errorf("cooling %v; want k2, still cooling, kept over one already over", got)
	}
}

// A fresh-data waiter does not take a fetch another instance's invalidation
// overtook: the backend stored it stale, and the waiter fetches again.
func TestBackend_AFreshDataWaiterRetriesAFetchAnotherInstanceOvertook(t *testing.T) {
	backend := cache.NewSharedMemory()
	s1 := shared(t, backend, Options{})
	s2 := shared(t, backend, Options{})
	started, release := make(chan struct{}), make(chan struct{})
	slow := func(context.Context) (types.CacheValue, error) {
		close(started)
		<-release
		return str("old"), nil
	}
	var wg sync.WaitGroup
	wg.Go(func() { s2.Load(bg, "k", 0, []string{"t"}, slow, decodeStr) })
	<-started
	var fresh types.CacheValue
	wg.Go(func() {
		var calls atomic.Int32
		v, _, err := s2.Load(types.WithFreshData(bg), "k", 0, []string{"t"}, counting(&calls, "new"), decodeStr)
		if err != nil {
			t.Errorf("fresh-data Load: %v", err)
		}
		fresh = v
	})
	// Let the waiter join the running fetch, then invalidate on the other
	// instance: in its backend, as the App does, and in its own store.
	time.Sleep(50 * time.Millisecond)
	markStale(t, backend, "t")
	s1.Invalidate([]string{"t"})
	close(release)
	wg.Wait()
	if fresh != str("new") {
		t.Errorf("fresh-data Load = %v; want new, not the overtaken fetch's old", fresh)
	}
}

// Concurrent loads of one key on one instance share one fetch.
func TestBackend_ConcurrentLoadsShareOneFetch(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{})
	release := make(chan struct{})
	var calls atomic.Int32
	fetch := func(context.Context) (types.CacheValue, error) {
		calls.Add(1)
		<-release
		return str("v"), nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if v, _, err := s.Load(bg, "k", 0, nil, fetch, decodeStr); err != nil || v != str("v") {
				t.Errorf("Load = %v, %v", v, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", calls.Load())
	}
}

// Close racing loads that start refreshes waits for those that started and
// lets no more start.
func TestBackend_CloseRacesARefresh(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := New(Options{Backend: backend})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v1"), decodeStr)
	markStale(t, backend, "t")
	slow := func(context.Context) (types.CacheValue, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return str("v2"), nil
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { s.Load(bg, "k", 0, []string{"t"}, slow, decodeStr) })
	}
	wg.Go(func() {
		if err := s.Close(bg); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	wg.Wait()
	s.mu.Lock()
	inflight := len(s.inflight)
	s.mu.Unlock()
	if inflight != 0 {
		t.Errorf("%d fetches still in flight after Close and the loads returned", inflight)
	}
}

// With a backend, a load that joins a fetch another instance's invalidation
// overtook gets its value stale, as the backend stored it.
func TestBackend_JoinerOfOvertakenFetchIsStale(t *testing.T) {
	backend := cache.NewSharedMemory()
	s := shared(t, backend, Options{})
	v, stale := joinAfterInvalidation(t, s, func() { markStale(t, backend, "t") })
	if v != str("old") || !stale {
		t.Errorf("joiner got %v, stale %v; want old, stale", v, stale)
	}
}

// A backend that fails and answers by turns, as a full Redis under noeviction
// does — the epoch and reads answer, every write fails with OOM — logs at most
// one warning and one recovery per backendQuiet, not one of each per load.
func TestBackend_LogsFlapping(t *testing.T) {
	backend := &flaky{SharedMemory: cache.NewSharedMemory()}
	var logs strings.Builder
	var logsMu sync.Mutex
	s := shared(t, backend, Options{Logger: slog.New(slog.NewTextHandler(lockedWriter{&logsMu, &logs}, nil))})
	clk := newClock(s)
	count := func(level string) int {
		logsMu.Lock()
		defer logsMu.Unlock()
		return strings.Count(logs.String(), "level="+level)
	}
	backend.failSet.Store(true)
	var calls atomic.Int32
	for i := range 1000 {
		if i%100 == 0 {
			clk.advance(100 * time.Millisecond)
		}
		// A key of its own each time, so every load misses, fetches and
		// writes: an epoch read that answers, then a write that fails.
		key := "k" + strconv.Itoa(i)
		if v, _, err := s.Load(bg, key, 0, nil, counting(&calls, "v"), decodeStr); err != nil || v != str("v") {
			t.Fatalf("Load = %v, %v; want the fetched value", v, err)
		}
	}
	if n := count("WARN"); n != 1 {
		t.Fatalf("1,000 failed writes between successful reads logged %d warnings, want 1:\n%s", n, logs.String())
	}
	if n := count("INFO"); n > 1 {
		t.Fatalf("1,000 recoveries logged %d infos, want at most 1:\n%s", n, logs.String())
	}
	clk.advance(backendQuiet)
	s.Load(bg, "last", 0, nil, counting(&calls, "v"), decodeStr)
	if n := count("WARN"); n != 2 {
		t.Fatalf("a failure after %v logged %d warnings in all, want 2:\n%s", backendQuiet, n, logs.String())
	}
	if n := strings.Count(logs.String(), "suppressed=999"); n != 1 {
		t.Errorf("the second warning does not count the 999 failures between the warnings:\n%s", logs.String())
	}
}
