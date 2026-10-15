package datacache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

func open(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

// write loads key into a store on dir, fetching value, and waits for it to be
// on disk: a store about to be reopened.
func write(t *testing.T, dir, key string, tags []string, value string) {
	t.Helper()
	s := open(t, dir, Options{})
	var calls atomic.Int32
	s.Load(bg, key, 0, tags, counting(&calls, value), decodeStr)
	if err := s.Close(bg); err != nil {
		t.Fatal(err)
	}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			names = append(names, e.Name())
		}
	}
	return names
}

func never(t *testing.T) func(context.Context) (types.CacheValue, error) {
	return func(context.Context) (types.CacheValue, error) {
		t.Error("fetched; want the persisted value")
		return nil, errors.New("unexpected fetch")
	}
}

// A restart comes up warm: the new store serves what the old one fetched.
func TestOpen_ARestartServesWhatWasPersisted(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "k", []string{"t"}, "v")

	v, stale, err := open(t, dir, Options{}).Load(bg, "k", 0, []string{"t"}, never(t), decodeStr)
	if v != str("v") || stale || err != nil {
		t.Fatalf("after reopening Load = %v, %v, %v", v, stale, err)
	}
}

// Staleness survives a restart, and so do the tags that invalidate.
func TestOpen_StaleAndTagsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	first := open(t, dir, Options{})
	first.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
	first.Invalidate([]string{"t"})
	first.Close(bg)

	second := open(t, dir, Options{})
	if v, stale, _ := second.Load(bg, "k", 0, nil, failing(errors.New("down")), decodeStr); v != str("v") || !stale {
		t.Fatalf("Load = %v, stale=%v; want the persisted value, still stale", v, stale)
	}
	second.Close(bg)
	third := open(t, dir, Options{})
	if n := third.Invalidate([]string{"t"}); n != 1 {
		t.Errorf("Invalidate after reopening marked %d, want 1: the tags were not persisted", n)
	}
}

// A file that no longer decodes is a miss: dropped and fetched, never an error.
func TestOpen_AValueThatNoLongerDecodesIsAMiss(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	write(t, dir, "k", nil, "v")

	broken := func([]byte) (types.CacheValue, error) { return nil, errors.New("shape changed") }
	v, stale, err := open(t, dir, Options{}).Load(bg, "k", 0, nil, counting(&calls, "fresh"), broken)
	if v != str("fresh") || stale || err != nil {
		t.Fatalf("Load = %v, %v, %v; want a fresh fetch", v, stale, err)
	}
}

// What looks like the store's own leftovers — a torn temporary file, an entry
// file that no longer parses — is removed at startup rather than tripped over.
// Anything else in the directory is not the store's, and is left alone.
func TestOpen_RemovesOnlyItsOwnBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	corrupt := strings.Repeat("ab", 32)
	os.WriteFile(filepath.Join(dir, corrupt), []byte("not an entry"), 0o644)
	os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte("{"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(dir, strings.Repeat("CD", 32)), []byte("not hex we write"), 0o644)
	open(t, dir, Options{})
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 2 || !slices.Contains(left, "notes.txt") || !slices.Contains(left, strings.Repeat("CD", 32)) {
		t.Errorf("left %v, want only notes.txt and the upper-case file", left)
	}
}

// What cannot be encoded is served from memory and never written; an older
// copy on disk is removed rather than left to come back after a restart.
func TestOpen_AnUnencodableValueIsServedButNotWritten(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	s := open(t, dir, Options{})
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
	s.Invalidate([]string{"t"})
	s.Load(bg, "k", 0, []string{"t"}, func(context.Context) (types.CacheValue, error) { return unencodable{}, nil }, decodeStr)
	s.refreshes.Wait()
	s.drain()
	if v, _, _ := s.Load(bg, "k", 0, nil, never(t), decodeStr); v != (unencodable{}) {
		t.Fatalf("Load = %v, want the unencodable value from memory", v)
	}
	if names := files(t, dir); len(names) != 0 {
		t.Errorf("files = %v, want none", names)
	}
}

type unencodable struct{}

func (unencodable) Encode() ([]byte, error) { return nil, errors.New("cannot encode") }

