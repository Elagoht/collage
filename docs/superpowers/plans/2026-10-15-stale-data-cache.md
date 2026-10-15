# Stale-on-failure data cache (v0.62.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `collage.Cached` keeps serving the last good value when the upstream fails or is slow. Invalidation marks values stale instead of deleting them, and the store persists to disk so that a restart comes up warm.

**Architecture:** `internal/datacache.Store` marks entries stale on invalidation and expiry, and serves stale values at once while a single detached background fetch replaces them. Values are typed through a small `types.CacheValue` interface that `Cached[T]` implements generically with JSON, which lets the store write them to `<Cache.Dir>/data/` and read them back lazily. A render that read a stale value is degraded: it is served but neither cached nor stored at a CDN.

**Tech Stack:** Go 1.25+ stdlib only (`encoding/json`, `crypto/sha256`, `os`, `log/slog`).

**Spec:** `docs/superpowers/specs/2026-10-15-stale-cache-design.md` (the v0.62.0 section).

## Global Constraints

- Never use the type `any`. The store's existing `any`s go away. `sync.Map` is not used, because its API is `any`.
- Branch `feat/stale-data-cache` in a git worktree, because other sessions share `~/Desktop/collage`.
- Minor bump: CHANGELOG `## v0.62.0`. **No tag, no push** without the user's go-ahead.
- Run tests with `go test -race -count=1`. The test cache hides breakage.
- Never use `git add -A` or `git add .`. Stage named files after reading `git status`.
- Comments match the repo's style: explain *why*, in full sentences, at the density of the surrounding code.
- Stale serving has no maximum age. The background refresh timeout is a fixed 30s (`datacache.RefreshTimeout`) and is not configurable.
- `ErrNotFound` never falls back. It removes the entry and its file.
- A disk failure never fails a load or a request. It is logged at warn.
- Deviations from the spec, both required by code the spec did not look at:
  1. The type identity in the store key also carries a **shape hash** (field names, types, json tags, recursively). `encoding/json` decodes a changed struct without error, so `PkgPath`+`String` alone would serve wrong-shaped data.
  2. A **static build and its header capture never serve stale data** (`types.WithFreshData`). The builder fails on a degraded render, and an export must not ship stale pages silently.

## Review Focus

1. **An action that invalidates and then redirects shows the editor the old content once.** This follows from stale-while-revalidate, which the user chose. It must be documented in `docs/actions.md`, and a test must pin that the *second* request after the redirect is fresh (Task 3, `TestCached_TheRequestAfterAStaleOneIsFresh`).
2. **A static export after an invalidation must contain the new data, never stale.** Pinned by `TestCached_AnExportAfterAnInvalidationIsFresh` (Task 3).
3. **Concurrent loads, invalidations and disk writes must leave disk equal to memory.** Writes can race. Pinned by `TestOpen_ConcurrentChangesLeaveDiskMatchingMemory` (Task 4).
4. **An interface-typed key (`NewKey[fmt.Stringer]`) cannot be decoded from JSON.** It must still work from memory and must not be persisted. Pinned by `TestCached_InterfaceKeysAreNotPersisted` (Task 1) and `TestOpen_AnUnencodableValueIsServedButNotWritten` (Task 4).
5. **A background refresh that panics must not crash the process**, because it runs on its own goroutine with nobody to recover it. It must not hold the key either. Pinned by `TestStore_APanickingRefreshKeepsTheStaleValue` (Task 2).

---

### Task 0: Worktree

- [ ] **Step 1: Create the worktree and branch**

```bash
cd /Users/furkan/Desktop/collage
git status --short          # expect clean
git worktree add ../collage-stale -b feat/stale-data-cache main
cd ../collage-stale
go test -count=1 ./internal/datacache/ ./internal/types/ ./pkg/collage/ 2>&1 | tail -5
```
Expected: all `ok`. Every later path is relative to `/Users/furkan/Desktop/collage-stale`.

---

### Task 1: Typed, encodable values and a stable store key

The behaviour does not change. The store swaps `any` for `types.CacheValue`, `Load` gains a `decode` parameter (unused until Task 4) and a `stale` result (always false until Task 2), and the store key becomes stable across processes.

**Files:**
- Modify: `internal/types/cached.go` (whole file below)
- Modify: `internal/types/cached_test.go`
- Modify: `internal/datacache/store.go` (signatures only)
- Modify: `internal/datacache/store_test.go` (mechanical)

**Interfaces:**
- Produces:
  - `types.CacheValue interface { Encode() ([]byte, error) }`
  - `types.DataCache interface { Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (CacheValue, error), decode func([]byte) (CacheValue, error)) (value CacheValue, stale bool, err error) }`
  - `(*datacache.Store).Load` with exactly that signature
  - unexported `types.storeKey(keyID) string` = `name + "\x00" + typeIdentity(typ)`

- [ ] **Step 1: Write the failing tests** (append to `internal/types/cached_test.go`; rewrite its `mapCache` and `wrongStore` as shown)

```go
// mapCache is a DataCache that keeps everything forever.
type mapCache map[string]CacheValue

func (m mapCache) Load(ctx context.Context, key string, _ time.Duration, _ []string, fetch func(context.Context) (CacheValue, error), _ func([]byte) (CacheValue, error)) (CacheValue, bool, error) {
	if v, ok := m[key]; ok {
		return v, false, nil
	}
	v, err := fetch(ctx)
	if err == nil {
		m[key] = v
	}
	return v, false, err
}

// wrongStore answers every Load with a value of its own choosing.
type wrongStore struct{ value CacheValue }

func (w wrongStore) Load(context.Context, string, time.Duration, []string, func(context.Context) (CacheValue, error), func([]byte) (CacheValue, error)) (CacheValue, bool, error) {
	return w.value, false, nil
}

type rawString string

func (r rawString) Encode() ([]byte, error) { return []byte(r), nil }
```

In `TestCached_AStoreReturningAnotherTypeIsAnError`, replace `wrongStore{value: "not an int"}` with `wrongStore{value: rawString("not an int")}`.

New tests:

```go
// A store key names its type the same way in every process, so a value written
// by one run is found by the next; and differently once the type's shape
// changes, so a value written for the old shape is not decoded into the new.
func TestStoreKey_StableAndShapeSensitive(t *testing.T) {
	type post struct{ Title string }
	a := storeKey(NewKey[post]("p").id())
	if b := storeKey(NewKey[post]("p").id()); a != b {
		t.Fatalf("one key, two store keys: %q / %q", a, b)
	}
	if strings.Contains(a, "0x") {
		t.Errorf("store key %q carries a pointer, which no other process shares", a)
	}
	changed := func() string {
		type post struct {
			Title string
			Body  string
		}
		return storeKey(NewKey[post]("p").id())
	}()
	if changed == a {
		t.Errorf("a post with a new field has the old post's store key %q", a)
	}
	tagged := func() string {
		type post struct {
			Title string `json:"headline"`
		}
		return storeKey(NewKey[post]("p").id())
	}()
	if tagged == a {
		t.Errorf("a renamed json field kept the store key %q", a)
	}
}

// A self-referencing type has a finite identity.
func TestStoreKey_RecursiveType(t *testing.T) {
	type node struct {
		Name     string
		Children []*node
	}
	if key := storeKey(NewKey[node]("n").id()); key == "" {
		t.Fatal("empty store key")
	}
}

// What Cached stores encodes as JSON and decodes back into the key's type.
func TestCached_ValuesRoundTripThroughJSON(t *testing.T) {
	type post struct{ Title string }
	encoded, err := cachedValue[post]{value: post{Title: "hi"}}.Encode()
	if err != nil || string(encoded) != `{"Title":"hi"}` {
		t.Fatalf("Encode = %s, %v", encoded, err)
	}
	decoded, err := decodeCached[post](encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.(cachedValue[post]).value; got.Title != "hi" {
		t.Errorf("decoded %+v", got)
	}
}

// An interface-typed value cannot be decoded from JSON — nothing says which
// concrete type to make — so it is not encoded either: kept in memory only.
func TestCached_InterfaceKeysAreNotPersisted(t *testing.T) {
	if _, err := (cachedValue[fmt.Stringer]{}).Encode(); err == nil {
		t.Error("an interface-typed value encoded")
	}
	if _, err := decodeCached[fmt.Stringer]([]byte(`null`)); err == nil {
		t.Error("an interface-typed value decoded")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test -count=1 ./internal/types/ -run 'StoreKey|Cached'`
