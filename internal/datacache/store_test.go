package datacache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func counting(calls *atomic.Int32, value string) func(context.Context) (any, error) {
	return func(context.Context) (any, error) {
		calls.Add(1)
		return value, nil
	}
}

func TestStore_LoadsOnceAndServesAfter(t *testing.T) {
	s := New(0)
	var calls atomic.Int32
	for range 5 {
		v, err := s.Load(context.Background(), "author:A", 0, nil, counting(&calls, "A"))
		if err != nil || v != "A" {
			t.Fatalf("Load = %v, %v", v, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", calls.Load())
	}
}

// Concurrent loads of one key share one fetch.
func TestStore_ConcurrentLoadsShareOneFetch(t *testing.T) {
	s := New(0)
	var calls atomic.Int32
	release := make(chan struct{})
	fetch := func(context.Context) (any, error) {
		calls.Add(1)
		<-release
		return "A", nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if v, err := s.Load(context.Background(), "k", 0, nil, fetch); err != nil || v != "A" {
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
	s := New(0)
	now := time.Now()
	s.now = func() time.Time { return now }
	var calls atomic.Int32
	s.Load(context.Background(), "k", time.Minute, nil, counting(&calls, "v"))
	now = now.Add(59 * time.Second)
	s.Load(context.Background(), "k", time.Minute, nil, counting(&calls, "v"))
	now = now.Add(2 * time.Second)
	s.Load(context.Background(), "k", time.Minute, nil, counting(&calls, "v"))
	if calls.Load() != 2 {
		t.Errorf("fetches = %d, want 2: one, then one after the minute", calls.Load())
	}
}

func TestStore_InvalidateByTag(t *testing.T) {
	s := New(0)
	var calls atomic.Int32
	s.Load(context.Background(), "author:A", 0, []string{"author:A"}, counting(&calls, "A"))
	s.Load(context.Background(), "author:B", 0, []string{"author:B"}, counting(&calls, "B"))

	if dropped := s.Invalidate([]string{"author:A"}); dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	s.Load(context.Background(), "author:A", 0, []string{"author:A"}, counting(&calls, "A"))
	s.Load(context.Background(), "author:B", 0, []string{"author:B"}, counting(&calls, "B"))
	if calls.Load() != 3 {
		t.Errorf("fetches = %d, want 3: A again, B still stored", calls.Load())
	}
}

// A fetch that started before an invalidation brings back what the invalidation
// was meant to replace: its callers get it, the store does not keep it.
func TestStore_AFetchOverlappingAnInvalidationIsNotStored(t *testing.T) {
	s := New(0)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Load(context.Background(), "k", 0, []string{"t"}, func(context.Context) (any, error) {
			<-release
			return "old", nil
		})
	}()
	time.Sleep(10 * time.Millisecond)
	s.Invalidate([]string{"t"})
	close(release)
	<-done
	if s.Len() != 0 {
		t.Errorf("stored %d values, want none: the fetch predates the invalidation", s.Len())
	}
}

func TestStore_ErrorsAreNotStored(t *testing.T) {
	s := New(0)
	failure := errors.New("upstream down")
	if _, err := s.Load(context.Background(), "k", 0, nil, func(context.Context) (any, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatalf("Load error = %v", err)
	}
	var calls atomic.Int32
	s.Load(context.Background(), "k", 0, nil, counting(&calls, "v"))
	if calls.Load() != 1 {
		t.Error("a failed fetch was stored")
	}
}

func TestStore_EvictsTheLeastRecentlyUsed(t *testing.T) {
	s := New(2)
	var calls atomic.Int32
	s.Load(context.Background(), "a", 0, nil, counting(&calls, "a"))
	s.Load(context.Background(), "b", 0, nil, counting(&calls, "b"))
	s.Load(context.Background(), "a", 0, nil, counting(&calls, "a")) // a is now the most recent
	s.Load(context.Background(), "c", 0, nil, counting(&calls, "c")) // evicts b
	s.Load(context.Background(), "a", 0, nil, counting(&calls, "a"))
	s.Load(context.Background(), "b", 0, nil, counting(&calls, "b"))
	if calls.Load() != 4 {
		t.Errorf("fetches = %d, want 4: a, b, c, then b again", calls.Load())
	}
}

// A fetch that panics must not leave its key behind as a fetch forever running:
// every later load of the key would wait on it, and none would fetch again.
func TestStore_APanickingFetchDoesNotHoldTheKey(t *testing.T) {
	s := New(0)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not reach the caller whose fetch it was")
			}
		}()
		_, _ = s.Load(context.Background(), "k", 0, nil, func(context.Context) (any, error) { panic("upstream") })
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := s.Load(ctx, "k", 0, nil, func(context.Context) (any, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("Load() after a panic = %v, %v; want a fresh fetch", got, err)
	}
}

// A waiter on a panicking fetch is told it failed, not left waiting.
func TestStore_AWaiterOnAPanickingFetchIsAnswered(t *testing.T) {
	s := New(0)
	release := make(chan struct{})
	go func() {
		defer func() { _ = recover() }()
		_, _ = s.Load(context.Background(), "k", 0, nil, func(context.Context) (any, error) {
			<-release
			panic("upstream")
		})
	}()
	waitInflight(t, s, "k")

	done := make(chan error, 1)
	go func() {
		_, err := s.Load(context.Background(), "k", 0, nil, func(context.Context) (any, error) { return "second", nil })
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
	s := New(0)
	first, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = s.Load(first, "k", 0, nil, func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}()
	waitInflight(t, s, "k")

	done := make(chan struct{})
	var got string
	var err error
	go func() {
		defer close(done)
		var value any // any: Load's own return type
		value, err = s.Load(context.Background(), "k", 0, nil, func(context.Context) (any, error) { return "mine", nil })
		got, _ = value.(string)
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