// Purge and not found remove the file too.
func TestOpen_PurgeAndNotFoundRemoveFiles(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	s := open(t, dir, Options{})
	s.Load(bg, "a", 0, []string{"a"}, counting(&calls, "a"), decodeStr)
	s.Load(bg, "b", 0, []string{"b"}, counting(&calls, "b"), decodeStr)
	s.Purge([]string{"a"})
	s.Invalidate([]string{"b"})
	s.Load(bg, "b", 0, nil, failing(fmt.Errorf("gone: %w", types.ErrNotFound)), decodeStr)
	s.Close(bg)
	if names := files(t, dir); len(names) != 0 {
		t.Errorf("files = %v, want none", names)
	}
}

// MaxEntries holds on disk: reopening with a smaller bound keeps the newest.
func TestOpen_KeepsTheNewestWithinMaxEntries(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	s := open(t, dir, Options{})
	for _, k := range []string{"a", "b", "c"} {
		s.Load(bg, k, 0, nil, counting(&calls, k), decodeStr)
	}
	s.Close(bg)
	small := open(t, dir, Options{MaxEntries: 2})
	if small.Len() != 2 || len(files(t, dir)) != 2 {
		t.Fatalf("Len = %d, files = %d; want 2, 2", small.Len(), len(files(t, dir)))
	}
	if v, _, _ := small.Load(bg, "c", 0, nil, never(t), decodeStr); v != str("c") {
		t.Errorf("newest entry lost: %v", v)
	}
}

// A disk that refuses writes costs persistence, never a load.
func TestOpen_AnUnwritableDirectoryNeverFailsALoad(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	s := open(t, dir, Options{})
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	var calls atomic.Int32
	if v, _, err := s.Load(bg, "k", 0, nil, counting(&calls, "v"), decodeStr); v != str("v") || err != nil {
		t.Fatalf("Load = %v, %v", v, err)
	}
}

// Writes race; what is on disk afterwards is what is in memory.
func TestOpen_ConcurrentChangesLeaveDiskMatchingMemory(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			key := fmt.Sprintf("k%d", i%5)
			s.Load(bg, key, 0, []string{key}, func(context.Context) (types.CacheValue, error) {
				return str(fmt.Sprint(i)), nil
			}, decodeStr)
			if i%3 == 0 {
				s.Invalidate([]string{key})
			}
		})
	}
	wg.Wait()
	s.refreshes.Wait()
	// A plain Load returns what is stored, fresh or stale; the refreshes it
	// starts fail and change nothing, and are waited out before reopening.
	down := failing(errors.New("down"))
	want := make(map[string]types.CacheValue)
	for i := range 5 {
		key := fmt.Sprintf("k%d", i)
		want[key], _, _ = s.Load(bg, key, 0, nil, down, decodeStr)
	}
	s.Close(bg)
	reopened := open(t, dir, Options{})
	for key, value := range want {
		if got, _, _ := reopened.Load(bg, key, 0, nil, down, decodeStr); got != value {
			t.Errorf("%s: disk %v, memory %v", key, got, value)
		}
	}
	reopened.refreshes.Wait()
}

// An Encode that panics is a value kept in memory only, not a crashed process:
// it runs on a refresh's goroutine, where nothing above would recover it.
func TestOpen_AnEncodeThatPanicsIsServedButNotWritten(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	s := open(t, dir, Options{})
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
	s.Invalidate([]string{"t"})
	s.Load(bg, "k", 0, []string{"t"}, func(context.Context) (types.CacheValue, error) { return panicking{}, nil }, decodeStr)
	s.refreshes.Wait()
	s.drain()
	if v, _, _ := s.Load(bg, "k", 0, nil, never(t), decodeStr); v != (panicking{}) {
		t.Fatalf("Load = %v, want the value from memory", v)
	}
	if names := files(t, dir); len(names) != 0 {
		t.Errorf("files = %v, want none", names)
	}
}

type panicking struct{}

func (panicking) Encode() ([]byte, error) { panic("encoder bug") }

// A decode that panics, or that returns no value, is a miss like one that
// fails: the entry and its file are dropped, so the key does not fail on every
// request and after every restart.
func TestOpen_ADecodeThatPanicsOrReturnsNothingIsAMiss(t *testing.T) {
	for name, decode := range map[string]func([]byte) (types.CacheValue, error){
		"panics":  func([]byte) (types.CacheValue, error) { panic("decoder bug") },
		"nothing": func([]byte) (types.CacheValue, error) { return nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var calls atomic.Int32
			write(t, dir, "k", nil, "v")

			second := open(t, dir, Options{})
			v, stale, err := second.Load(bg, "k", 0, nil, counting(&calls, "fresh"), decode)
			if v != str("fresh") || stale || err != nil {
				t.Fatalf("Load = %v, %v, %v; want a fresh fetch", v, stale, err)
			}
			second.Close(bg)
			if v, _, _ := open(t, dir, Options{}).Load(bg, "k", 0, nil, never(t), decodeStr); v != str("fresh") {
				t.Errorf("after reopening Load = %v, want the fresh value, not the old file", v)
			}
		})
	}
}

