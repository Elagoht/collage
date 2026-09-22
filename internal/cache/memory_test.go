package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// clock is a manually advanced time source for tests, so expiry is exercised by
// moving the clock forward rather than sleeping.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock(start time.Time) *clock {
	return &clock{now: start}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestMemoryCache_SetGet(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{})

	wantETag, err := c.Set(ctx, "k1", []byte("hello"), time.Minute)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	content, etag, found := c.Get(ctx, "k1")
	if !found {
		t.Fatal("Get() found = false, want true")
	}
	if string(content) != "hello" {
		t.Errorf("Get() content = %q, want %q", content, "hello")
	}
	if etag != wantETag {
		t.Errorf("Get() etag = %q, want %q", etag, wantETag)
	}
	if etag != ETag([]byte("hello")) {
		t.Errorf("Get() etag = %q, want ETag(content) = %q", etag, ETag([]byte("hello")))
	}
}

func TestMemoryCache_GetMiss(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{})

	_, _, found := c.Get(ctx, "missing")
	if found {
		t.Fatal("Get() found = true for a key that was never set")
	}
}

func TestMemoryCache_Expiry(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(0, 0))
	c := NewMemory(MemoryConfig{Now: clk.Now})

	if _, err := c.Set(ctx, "k1", []byte("hello"), time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Just before expiry: still live.
	clk.Advance(59 * time.Second)
	if _, _, found := c.Get(ctx, "k1"); !found {
		t.Fatal("Get() found = false before TTL elapsed")
	}

	// At and past expiry: gone, and lazily removed.
	clk.Advance(time.Second)
	if _, _, found := c.Get(ctx, "k1"); found {
		t.Fatal("Get() found = true after TTL elapsed")
	}

	c.mu.RLock()
	_, stillStored := c.entries["k1"]
	c.mu.RUnlock()
	if stillStored {
		t.Fatal("expired entry was not lazily deleted from entries")
	}
}

func TestMemoryCache_SetTTLLessOrEqualZeroUsesDefault(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(0, 0))
	c := NewMemory(MemoryConfig{Now: clk.Now, DefaultTTL: time.Minute})

	for _, ttl := range []time.Duration{0, -time.Second} {
		t.Run(fmt.Sprintf("ttl=%v", ttl), func(t *testing.T) {
			if _, err := c.Set(ctx, "k", []byte("v"), ttl); err != nil {
				t.Fatalf("Set() error = %v", err)
			}
			clk.Advance(59 * time.Second)
			if _, _, found := c.Get(ctx, "k"); !found {
				t.Fatal("entry expired before DefaultTTL elapsed")
			}
			clk.Advance(2 * time.Second)
			if _, _, found := c.Get(ctx, "k"); found {
				t.Fatal("entry still live after DefaultTTL elapsed")
			}
		})
	}
}

func TestMemoryCache_DefaultTTLZeroMeansNoExpiry(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(0, 0))
	c := NewMemory(MemoryConfig{Now: clk.Now}) // DefaultTTL left at zero

	if _, err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	clk.Advance(365 * 24 * time.Hour)
	if _, _, found := c.Get(ctx, "k"); !found {
		t.Fatal("entry with DefaultTTL=0 expired")
	}
}

func TestMemoryCache_FIFOEviction(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxEntries: 2})

	if _, err := c.Set(ctx, "a", []byte("1"), time.Hour); err != nil {
		t.Fatalf("Set(a) error = %v", err)
	}
	if _, err := c.Set(ctx, "b", []byte("2"), time.Hour); err != nil {
		t.Fatalf("Set(b) error = %v", err)
	}
	if _, err := c.Set(ctx, "c", []byte("3"), time.Hour); err != nil {
		t.Fatalf("Set(c) error = %v", err)
	}

	if _, _, found := c.Get(ctx, "a"); found {
		t.Error("oldest entry \"a\" was not evicted")
	}
	if _, _, found := c.Get(ctx, "b"); !found {
		t.Error("entry \"b\" was evicted, want it retained")
	}
	if _, _, found := c.Get(ctx, "c"); !found {
		t.Error("entry \"c\" was evicted, want it retained")
	}

	stats := c.Stats()
	if stats.Evictions != 1 {
		t.Errorf("Stats().Evictions = %d, want 1", stats.Evictions)
	}
	if stats.Entries != 2 {
		t.Errorf("Stats().Entries = %d, want 2", stats.Entries)
	}
}