Expected: compile errors (`CacheValue`, `cachedValue`, `decodeCached` undefined).

- [ ] **Step 3: Replace `internal/types/cached.go`**

```go
package types

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"
)

// CacheValue is one value the data cache holds: a caller's typed value that can
// write itself out, so the store can keep it on disk without knowing its type.
type CacheValue interface {
	Encode() ([]byte, error)
}

// DataCache is what Cached stores values in across renders. The framework
// supplies the store; the interface is declared beside RenderContext so a
// render can carry one without depending on how it is kept.
//
// decode turns what Encode wrote back into a value of the same type; a store
// that keeps values only in memory never calls it. stale reports a value the
// store kept after an invalidation or past its ttl, served because no fresher
// one is to be had yet.
type DataCache interface {
	Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (CacheValue, error), decode func([]byte) (CacheValue, error)) (value CacheValue, stale bool, err error)
}

// errInterfaceValue is why a value stored under an interface-typed key is kept in
// memory only: JSON records no concrete type to decode it back into.
var errInterfaceValue = errors.New("collage: a value of an interface type cannot be persisted")

// cachedValue is a T as the data cache holds it.
type cachedValue[T any] struct{ value T }

func (c cachedValue[T]) Encode() ([]byte, error) {
	if reflect.TypeFor[T]().Kind() == reflect.Interface {
		return nil, errInterfaceValue
	}
	return json.Marshal(c.value)
}

func decodeCached[T any](data []byte) (CacheValue, error) {
	if reflect.TypeFor[T]().Kind() == reflect.Interface {
		return nil, errInterfaceValue
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return cachedValue[T]{value: value}, nil
}

// BindDataCache, SkipDataCache, DeclaredTags: unchanged — keep them exactly as
// they are in the current file.

// Cached returns ... (keep the existing doc comment unchanged)
func Cached[T any](rc *RenderContext, key Key[T], ttl time.Duration, tags []string, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	id := key.id()
	if rc == nil || rc.state == nil {
		if fetch == nil {
			return zero, nil
		}
		return fetch(context.Background())
	}

	rc.state.mu.Lock()
	rc.state.tags = append(rc.state.tags, tags...)
	store, uncached := rc.state.data, rc.state.uncached
	rc.state.mu.Unlock()

	if store == nil || uncached {
		// Namespaced, so a Cached key and a Once key the application happens to
		// spell alike do not share a fetch.
		return Once(rc, Key[T]{name: "collage:cached:" + key.name}, fetch)
	}

	value, _, err := store.Load(rc.Context(), storeKey(id), ttl, tags, func(ctx context.Context) (CacheValue, error) {
		fetched, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		return cachedValue[T]{value: fetched}, nil
	}, decodeCached[T])
	if err != nil {
		return zero, err
	}
	typed, ok := value.(cachedValue[T])
	if !ok {
		// The store key carries T, so only a store handing back what it was not
		// given gets here. Saying so beats an empty section.
		return zero, fmt.Errorf("collage: Cached %q: the store returned a %T, want %s", key.name, value, id.typ)
	}
	return typed.value, nil
}

// storeKey is the string the store keeps a value under: the key's name and its
// type, so two keys of one name never share an entry. It is the same in every
// process, because a store on disk outlives the process that wrote it.
func storeKey(id keyID) string {
	return id.name + "\x00" + typeIdentity(id.typ)
}

var identities = struct {
	sync.Mutex
	byType map[reflect.Type]string
}{byType: make(map[reflect.Type]string)}

// typeIdentity names t the same way in every process that has it, and
// differently once its shape changes. encoding/json decodes a struct that gained
// or lost a field without complaint, so the name alone would hand the new build
// the old build's data, zeros where the new fields are.
func typeIdentity(t reflect.Type) string {
	identities.Lock()
	defer identities.Unlock()
	if id, ok := identities.byType[t]; ok {
		return id
	}
	sum := sha256.New()
	writeShape(sum, t, make(map[reflect.Type]bool))
	id := t.PkgPath() + "\x00" + t.String() + "\x00" + hex.EncodeToString(sum.Sum(nil)[:8])
	identities.byType[t] = id
	return id
}

// writeShape writes what decides how t encodes: its kind and name, and, through
// structs, pointers, slices, arrays and maps, the same of what it holds. seen
// stops a type that refers to itself.
func writeShape(w io.Writer, t reflect.Type, seen map[reflect.Type]bool) {
	fmt.Fprintf(w, "%s|%s|%s;", t.Kind(), t.PkgPath(), t.String())
	if seen[t] {
		return
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			field := t.Field(i)
			fmt.Fprintf(w, "{%s %q}", field.Name, field.Tag.Get("json"))
			writeShape(w, field.Type, seen)
		}
	case reflect.Map:
		writeShape(w, t.Key(), seen)
		writeShape(w, t.Elem(), seen)
	case reflect.Pointer, reflect.Slice, reflect.Array:
		writeShape(w, t.Elem(), seen)
	}
}
```

(When writing the file, copy `BindDataCache`, `SkipDataCache`, `DeclaredTags` and `Cached`'s doc comment verbatim from the current file. They do not change.)

- [ ] **Step 4: Change `internal/datacache/store.go` signatures**

Add the import `"github.com/Elagoht/collage/internal/types"`. Then:
- In `entry`, `value any // any: …` becomes `value types.CacheValue`.
- In `call`, `value any // any: see entry.value` becomes `value types.CacheValue`.
- `Load` becomes:

```go
func (s *Store) Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), decode func([]byte) (types.CacheValue, error)) (types.CacheValue, bool, error) {
	for {
		value, err, again := s.load(ctx, key, ttl, tags, fetch)
		if !again {
			return value, false, err
		}
	}
}
```
- In `load`, `store`: replace `any` with `types.CacheValue` and remove the `// any:` comments. `decode` is accepted and not used yet. Task 4 uses it.

- [ ] **Step 5: Update `internal/datacache/store_test.go` mechanically**

Add these helpers at the top and delete the old `counting`:

```go
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
```

Then, throughout the file:
- `func(context.Context) (any, error)` and `func(ctx context.Context) (any, error)` become the same with `types.CacheValue`.
- `return "x", nil` inside fetches becomes `return str("x"), nil`.
- Every `s.Load(ctx, k, ttl, tags, fetch)` becomes `s.Load(ctx, k, ttl, tags, fetch, decodeStr)`.
- Two-result uses `v, err := s.Load(...)` become `v, _, err := s.Load(...)`, and comparisons `v != "A"` become `v != str("A")`.
- In `TestStore_AWaiterOutlivesTheCallerThatStartedTheFetch`, `var value any` becomes `var value types.CacheValue`, followed by `value, _, err = s.Load(...)` and `got = string(value.(str))` guarded by `if value != nil`.

Imports: add `encoding/json` and `github.com/Elagoht/collage/internal/types`.

- [ ] **Step 6: Run everything that compiles against these types**

Run: `go build ./... && go vet ./internal/... && go test -race -count=1 ./internal/types/ ./internal/datacache/ ./internal/render/ ./internal/core/ ./pkg/collage/`
Expected: all `ok`. If something else implements `types.DataCache`, the compiler names it. Update it in the same way.

- [ ] **Step 7: Confirm no `any` remains in the touched files**

Run: `grep -n '\bany\b' internal/types/cached.go internal/datacache/store.go internal/datacache/store_test.go internal/types/cached_test.go`
Expected: only the generic constraints `[T any]`. A type constraint is not the type `any` in a value position, and the repo already uses it.

- [ ] **Step 8: Commit**

```bash
git status --short
git add internal/types/cached.go internal/types/cached_test.go internal/datacache/store.go internal/datacache/store_test.go
git commit -m "refactor: data cache holds typed, encodable values under a stable key

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Stale semantics in the store

**Files:**
- Modify: `internal/datacache/store.go` (whole file below)
- Modify: `internal/datacache/store_test.go`
- Modify: `internal/types/capture.go` (append `WithFreshData`, `WantsFreshData`)
- Modify: `internal/observability/metrics.go` (add `CacheStale`)
- Modify: `pkg/collage/collage.go` (re-export `CacheStale` beside `CacheCoalesced`)
- Modify: `internal/core/app.go:598` (construct with `Options`)

**Interfaces:**
- Consumes: Task 1's `types.CacheValue` and the `Load` signature.
- Produces:
  - `datacache.Options{MaxEntries int; Logger *slog.Logger; Metrics observability.Metrics}`
  - `datacache.New(Options) *Store`
  - `datacache.RefreshTimeout = 30 * time.Second`
  - `(*Store).Invalidate(tags []string) int`, which marks entries stale
  - `(*Store).Purge(tags []string) int`, which removes entries
  - `(*Store).Len() int`
  - `types.WithFreshData(ctx) context.Context` and `types.WantsFreshData(ctx) bool` (true also under `IsCapture`)
  - `observability.CacheStale CacheEvent = "stale"` and `collage.CacheStale`

- [ ] **Step 1: Write the failing tests** (append to `store_test.go`, and change every `New(0)` to `New(Options{})` and `New(2)` to `New(Options{MaxEntries: 2})`)

```go
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
	now := time.Now()
	s.now = func() time.Time { return now }
	var calls atomic.Int32
	s.Load(bg, "k", time.Minute, nil, counting(&calls, "old"), decodeStr)
	now = now.Add(2 * time.Minute)
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
	var calls atomic.Int32
	s.Load(bg, "k", 0, []string{"t"}, counting(&calls, "old"), decodeStr)
	s.Invalidate([]string{"t"})
	s.Load(bg, "k", 0, nil, func(context.Context) (types.CacheValue, error) { panic("upstream") }, decodeStr)
	s.refreshes.Wait()
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
```

Adapt three existing tests to the new semantics:
- `TestStore_Expires`: after `now = now.Add(2 * time.Second)` the Load returns the stale value and starts a refresh. Add `s.refreshes.Wait()` before the assertion. `calls == 2` still holds.
- `TestStore_InvalidateByTag`: rename the variable `dropped` to `marked`. After the invalidation, the Load of A returns stale and starts a refresh. Add `s.refreshes.Wait()` before the assertion. `calls == 3` still holds.
- Delete `TestStore_AFetchOverlappingAnInvalidationIsNotStored`. `TestStore_AFetchOverlappingAnInvalidationIsStoredStale` replaces it.

Imports to add: `fmt`, `github.com/Elagoht/collage/internal/observability`.

- [ ] **Step 2: Run and watch them fail**

Run: `go test -race -count=1 ./internal/datacache/`
Expected: compile errors (`Options`, `Purge`, `refreshes`, `types.WithFreshData`, `observability.CacheStale`).

- [ ] **Step 3: Add the fresh-data marker** (append to `internal/types/capture.go`)

```go
type freshDataKey struct{}

// WithFreshData returns ctx marked as a render that must not be built from
// stale cached data: a static export, which writes what it renders to files
// nobody re-renders, waits for the upstream instead.
func WithFreshData(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshDataKey{}, true)
}

