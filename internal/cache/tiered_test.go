package cache

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// newTestTier builds a tier over a fresh disk directory, both tiers on clk.
func newTestTier(t *testing.T, dir string, clk *clock, mem MemoryConfig) *TieredCache {
	t.Helper()
	back := newTestDisk(t, dir, "v1", clk.Now)
	mem.Now = clk.Now
	return NewTiered(NewMemory(mem), back)
}

// frontKeys lists which of keys the memory tier holds, whatever their freshness.
func frontKeys(tier *TieredCache, keys ...string) []string {
	var got []string
	for _, k := range keys {
		if _, _, _, found := tier.front.GetStale(context.Background(), k); found {
			got = append(got, k)
		}
	}
	return got
}

func TestTiered_ReadsMemoryThenDiskAndPromotes(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(1_000_000, 0))
	dir := t.TempDir()

	// What a previous process left on disk, written before this tier existed,
	// with warm-up turned off so the read has to reach the disk.
	back := newTestDisk(t, dir, "v1", clk.Now)
	etag, err := back.SetTagged(ctx, "k", []byte("from disk"), time.Minute, []string{"page:home"})
	if err != nil {
		t.Fatalf("SetTagged: %v", err)
	}
	front := NewMemory(MemoryConfig{Now: clk.Now})
	tier := &TieredCache{front: front, back: back}

	content, gotETag, found := tier.Get(ctx, "k")
	if !found || string(content) != "from disk" || gotETag != etag {
		t.Fatalf("Get = %q, %q, %v; want the disk entry", content, gotETag, found)
	}
	if _, _, found := front.Get(ctx, "k"); !found {
		t.Fatal("a disk hit was not promoted into memory")
	}

	// The promoted copy keeps the disk entry's tags: invalidating the tag reaches it.
	if err := front.Invalidate(ctx, []string{"page:home"}); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, _, found := front.Get(ctx, "k"); found {
		t.Error("the promoted entry lost its tags")
	}

	// And its expiry: it goes stale in memory when the disk entry would.
	if _, _, found := tier.Get(ctx, "k"); !found {
		t.Fatal("re-read after the memory copy went: want a second promotion")
	}
	clk.Advance(time.Minute)
	if _, _, found := front.Get(ctx, "k"); found {
		t.Error("the promoted entry outlived the disk entry's expiry")
	}

	// Memory answers first: a disk entry removed behind the tier's back is still
	// served from memory, and memory's answer, stale or not, is final.
	if _, err := tier.Set(ctx, "m", []byte("memory"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := back.InvalidateKey(ctx, "m"); err != nil {
		t.Fatalf("InvalidateKey: %v", err)
	}
	if content, _, found := tier.Get(ctx, "m"); !found || string(content) != "memory" {
		t.Errorf("Get(m) = %q, %v; want the memory copy", content, found)
	}

	if _, _, found := tier.Get(ctx, "absent"); found {
		t.Error("Get of an absent key reported found")
	}
}

func TestTiered_PromotionKeepsStaleness(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(1_000_000, 0))
	dir := t.TempDir()

	back := newTestDisk(t, dir, "v1", clk.Now)
	if _, err := back.Set(ctx, "marked", []byte("marked page"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := back.MarkStaleKey(ctx, "marked"); err != nil {
		t.Fatalf("MarkStaleKey: %v", err)
	}
	if _, err := back.Set(ctx, "expired", []byte("expired page"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	clk.Advance(2 * time.Minute)

	tier := &TieredCache{front: NewMemory(MemoryConfig{Now: clk.Now}), back: back}

	// Get reports neither, but GetStale promotes both and they stay stale in memory.
	if _, _, found := tier.Get(ctx, "marked"); found {
		t.Error("Get reported a stale-marked disk entry fresh")
	}
	assertStale(t, tier, "marked", "marked page")
	assertStale(t, tier, "expired", "expired page")
	assertStale(t, tier.front, "marked", "marked page")
	assertStale(t, tier.front, "expired", "expired page")

	// A mark rewrites the disk header; the key in it survives, so a warm-up after
	// a restart still finds the entry, stale.
	warmed := NewTiered(NewMemory(MemoryConfig{Now: clk.Now}), newTestDisk(t, dir, "v1", clk.Now))
	assertStale(t, warmed.front, "marked", "marked page")
}

func TestTiered_MarkStaleReachesBothTiers(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(1_000_000, 0))
	tier := newTestTier(t, t.TempDir(), clk, MemoryConfig{})

	if _, err := tier.SetTagged(ctx, "a", []byte("A"), time.Hour, []string{"t"}); err != nil {
		t.Fatalf("SetTagged: %v", err)
	}
	if _, err := tier.Set(ctx, "b", []byte("B"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := tier.MarkStale(ctx, []string{"t"}); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	if err := tier.MarkStaleKey(ctx, "b"); err != nil {
		t.Fatalf("MarkStaleKey: %v", err)
	}
	for _, c := range []StaleCache{tier.front, tier.back, tier} {
		assertStale(t, c, "a", "A")
		assertStale(t, c, "b", "B")
	}

	// A write clears the mark in both.
	if _, err := tier.Set(ctx, "b", []byte("B2"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	for _, c := range []Cache{tier.front, tier.back} {
		if content, _, found := c.Get(ctx, "b"); !found || string(content) != "B2" {
			t.Errorf("%T Get(b) = %q, %v; want the fresh rewrite", c, content, found)
		}
	}
}

func TestTiered_InvalidateAndClearReachBothTiers(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(1_000_000, 0))
	tier := newTestTier(t, t.TempDir(), clk, MemoryConfig{})

	for _, k := range []string{"a", "b", "c"} {
		if _, err := tier.SetTagged(ctx, k, []byte(k), time.Hour, []string{"tag:" + k}); err != nil {
			t.Fatalf("SetTagged: %v", err)
		}
	}
	gone := func(k string) {
		t.Helper()
		for _, c := range []StaleCache{tier.front, tier.back} {
			if _, _, _, found := c.GetStale(ctx, k); found {
				t.Errorf("%T still holds %q", c, k)
			}
		}
	}

	if err := tier.Invalidate(ctx, []string{"tag:a"}); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	gone("a")
	if err := tier.InvalidateKey(ctx, "b"); err != nil {
		t.Fatalf("InvalidateKey: %v", err)
	}
	gone("b")
	if _, _, found := tier.Get(ctx, "c"); !found {
		t.Fatal("an untouched entry went")
	}
	if err := tier.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	gone("c")
}

func TestTiered_WarmsFromDiskNewestFirstWithinMemoryLimits(t *testing.T) {
	ctx := context.Background()
	clk := newClock(time.Unix(1_000_000, 0))
	dir := t.TempDir()

	back := newTestDisk(t, dir, "v1", clk.Now)
	base := time.Unix(2_000_000, 0)
	for i := range 5 {
		k := "k" + strconv.Itoa(i)
		if _, err := back.Set(ctx, k, []byte("page "+k), time.Hour); err != nil {
			t.Fatalf("Set: %v", err)
		}
		path, _ := back.path(k)
		at := base.Add(time.Duration(i) * time.Second) // k4 is the newest
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}
	if err := back.MarkStaleKey(ctx, "k4"); err != nil {
		t.Fatalf("MarkStaleKey: %v", err)
	}
	// A corrupt file and one written before headers carried keys are both skipped.
	if err := os.WriteFile(filepath.Join(back.Dir(), "corrupt"), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"etag":"\"x\"","expiresAt":` + strconv.FormatInt(clk.Now().Add(time.Hour).UnixNano(), 10) + "}\nlegacy")
	legacyPath, _ := back.path("legacy")
	if err := os.WriteFile(legacyPath, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	newest := base.Add(time.Hour)
	for _, path := range []string{filepath.Join(back.Dir(), "corrupt"), legacyPath} {
		if err := os.Chtimes(path, newest, newest); err != nil {
			t.Fatal(err)
		}
	}

	all := []string{"k0", "k1", "k2", "k3", "k4"}

	// The entry cap: the three newest, the stale one among them.
	front := NewMemory(MemoryConfig{MaxEntries: 3, Now: clk.Now})
	tier := NewTiered(front, back)
	if got := frontKeys(tier, all...); !equalKeys(got, []string{"k2", "k3", "k4"}) {
		t.Fatalf("warmed %v, want the three newest", got)
	}
	if s := front.Stats(); s.Evictions != 0 {
		t.Errorf("warm-up evicted %d entries; it must stop at the limit", s.Evictions)
	}
	assertStale(t, front, "k4", "page k4")

	// Memory's FIFO order follows the disk's age: the next insertion evicts the
	// oldest warmed entry, not the newest.
	if _, err := front.Set(ctx, "new", []byte("new"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := frontKeys(tier, all...); !equalKeys(got, []string{"k3", "k4"}) {
		t.Errorf("after one more insertion memory holds %v, want k3 and k4", got)
	}

	// The byte cap: each page is seven bytes, so fifteen fit two.
	front = NewMemory(MemoryConfig{MaxBytes: 15, Now: clk.Now})
	tier = NewTiered(front, back)
	if got := frontKeys(tier, all...); !equalKeys(got, []string{"k3", "k4"}) {
		t.Errorf("warmed %v under a byte cap, want the two newest", got)
	}
	if s := front.Stats(); s.Evictions != 0 {
		t.Errorf("warm-up evicted %d entries under a byte cap", s.Evictions)
	}

	// The file without a key was not warmed, but a read still promotes it.
	if _, _, _, found := front.GetStale(ctx, "legacy"); found {
		t.Error("a file without a key was warmed")
	}
	if content, _, found := tier.Get(ctx, "legacy"); !found || string(content) != "legacy" {
		t.Errorf("Get(legacy) = %q, %v; want the file without a key served from disk", content, found)
	}
}

func TestTiered_ADiskWriteFailureStillWritesMemory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	ctx := context.Background()
	clk := newClock(time.Unix(1_000_000, 0))
	tier := newTestTier(t, t.TempDir(), clk, MemoryConfig{})

	if err := os.Chmod(tier.Dir(), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(tier.Dir(), 0o755) })

	etag, err := tier.SetTagged(ctx, "k", []byte("page"), time.Hour, []string{"t"})
	if err == nil {
		t.Error("a failed disk write was not reported")
	}
	if etag != ETag([]byte("page")) {
		t.Errorf("etag = %q, want the content's ETag even when the disk fails", etag)
	}
	if content, _, found := tier.front.Get(ctx, "k"); !found || string(content) != "page" {
		t.Errorf("memory Get = %q, %v; want the page written despite the disk", content, found)
	}
	if content, _, found := tier.Get(ctx, "k"); !found || string(content) != "page" {
		t.Errorf("tier Get = %q, %v; want the page", content, found)
	}
}

func TestTiered_DefaultTTLAgreesAcrossTiers(t *testing.T) {
	// The disk tier turns a zero default TTL into five minutes, while memory reads
	// zero as never expiring. A write without a ttl must expire in both at once.
	ctx := context.Background()
	clk := newClock(time.Unix(1_000_000, 0))
	back, err := NewDisk(DiskConfig{Dir: t.TempDir(), Version: "v1", Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	tier := NewTiered(NewMemory(MemoryConfig{Now: clk.Now}), back)
	if _, err := tier.Set(ctx, "k", []byte("page"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	clk.Advance(5 * time.Minute)
	if _, _, found := tier.front.Get(ctx, "k"); found {
		t.Error("memory kept the entry fresh past the disk's default TTL")
	}
}