func TestMemoryCache_MaxEntriesNegativeIsUnlimited(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxEntries: -1})

	for i := range 5000 {
		key := fmt.Sprintf("k%d", i)
		if _, err := c.Set(ctx, key, []byte("v"), time.Hour); err != nil {
			t.Fatalf("Set(%s) error = %v", key, err)
		}
	}

	if _, _, found := c.Get(ctx, "k0"); !found {
		t.Fatal("first-inserted entry was evicted under MaxEntries = -1 (unlimited)")
	}
	if got := c.Stats().Entries; got != 5000 {
		t.Errorf("Stats().Entries = %d, want 5000", got)
	}
}

func TestMemoryCache_ZeroMaxEntriesUsesDefaultNotUnlimited(t *testing.T) {
	c := NewMemory(MemoryConfig{})
	if c.cfg.MaxEntries != defaultMaxEntries {
		t.Errorf("NewMemory(MemoryConfig{}).cfg.MaxEntries = %d, want default %d", c.cfg.MaxEntries, defaultMaxEntries)
	}
}

func TestMemoryCache_InvalidateByTag(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{})

	if _, err := c.SetTagged(ctx, "post-1", []byte("a"), time.Hour, []string{"posts", "post-1"}); err != nil {
		t.Fatalf("SetTagged(post-1) error = %v", err)
	}
	if _, err := c.SetTagged(ctx, "post-2", []byte("b"), time.Hour, []string{"posts", "post-2"}); err != nil {
		t.Fatalf("SetTagged(post-2) error = %v", err)
	}
	if _, err := c.SetTagged(ctx, "about", []byte("c"), time.Hour, []string{"static"}); err != nil {
		t.Fatalf("SetTagged(about) error = %v", err)
	}

	if err := c.Invalidate(ctx, []string{"post-1"}); err != nil {
		t.Fatalf("Invalidate() error = %v", err)
	}

	if _, _, found := c.Get(ctx, "post-1"); found {
		t.Error("post-1 still present after invalidating its tag")
	}
	if _, _, found := c.Get(ctx, "post-2"); !found {
		t.Error("post-2 was removed by an unrelated tag invalidation")
	}
	if _, _, found := c.Get(ctx, "about"); !found {
		t.Error("about was removed by an unrelated tag invalidation")
	}
}