// WantsFreshData reports whether ctx was marked by WithFreshData, or is a static
// build's header capture, whose headers are the export's too.
func WantsFreshData(ctx context.Context) bool {
	if IsCapture(ctx) {
		return true
	}
	fresh, _ := ctx.Value(freshDataKey{}).(bool)
	return fresh
}
```

- [ ] **Step 4: Add the event** (in `internal/observability/metrics.go`, after `CacheCoalesced`)

```go
	// CacheStale means collage.Cached served a value kept past an invalidation or
	// its ttl, because the fetch replacing it had not succeeded. It is reported
	// once per value served; the key is the Cached key's name. A count that
	// climbs is an upstream that is down.
	CacheStale CacheEvent = "stale"
```

In `pkg/collage/collage.go`, next to `CacheCoalesced = observability.CacheCoalesced`, add the same doc comment, shortened, and `CacheStale = observability.CacheStale`.

- [ ] **Step 5: Replace `internal/datacache/store.go`**

```go
// Package datacache keeps the data fragments fetch, across renders, so that twenty
// pages showing one author ask the upstream for that author once.
//
// The page cache stores what a render produced; this stores what renders are made
// from. It is bounded, keyed by the application's own keys, and marked stale by the
// same dependency tags the page cache is invalidated by. A stale value is still
// served while one fetch in the background replaces it: an upstream that is down
// or slow costs freshness, never the page.
package datacache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

// ErrFetchPanicked is what a caller waiting on a fetch is told when the fetch
// panicked. The panic itself goes to the caller whose fetch it was.
var ErrFetchPanicked = errors.New("collage: data fetch panicked")

// DefaultMaxEntries bounds a store given no bound of its own.
const DefaultMaxEntries = 10000

// RefreshTimeout bounds a background refresh. It is not a request's timeout:
// nobody waits on a refresh, and an upstream slower than a page's patience may
// still answer in time for the next one.
const RefreshTimeout = 30 * time.Second

// Options configures a Store.
type Options struct {
	// MaxEntries bounds the store: 0 means DefaultMaxEntries and a negative
	// value means unbounded.
	MaxEntries int
	// Logger receives failed refreshes. Nil means slog.Default().
	Logger *slog.Logger
	// Metrics receives a CacheStale event for each stale value served. Nil
	// means none.
	Metrics observability.Metrics
}

// Store is a bounded, tag-invalidated cache of fetched values. It is safe for
// concurrent use.
type Store struct {
	mu       sync.Mutex
	max      int
	entries  map[string]*list.Element
	order    *list.List // front is most recently used
	byTag    map[string]map[string]struct{}
	inflight map[string]*call
	// epoch counts invalidations and purges. A fetch that started before one
	// brought back what it was meant to replace: it is stored, but stale.
	epoch uint64
	// purges counts purges. A fetch that started before one stores nothing,
	// since the application asked for the value to be gone.
	purges         uint64
	now            func() time.Time
	logger         *slog.Logger
	metrics        observability.Metrics
	refreshTimeout time.Duration
	// refreshes counts background refreshes still running.
	refreshes sync.WaitGroup
}

type entry struct {
	key     string
	value   types.CacheValue
	expires time.Time
	tags    []string
	// stale is set by an invalidation; an entry past expires is stale as well.
	stale      bool
	staleSince time.Time
}

type call struct {
	done  chan struct{}
	value types.CacheValue
	err   error
	// abandoned reports a fetch that failed because the caller that started it
	// went away: an answer about that caller, not about the key.
	abandoned bool
}

// New returns an empty store kept in memory only.
func New(opts Options) *Store {
	maxEntries := opts.MaxEntries
	if maxEntries == 0 {
		maxEntries = DefaultMaxEntries
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		max:            maxEntries,
		entries:        make(map[string]*list.Element),
		order:          list.New(),
		byTag:          make(map[string]map[string]struct{}),
		inflight:       make(map[string]*call),
		now:            time.Now,
		logger:         logger,
		metrics:        observability.MetricsOrNoop(opts.Metrics),
		refreshTimeout: RefreshTimeout,
	}
}

// Load returns the value stored under key, fetching it when there is none.
// Concurrent loads of one key share one fetch. A ttl of zero or less keeps the
// value fresh until its tags are invalidated.
//
// A value past its ttl, or one whose tags were invalidated, is stale: Load
// returns it at once with stale true, and starts one fetch in the background to
// replace it. That fetch is not the caller's: it runs detached from ctx, under
// RefreshTimeout, so a reader who leaves does not cancel it. If it fails the
// stale value stays and is served again; if it reports types.ErrNotFound the
// value is dropped, so the next Load fetches and returns that error.
//
// With no value at all there is nothing to serve meanwhile, and Load waits for
// the fetch, whose error, if any, goes to every caller waiting on it and is
// never stored. A ctx marked with types.WantsFreshData waits the same way on a
// stale value, and gets the upstream's error rather than the value.
//
// decode is not used by a store kept in memory.
func (s *Store) Load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), decode func([]byte) (types.CacheValue, error)) (types.CacheValue, bool, error) {
	for {
		value, stale, err, again := s.load(ctx, key, ttl, tags, fetch)
		if !again {
			return value, stale, err
		}
	}
}

