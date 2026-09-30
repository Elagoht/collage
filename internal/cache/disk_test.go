package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestDisk(t *testing.T, dir, version string, now func() time.Time) *DiskCache {
	t.Helper()
	c, err := NewDisk(DiskConfig{Dir: dir, Version: version, DefaultTTL: time.Minute, Now: now})
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	return c
}

func TestDisk_SurvivesANewCacheOverTheSameDirectory(t *testing.T) {
	// The whole point: a process that exits leaves its work behind, and the next
	// one picks it up.
	dir := t.TempDir()
	ctx := context.Background()

	first := newTestDisk(t, dir, "build-1", nil)
	etag, err := first.SetTagged(ctx, "k", []byte("<html>hello</html>"), time.Minute, []string{"page:home"})
	if err != nil {
		t.Fatalf("SetTagged: %v", err)
	}

	second := newTestDisk(t, dir, "build-1", nil)
	content, gotETag, found := second.Get(ctx, "k")
	if !found {
		t.Fatal("the entry did not survive")
	}
	if string(content) != "<html>hello</html>" {
		t.Errorf("content = %q", content)
	}
	if gotETag != etag {
		t.Errorf("etag = %q, want %q — a restart must not change what a client is holding", gotETag, etag)
	}
}

func TestDisk_ADifferentVersionSeesNothing(t *testing.T) {
	// A disk cache outlives the process that filled it, so a new binary must not
	// serve HTML the old one rendered. Entries live under a directory named for the
	// version, so this is structural rather than a check someone can forget.
	dir := t.TempDir()
	ctx := context.Background()

	old := newTestDisk(t, dir, "build-1", nil)
	if _, err := old.Set(ctx, "k", []byte("old markup"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	fresh := newTestDisk(t, dir, "build-2", nil)
	if _, _, found := fresh.Get(ctx, "k"); found {
		t.Error("a new version read the previous build's output")
	}
}

func TestDisk_ExpiredEntryIsGoneAndRemoved(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	now := time.Now()
	clock := func() time.Time { return now }

	c := newTestDisk(t, dir, "v", clock)
	if _, err := c.Set(ctx, "k", []byte("body"), 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, _, found := c.Get(ctx, "k"); !found {
		t.Fatal("the entry is missing before it expired")
	}

	now = now.Add(11 * time.Second)
	if _, _, found := c.Get(ctx, "k"); found {
		t.Error("an expired entry was served")
	}

	// Swept on the way out, so a cache that is read but never written does not grow.
	entries, err := os.ReadDir(c.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d files left after an expired read, want 0", len(entries))
	}
}

func TestDisk_InvalidateByTag(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	c := newTestDisk(t, dir, "v", nil)

	if _, err := c.SetTagged(ctx, "a", []byte("A"), time.Minute, []string{"articles"}); err != nil {
		t.Fatalf("SetTagged: %v", err)
	}
	if _, err := c.SetTagged(ctx, "b", []byte("B"), time.Minute, []string{"authors"}); err != nil {
		t.Fatalf("SetTagged: %v", err)
	}

	if err := c.Invalidate(ctx, []string{"articles"}); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, _, found := c.Get(ctx, "a"); found {
		t.Error("the tagged entry survived invalidation")
	}
	if _, _, found := c.Get(ctx, "b"); !found {
		t.Error("an entry with a different tag was invalidated too")
	}
}

func TestDisk_TagsSurviveARestart(t *testing.T) {
	// The tags are in each entry's own header rather than in an index, so there is
	// no second structure to rebuild or to be left behind by a crash.
	dir := t.TempDir()
	ctx := context.Background()

	first := newTestDisk(t, dir, "v", nil)
	if _, err := first.SetTagged(ctx, "a", []byte("A"), time.Minute, []string{"articles"}); err != nil {
		t.Fatalf("SetTagged: %v", err)
	}

	second := newTestDisk(t, dir, "v", nil)
	if err := second.Invalidate(ctx, []string{"articles"}); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, _, found := second.Get(ctx, "a"); found {
		t.Error("a tag written by an earlier process could not be invalidated by a later one")
	}
}

func TestDisk_RequiresADirectoryAndAVersion(t *testing.T) {
	if _, err := NewDisk(DiskConfig{Version: "v"}); err != ErrEmptyCacheDir {
		t.Errorf("NewDisk with no dir = %v, want ErrEmptyCacheDir", err)
	}
	if _, err := NewDisk(DiskConfig{Dir: t.TempDir()}); err != ErrEmptyCacheVersion {
		t.Errorf("NewDisk with no version = %v, want ErrEmptyCacheVersion", err)
	}
}

func TestDisk_AKeyCannotNameAPath(t *testing.T) {
	// Cache is a public interface: a caller can store under anything. Keys are
	// hashed, so none of them can escape the directory or overrun a filename limit.
	dir := t.TempDir()
	ctx := context.Background()
	c := newTestDisk(t, dir, "v", nil)

	for _, key := range []string{"../escape", "a/b/c", string(make([]byte, 400))} {
		if _, err := c.Set(ctx, key, []byte("x"), time.Minute); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "escape")); err == nil {
		t.Error("a key wrote outside the cache directory")
	}

	entries, err := os.ReadDir(c.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("%d entries, want 3 distinct files", len(entries))
	}
}

func TestDisk_ClearEmptiesIt(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	c := newTestDisk(t, dir, "v", nil)

	for _, key := range []string{"a", "b", "c"} {
		if _, err := c.Set(ctx, key, []byte(key), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := c.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	entries, err := os.ReadDir(c.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d entries left after Clear", len(entries))
	}
}

// TestDisk_EvictsOldestOverMaxEntries: the cap holds, so an anonymous caller
// choosing distinct keys (through the Host or the query, which are in the cache
// key) cannot fill the disk one never-evicted file at a time. The oldest go first.
func TestDisk_EvictsOldestOverMaxEntries(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	c, err := NewDisk(DiskConfig{Dir: dir, Version: "v", DefaultTTL: time.Minute, MaxEntries: 8})
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}

	const n = 40
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%02d", i)
		if _, err := c.Set(ctx, key, []byte(key), time.Minute); err != nil {
			t.Fatalf("Set %s: %v", key, err)
		}
	}

	if got := c.countEntries(); got > 8 {
		t.Errorf("entries on disk = %d, want <= MaxEntries 8: the cap did not hold", got)
	}
	if _, _, ok := c.Get(ctx, "k39"); !ok {
		t.Error("the most recently written entry was evicted")
	}
	if _, _, ok := c.Get(ctx, "k00"); ok {
		t.Error("the oldest entry was not evicted past the cap")
	}
}

// TestDisk_NegativeMaxEntriesIsUnlimited: a deliberately unbounded cache keeps
// everything, matching the project-wide convention.
func TestDisk_NegativeMaxEntriesIsUnlimited(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	c, err := NewDisk(DiskConfig{Dir: dir, Version: "v", DefaultTTL: time.Minute, MaxEntries: -1})
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := c.Set(ctx, fmt.Sprintf("k%02d", i), []byte("x"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if got := c.countEntries(); got != 50 {
		t.Errorf("entries = %d, want all 50 kept when MaxEntries is negative", got)
	}
}

// TestDisk_CapHoldsAcrossARestart: a new cache over a full directory counts what
// is there, so the cap is not reset to zero by a restart.
func TestDisk_CapHoldsAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	first, err := NewDisk(DiskConfig{Dir: dir, Version: "v", DefaultTTL: time.Minute, MaxEntries: 8})
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := first.Set(ctx, fmt.Sprintf("a%02d", i), []byte("x"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	second, err := NewDisk(DiskConfig{Dir: dir, Version: "v", DefaultTTL: time.Minute, MaxEntries: 8})
	if err != nil {
		t.Fatalf("NewDisk restart: %v", err)
	}
	for i := 0; i < 20; i++ {
		if _, err := second.Set(ctx, fmt.Sprintf("b%02d", i), []byte("x"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if got := second.countEntries(); got > 8 {
		t.Errorf("entries after restart = %d, want <= 8: the restart lost the running count", got)
	}
}