func TestMemoryCache_TagIndexHasNoOrphans(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(0, 0))

	t.Run("after InvalidateKey", func(t *testing.T) {
		c := NewMemory(MemoryConfig{Now: clk.Now})
		if _, err := c.SetTagged(ctx, "k", []byte("v"), time.Hour, []string{"t1", "t2"}); err != nil {
			t.Fatalf("SetTagged() error = %v", err)
		}
		if err := c.InvalidateKey(ctx, "k"); err != nil {
			t.Fatalf("InvalidateKey() error = %v", err)
		}
		assertTagIndexEmpty(t, c)
	})

	t.Run("after Invalidate", func(t *testing.T) {
		c := NewMemory(MemoryConfig{Now: clk.Now})
		if _, err := c.SetTagged(ctx, "k", []byte("v"), time.Hour, []string{"t1", "t2"}); err != nil {
			t.Fatalf("SetTagged() error = %v", err)
		}
		if err := c.Invalidate(ctx, []string{"t1", "t2"}); err != nil {
			t.Fatalf("Invalidate() error = %v", err)
		}
		assertTagIndexEmpty(t, c)
	})

	t.Run("after FIFO eviction", func(t *testing.T) {
		c := NewMemory(MemoryConfig{MaxEntries: 1})
		if _, err := c.SetTagged(ctx, "k1", []byte("v"), time.Hour, []string{"t1"}); err != nil {
			t.Fatalf("SetTagged(k1) error = %v", err)
		}
		if _, err := c.SetTagged(ctx, "k2", []byte("v"), time.Hour, []string{"t2"}); err != nil {
			t.Fatalf("SetTagged(k2) error = %v", err)
		}
		// k1 was evicted; only t2's index entry should remain.
		c.mu.RLock()
		_, hasT1 := c.tags["t1"]
		_, hasT2 := c.tags["t2"]
		c.mu.RUnlock()
		if hasT1 {
			t.Error("tag index still has \"t1\" after its only key was FIFO-evicted")
		}
		if !hasT2 {
			t.Error("tag index lost \"t2\" although its key is still live")
		}
	})

	t.Run("after lazy expiry", func(t *testing.T) {
		clk := newClock(time.Unix(0, 0))
		c := NewMemory(MemoryConfig{Now: clk.Now})
		if _, err := c.SetTagged(ctx, "k", []byte("v"), time.Minute, []string{"t1", "t2"}); err != nil {
			t.Fatalf("SetTagged() error = %v", err)
		}
		clk.Advance(2 * time.Minute)
		if _, _, found := c.Get(ctx, "k"); found {
			t.Fatal("entry should have been expired")
		}
		assertTagIndexEmpty(t, c)
	})

	t.Run("after Clear", func(t *testing.T) {
		c := NewMemory(MemoryConfig{})
		if _, err := c.SetTagged(ctx, "k", []byte("v"), time.Hour, []string{"t1", "t2"}); err != nil {
			t.Fatalf("SetTagged() error = %v", err)
		}
		if err := c.Clear(ctx); err != nil {
			t.Fatalf("Clear() error = %v", err)
		}
		assertTagIndexEmpty(t, c)
	})

	t.Run("partial invalidation leaves other tag's index intact", func(t *testing.T) {
		c := NewMemory(MemoryConfig{})
		if _, err := c.SetTagged(ctx, "k1", []byte("v"), time.Hour, []string{"shared", "only-k1"}); err != nil {
			t.Fatalf("SetTagged(k1) error = %v", err)
		}
		if _, err := c.SetTagged(ctx, "k2", []byte("v"), time.Hour, []string{"shared"}); err != nil {
			t.Fatalf("SetTagged(k2) error = %v", err)
		}
		if err := c.InvalidateKey(ctx, "k1"); err != nil {
			t.Fatalf("InvalidateKey(k1) error = %v", err)
		}

		c.mu.RLock()
		_, hasOnlyK1 := c.tags["only-k1"]
		sharedKeys, hasShared := c.tags["shared"]
		c.mu.RUnlock()

		if hasOnlyK1 {
			t.Error("tag \"only-k1\" still indexed after its only key was invalidated")
		}
		if !hasShared {
			t.Fatal("tag \"shared\" was dropped although k2 still references it")
		}
		if _, ok := sharedKeys["k1"]; ok {
			t.Error("tag \"shared\" still references removed key \"k1\"")
		}
		if _, ok := sharedKeys["k2"]; !ok {
			t.Error("tag \"shared\" lost its reference to live key \"k2\"")
		}
	})
}

// assertTagIndexEmpty fails the test unless c's tag index has no entries at all.
func assertTagIndexEmpty(t *testing.T, c *MemoryCache) {
	t.Helper()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.tags) != 0 {
		t.Fatalf("tag index not empty: %v", c.tags)
	}
}