// load is one attempt at Load. again reports that it waited on a fetch its
// starter abandoned, and the caller should try once more.
func (s *Store) load(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error)) (value types.CacheValue, stale bool, err error, again bool) {
	s.mu.Lock()
	if element, ok := s.entries[key]; ok {
		stored := element.Value.(*entry)
		if !s.staleLocked(stored) {
			s.order.MoveToFront(element)
			s.mu.Unlock()
			return stored.value, false, nil, false
		}
		if !types.WantsFreshData(ctx) {
			s.order.MoveToFront(element)
			s.refreshLocked(ctx, key, ttl, tags, fetch)
			value := stored.value
			s.mu.Unlock()
			s.metrics.CacheEvent(ctx, observability.CacheStale, keyName(key))
			return value, true, nil, false
		}
		// Fresh data wanted: wait below like a miss. The stale value stays
		// until a successful fetch replaces it.
	}
	if running, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		select {
		case <-running.done:
			// Not the starter's cancellation: one reader disconnecting, on
			// purpose or not, would fail every reader who shared its fetch.
			if running.abandoned && ctx.Err() == nil {
				return nil, false, nil, true
			}
			return running.value, false, running.err, false
		case <-ctx.Done():
			return nil, false, ctx.Err(), false
		}
	}
	started := &call{done: make(chan struct{})}
	s.inflight[key] = started
	epoch, purges := s.epoch, s.purges
	s.mu.Unlock()

	// A fetch that panics still gives the key up and answers its waiters. The
	// render recovers the panic further up, and without this the key would
	// stay in flight for good: every later load waits on it and none fetches.
	finished := false
	defer func() {
		if finished {
			return
		}
		started.err = ErrFetchPanicked
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		close(started.done)
	}()
	value, err = fetch(ctx)
	finished = true
	started.value, started.err = value, err
	started.abandoned = err != nil && ctx.Err() != nil

	s.mu.Lock()
	delete(s.inflight, key)
	s.settleLocked(key, value, err, ttl, tags, epoch, purges)
	s.mu.Unlock()
	close(started.done)
	return value, false, err, false
}

// refreshLocked starts the background fetch replacing key's stale value,
// unless one is already running. It must be called with s.mu held.
func (s *Store) refreshLocked(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error)) {
	if _, running := s.inflight[key]; running {
		return
	}
	started := &call{done: make(chan struct{})}
	s.inflight[key] = started
	epoch, purges := s.epoch, s.purges
	s.refreshes.Add(1)
	go s.refresh(context.WithoutCancel(ctx), key, ttl, tags, fetch, started, epoch, purges)
}

func (s *Store) refresh(ctx context.Context, key string, ttl time.Duration, tags []string, fetch func(context.Context) (types.CacheValue, error), started *call, epoch, purges uint64) {
	defer s.refreshes.Done()
	ctx, cancel := context.WithTimeout(ctx, s.refreshTimeout)
	defer cancel()
	value, err := guarded(ctx, fetch)
	started.value, started.err = value, err

	s.mu.Lock()
	delete(s.inflight, key)
	var staleFor time.Duration
	if element, ok := s.entries[key]; ok && err != nil {
		staleFor = s.now().Sub(s.staleSinceLocked(element.Value.(*entry)))
	}
	s.settleLocked(key, value, err, ttl, tags, epoch, purges)
	s.mu.Unlock()
	close(started.done)

	if err != nil && !errors.Is(err, types.ErrNotFound) {
		s.logger.Warn("collage: serving stale data, its refresh failed",
			"key", keyName(key), "stale_for", staleFor, "err", err)
	}
}

// settleLocked records what a fetch brought back: a value is stored, stale if
// an invalidation overtook the fetch and not at all if a purge did; not found
// drops what was there; any other error leaves it. It must be called with s.mu
// held.
func (s *Store) settleLocked(key string, value types.CacheValue, err error, ttl time.Duration, tags []string, epoch, purges uint64) {
	switch {
	case err == nil:
		if purges == s.purges {
			s.storeLocked(key, value, ttl, tags, epoch != s.epoch)
		}
	case errors.Is(err, types.ErrNotFound):
		if element, ok := s.entries[key]; ok {
			s.removeLocked(element)
		}
	}
}

// guarded runs fetch, turning a panic into ErrFetchPanicked: a refresh runs on
// its own goroutine, where nothing above it would recover one.
func guarded(ctx context.Context, fetch func(context.Context) (types.CacheValue, error)) (value types.CacheValue, err error) {
	defer func() {
		if r := recover(); r != nil {
			value, err = nil, fmt.Errorf("%w: %v", ErrFetchPanicked, r)
		}
	}()
	return fetch(ctx)
}

// Invalidate marks every value stored under any of tags stale and returns how
// many it marked. They are still served until a fetch replaces them. A fetch
// running when it is called stores what it brings back as stale.
func (s *Store) Invalidate(tags []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	now := s.now()
	marked := 0
	for _, tag := range tags {
		for key := range s.byTag[tag] {
			if element, ok := s.entries[key]; ok {
				s.markStaleLocked(element.Value.(*entry), now)
				marked++
			}
		}
	}
	return marked
}

// Purge removes every value stored under any of tags and returns how many it
// removed. A fetch running when it is called stores nothing.
func (s *Store) Purge(tags []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	s.purges++
	removed := 0
	for _, tag := range tags {
		for key := range s.byTag[tag] {
			if element, ok := s.entries[key]; ok {
				s.removeLocked(element)
				removed++
			}
		}
	}
	return removed
}

// Len reports how many values are stored, stale ones included.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *Store) staleLocked(e *entry) bool {
	return e.stale || (!e.expires.IsZero() && !s.now().Before(e.expires))
}

func (s *Store) staleSinceLocked(e *entry) time.Time {
	if e.stale {
		return e.staleSince
	}
	return e.expires
}

func (s *Store) markStaleLocked(e *entry, now time.Time) {
	if !e.stale {
		e.stale, e.staleSince = true, now
	}
}

// storeLocked must be called with s.mu held.
func (s *Store) storeLocked(key string, value types.CacheValue, ttl time.Duration, tags []string, stale bool) {
	if element, ok := s.entries[key]; ok {
		s.removeLocked(element)
	}
	stored := &entry{key: key, value: value, tags: append([]string(nil), tags...)}
	now := s.now()
	if ttl > 0 {
		stored.expires = now.Add(ttl)
	}
	if stale {
		s.markStaleLocked(stored, now)
	}
	s.entries[key] = s.order.PushFront(stored)
	s.indexLocked(stored)
	for s.max > 0 && len(s.entries) > s.max {
		s.removeLocked(s.order.Back())
	}
}

// indexLocked must be called with s.mu held.
func (s *Store) indexLocked(e *entry) {
	for _, tag := range e.tags {
		keys, ok := s.byTag[tag]
		if !ok {
			keys = make(map[string]struct{})
			s.byTag[tag] = keys
		}
		keys[e.key] = struct{}{}
	}
}

// removeLocked must be called with s.mu held.
func (s *Store) removeLocked(element *list.Element) {
	stored := element.Value.(*entry)
	s.order.Remove(element)
	delete(s.entries, stored.key)
	for _, tag := range stored.tags {
		if keys, ok := s.byTag[tag]; ok {
			delete(keys, stored.key)
			if len(keys) == 0 {
				delete(s.byTag, tag)
			}
		}
	}
}

// keyName is the part of a store key the application chose, for logs and
// metrics: types.storeKey appends the type after a NUL.
func keyName(key string) string {
	name, _, _ := strings.Cut(key, "\x00")
	return name
}
```

- [ ] **Step 6: Construct with Options** (`internal/core/app.go`, at the `app.data = datacache.New(...)` line)

```go
		app.data = datacache.New(datacache.Options{MaxEntries: cfg.Cache.MaxEntries, Logger: logger, Metrics: metrics})
```
`logger` and `metrics` are already locals by then (lines ~483 and ~546). If `metrics` is declared after this line, move this statement below its declaration.

- [ ] **Step 7: Run the tests**

Run: `go test -race -count=1 ./internal/datacache/ ./internal/types/ ./internal/observability/`
Expected: `ok`.

Run: `go build ./... && go test -race -count=1 ./pkg/collage/ -run Cached`
Expected: `TestCached_InvalidatesWithThePages` FAILS, because the first request after the invalidation now shows the stale name. Task 3 fixes that test. Everything else passes.

- [ ] **Step 8: Commit**

```bash
git status --short
git add internal/datacache/store.go internal/datacache/store_test.go internal/types/capture.go internal/observability/metrics.go pkg/collage/collage.go internal/core/app.go
git commit -m "feat: data cache serves stale values while one background fetch replaces them

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: A render built from stale data is degraded

