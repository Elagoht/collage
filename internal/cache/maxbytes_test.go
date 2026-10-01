package cache

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

// MaxEntries counts entries, not what they weigh: ten thousand copies of a large
// page are as many entries as ten thousand small ones, and an anonymous client
// asking for one page under ten thousand invented queries fills the cache with
// the large one. MaxBytes bounds the weight.

func TestMemoryCache_MaxBytesEvictsTheOldest(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxBytes: 1000})
	body := bytes.Repeat([]byte("x"), 100)
	for i := range 25 {
		if _, err := c.Set(ctx, fmt.Sprintf("k%02d", i), body, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.Stats().Bytes; got > 1000 {
		t.Fatalf("the cache holds %d bytes, MaxBytes 1000", got)
	}
	if _, _, ok := c.Get(ctx, "k24"); !ok {
		t.Error("the newest entry was evicted")
	}
	if _, _, ok := c.Get(ctx, "k00"); ok {
		t.Error("the oldest entry survived past MaxBytes")
	}
}

// An entry heavier than the whole cap is not kept, and does not empty the cache
// on its way through.
func TestMemoryCache_AnEntryOverMaxBytesIsNotKept(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxBytes: 1000})
	if _, err := c.Set(ctx, "small", []byte("x"), time.Minute); err != nil {
		t.Fatal(err)
	}
	etag, err := c.Set(ctx, "huge", bytes.Repeat([]byte("x"), 2000), time.Minute)
	if err != nil || etag == "" {
		t.Fatalf("Set of an oversized entry: %q, %v; want its ETag and no error", etag, err)
	}
	if _, _, ok := c.Get(ctx, "huge"); ok {
		t.Error("an entry over MaxBytes was kept")
	}
	if _, _, ok := c.Get(ctx, "small"); !ok {
		t.Error("an oversized entry evicted what was there")
	}
}

// Replacing an entry, removing one and clearing the cache all keep the count
// true, so the cap neither drifts loose nor tightens.
func TestMemoryCache_MaxBytesFollowsEveryChange(t *testing.T) {
	ctx := context.Background()
	c := NewMemory(MemoryConfig{MaxBytes: -1})
	_, _ = c.SetTagged(ctx, "a", make([]byte, 100), time.Minute, []string{"t"})
	_, _ = c.Set(ctx, "a", make([]byte, 40), time.Minute)
	_, _ = c.SetTagged(ctx, "b", make([]byte, 10), time.Minute, []string{"t"})
	if got := c.Stats().Bytes; got != 50 {
		t.Fatalf("after a replace: %d bytes, want 50", got)
	}
	_ = c.InvalidateKey(ctx, "a")
	if got := c.Stats().Bytes; got != 10 {
		t.Fatalf("after InvalidateKey: %d bytes, want 10", got)
	}
	_ = c.Invalidate(ctx, []string{"t"})
	_, _ = c.Set(ctx, "c", make([]byte, 7), time.Minute)
	_ = c.Clear(ctx)
	if got := c.Stats().Bytes; got != 0 {
		t.Fatalf("after Clear: %d bytes, want 0", got)
	}
}

func TestMemoryCache_MaxBytesDefaults(t *testing.T) {
	if got := NewMemory(MemoryConfig{}).cfg.MaxBytes; got != DefaultMemoryMaxBytes {
		t.Errorf("zero MaxBytes became %d, want %d", got, DefaultMemoryMaxBytes)
	}
	c := NewMemory(MemoryConfig{MaxBytes: -1})
	_, _ = c.Set(context.Background(), "big", make([]byte, 1<<20), time.Minute)
	if _, _, ok := c.Get(context.Background(), "big"); !ok {
		t.Error("a negative MaxBytes still refused an entry")
	}
}

func TestDisk_MaxBytesEvictsTheOldest(t *testing.T) {
	ctx := context.Background()
	c, err := NewDisk(DiskConfig{Dir: t.TempDir(), Version: "v", DefaultTTL: time.Minute, MaxEntries: -1, MaxBytes: 4000})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("x"), 300)
	for i := range 40 {
		if _, err := c.Set(ctx, fmt.Sprintf("k%02d", i), body, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.storedBytes(); got > 4000 {
		t.Fatalf("the disk holds %d bytes, MaxBytes 4000", got)
	}
	if _, _, ok := c.Get(ctx, "k39"); !ok {
		t.Error("the newest entry was evicted")
	}
	if _, _, ok := c.Get(ctx, "k00"); ok {
		t.Error("the oldest entry survived past MaxBytes")
	}
}

func TestDisk_AnEntryOverMaxBytesIsNotKept(t *testing.T) {
	ctx := context.Background()
	c, err := NewDisk(DiskConfig{Dir: t.TempDir(), Version: "v", DefaultTTL: time.Minute, MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Set(ctx, "small", []byte("x"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if etag, err := c.Set(ctx, "huge", bytes.Repeat([]byte("x"), 2000), time.Minute); err != nil || etag == "" {
		t.Fatalf("Set of an oversized entry: %q, %v", etag, err)
	}
	if _, _, ok := c.Get(ctx, "huge"); ok {
		t.Error("an entry over MaxBytes was kept")
	}
	if _, _, ok := c.Get(ctx, "small"); !ok {
		t.Error("an oversized entry evicted what was there")
	}
}

// A new cache over a full directory weighs what is there, so a restart does not
// reset the byte cap any more than the entry cap.
func TestDisk_MaxBytesHoldsAcrossARestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := DiskConfig{Dir: dir, Version: "v", DefaultTTL: time.Minute, MaxEntries: -1, MaxBytes: 4000}
	first, err := NewDisk(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("x"), 300)
	for i := range 10 {
		_, _ = first.Set(ctx, fmt.Sprintf("a%02d", i), body, time.Minute)
	}
	second, err := NewDisk(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		_, _ = second.Set(ctx, fmt.Sprintf("b%02d", i), body, time.Minute)
	}
	if got := second.storedBytes(); got > 4000 {
		t.Fatalf("after a restart the disk holds %d bytes, MaxBytes 4000", got)
	}
}

func TestDisk_MaxBytesDefaults(t *testing.T) {
	c, err := NewDisk(DiskConfig{Dir: t.TempDir(), Version: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if c.maxBytes != DefaultDiskMaxBytes {
		t.Errorf("zero MaxBytes became %d, want %d", c.maxBytes, DefaultDiskMaxBytes)
	}
}