func TestMemoryCache_Clear(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{})

	if _, err := c.Set(ctx, "a", []byte("1"), time.Hour); err != nil {
		t.Fatalf("Set(a) error = %v", err)
	}
	if _, err := c.Set(ctx, "b", []byte("2"), time.Hour); err != nil {
		t.Fatalf("Set(b) error = %v", err)
	}

	if err := c.Clear(ctx); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	if _, _, found := c.Get(ctx, "a"); found {
		t.Error("\"a\" still present after Clear")
	}
	if _, _, found := c.Get(ctx, "b"); found {
		t.Error("\"b\" still present after Clear")
	}
	if got := c.Stats().Entries; got != 0 {
		t.Errorf("Stats().Entries = %d after Clear, want 0", got)
	}
}

func TestMemoryCache_InvalidateKeyUnknownKeyIsNotError(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{})
	if err := c.InvalidateKey(ctx, "never-set"); err != nil {
		t.Fatalf("InvalidateKey() error = %v, want nil", err)
	}
}

func TestMemoryCache_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := NewMemory(MemoryConfig{})

	if _, _, found := c.Get(ctx, "k"); found {
		t.Error("Get() with a canceled context reported found = true")
	}
	if _, err := c.Set(ctx, "k", []byte("v"), time.Hour); err == nil {
		t.Error("Set() with a canceled context returned nil error")
	}
	if err := c.InvalidateKey(ctx, "k"); err == nil {
		t.Error("InvalidateKey() with a canceled context returned nil error")
	}
	if err := c.Invalidate(ctx, []string{"t"}); err == nil {
		t.Error("Invalidate() with a canceled context returned nil error")
	}
	if err := c.Clear(ctx); err == nil {
		t.Error("Clear() with a canceled context returned nil error")
	}
}

func TestMemoryCache_SetDoesNotAliasCallerSlice(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{})

	content := []byte("original")
	if _, err := c.Set(ctx, "k", content, time.Hour); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	content[0] = 'X'

	got, _, found := c.Get(ctx, "k")
	if !found {
		t.Fatal("Get() found = false")
	}
	if string(got) != "original" {
		t.Errorf("Get() content = %q, want %q (mutating the caller's slice after Set must not affect the cache)", got, "original")
	}
}

func TestMemoryCache_Concurrent(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxEntries: 50})

	const goroutines = 20
	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(g int) {
			defer wg.Done()
			for i := range opsPerGoroutine {
				key := fmt.Sprintf("k%d", (g+i)%25)
				switch i % 4 {
				case 0:
					_, _, _ = c.Get(ctx, key)
				case 1:
					_, _ = c.SetTagged(ctx, key, []byte("v"), time.Hour, []string{fmt.Sprintf("tag%d", i%5)})
				case 2:
					_ = c.Invalidate(ctx, []string{fmt.Sprintf("tag%d", i%5)})
				case 3:
					_ = c.InvalidateKey(ctx, key)
				}
			}
		}(g)
	}
	wg.Wait()

	// Reaching here without the race detector firing is the point of this test;
	// also sanity-check the cache is still internally usable afterward.
	if _, err := c.Set(ctx, "final", []byte("v"), time.Hour); err != nil {
		t.Fatalf("Set() error after concurrent access = %v", err)
	}
	if _, _, found := c.Get(ctx, "final"); !found {
		t.Fatal("Get() found = false for key set after concurrent access")
	}
}

// ---------------------------------------------------------------------------
// FIFO queue integrity
//
// The eviction queue is a hand-rolled doubly linked list, and the tests above only
// ever remove from its head — which is the one case where a broken unlink is
// invisible, because head removal never has to patch a predecessor. These three pin
// the rest of it: removing from the middle, removing the tail, and re-Setting a key,
// which removes it from wherever it is and pushes it back as a new insertion. Each
// one asserts through subsequent eviction order, since that is the only observable
// the queue has.
// ---------------------------------------------------------------------------

// fill writes keys in order with a long TTL, failing the test on any error.
func fill(t *testing.T, c *MemoryCache, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, err := c.Set(context.Background(), key, []byte(key), time.Hour); err != nil {
			t.Fatalf("Set(%q) error = %v", key, err)
		}
	}
}