**Files:**
- Modify: `internal/types/context.go` (add field `stale bool` to the render state struct, beside `uncached`)
- Modify: `internal/types/cached.go` (record stale; add `ServedStale`)
- Modify: `internal/render/engine.go` (`Result.Stale`, `Degraded()`)
- Modify: `internal/render/document.go` (`DocumentResult.Stale`)
- Modify: `internal/httpx/document.go` (do not cache a stale document; send `no-store`)
- Modify: `internal/build/builder.go:358` (`Build` marks its ctx with `WithFreshData`)
- Test: `internal/types/cached_test.go`, `pkg/collage/cached_test.go`

**Interfaces:**
- Consumes: `types.DataCache.Load` returning `stale`, and `types.WithFreshData` (Task 2).
- Produces: `types.ServedStale(rc *RenderContext) bool`, `render.Result.Stale bool`, `render.DocumentResult.Stale bool`.

- [ ] **Step 1: Write the failing tests**

In `internal/types/cached_test.go`:

```go
// staleCache serves every key as a stale "old".
type staleCache struct{}

func (staleCache) Load(context.Context, string, time.Duration, []string, func(context.Context) (CacheValue, error), func([]byte) (CacheValue, error)) (CacheValue, bool, error) {
	return cachedValue[string]{value: "old"}, true, nil
}

// A render that read a stale value says so, so it is not cached as if fresh.
func TestCached_RecordsAStaleRead(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	BindDataCache(rc, mapCache{})
	Cached(rc, NewKey[string]("a"), 0, nil, func(context.Context) (string, error) { return "a", nil })
	if ServedStale(rc) {
		t.Fatal("a fresh read was recorded as stale")
	}
	BindDataCache(rc, staleCache{})
	if v, err := Cached(rc, NewKey[string]("b"), 0, nil, func(context.Context) (string, error) { return "new", nil }); v != "old" || err != nil {
		t.Fatalf("Cached = %q, %v", v, err)
	}
	if !ServedStale(rc) {
		t.Error("a stale read was not recorded")
	}
}
```

In `pkg/collage/cached_test.go`, change the site so that tests can fail the upstream and use a disk cache. Add a `failure error` field to `authorSite`. Rename `newAuthorSite(t, devMode)` to delegate:

```go
func newAuthorSite(t *testing.T, devMode bool) *authorSite {
	t.Helper()
	return newAuthorSiteWith(t, devMode, collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour})
}

func newAuthorSiteWith(t *testing.T, devMode bool, cacheConfig collage.CacheConfig) *authorSite {
	// … the current body of newAuthorSite, with `Cache: cacheConfig`, and the
	// card's fetch becoming:
	//
	//	func(context.Context) (author, error) {
	//		site.mu.Lock()
	//		defer site.mu.Unlock()
	//		if site.failure != nil {
	//			return author{}, site.failure
	//		}
	//		site.calls[id]++
	//		return author{Name: site.names[id]}, nil
	//	}
}

func (s *authorSite) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failure = err
}

func (s *authorSite) serve(t *testing.T, i int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/p/%d", i), nil))
	return rec
}

// eventually re-requests page i until check passes, for what a background
// refresh changes; it fails after two seconds.
func (s *authorSite) eventually(t *testing.T, i int, what string, check func(*httptest.ResponseRecorder) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec := s.serve(t, i)
		if check(rec) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("page %d never showed %s; last: %d %q", i, what, rec.Code, rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
```

Replace the body of `TestCached_InvalidatesWithThePages` after the `InvalidateTagsN` check with:

```go
	// The first request after the invalidation is served the stale author at
	// once, and starts the refresh; a later one shows the new name.
	if body := site.get(t, 3, false); body != "<article><p>Ada</p></article>" {
		t.Errorf("A's page right after the invalidation = %q, want the stale name", body)
	}
	site.eventually(t, 3, "the new name", func(rec *httptest.ResponseRecorder) bool {
		return rec.Body.String() == "<article><p>Ada Lovelace</p></article>"
	})
	if body := site.get(t, 25, false); body != "<article><p>Bo</p></article>" {
		t.Errorf("B's page = %q", body)
	}
	if got := site.fetched(); got["A"] != 2 || got["B"] != 1 {
		t.Errorf("fetched %v, want A twice and B once", got)
	}
```

New tests:

```go
// A page built from stale data is served, but neither cached here nor at a CDN:
// otherwise the old name would be pinned for the page's whole TTL.
func TestCached_TheRequestAfterAStaleOneIsFresh(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()
	site.app.InvalidateTags(context.Background(), "author:A")

	rec := site.serve(t, 3)
	if rec.Body.String() != "<article><p>Ada</p></article>" {
		t.Fatalf("stale render = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control on a stale render = %q, want no-store", cc)
	}
	site.eventually(t, 3, "the new name", func(rec *httptest.ResponseRecorder) bool {
		return rec.Body.String() == "<article><p>Ada Lovelace</p></article>"
	})
}

// The point of it all: invalidated, upstream down, the site still answers.
func TestCached_AnInvalidatedPageRendersWhileTheUpstreamIsDown(t *testing.T) {
	site := newAuthorSite(t, false)
	for i := range 30 {
		site.get(t, i, false)
	}
	site.fail(errors.New("cms down"))
	site.app.InvalidateTags(context.Background(), "author:A", "author:B")
	for i := range 30 {
		rec := site.serve(t, i)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<p>") {
			t.Fatalf("page %d with the upstream down = %d %q", i, rec.Code, rec.Body.String())
		}
	}
}

// Content removed upstream leaves the site: not found never falls back. The
// author card sits in a required slot, so its data's not found is the page's
// 404 (see pkg/collage/fragmentpath_test.go for the rule this relies on; if the
// card's failure is a 500 here instead, make the card's slot hold a Required
// fragment as that test does — the point under test is that the page stops
// showing the stale author, not which status the slot policy picks).
func TestCached_NotFoundUpstreamBecomesA404(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.fail(fmt.Errorf("author A: %w", collage.ErrNotFound))
	site.app.InvalidateTags(context.Background(), "author:A")
	site.eventually(t, 3, "a 404", func(rec *httptest.ResponseRecorder) bool {
		return rec.Code == http.StatusNotFound
	})
}

// An export writes files nobody re-renders, so it waits for fresh data.
func TestCached_AnExportAfterAnInvalidationIsFresh(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()
	site.app.InvalidateTags(context.Background(), "author:A")
	out := t.TempDir()
	if _, err := collage.Build(context.Background(), site.app, collage.BuildOptions{OutDir: out, Concurrency: 4}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "p", "3", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Ada Lovelace") {
		t.Errorf("exported page = %q, want the new name", data)
	}
}
```