// The memory-only warning is logged once per type, not per key: a site with a
// thousand unencodable authors logs one line, and remembers one type.
func TestOpen_AnUnencodableTypeIsLoggedOnce(t *testing.T) {
	var logs strings.Builder
	var logsMu sync.Mutex
	s := open(t, t.TempDir(), Options{Logger: slog.New(slog.NewTextHandler(lockedWriter{&logsMu, &logs}, nil))})
	bad := func(context.Context) (types.CacheValue, error) { return unencodable{}, nil }
	for _, key := range []string{"a\x00T", "b\x00T", "c\x00T", "d\x00U"} {
		s.Load(bg, key, 0, nil, bad, decodeStr)
	}
	s.drain()
	logsMu.Lock()
	defer logsMu.Unlock()
	if n := strings.Count(logs.String(), "kept in memory only"); n != 2 {
		t.Errorf("logged %d times, want once per type (2):\n%s", n, logs.String())
	}
	if len(s.unencodable) != 2 {
		t.Errorf("remembered %d, want 2", len(s.unencodable))
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Invalidate marks values in memory and returns; their files are rewritten by
// the store's writer, not by the caller. With the disk held, it still returns.
func TestOpen_InvalidateDoesNotWaitOnTheDisk(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	var calls atomic.Int32
	for i := range 200 {
		s.Load(bg, fmt.Sprintf("k%d", i), 0, []string{"t"}, counting(&calls, "v"), decodeStr)
	}
	s.drain()

	s.diskMu.Lock()
	returned := make(chan int)
	go func() { returned <- s.Invalidate([]string{"t"}) }()
	select {
	case n := <-returned:
		if n != 200 {
			t.Errorf("Invalidate marked %d, want 200", n)
		}
	case <-time.After(2 * time.Second):
		t.Error("Invalidate waited on the disk")
		s.diskMu.Unlock()
		<-returned
		s.diskMu.Lock()
	}
	s.diskMu.Unlock()

	if err := s.Close(bg); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, name := range files(t, dir) {
		rec, err := readRecord(filepath.Join(dir, name))
		if err != nil || !rec.header.Stale {
			t.Fatalf("%s: stale=%v err=%v; Close did not drain the writer", name, rec.header.Stale, err)
		}
	}
	if n := len(files(t, dir)); n != 200 {
		t.Errorf("files = %d, want 200", n)
	}
}

// Close waits for a running refresh, ends the wait when ctx does, and is
// idempotent. A store kept in memory has no writer, and closes all the same.
func TestStore_CloseWaitsForRefreshes(t *testing.T) {
	for name, s := range map[string]*Store{"memory": New(Options{}), "disk": open(t, t.TempDir(), Options{})} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
			s.Invalidate([]string{"t"})
			gate := make(chan struct{})
			s.Load(bg, "k", 0, nil, func(context.Context) (types.CacheValue, error) {
				<-gate
				return str("new"), nil
			}, decodeStr)

			expired, cancel := context.WithCancel(bg)
			cancel()
			closed := make(chan error, 1)
			go func() { closed <- s.Close(bg) }()
			select {
			case err := <-closed:
				t.Fatalf("Close returned %v with a refresh running", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(gate)
			if err := <-closed; err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := s.Close(expired); err != nil {
				t.Errorf("second Close = %v, want the first's nil", err)
			}
			if v, _, _ := s.Load(bg, "k", 0, nil, never(t), decodeStr); v != str("new") {
				t.Errorf("Load after Close = %v, want the refreshed value", v)
			}
		})
	}
}

// A ctx that ends first ends Close's wait, and Close says so.
func TestStore_CloseGivesUpWithItsContext(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)
	s.Invalidate([]string{"t"})
	gate := make(chan struct{})
	defer close(gate)
	s.Load(bg, "k", 0, nil, func(context.Context) (types.CacheValue, error) {
		<-gate
		return str("new"), nil
	}, decodeStr)
	ctx, cancel := context.WithTimeout(bg, 20*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close = %v, want the deadline", err)
	}
}

// drain waits for the writer to make every queued write, for tests that read
// the directory or reopen it.
func (s *Store) drain() {
	if err := s.drainUntil(context.Background()); err != nil {
		panic(err)
	}
}