// present reports which of keys the cache still holds, in the order given.
func present(t *testing.T, c *MemoryCache, keys ...string) []string {
	t.Helper()
	var live []string
	for _, key := range keys {
		if _, _, found := c.Get(context.Background(), key); found {
			live = append(live, key)
		}
	}
	return live
}

// equalKeys reports whether got and want hold the same keys in the same order.
func equalKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMemoryCache_FIFOQueueSurvivesMiddleRemoval(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxEntries: 3})
	fill(t, c, "a", "b", "c")

	// "b" is neither head nor tail: unlinking it has to patch both neighbours.
	if err := c.InvalidateKey(ctx, "b"); err != nil {
		t.Fatalf("InvalidateKey(b) error = %v", err)
	}

	// Two more insertions take the cache back over the cap twice, which must evict
	// "a" then "c" — in that order — and never revisit the removed "b".
	fill(t, c, "d", "e")

	if got := present(t, c, "a", "b", "c", "d", "e"); !equalKeys(got, []string{"c", "d", "e"}) {
		t.Fatalf("live keys = %v, want [c d e]", got)
	}

	fill(t, c, "f")
	if got := present(t, c, "c", "d", "e", "f"); !equalKeys(got, []string{"d", "e", "f"}) {
		t.Fatalf("live keys after one more insertion = %v, want [d e f]", got)
	}
}

func TestMemoryCache_FIFOQueueSurvivesTailRemoval(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxEntries: 3})
	fill(t, c, "a", "b", "c")

	// "c" is the tail: unlinking it has to move the tail pointer back to "b", or
	// the next insertion appends to a node that is no longer in the queue.
	if err := c.InvalidateKey(ctx, "c"); err != nil {
		t.Fatalf("InvalidateKey(c) error = %v", err)
	}
	fill(t, c, "d", "e")

	if got := present(t, c, "a", "b", "c", "d", "e"); !equalKeys(got, []string{"b", "d", "e"}) {
		t.Fatalf("live keys = %v, want [b d e]", got)
	}

	// Removing the only entry leaves both pointers nil, and the queue has to be
	// usable again afterwards.
	single := NewMemory(MemoryConfig{MaxEntries: 1})
	fill(t, single, "solo")
	if err := single.InvalidateKey(ctx, "solo"); err != nil {
		t.Fatalf("InvalidateKey(solo) error = %v", err)
	}
	fill(t, single, "next")
	if got := present(t, single, "solo", "next"); !equalKeys(got, []string{"next"}) {
		t.Fatalf("live keys after emptying and refilling = %v, want [next]", got)
	}
}

func TestMemoryCache_ReSetRefreshesFIFOPosition(t *testing.T) {
	c := NewMemory(MemoryConfig{MaxEntries: 3})
	fill(t, c, "a", "b", "c")

	// Re-Setting "a" replaces the entry entirely, which SetTagged documents as a
	// new insertion: "a" moves to the tail and "b" becomes the oldest.
	fill(t, c, "a")
	if entries := c.Stats().Entries; entries != 3 {
		t.Fatalf("Stats().Entries after re-Set = %d, want 3: the replacement was counted as a new entry", entries)
	}

	fill(t, c, "d")
	if got := present(t, c, "a", "b", "c", "d"); !equalKeys(got, []string{"a", "c", "d"}) {
		t.Fatalf("live keys = %v, want [a c d]: a re-Set key must not still be the oldest", got)
	}

	// Reading is not a refresh: this cache is FIFO, not LRU, so touching "c" must
	// not save it from being next.
	if _, _, found := c.Get(context.Background(), "c"); !found {
		t.Fatal("Get(c) found nothing, want the entry")
	}
	fill(t, c, "e")
	if got := present(t, c, "a", "c", "d", "e"); !equalKeys(got, []string{"a", "d", "e"}) {
		t.Fatalf("live keys = %v, want [a d e]: a Get must not refresh FIFO position", got)
	}
}