(Check the export's file layout against an existing `collage.Build` test in `pkg/collage`, for example by grepping `index.html` in `*_test.go`, and use its path form. Add the imports `errors`, `os`, `path/filepath`.)

- [ ] **Step 2: Run and watch them fail**

Run: `go test -race -count=1 ./internal/types/ ./pkg/collage/ -run 'Cached'`
Expected: `ServedStale` is undefined. Once that is stubbed, `TheRequestAfterAStaleOneIsFresh` fails on `Cache-Control` and `AnExportAfterAnInvalidationIsFresh` fails on the stale name.

- [ ] **Step 3: Record stale reads** (`internal/types/context.go` and `cached.go`)

In the render state struct, after `uncached bool`:

```go
	// stale is set when Cached served a value kept past an invalidation or its
	// ttl. A render built from one is degraded: served, never cached.
	stale bool
```

In `Cached`, replace `value, _, err := store.Load(` with `value, stale, err := store.Load(`, and after the `err != nil` return add:

```go
	if stale {
		rc.state.mu.Lock()
		rc.state.stale = true
		rc.state.mu.Unlock()
	}
```

Append to `cached.go`:

```go
// ServedStale reports whether any Cached call in this render was served a
// stale value.
func ServedStale(rc *RenderContext) bool {
	if rc == nil || rc.state == nil {
		return false
	}
	rc.state.mu.Lock()
	defer rc.state.mu.Unlock()
	return rc.state.stale
}
```

- [ ] **Step 4: Make the page result degraded** (`internal/render/engine.go`)

Add to `Result`:

```go
	// Stale reports that the render read a value collage.Cached kept past an
	// invalidation or its ttl. It makes the result Degraded.
	Stale bool
```

In the `result := &Result{...}` literal (line ~319), add `Stale: types.ServedStale(rc),`. In `Degraded()`, after the nil check, add:

```go
	if r.Stale {
		return true
	}
```
Update `Degraded`'s doc comment: "…whether any fragment in the render failed, whether or not a fallback covered for it, or the render was built from stale cached data."

- [ ] **Step 5: Documents** (`internal/render/document.go` and `internal/httpx/document.go`)

Add `Stale bool` (with the same comment as above) to `DocumentResult`, and before `if err != nil {` in `ExecuteDocument` add `result.Stale = types.ServedStale(rc)`.

In `internal/httpx/document.go`:
- `if cacheable && r.Method == http.MethodGet {` becomes `if cacheable && r.Method == http.MethodGet && !result.Stale {`
- `return &outcome{content: result.Body, etag: etag, renderTime: result.Timing.Total}` becomes `return &outcome{content: result.Body, etag: etag, renderTime: result.Timing.Total, degraded: result.Stale}`
- after `h.setCacheHeaders(header, r, doc.Strategy, doc.CacheTTL)`:

```go
	// Built from stale data, so not cached here; not at a CDN either.
	if out.degraded {
		header.Set("Cache-Control", "no-store")
	}
```

- [ ] **Step 6: Exports wait for fresh data** (`internal/build/builder.go`, first line of `func (b *Builder) Build(ctx context.Context)`)

```go
	// An export writes files nothing re-renders: it waits for the upstream
	// rather than writing what the data cache kept past an invalidation.
	ctx = types.WithFreshData(ctx)
```

- [ ] **Step 7: Run the tests**

Run: `go test -race -count=1 ./internal/types/ ./internal/render/ ./internal/httpx/ ./internal/build/ ./pkg/collage/`
Expected: `ok`.

- [ ] **Step 8: Commit**

```bash
git status --short
git add internal/types/context.go internal/types/cached.go internal/types/cached_test.go internal/render/engine.go internal/render/document.go internal/httpx/document.go internal/build/builder.go pkg/collage/cached_test.go
git commit -m "feat: a render built from stale data is degraded; exports wait for fresh data

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Persistence to disk

**Files:**
- Create: `internal/datacache/disk.go`
- Modify: `internal/datacache/store.go`
- Create: `internal/datacache/disk_test.go`

**Interfaces:**
- Consumes: Task 2's `Store` and `Options`.
- Produces: `datacache.Open(dir string, opts Options) (*Store, error)`.

- [ ] **Step 1: Write the failing tests** (`internal/datacache/disk_test.go`)

```go
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
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test -race -count=1 ./internal/datacache/ -run Open`
Expected: `Open` is undefined.

- [ ] **Step 3: Create `internal/datacache/disk.go`**

```go
package datacache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// formatVersion is written into every file; a file of any other version is
// removed at startup rather than guessed at.
const formatVersion = 1

// header is the first line of an entry's file. The encoded value follows it.
type header struct {
	Version    int       `json:"v"`
	Key        string    `json:"key"`
	Tags       []string  `json:"tags,omitempty"`
	Expires    time.Time `json:"expires,omitzero"`
	Stale      bool      `json:"stale,omitempty"`
	StaleSince time.Time `json:"staleSince,omitzero"`
	Written    time.Time `json:"written"`
}

type record struct {
	header  header
	payload []byte
}

var errCorrupt = errors.New("not a data cache entry")

// disk keeps one file per entry in dir, named by the hash of its key. It is
// not namespaced by build: data is not a build's output, and a new deploy
// with its upstream down should still render from yesterday's.
type disk struct{ dir string }

func (d *disk) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:]))
}

// write replaces key's file atomically: a reader, or a crash, sees the old
// file or the new one, never half of either.
func (d *disk) write(h header, payload []byte) error {
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(append(append(line, '\n'), payload...))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, d.path(h.Key))
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

