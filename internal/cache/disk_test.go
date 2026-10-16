package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
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

// fullDisk is a cache at its cap whose next eviction scan stops until release is
// closed, reporting through scanning that it has started.
func fullDisk(t *testing.T, limit int) (c *DiskCache, scanning chan struct{}, release chan struct{}) {
	t.Helper()
	c, err := NewDisk(DiskConfig{Dir: t.TempDir(), Version: "v", DefaultTTL: time.Minute, MaxEntries: limit})
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	for i := range limit {
		if _, err := c.Set(context.Background(), fmt.Sprintf("old%02d", i), []byte("x"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	scanning, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.scanned = func() {
		once.Do(func() { close(scanning) })
		<-release
	}
	return c, scanning, release
}

// An eviction reads the whole directory. A write that arrives meanwhile is not
// made to wait for it: on a cache under a flood of new keys the scan is what every
// writer would otherwise queue behind, a request at a time.
func TestDisk_AWriteDoesNotWaitForAnEvictionScan(t *testing.T) {
	c, scanning, release := fullDisk(t, 8)
	defer close(release)
	go c.Set(context.Background(), "trigger", []byte("x"), time.Minute)
	<-scanning

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Set(context.Background(), "other", []byte("x"), time.Minute)
		c.Set(context.Background(), "old00", []byte("replaced"), time.Minute)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a write waited for the eviction scan")
	}
}

// What the scan has not yet made room for is not stored past the cap: a new entry
// arriving at a full cache mid-scan is skipped, a replaced one is still written,
// and the cap holds once the scan finishes.
func TestDisk_TheCapHoldsWhileAnEvictionScans(t *testing.T) {
	const limit = 8
	c, scanning, release := fullDisk(t, limit)
	evicted := make(chan struct{})
	go func() {
		defer close(evicted)
		c.Set(context.Background(), "trigger", []byte("x"), time.Minute)
	}()
	<-scanning

	for i := range 20 {
		if _, err := c.Set(context.Background(), fmt.Sprintf("new%02d", i), []byte("x"), time.Minute); err != nil {
			t.Fatalf("a write at a full cache failed rather than being skipped: %v", err)
		}
	}
	if got := c.countEntries(); got > limit+1 {
		t.Errorf("mid-scan the disk holds %d entries, cap %d", got, limit)
	}
	if _, err := c.Set(context.Background(), "old01", []byte("replaced"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if body, _, ok := c.Get(context.Background(), "old01"); !ok || string(body) != "replaced" {
		t.Errorf("a replaced entry mid-scan: %q %v", body, ok)
	}

	close(release)
	<-evicted
	if got := c.countEntries(); got > limit {
		t.Errorf("after the scan the disk holds %d entries, cap %d", got, limit)
	}
	if _, _, ok := c.Get(context.Background(), "trigger"); !ok {
		t.Error("the write that started the eviction was itself evicted")
	}
}

// A cache Clear empties while a write is between its temporary file and its
// rename loses that write, and says nothing of it: the entry is simply not there.
func TestDisk_AWriteRacingClearIsLostQuietly(t *testing.T) {
	c := newTestDisk(t, t.TempDir(), "v", nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if _, err := c.Set(context.Background(), fmt.Sprintf("k%02d", i), []byte("x"), time.Minute); err != nil {
				t.Errorf("Set racing Clear: %v", err)
			}
		})
		if i%10 == 0 {
			wg.Go(func() { _ = c.Clear(context.Background()) })
		}
	}
	wg.Wait()
}

func BenchmarkDisk_SetAtTheCap(b *testing.B) {
	c, err := NewDisk(DiskConfig{Dir: b.TempDir(), Version: "v", DefaultTTL: time.Minute, MaxEntries: 2000})
	if err != nil {
		b.Fatal(err)
	}
	body := make([]byte, 32<<10)
	var n atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := c.Set(context.Background(), strconv.FormatInt(n.Add(1), 10), body, time.Minute); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func TestDisk_MarkStaleKeyKeepsTheEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	if _, err := c.Set(ctx, "k", []byte("old page"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.MarkStaleKey(ctx, "k"); err != nil {
		t.Fatalf("MarkStaleKey: %v", err)
	}
	if _, _, found := c.Get(ctx, "k"); found {
		t.Error("Get served an entry marked stale")
	}
	assertStale(t, c, "k", "old page")
}

func TestDisk_MarkStaleByTag(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	if _, err := c.SetTagged(ctx, "a", []byte("A"), time.Hour, []string{"post:1"}); err != nil {
		t.Fatalf("SetTagged(a): %v", err)
	}
	if _, err := c.SetTagged(ctx, "b", []byte("B"), time.Hour, []string{"post:2", "list"}); err != nil {
		t.Fatalf("SetTagged(b): %v", err)
	}
	if _, err := c.SetTagged(ctx, "c", []byte("C"), time.Hour, []string{"other"}); err != nil {
		t.Fatalf("SetTagged(c): %v", err)
	}
	if err := c.MarkStale(ctx, []string{"post:1", "list"}); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	assertStale(t, c, "a", "A")
	assertStale(t, c, "b", "B")
	if _, _, found := c.Get(ctx, "c"); !found {
		t.Error("Get missed an entry no tag reached")
	}

	// Marking keeps the tags, so a later invalidation still reaches the entry.
	if err := c.Invalidate(ctx, []string{"post:2"}); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, _, _, found := c.GetStale(ctx, "b"); found {
		t.Error("a stale entry lost its tags: Invalidate did not reach it")
	}
}

func TestDisk_ExpiredIsStaleNotGone(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	c := newTestDisk(t, t.TempDir(), "v", func() time.Time { return now })
	if _, err := c.Set(ctx, "k", []byte("body"), 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, _, found := c.Get(ctx, "k"); !found {
		t.Fatal("the entry is missing before it expired")
	}
	now = now.Add(11 * time.Second)
	if _, _, found := c.Get(ctx, "k"); found {
		t.Error("Get served an expired entry")
	}
	// Get used to delete an expired file on the way out. It is now the copy a
	// failed re-render falls back to, and eviction is what bounds the directory.
	assertStale(t, c, "k", "body")
}

func TestDisk_SetClearsStale(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	if _, err := c.SetTagged(ctx, "k", []byte("old"), time.Hour, []string{"t"}); err != nil {
		t.Fatalf("SetTagged: %v", err)
	}
	if err := c.MarkStale(ctx, []string{"t"}); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	if _, err := c.Set(ctx, "k", []byte("new"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	content, _, stale, found := c.GetStale(ctx, "k")
	if !found || stale || string(content) != "new" {
		t.Errorf("GetStale = %q, stale %v, found %v; want \"new\", fresh", content, stale, found)
	}
	if _, _, found := c.Get(ctx, "k"); !found {
		t.Error("Get missed an entry rewritten after it was marked stale")
	}
}

func TestDisk_MarkStaleOnMissingKeyIsNoError(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	if err := c.MarkStaleKey(ctx, "missing"); err != nil {
		t.Errorf("MarkStaleKey(missing) = %v, want nil", err)
	}
	if err := c.MarkStale(ctx, []string{"no-such-tag"}); err != nil {
		t.Errorf("MarkStale(no-such-tag) = %v, want nil", err)
	}
	if _, _, _, found := c.GetStale(ctx, "missing"); found {
		t.Error("marking a missing key stale created an entry")
	}
}

func TestDisk_StaleSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first := newTestDisk(t, dir, "build-1", nil)
	if _, err := first.SetTagged(ctx, "k", []byte("page"), time.Hour, []string{"t"}); err != nil {
		t.Fatalf("SetTagged: %v", err)
	}
	if err := first.MarkStale(ctx, []string{"t"}); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}

	second := newTestDisk(t, dir, "build-1", nil)
	if _, _, found := second.Get(ctx, "k"); found {
		t.Error("a restart turned a stale entry fresh")
	}
	assertStale(t, second, "k", "page")
}

func TestDisk_MarkStaleRemovesAnUnreadableEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	broken := filepath.Join(c.Dir(), "broken")
	if err := os.WriteFile(broken, []byte("not a header"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkStale(ctx, []string{"t"}); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	if _, err := os.Stat(broken); !os.IsNotExist(err) {
		t.Errorf("an unreadable entry survived MarkStale: %v", err)
	}
}

// A file MarkStaleKey finds broken — a header cut off before its newline — is
// removed, as one MarkStale's scan finds is.
func TestDisk_MarkStaleKeyRemovesACorruptEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	path, _ := c.path("k")
	if err := os.WriteFile(path, []byte(`{"etag":"x"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkStaleKey(ctx, "k"); err != nil {
		t.Fatalf("MarkStaleKey: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a corrupt entry survived MarkStaleKey: %v", err)
	}
}

// unreadable makes the file at path unreadable for the rest of the test, which
// is skipped where permissions do not stop the reader.
func unreadable(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its permissions")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
}

// A file that cannot be read for a reason of the system's — permissions here,
// EMFILE or EIO in production — is not a broken entry. MarkStale leaves it and
// says so, rather than deleting a page it would have kept.
func TestDisk_MarkStaleKeepsAnEntryItCannotRead(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	for _, key := range []string{"locked", "open"} {
		if _, err := c.SetTagged(ctx, key, []byte(key), time.Hour, []string{"t"}); err != nil {
			t.Fatal(err)
		}
	}
	locked, _ := c.path("locked")
	unreadable(t, locked)

	if err := c.MarkStale(ctx, []string{"t"}); err == nil {
		t.Error("MarkStale = nil, want the read error reported")
	}
	if _, err := os.Stat(locked); err != nil {
		t.Errorf("MarkStale removed an entry it could not read: %v", err)
	}
	assertStale(t, c, "open", "open")
}

func TestDisk_MarkStaleKeyKeepsAnEntryItCannotRead(t *testing.T) {
	ctx := context.Background()
	c := newTestDisk(t, t.TempDir(), "v", nil)
	if _, err := c.SetTagged(ctx, "locked", []byte("locked"), time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	locked, _ := c.path("locked")
	unreadable(t, locked)

	if err := c.MarkStaleKey(ctx, "locked"); err == nil {
		t.Error("MarkStaleKey = nil, want the read error reported")
	}
	if _, err := os.Stat(locked); err != nil {
		t.Errorf("MarkStaleKey removed an entry it could not read: %v", err)
	}
}

// TestDisk_EvictionStillBoundsStaleEntries: entries kept as stale count against
// the cap like any other, and marking one does not make it look new. Eviction
// goes oldest first by modification time, so a rewrite that bumped the time
// would evict fresh pages ahead of the stale ones.
func TestDisk_EvictionStillBoundsStaleEntries(t *testing.T) {
	ctx := context.Background()
	c, err := NewDisk(DiskConfig{Dir: t.TempDir(), Version: "v", DefaultTTL: time.Minute, MaxEntries: 8})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := range 6 {
		key := fmt.Sprintf("k%02d", i)
		if _, err := c.SetTagged(ctx, key, []byte("v"), time.Hour, []string{"t" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
		path, _ := c.path(key)
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	// Compared against what the filesystem stored rather than base, which it may
	// have rounded.
	path, _ := c.path("k00")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MarkStale(ctx, []string{"t0", "t1"}); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	if err := c.MarkStaleKey(ctx, "k02"); err != nil {
		t.Fatalf("MarkStaleKey: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("marking stale changed the modification time: %v, want %v", after.ModTime(), before.ModTime())
	}

	// At MaxEntries 8 each write past the cap evicts one file, so these five
	// writes evict exactly three: the stale k00–k02 if marking kept their age,
	// k03–k05 if it made them look new.
	for i := 6; i < 11; i++ {
		if _, err := c.Set(ctx, fmt.Sprintf("k%02d", i), []byte("v"), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.countEntries(); got != 8 {
		t.Errorf("entries on disk = %d, want MaxEntries 8: stale entries escaped the cap", got)
	}
	for _, key := range []string{"k00", "k01", "k02"} {
		if _, _, _, found := c.GetStale(ctx, key); found {
			t.Errorf("the old stale entry %s survived eviction", key)
		}
	}
	for _, key := range []string{"k03", "k04", "k05", "k10"} {
		if _, _, _, found := c.GetStale(ctx, key); !found {
			t.Errorf("%s was evicted ahead of older stale entries", key)
		}
	}
}
