package datacache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func open(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
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
	var calls atomic.Int32
	open(t, dir, Options{}).Load(bg, "k", 0, []string{"t"}, counting(&calls, "v"), decodeStr)

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

	second := open(t, dir, Options{})
	if v, stale, _ := second.Load(bg, "k", 0, nil, failing(errors.New("down")), decodeStr); v != str("v") || !stale {
		t.Fatalf("Load = %v, stale=%v; want the persisted value, still stale", v, stale)
	}
	second.refreshes.Wait()
	third := open(t, dir, Options{})
	if n := third.Invalidate([]string{"t"}); n != 1 {
		t.Errorf("Invalidate after reopening marked %d, want 1: the tags were not persisted", n)
	}
}

// A file that no longer decodes is a miss: dropped and fetched, never an error.
func TestOpen_AValueThatNoLongerDecodesIsAMiss(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	open(t, dir, Options{}).Load(bg, "k", 0, nil, counting(&calls, "v"), decodeStr)

	broken := func([]byte) (types.CacheValue, error) { return nil, errors.New("shape changed") }
	v, stale, err := open(t, dir, Options{}).Load(bg, "k", 0, nil, counting(&calls, "fresh"), broken)
	if v != str("fresh") || stale || err != nil {
		t.Fatalf("Load = %v, %v, %v; want a fresh fetch", v, stale, err)
	}
}

// Files that are not entries — torn writes, leftovers, garbage — are removed
// at startup rather than tripped over.
func TestOpen_CorruptFilesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "garbage"), []byte("not an entry"), 0o644)
	os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte("{"), 0o644)
	open(t, dir, Options{})
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("left %d files, want none", len(entries))
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
	s.refreshes.Wait()
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
	s.refreshes.Wait()
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
	if v, _, _ := s.Load(bg, "k", 0, nil, never(t), decodeStr); v != (panicking{}) {
		t.Fatalf("Load = %v, want the value from memory", v)
	}
	if names := files(t, dir); len(names) != 0 {
		t.Errorf("files = %v, want none", names)
	}
}

type panicking struct{}

func (panicking) Encode() ([]byte, error) { panic("encoder bug") }