func (d *disk) remove(key string) error {
	if err := os.Remove(d.path(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// load reads every entry in the directory, newest first. Leftover temporary
// files and files that are not entries are removed.
func (d *disk) load() ([]record, error) {
	dirEntries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	var records []record
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() {
			continue
		}
		path := filepath.Join(d.dir, dirEntry.Name())
		if strings.HasPrefix(dirEntry.Name(), ".tmp-") {
			_ = os.Remove(path)
			continue
		}
		rec, err := readRecord(path)
		if err != nil || d.path(rec.header.Key) != path {
			_ = os.Remove(path)
			continue
		}
		records = append(records, rec)
	}
	slices.SortFunc(records, func(a, b record) int { return b.header.Written.Compare(a.header.Written) })
	return records, nil
}

func readRecord(path string) (record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record{}, err
	}
	line, payload, ok := bytes.Cut(data, []byte{'\n'})
	if !ok {
		return record{}, errCorrupt
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil || h.Version != formatVersion || h.Key == "" {
		return record{}, fmt.Errorf("%w: %s", errCorrupt, filepath.Base(path))
	}
	return record{header: h, payload: payload}, nil
}
```

- [ ] **Step 4: Wire the disk into the store** (`internal/datacache/store.go`)

4a. New fields on `Store`, after `refreshes`:

```go
	// disk is nil for a store kept in memory only.
	disk *disk
	// pending holds the disk writes the current critical section made; unlock
	// performs them after releasing mu.
	pending []diskOp
	// gens holds each key's latest change, numbered from seq. A disk write
	// carries the number of the change it records and is skipped once a later
	// change exists, so writes that finish out of order cannot leave an older
	// value on disk.
	gens map[string]uint64
	seq  uint64
	// diskMu orders disk writes. It is never taken while mu is held.
	diskMu sync.Mutex
	// unencodable remembers the key names already logged as not persistable;
	// guarded by diskMu.
	unencodable map[string]bool
```

4b. New field on `entry`, after `value`:

```go
	// raw is the encoded value as read from disk, decoded on first load. While
	// it is set, value is nil.
	raw []byte
```

4c. The op type and helpers (append to `store.go`):

```go
// diskOp records one change to an entry for the disk: its whole current state,
// or its removal.
type diskOp struct {
	key    string
	gen    uint64
	remove bool
	header header
	value  types.CacheValue
	raw    []byte
}

// changedLocked records that key changed and, for a store on disk, queues the
// write that records it. e is nil when the entry was removed. It must be called
// with s.mu held.
func (s *Store) changedLocked(key string, e *entry) {
	if s.disk == nil {
		return
	}
	s.seq++
	s.gens[key] = s.seq
	op := diskOp{key: key, gen: s.seq, remove: e == nil}
	if e != nil {
		op.header = header{
			Version: formatVersion, Key: key, Tags: e.tags, Expires: e.expires,
			Stale: e.stale, StaleSince: e.staleSince, Written: s.now(),
		}
		op.value, op.raw = e.value, e.raw
	}
	s.pending = append(s.pending, op)
}

// unlock releases s.mu and writes what the critical section changed to disk.
func (s *Store) unlock() {
	ops := s.pending
	s.pending = nil
	s.mu.Unlock()
	s.flush(ops)
}

func (s *Store) flush(ops []diskOp) {
	if len(ops) == 0 {
		return
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	for _, op := range ops {
		s.mu.Lock()
		current := s.gens[op.key] == op.gen
		s.mu.Unlock()
		if !current {
			continue
		}
		var err error
		if op.remove {
			err = s.disk.remove(op.key)
			s.mu.Lock()
			if s.gens[op.key] == op.gen {
				delete(s.gens, op.key)
			}
			s.mu.Unlock()
		} else {
			err = s.writeOp(op)
		}
		if err != nil {
			s.logger.Warn("collage: data cache disk write failed", "key", keyName(op.key), "err", err)
		}
	}
}

// writeOp writes op's entry. A value that cannot be encoded is kept in memory
// only, and an older copy of it on disk is removed so a restart does not
// bring it back.
func (s *Store) writeOp(op diskOp) error {
	payload := op.raw
	if payload == nil {
		encoded, err := op.value.Encode()
		if err != nil {
			if name := keyName(op.key); !s.unencodable[name] {
				s.unencodable[name] = true
				s.logger.Warn("collage: data cache value kept in memory only", "key", name, "err", err)
			}
			return s.disk.remove(op.key)
		}
		payload = encoded
	}
	return s.disk.write(op.header, payload)
}

// Open returns a store that keeps its values in dir as well as in memory, and
// starts with what dir already holds: the newest entries, up to the bound, the
// rest removed. Their values are decoded on first load.
func Open(dir string, opts Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("collage: data cache directory: %w", err)
	}
	s := New(opts)
	s.disk = &disk{dir: dir}
	s.gens = make(map[string]uint64)
	s.unencodable = make(map[string]bool)
	records, err := s.disk.load()
	if err != nil {
		return nil, fmt.Errorf("collage: data cache directory: %w", err)
	}
	for _, rec := range records {
		if s.max > 0 && len(s.entries) >= s.max {
			_ = s.disk.remove(rec.header.Key)
			continue
		}
		raw := rec.payload
		if raw == nil {
			raw = []byte{}
		}
		stored := &entry{
			key: rec.header.Key, raw: raw, expires: rec.header.Expires, tags: rec.header.Tags,
			stale: rec.header.Stale, staleSince: rec.header.StaleSince,
		}
		s.entries[stored.key] = s.order.PushBack(stored)
		s.indexLocked(stored)
	}
	return s, nil
}
```
`store.go` needs the `os` import for `Open`.

4d. Every change notifies the disk:
- At the end of `storeLocked`, after `s.indexLocked(stored)` and **before** the eviction loop, add `s.changedLocked(key, stored)`.
- In `removeLocked`, at the end, add `s.changedLocked(stored.key, nil)`.
- In `markStaleLocked`, inside the `if !e.stale` block, add `s.changedLocked(e.key, e)`.

4e. Writes happen after unlocking:
- In `load`: replace the `s.mu.Unlock()` that follows `s.settleLocked(...)` with `s.unlock()`.
- In `refresh`: replace the `s.mu.Unlock()` after `s.settleLocked(...)` with `s.unlock()`.
- In `Invalidate` and `Purge`: replace `defer s.mu.Unlock()` with `defer s.unlock()`.

4f. Decode an entry read from disk on its first load. At the top of `load`, replace the first `s.mu.Lock()` with:

```go
	s.mu.Lock()
	if element, ok := s.entries[key]; ok && element.Value.(*entry).raw != nil {
		// Decoded outside the lock: a large value must not stall every other
		// key. Whoever finishes first installs it.
		stored := element.Value.(*entry)
		raw := stored.raw
		s.mu.Unlock()
		decoded, decodeErr := decode(raw)
		s.mu.Lock()
		if current, still := s.entries[key]; still && current == element && stored.raw != nil {
			if decodeErr != nil {
				// The type changed between builds in a way its identity
				// did not catch. A miss, never the page's error.
				s.logger.Warn("collage: dropping a stored value that no longer decodes", "key", keyName(key), "err", decodeErr)
				s.removeLocked(element)
				s.unlock()
				s.mu.Lock()
			} else {
				stored.value, stored.raw = decoded, nil
			}
		}
	}
```
`load` now needs `decode`, so add it to `load`'s parameters and pass it from `Load`.

- [ ] **Step 5: Run the tests**

Run: `go test -race -count=1 ./internal/datacache/`
Expected: `ok`. Run it 20 times to catch races: `go test -race -count=20 ./internal/datacache/ -run 'Open|Stale|Refresh'`.

- [ ] **Step 6: Commit**

```bash
git status --short
git add internal/datacache/disk.go internal/datacache/disk_test.go internal/datacache/store.go
git commit -m "feat: data cache persists to disk and starts warm from it

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: App wiring and `PurgeTags`

**Files:**
- Modify: `internal/core/cache.go` (add `buildDataCache`)
- Modify: `internal/core/app.go` (use it; `PurgeTags`; split `InvalidateTagsN` into a shared helper)
- Modify: `pkg/collage/app.go` (`PurgeTags`, and the `InvalidateTags`/`InvalidateTagsN` doc comments)
- Test: `pkg/collage/cached_test.go`

**Interfaces:**
- Consumes: `datacache.Open`, `datacache.New(Options)`, `(*Store).Invalidate`, `(*Store).Purge`.
- Produces: `func (a *App) PurgeTags(ctx context.Context, tags ...string) error` in both `internal/core` and `pkg/collage`.

- [ ] **Step 1: Write the failing tests** (`pkg/collage/cached_test.go`)

```go
func diskCache(dir, version string) collage.CacheConfig {
	return collage.CacheConfig{Enabled: true, Type: "disk", Dir: dir, Version: version, DefaultTTL: time.Hour}
}

// A new deploy — a new build, so a cold page cache — with its upstream down
// still renders, from the data the last one fetched.
func TestCached_ARestartRendersFromPersistedData(t *testing.T) {
	dir := t.TempDir()
	first := newAuthorSiteWith(t, false, diskCache(dir, "build-1"))
	first.get(t, 3, false)

	second := newAuthorSiteWith(t, false, diskCache(dir, "build-2"))
	second.fail(errors.New("cms down"))
	rec := second.serve(t, 3)
	if rec.Code != http.StatusOK || rec.Body.String() != "<article><p>Ada</p></article>" {
		t.Fatalf("after restart = %d %q", rec.Code, rec.Body.String())
	}
	if got := second.fetched(); got["A"] != 0 {
		t.Errorf("fetched %v after restart, want nothing", got)
	}
}

// A data directory that cannot be made costs persistence, not the site.
func TestCached_AnUnusableDataDirectoryFallsBackToMemory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	site := newAuthorSiteWith(t, false, diskCache(dir, "b"))
	if rec := site.serve(t, 3); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// PurgeTags is the hard delete: nothing stale is left to serve.
func TestPurgeTags_LeavesNothingToServe(t *testing.T) {
	site := newAuthorSite(t, false)
	site.get(t, 3, false)
	site.mu.Lock()
	site.names["A"] = "Ada Lovelace"
	site.mu.Unlock()
	if err := site.app.PurgeTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	if body := site.get(t, 3, false); body != "<article><p>Ada Lovelace</p></article>" {
		t.Errorf("after PurgeTags = %q, want the new name at once", body)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test -race -count=1 ./pkg/collage/ -run 'Restart|UnusableData|PurgeTags'`
Expected: `PurgeTags` is undefined. The restart test fetches, because the data store is memory-only.

- [ ] **Step 3: `buildDataCache`** (append to `internal/core/cache.go`; imports `path/filepath`, `datacache`, `observability`)

```go
// buildDataCache constructs the store collage.Cached keeps values in: on disk
// beside a built-in disk cache, so a restart starts warm and a deploy with its
// upstream down renders from what the last one fetched; in memory otherwise. A
// directory it cannot use costs persistence, not the start.
func buildDataCache(cfg Config, logger *slog.Logger, metrics observability.Metrics) *datacache.Store {
	opts := datacache.Options{MaxEntries: cfg.Cache.MaxEntries, Logger: logger, Metrics: metrics}
	if cfg.Cache.Store != nil || cfg.Cache.Type != "disk" || cfg.Cache.Dir == "" {
		return datacache.New(opts)
	}
	dir := filepath.Join(cfg.Cache.Dir, "data")
	store, err := datacache.Open(dir, opts)
	if err != nil {
		logger.Warn("collage: cannot use the data cache directory, keeping data in memory only", "dir", dir, "err", err)
		return datacache.New(opts)
	}
	return store
}
```

In `internal/core/app.go`, replace the `app.data = datacache.New(...)` line from Task 2 with `app.data = buildDataCache(cfg, logger, metrics)`. The surrounding `if cfg.Cache.Enabled && !devMode` stays.

- [ ] **Step 4: `PurgeTags` in core** (`internal/core/app.go`)

Rename the body of `InvalidateTagsN` to an unexported `dropTags(ctx context.Context, tags []string, purge bool) (int, error)`, changing only the data-store step:

```go
	// The data first, so a page re-rendered the moment its entry is dropped
	// below is built from the value being replaced only as a stale, uncached
	// render — or, after a purge, not at all.
	if a.data != nil {
		if purge {
			a.data.Purge(tags)
		} else {
			a.data.Invalidate(tags)
		}
	}
```

Then:

```go
func (a *App) InvalidateTagsN(ctx context.Context, tags ...string) (int, error) {
	return a.dropTags(ctx, tags, false)
}

// PurgeTags removes every cache entry associated with any of tags: pages as
// InvalidateTags does, and collage.Cached values outright, on disk too, rather
// than keeping them to serve stale. The pages count and the errors are
// InvalidateTagsN's.
func (a *App) PurgeTags(ctx context.Context, tags ...string) error {
	_, err := a.dropTags(ctx, tags, true)
	return err
}
```

Keep the long doc comment on `InvalidateTagsN`, and add a sentence after its first paragraph: "collage.Cached values are not removed but marked stale: they are served until a fetch replaces them, so an upstream that is down costs freshness, not pages. PurgeTags removes them."

- [ ] **Step 5: Public API** (`pkg/collage/app.go`)

```go
// InvalidateTags removes every cached render that depended on any of tags and
// marks every collage.Cached value under them stale: the next render is served
// the stale value at once while one fetch replaces it, and keeps being served
// it while the upstream is down. It is InvalidateTagsN without the count.
func (a *App) InvalidateTags(ctx context.Context, tags ...string) error {
	return a.c.InvalidateTags(ctx, tags...)
}

// InvalidateTagsN is InvalidateTags returning how many cached renders it removed.
// A failure to remove one entry does not stop the rest; the failures are joined
// into the returned error.
func (a *App) InvalidateTagsN(ctx context.Context, tags ...string) (int, error) {
	return a.c.InvalidateTagsN(ctx, tags...)
}

// PurgeTags removes every cached render and every collage.Cached value under
// any of tags, from disk too. Unlike InvalidateTags nothing is left to serve
// stale: the next render waits for the upstream, and fails if it is down. For
// data that must not be shown again, once; InvalidateTags is the everyday call.
func (a *App) PurgeTags(ctx context.Context, tags ...string) error {
	return a.c.PurgeTags(ctx, tags...)
}
```

If `a.c` is typed as an interface (check the field's declaration), add `PurgeTags(ctx context.Context, tags ...string) error` to that interface too.

- [ ] **Step 6: Run everything**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 7: Commit**

```bash
git status --short
git add internal/core/cache.go internal/core/app.go pkg/collage/app.go pkg/collage/cached_test.go
git commit -m "feat: Cached persists beside a disk cache; App.PurgeTags

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Docs and changelog

**Files:**
- Modify: `docs/caching.md` (section "Caching data, not only pages" and "Keeping the cache across restarts")
- Modify: `docs/actions.md` ("Invalidating what the change made wrong")
- Modify: `CHANGELOG.md`

- [ ] **Step 1: `docs/caching.md`, "Caching data, not only pages"**

Replace the bullet "**Errors are not stored**, …" with:

```markdown
- **Invalidation keeps the old value, to serve until a new one arrives.**
  `InvalidateTags` and a passed `ttl` mark a value stale rather than removing it.
  The next render is served the stale value at once, and one fetch in the
  background replaces it, so the request after that one sees the change. If that
  fetch fails, the stale value stays and keeps being served, however long the
  upstream is down. A render built from a stale value is served with
  `Cache-Control: no-store` and is not cached, so the page is rendered again once
  the value is fresh. Each stale value served is reported as the `CacheStale`
  metrics event, and each failed refresh is logged at warn with its error.
- **Not found is the exception.** A fetch whose error wraps `collage.ErrNotFound`
  drops the value, so a record deleted upstream disappears from the site and its
  page answers 404.
- **Errors are not stored.** A fetch that finds nothing stale to fall back on
  returns its error to the render.
- **`PurgeTags` removes outright.** It drops pages and values with nothing left
  to serve stale, for data that must not be shown again even once.
- **A static export never uses stale data.** `collage.Build` waits for the
  upstream, and fails if the upstream fails.
```

Replace "**Bounded and in-process.** Values are kept in memory, …" with:

```markdown
- **Bounded, and on disk beside a disk cache.** Values are kept in memory, up to
  `Cache.MaxEntries`, least recently used first to go — unlike the page memory
  cache, which evicts in insertion order (see [below](#the-built-in-memory-cache)).
  With `Cache.Type` `"disk"`, they are also written to `<Cache.Dir>/data/` as
  JSON and read back at startup, so a restart comes up warm. That directory is
  not namespaced by build, so a new deploy with its upstream down renders from
  the last one's data. A value whose type changed shape is not read back, and an
  interface-typed key is kept in memory only. Each instance of a multi-instance
  deployment keeps its own store.
```

- [ ] **Step 2: `docs/caching.md`, "Keeping the cache across restarts"**

After the paragraph that ends "…adds a line to `.gitignore`.", add:

```markdown
The `collage.Cached` data store lives beside the pages, in `Dir/data/`. Unlike
the pages it is shared across builds: see
[Caching data, not only pages](#caching-data-not-only-pages).
```

- [ ] **Step 3: `docs/actions.md`**

After the paragraph "Declarative, and it runs **before** the response…", add:

```markdown
The pages go; the `collage.Cached` values they were built from are kept, marked
stale. The page the redirect lands on is rendered from the stale value, which
also starts the fetch that replaces it, so the reader sees the change on the
next load, not this one. That is the price of a site that keeps answering while
its upstream is down. When an action must show its own change at once, use
`app.PurgeTags` from the handler for those tags instead. Nothing is then kept,
so a failed fetch fails the page.
```

- [ ] **Step 4: CHANGELOG**

Add at the top, under `# Changelog`:

```markdown
## v0.62.0

### Changed

- **`collage.Cached` serves stale data instead of failing.** `InvalidateTags`, and
  a value's `ttl` running out, mark it stale rather than removing it. The next
  render gets the stale value at once and one background fetch replaces it, with
  a fixed 30-second timeout, detached from the request. If that fetch fails, the
  stale value keeps being served, without limit. Before, an invalidation followed
  by a slow or down upstream failed the layout's data, and with it every page.
  A fetch error wrapping `ErrNotFound` still drops the value, so deleted content
  answers 404. A render built from stale data is degraded: served with
  `Cache-Control: no-store` and not cached. `collage.Build` never uses stale
  data.

### Added

- **The data store persists beside a disk cache.** With `Cache.Type` `"disk"`,
  `Cached` values are also written to `<Cache.Dir>/data/` and loaded at startup.
  This directory is not namespaced by build. Store keys are now stable across
  processes and change with a type's shape.
- **`App.PurgeTags`** removes pages and `Cached` values outright, from disk too.
- **`CacheStale` metrics event**, reported for each stale value served. A failed
  background refresh is logged at warn.
```

- [ ] **Step 5: Commit**

```bash
git status --short
git add docs/caching.md docs/actions.md CHANGELOG.md
git commit -m "docs: stale data cache, persistence, PurgeTags; changelog v0.62.0

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Verification and end to end

- [ ] **Step 1: Full suite, uncached, with the race detector**

Run: `go build ./... && go vet ./... && go test -race -count=1 ./...`
Expected: all `ok`. Paste the tail of the output into the report.

- [ ] **Step 2: Scaffold and templates still build** (memory: the test cache hides scaffold breakage)

Run: `grep -rn "InvalidateTags\|datacache.New\|Cached(" --include='*.go.tmpl' . | head`. For each hit, confirm it still compiles against the new API. Then run the scaffold test package with `-count=1`.

- [ ] **Step 3: No new `any`**

Run: `git diff main --stat && git diff main -- '*.go' | grep -n '^+.*\bany\b' | grep -v '\[T any\]'`
Expected: no output.

- [ ] **Step 4: End to end on ose-landing**

```bash
cd ~/Desktop/ose/ose-landing
cat go.work 2>/dev/null || go work init . ../../collage-stale   # point at the worktree; do not commit go.work changes in ose-landing
```
Then build and run it in production mode with `Cache.Type` `"disk"`, which is the app's config. Check its main.go. If it is not disk, test with a temporary local override that is not committed. Then:
1. Request every page once (warm).
2. Trigger the CMS webhook, or call the app's invalidation for `cms`.
3. Stop the CMS, or point its URL at a closed port.
4. `curl -s -o /dev/null -w '%{http_code}\n'` every page. **All must be 200.**
5. Restart the binary with the CMS still down. **All pages must still be 200.**
6. Restore `go.work` to how it was.

Record the commands and results in the report. If ose-landing cannot be run locally (missing secrets, for example), say so plainly and do not claim it passed.

- [ ] **Step 5: Final review**

Request a whole-branch review (superpowers:requesting-code-review) against the spec and this plan. Fix what it confirms.

- [ ] **Step 6: Merge locally, no tag, no push**

```bash
cd /Users/furkan/Desktop/collage
git status --short          # must be clean
git merge --no-ff feat/stale-data-cache -m "Merge feat/stale-data-cache: stale-on-failure data cache (v0.62.0)

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
git worktree remove ../collage-stale
```
Stop here and ask the user before tagging `v0.62.0` or pushing. After they approve and the release is out, the docs site (`~/Desktop/collage-docs`, EN and TR) needs the same doc changes.
