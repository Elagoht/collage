# Typed Keys, No SEO Bag, Typed Plugin Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the last `any`s from collage's public API: a typed `collage.Key[T]` for the render bag, `Once` and `Cached`; no `Page.SEO`; plugin configuration read with `collage.PluginConfig(host, defaults)`.

**Architecture:** `internal/types` gains `Key[T]` (name + type identity) and `Values`, the render's typed bag that replaces `RenderContext.SharedData`; `Once` and `Cached` take a `Key[T]`, so the stored type can no longer disagree with the asked one. `AfterRenderEvent` carries the bag as `Values` and keys read it with `In`. Plugin configuration goes through a sealed `ConfigReader` that collage's hosts satisfy by embedding a struct from `internal/plugin`.

**Tech Stack:** Go 1.26 (generic type aliases), `reflect`, `encoding/json`.

**Spec:** `docs/superpowers/specs/2026-10-06-typed-keys-design.md`

## Global Constraints

- Never write the type `any` in new code except where html/template, `recover` or `encoding/json` define it; such lines carry a trailing `// any: <why>` comment (repo convention). `[T any]` constraints are exempt.
- A key's identity is its name and its type: `NewKey[A]("x")` and `NewKey[B]("x")` are different keys; two keys equal in name and type are the same key, wherever and however often they were created.
- `NewKey` panics on an empty name.
- `With(part)` builds `name + ":" + part`, same `T`.
- `PluginConfig`: no section → `defaults` unchanged; a section → decoded over a copy of `defaults`; malformed JSON and unknown keys reported as today.
- Run tests with `go test -count=1`, and grep `.go.tmpl` files on every API rename (the test cache hides scaffold breakage).
- Commit trailer: `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. Read `git status` before every `git add`; never `git add -A`.
- Work in a worktree: `git worktree add -b feat/typed-keys ../collage-keys main`.

## Review Focus

1. **Concurrent sibling handlers** setting and getting keys in one render must not race (the bag has its own lock, shared by every `WithContext`/`WithFragment` copy). Pinned in Task 1 with `-race`.
2. **A hand-built `RenderContext`** (tests do `&types.RenderContext{}` or `NewRenderContext`) must keep working: `Key.Get` on one with no bag is `false`, `Set` allocates. Pinned in Task 1.
3. **`Cached` across two keys with the same name and different types** stores two values, in the in-process store as well as in the Once fallback (`SkipCache`, dev). Pinned in Task 2.
4. **`PluginConfig` with defaults holding a map** documents in-place filling; a section that is JSON `null` keeps defaults; an empty section `{}` keeps defaults. Pinned in Task 4.
5. **An `AfterRender` hook** reading a key the render never set gets `false`, and one reading through a nil `ev.Values` (a hand-built event) gets `false`, never a panic. Pinned in Task 3.

---

### Task 1: `Key[T]` and the typed render bag

**Files:**
- Create: `internal/types/key.go`
- Modify: `internal/types/context.go` (replace `SharedData` with `values *Values`; remove `Get`/`Set`; `NewRenderContext`; doc comments)
- Modify: `pkg/collage/once.go` (remove `Get[T]`), `pkg/collage/types.go` (aliases)
- Test: `internal/types/key_test.go`; migrate every test using `rc.Get`, `rc.Set`, `SharedData`, `collage.Get[`

**Interfaces:**
- Produces (package `types`):
  ```go
  type Key[T any] struct{ name string }
  func NewKey[T any](name string) Key[T]
  func (k Key[T]) With(part string) Key[T]
  func (k Key[T]) Name() string
  func (k Key[T]) Get(rc *RenderContext) (T, bool)
  func (k Key[T]) Set(rc *RenderContext, v T)
  func (k Key[T]) In(v *Values) (T, bool)
  type Values struct{ mu sync.Mutex; m map[keyID]any }
  func ValuesOf(rc *RenderContext) *Values   // for collage's own AfterRender wiring
  type keyID struct{ name string; typ reflect.Type }   // unexported, used by Task 2
  func (k Key[T]) id() keyID
  ```
- Produces (package `collage`): `type Key[T any] = types.Key[T]`, `func NewKey[T any](name string) Key[T]`, `type RenderValues = types.Values`.

- [ ] **Step 1: Write the failing tests**

`internal/types/key_test.go`:

```go
package types

import (
	"context"
	"sync"
	"testing"
)

type article struct{ Title string }

func TestKey_GetSet(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k := NewKey[article]("article")
	if _, ok := k.Get(rc); ok {
		t.Fatal("Get before Set = true")
	}
	k.Set(rc, article{Title: "a"})
	if got, ok := k.Get(rc); !ok || got.Title != "a" {
		t.Errorf("Get = %+v, %v", got, ok)
	}
}

func TestKey_IdentityIsNameAndType(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	NewKey[string]("x").Set(rc, "s")
	NewKey[int]("x").Set(rc, 7)
	if s, _ := NewKey[string]("x").Get(rc); s != "s" {
		t.Errorf("string key = %q", s)
	}
	if n, _ := NewKey[int]("x").Get(rc); n != 7 {
		t.Errorf("int key = %d", n)
	}
	// A key made again, elsewhere, with the same name and type is the same key.
	if s, ok := NewKey[string]("x").Get(rc); !ok || s != "s" {
		t.Errorf("recreated key = %q, %v", s, ok)
	}
}

func TestKey_With(t *testing.T) {
	k := NewKey[article]("article").With("a").With("b")
	if k.Name() != "article:a:b" {
		t.Errorf("Name = %q", k.Name())
	}
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k.Set(rc, article{Title: "ab"})
	if _, ok := NewKey[article]("article").With("a").Get(rc); ok {
		t.Error("article:a found the value of article:a:b")
	}
}

func TestKey_EmptyNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewKey(\"\") did not panic")
		}
	}()
	NewKey[int]("")
}

func TestKey_SharedAcrossCopiesAndConcurrent(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k := NewKey[int]("n")
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := rc.WithFragment(1, i).WithContext(context.Background())
			NewKey[int]("n").With(string(rune('a'+i%26))).Set(child, i)
			k.Get(child)
		}()
	}
	wg.Wait()
	k.Set(rc.WithFragment(1, 0), 1)
	if n, ok := k.Get(rc); !ok || n != 1 {
		t.Errorf("a copy's Set is not visible to the original: %d, %v", n, ok)
	}
}

func TestKey_HandBuiltContext(t *testing.T) {
	rc := &RenderContext{}
	k := NewKey[int]("n")
	if _, ok := k.Get(rc); ok {
		t.Fatal("Get on a hand-built context = true")
	}
	k.Set(rc, 3)
	if n, ok := k.Get(rc); !ok || n != 3 {
		t.Errorf("Get after Set = %d, %v", n, ok)
	}
}

func TestKey_In(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	k := NewKey[article]("article")
	k.Set(rc, article{Title: "a"})
	if got, ok := k.In(ValuesOf(rc)); !ok || got.Title != "a" {
		t.Errorf("In = %+v, %v", got, ok)
	}
	if _, ok := k.In(nil); ok {
		t.Error("In(nil) = true")
	}
	if _, ok := NewKey[article]("other").In(ValuesOf(rc)); ok {
		t.Error("In with a key never set = true")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -run TestKey_ ./internal/types/`
Expected: FAIL to compile: `undefined: NewKey`.

- [ ] **Step 3: Implement**

`internal/types/key.go`:

```go
package types

import (
	"reflect"
	"sync"
)

// Key names a value one render's fragments share, and fixes its type: the value a
// fragment stores with Set is the one another reads with Get, as the type the key
// says, and nothing else can be stored under it.
//
// A key is its name and its type. NewKey[A]("x") and NewKey[B]("x") are two
// keys; two keys equal in both are one, wherever and however often each was
// made. Declare keys as package variables and derive per-value ones with With.
type Key[T any] struct {
	name string
}

// NewKey returns the key named name for values of type T. An empty name is a
// programming error and panics where the key is declared.
func NewKey[T any](name string) Key[T] {
	if name == "" {
		panic("collage: NewKey with an empty name")
	}
	return Key[T]{name: name}
}

// With returns the key named k's name, a colon and part, for the same type:
// articleKey.With(slug) for one article among many.
func (k Key[T]) With(part string) Key[T] {
	return Key[T]{name: k.name + ":" + part}
}

// Name returns the key's name, for logs and errors.
func (k Key[T]) Name() string { return k.name }

// keyID is what a value is stored under: the key's name and its type.
type keyID struct {
	name string
	typ  reflect.Type
}

func (k Key[T]) id() keyID {
	return keyID{name: k.name, typ: reflect.TypeFor[T]()}
}

// Get returns the value stored under k in rc's render, and whether one was.
func (k Key[T]) Get(rc *RenderContext) (T, bool) {
	return k.In(rc.bag(false))
}

// Set stores v under k in rc's render. Sibling fragments' data handlers run at
// the same time; Set and Get are safe between them. A Get that misses, followed
// by work and a Set, is two fragments doing that work twice — Once is the form
// without that gap.
func (k Key[T]) Set(rc *RenderContext, v T) {
	bag := rc.bag(true)
	bag.mu.Lock()
	defer bag.mu.Unlock()
	if bag.m == nil {
		bag.m = make(map[keyID]any) // any: the bag holds every key's type; Key restores it
	}
	bag.m[k.id()] = v
}

// In returns the value stored under k in values — a finished render's, as an
// AfterRender hook receives them — and whether one was. A nil values has none.
func (k Key[T]) In(values *Values) (T, bool) {
	var zero T
	if values == nil {
		return zero, false
	}
	values.mu.Lock()
	defer values.mu.Unlock()
	v, ok := values.m[k.id()]
	if !ok {
		return zero, false
	}
	typed, ok := v.(T)
	return typed, ok
}

// Values are what one render's fragments stored with Key.Set. They are opaque:
// a Key reads them with In.
type Values struct {
	mu sync.Mutex
	m  map[keyID]any // any: the bag holds every key's type; Key restores it
}

// ValuesOf returns rc's render's values, for collage to hand an AfterRender hook.
func ValuesOf(rc *RenderContext) *Values {
	return rc.bag(false)
}

// bag returns rc's values, allocating them on a hand-built context when create
// is set. A context from NewRenderContext always has them, so every copy the
// render makes shares one.
func (rc *RenderContext) bag(create bool) *Values {
	if rc == nil {
		return nil
	}
	if rc.values == nil && create {
		rc.values = &Values{}
	}
	return rc.values
}
```

In `internal/types/context.go`:
- Replace the `SharedData map[string]any` field and its comment with:

  ```go
  	// values are what this render's fragments share through Key.Set and
  	// Key.Get. A pointer, so the copies WithContext and WithFragment make all
  	// reach the same ones.
  	values *Values
  ```
- `NewRenderContext`: replace `SharedData: make(map[string]any),` with `values: &Values{},`.
- Delete `Get` and `Set`.
- Update the comments that name `SharedData` (`WithContext`, `WithFragment`, `renderShared`, the struct's lock comment) to say "the render's values"; `renderShared.mu` now guards only the Once table and the rest of the shared state.
- Fix the other comments naming SharedData: `internal/types/action.go:11,39`, `internal/types/fragment.go:32`, `internal/render/prefetch.go:21`, `internal/types/once.go:18-20` (rewrite that example with a key).

In `pkg/collage/once.go`: delete `Get[T]` and its comment.

In `pkg/collage/types.go`, beside the other render aliases:

```go
// Key names a value one render's fragments share and fixes its type. See
// NewKey.
type Key[T any] = types.Key[T]

// NewKey returns the key named name for values of type T:
//
//	var confirmKey = collage.NewKey[confirmView]("confirm")
//
//	confirmKey.Set(rc, view)
//	view, ok := confirmKey.Get(rc)
//
// A key is its name and its type, so two keys of one name never read each
// other's values. An empty name panics.
func NewKey[T any](name string) Key[T] { return types.NewKey[T](name) }

// RenderValues are a finished render's shared values, as an AfterRender hook
// receives them in AfterRenderEvent.Values. A Key reads them with In.
type RenderValues = types.Values
```

(`internal/core/app.go` and `internal/httpx/handler.go` still pass `rc.SharedData` to the event: Task 3 changes them. For this task, make them compile by passing `nil` and leave a `// Task 3` note in the commit message, not in code — or do Task 3's two-line change here; either is fine as long as the suite is green.)

- [ ] **Step 4: Migrate callers**

`grep -rn 'rc\.Set(\|rc\.Get(\|SharedData\|collage\.Get\[\|[^.]Get\[' --include='*.go' --include='*.tmpl' internal pkg cmd` — rewrite each with a key declared at package level in the file (`var xKey = types.NewKey[T]("x")` in internal tests, `collage.NewKey` elsewhere), with the value's real type.

- [ ] **Step 5: Run everything**

Run: `go vet ./... && go test -count=1 ./... && go test -race -count=1 ./internal/types/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git status --short
git add internal/types/key.go internal/types/key_test.go internal/types/context.go <other files by name>
git commit -m "feat!: a render's shared values are typed keys"
```

---

### Task 2: `Once` and `Cached` take a key

**Files:**
- Modify: `internal/types/once.go`, `internal/types/cached.go`, `internal/types/context.go` (`once map[keyID]*onceCall`), `internal/types/errors.go` (remove `ErrOnceTypeMismatch`), `pkg/collage/once.go`, `pkg/collage/types.go` (remove mismatch aliases if there)
- Modify: `internal/cli/scaffold/demo/fragments/pages/demo/counter.go.tmpl:34`
- Test: `internal/types/once_test.go`, `internal/types/cached_test.go` (and pkg/collage tests using Once/Cached)

**Interfaces:**
- Consumes: `Key[T]`, `keyID`, `k.id()` (Task 1).
- Produces:
  ```go
  func Once[T any](rc *RenderContext, key Key[T], fetch func(context.Context) (T, error)) (T, error)
  func Cached[T any](rc *RenderContext, key Key[T], ttl time.Duration, tags []string, fetch func(context.Context) (T, error)) (T, error)
  // pkg/collage: same signatures with collage.Key[T]
  ```

- [ ] **Step 1: Write the failing tests**

Add to `internal/types/once_test.go`:

```go
func TestOnce_SameNameDifferentTypes(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	s, err := Once(rc, NewKey[string]("x"), func(context.Context) (string, error) { return "s", nil })
	if err != nil || s != "s" {
		t.Fatalf("string Once = %q, %v", s, err)
	}
	n, err := Once(rc, NewKey[int]("x"), func(context.Context) (int, error) { return 7, nil })
	if err != nil || n != 7 {
		t.Errorf("int Once = %d, %v: one name, two types, two values", n, err)
	}
}
```

Add to `internal/types/cached_test.go` the same shape for `Cached`, twice: once with a bound in-memory store (`BindDataCache` with the store the existing tests use) and once with `SkipDataCache` (the Once fallback):

```go
func TestCached_SameNameDifferentTypes(t *testing.T) {
	for _, skip := range []bool{false, true} {
		rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
		BindDataCache(rc, newTestStore(t)) // use the helper the file already has; create one with datacache.New if it has none
		if skip {
			SkipDataCache(rc)
		}
		s, err := Cached(rc, NewKey[string]("x"), time.Hour, nil, func(context.Context) (string, error) { return "s", nil })
		n, err2 := Cached(rc, NewKey[int]("x"), time.Hour, nil, func(context.Context) (int, error) { return 7, nil })
		if err != nil || err2 != nil || s != "s" || n != 7 {
			t.Errorf("skip=%v: %q %v / %d %v", skip, s, err, n, err2)
		}
	}
}
```

Delete the tests asserting `ErrOnceTypeMismatch` / `ErrCachedTypeMismatch`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -run 'SameNameDifferentTypes' ./internal/types/`
Expected: FAIL to compile (Once takes a string).

- [ ] **Step 3: Implement**

- `context.go`: `once map[keyID]*onceCall`; `NewRenderContext` builds `make(map[keyID]*onceCall)`.
- `once.go`: signature `Once[T any](rc *RenderContext, key Key[T], fetch …)`; use `id := key.id()` as the table key; after `<-call.done`, `value, _ := call.value.(T)` (the table entry's type is the key's, so the assertion cannot fail); delete the mismatch branch and its comment; rewrite the doc comment's example with a key.
- `errors.go`: delete `ErrOnceTypeMismatch`.
- `cached.go`: signature with `key Key[T]`; the Once fallback becomes `Once(rc, Key[T]{name: "collage:cached:" + key.name}, fetch)`; the store key is `storeKey(key.id())`:

  ```go
  // storeKey is the string the store keeps a value under: the key's name and its
  // type, so two keys of one name never share an entry. The type's pointer is
  // unique within the process, which is all the store spans.
  func storeKey(id keyID) string {
  	return id.name + "\x00" + fmt.Sprintf("%p", id.typ)
  }
  ```
  After `store.Load`, `typed, _ := value.(T)`; delete `ErrCachedTypeMismatch` and its check.
- `pkg/collage/once.go`: `Once` and `Cached` take `Key[T]`; delete `ErrOnceTypeMismatch` and `ErrCachedTypeMismatch` aliases; rewrite the doc examples:

  ```go
  //	var articleKey = collage.NewKey[Article]("article")
  //
  //	article, err := collage.Once(rc, articleKey.With(slug), func(ctx context.Context) (Article, error) {
  //		return api.Article(ctx, slug)
  //	})
  ```
- `counter.go.tmpl`: declare `var countKey = collage.NewKey[int]("demo:count")` (use the real type the template's `count.Current` returns) and call `collage.Cached(rc, countKey, …)`.

- [ ] **Step 4: Migrate callers and run everything**

`grep -rn 'Once(\|Cached(' --include='*.go' --include='*.tmpl' internal pkg cmd | grep -v 'func '` — give each call a key. Then:
Run: `go vet ./... && go test -count=1 ./... && go test -race -count=1 ./internal/types/ ./internal/datacache/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add <files by name>
git commit -m "feat!: Once and Cached take a typed key"
```

---

### Task 3: `AfterRenderEvent.Values`

**Files:**
- Modify: `internal/plugin/hooks.go:430-447` (`Data` → `Values`), `internal/core/app.go:~1304`, `internal/httpx/handler.go:~989`
- Test: the existing AfterRender tests (`grep -rn 'ev.Data\|\.Data\[' --include='*_test.go' internal pkg`), plus a new one in `pkg/collage`

**Interfaces:**
- Consumes: `types.ValuesOf`, `Key.In`, `collage.RenderValues` (Task 1).
- Produces: `AfterRenderEvent.Values *types.Values`.

- [ ] **Step 1: Write the failing test**

In `pkg/collage` (beside the existing AfterRender tests, reusing their app setup helper):

```go
var hookStateKey = collage.NewKey[string]("hook-state")

type valuesHookPlugin struct{ got string; ok bool; missingOK bool }

func (p *valuesHookPlugin) Name() string    { return "test/values" }
func (p *valuesHookPlugin) Version() string { return "0" }
func (p *valuesHookPlugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	p.got, p.ok = hookStateKey.In(ev.Values)
	_, p.missingOK = collage.NewKey[int]("never-set").In(ev.Values)
	return nil
}
```

Register it, render a page whose fragment's data handler calls `hookStateKey.Set(rc, "from-render")`, and assert `got == "from-render" && ok && !missingOK`. Also assert `collage.NewKey[int]("x").In(nil)` is `false` (a hand-built event).

- [ ] **Step 2: Run to verify it fails**

Run: `go test -count=1 -run Values ./pkg/collage/`
Expected: FAIL to compile (`ev.Values` undefined).

- [ ] **Step 3: Implement**

`internal/plugin/hooks.go`: replace the `Data map[string]any` field and its comment with:

```go
	// Values are the render's shared values: what its fragments stored with
	// Key.Set. A plugin reads what it holds a key for — its own, or one an
	// application or another plugin exports — with Key.In:
	//
	//	st, ok := stateKey.In(ev.Values)
	//
	// They are the render's own, not a copy; the render has finished, so nothing
	// is writing to them. A plugin that keeps them past the hook is holding
	// request-scoped state.
	Values *types.Values
```

`internal/core/app.go` and `internal/httpx/handler.go`: `Values: types.ValuesOf(rc),` in place of `Data: rc.SharedData,`.

Migrate the existing tests that read `ev.Data` to keys.

- [ ] **Step 4: Run everything**

Run: `go vet ./... && go test -count=1 ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add internal/plugin/hooks.go internal/core/app.go internal/httpx/handler.go <tests by name>
git commit -m "feat!: an AfterRender hook reads the render's values with a key"
```

---

### Task 4: `collage.PluginConfig`

**Files:**
- Create: `internal/plugin/config.go`
- Modify: `internal/plugin/plugin.go` (`Host`: remove `Config`, embed `ConfigReader`), `internal/plugin/configure.go` (`ConfigHost`: same; `DecodeConfig` becomes the body of `PluginConfig`), `internal/core/host.go` (both views embed `plugin.ConfigSource`; remove their `Config` methods), `internal/plugin/registry_test.go:468` (`fakeHost`), `pkg/collage/` (wrapper + alias), every caller of `.Config(&` in the repo
- Test: `internal/plugin/config_test.go`, `pkg/collage/pluginconfig_test.go`

**Interfaces:**
- Produces:
  ```go
  // package plugin
  type ConfigReader interface{ pluginConfig() (json.RawMessage, string) }
  type ConfigSource struct{ Name string; Config map[string]json.RawMessage }
  func (s ConfigSource) pluginConfig() (json.RawMessage, string)
  func PluginConfig[T any](r ConfigReader, defaults T) (T, error)
  // package collage
  type ConfigReader = plugin.ConfigReader
  func PluginConfig[T any](host ConfigReader, defaults T) (T, error)
  ```

- [ ] **Step 1: Write the failing tests**

`internal/plugin/config_test.go`:

```go
package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

type limits struct {
	Limit  int               `json:"limit"`
	Window string            `json:"window"`
	Extra  map[string]string `json:"extra"`
}

func source(section string) ConfigSource {
	cfg := map[string]json.RawMessage{}
	if section != "" {
		cfg["test/p"] = json.RawMessage(section)
	}
	return ConfigSource{Name: "test/p", Config: cfg}
}

func TestPluginConfig(t *testing.T) {
	defaults := limits{Limit: 10, Window: "1m"}
	tests := []struct {
		name    string
		section string
		want    limits
		wantErr string
	}{
		{"no section", "", defaults, ""},
		{"null section", "null", defaults, ""},
		{"empty object", "{}", defaults, ""},
		{"partial overlay", `{"limit": 3}`, limits{Limit: 3, Window: "1m"}, ""},
		{"malformed", `{"limit": "three"}`, defaults, `collage: plugin "test/p" configuration`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := PluginConfig(source(test.section), defaults)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				if got.Limit != defaults.Limit || got.Window != defaults.Window {
					t.Errorf("on error got %+v, want the defaults", got)
				}
				return
			}
			if err != nil || got.Limit != test.want.Limit || got.Window != test.want.Window {
				t.Errorf("got %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
	if defaults.Limit != 10 {
		t.Error("PluginConfig changed the caller's defaults struct")
	}
}
```

`pkg/collage/pluginconfig_test.go`: a test plugin whose `Init(host)` calls `collage.PluginConfig(host, opts{N: 1})`, registered on an app built with `Config.PluginConfig` holding `{"test/cfg": {"n": 5}}`, asserting it saw `5`; and one whose `Configure(ctx, host)` (the ConfigHost path) does the same.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -run PluginConfig ./internal/plugin/ ./pkg/collage/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/plugin/config.go`:

```go
package plugin

import (
	"encoding/json"
	"fmt"
)

// ConfigReader is what PluginConfig reads a plugin's configuration through. Its
// one method is unexported, so only a type embedding ConfigSource — collage's own
// hosts — can satisfy it.
type ConfigReader interface {
	pluginConfig() (section json.RawMessage, name string)
}

// ConfigSource is the plugin's name and the application's plugin
// configuration. A host embeds it to satisfy ConfigReader.
type ConfigSource struct {
	Name   string
	Config map[string]json.RawMessage
}

func (s ConfigSource) pluginConfig() (json.RawMessage, string) {
	return s.Config[s.Name], s.Name
}

// PluginConfig returns the plugin's configuration decoded over defaults. With no
// section — or an empty or null one — defaults come back unchanged; a section is
// decoded over a copy of them, so what it leaves out keeps its default. A map or
// slice inside defaults is filled in place, as decoding into a pointer always
// did. A malformed section is an error, and defaults come back with it: the
// operator wrote something, and running on defaults silently would hide it.
func PluginConfig[T any](r ConfigReader, defaults T) (T, error) {
	raw, name := r.pluginConfig()
	if len(raw) == 0 {
		return defaults, nil
	}
	out := defaults
	if err := json.Unmarshal(raw, &out); err != nil {
		return defaults, fmt.Errorf("collage: plugin %q configuration: %w", name, err)
	}
	return out, nil
}
```

- `Host` (plugin.go) and `ConfigHost` (configure.go): delete `Config(v any) error` and its comment; add `ConfigReader` as an embedded interface with a comment: "ConfigReader lets collage.PluginConfig read this plugin's configuration."
- Delete `DecodeConfig` (its doc moves to `PluginConfig`); keep `CheckConfigKeys`.
- `internal/core/host.go`: `hostView` and `configHostView` gain an embedded `plugin.ConfigSource`; build it where the views are created (`grep -n 'hostView{\|configHostView{' internal/core/*.go`) with `plugin.ConfigSource{Name: name, Config: app.cfg.PluginConfig}`; delete both `Config` methods. Keep the `name` fields only if something else uses them.
- `internal/plugin/registry_test.go`: `fakeHost` embeds `ConfigSource` instead of its `Config` method.
- `pkg/collage`: next to the plugin aliases,

  ```go
  // ConfigReader is a host PluginConfig reads a plugin's configuration through:
  // the Host given to Init and the ConfigHost given to Configure.
  type ConfigReader = plugin.ConfigReader

  // PluginConfig returns the plugin's section of the application's plugin
  // configuration decoded over defaults:
  //
  //	cfg, err := collage.PluginConfig(host, Config{Limit: 10, Window: time.Minute})
  //
  // No section leaves defaults as they are; a malformed one is an error.
  func PluginConfig[T any](host ConfigReader, defaults T) (T, error) {
  	return plugin.PluginConfig(host, defaults)
  }
  ```
- Migrate every `host.Config(&x)` in the repo (`grep -rn '\.Config(&' --include='*.go' --include='*.tmpl' .`) to `x, err = collage.PluginConfig(host, x)`.

- [ ] **Step 4: Run everything**

Run: `go vet ./... && go test -count=1 ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add internal/plugin/config.go internal/plugin/config_test.go internal/plugin/plugin.go internal/plugin/configure.go internal/core/host.go internal/plugin/registry_test.go <pkg/collage files and callers by name>
git commit -m "feat!: plugins read their configuration with collage.PluginConfig"
```

---

### Task 5: `Page.SEO` and `WithSEO` removed

**Files:**
- Modify: `internal/types/page.go` (field), `pkg/collage/page.go:~214` (`WithSEO`), `internal/core/registry.go:~609-631` (the clone), `internal/plugin/plugin.go:~85,104` (comments naming SEO), every test using them (`grep -rn 'WithSEO\|\.SEO\b' --include='*.go' .`)

- [ ] **Step 1: Remove and let the compiler list the rest**

Delete the `SEO map[string]any` field and its comment, `WithSEO` and its comment, the `copied.SEO = maps.Clone(p.SEO)` line and the comment above it that explains the one-level copy (rewrite what remains of that comment so it describes what is still copied). Rewrite the two `plugin.go` comments that use `Page.SEO` as their example: use `Page.Paths` (a map on the copy) as the example instead.

- [ ] **Step 2: Fix the tests**

Run: `go vet ./... 2>&1 | head -30`; for each test that set or read SEO, delete that part when SEO was incidental, or delete the test when SEO was its subject.

- [ ] **Step 3: Run everything**

Run: `go vet ./... && go test -count=1 ./... && grep -rn 'WithSEO\|\.SEO\b' --include='*.go' --include='*.tmpl' . | grep -v superpowers`
Expected: tests PASS, grep prints nothing.

- [ ] **Step 4: Commit**

```bash
git status --short
git add <files by name>
git commit -m "feat!: Page.SEO and WithSEO are gone; nothing read them"
```

---

### Task 6: The compiler holds the types

**Files:**
- Create: `pkg/collage/testdata/keytypes/main.go` (outside the build: `testdata` is ignored by `go build ./...`)
- Test: `pkg/collage/keytypes_test.go`

- [ ] **Step 1: Write the test and its testdata**

`pkg/collage/testdata/keytypes/main.go`:

```go
// Package main must not compile: every line below is a type error the typed keys
// exist to catch. keytypes_test.go builds it and expects each one.
package main

import (
	"context"

	"github.com/Elagoht/collage/pkg/collage"
)

var countKey = collage.NewKey[int]("count")

func main() {
	var rc *collage.RenderContext
	countKey.Set(rc, "seven") // WANT: cannot use "seven"
	_, _ = collage.Once(rc, countKey, func(context.Context) (string, error) { return "", nil }) // WANT: type func
	var s string
	s, _ = countKey.Get(rc) // WANT: cannot use
	_ = s
}
```

`pkg/collage/keytypes_test.go`:

```go
package collage_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The typed keys promise that a wrong type does not compile. This builds a
// program that tries three ways and checks the compiler refuses each.
func TestKeyTypes_WrongTypesDoNotCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	out, err := exec.Command("go", "build", "-o", t.TempDir()+"/x", "./testdata/keytypes").CombinedOutput()
	if err == nil {
		t.Fatal("the wrong-type program compiled")
	}
	text := string(out)
	for _, want := range []string{"main.go:15:", "main.go:16:", "main.go:18:"} {
		if !strings.Contains(text, want) {
			t.Errorf("no compile error at %s:\n%s", want, text)
		}
	}
}
```

Check the line numbers against the file you wrote (the `WANT` lines); adjust the `want` list to them.

- [ ] **Step 2: Run it**

Run: `go test -count=1 -run KeyTypes ./pkg/collage/`
Expected: PASS (the program fails to compile at exactly those lines). Also confirm `go build ./...` and `go vet ./...` ignore `testdata`.

- [ ] **Step 3: Commit**

```bash
git add pkg/collage/testdata/keytypes/main.go pkg/collage/keytypes_test.go
git commit -m "test: a wrong type through a key does not compile"
```

---

### Task 7: Scaffold, generator, docs and changelog

**Files:**
- Grep: `grep -rn 'rc\.Set(\|rc\.Get(\|collage\.Get\[\|Once(\|Cached(\|WithSEO\|\.SEO\b\|\.Config(&\|ev\.Data\|SharedData\|ErrOnceTypeMismatch\|ErrCachedTypeMismatch' --include='*.go' --include='*.tmpl' --include='*.md' . | grep -v superpowers`
- Modify: every `.go.tmpl` under `internal/cli/scaffold`, `internal/cli/add.go` if it generates any of these, `docs/*.md`, `README.md`, `CHANGELOG.md`

- [ ] **Step 1: Migrate every remaining sample** by the spec's Migration table, with real types and keys declared at package level.

- [ ] **Step 2: Docs.** `docs/caching.md` (Once/Cached), `docs/fragments.md` (sharing values between fragments; SharedData → keys), `docs/plugins.md` (PluginConfig; AfterRenderEvent.Values and `In`; ConfigReader), anything naming SEO. Explain the identity rule (name + type), `With`, and `In` for hooks.

- [ ] **Step 3: CHANGELOG** — `## Unreleased`: **Breaking** first with the spec's Migration table (plus a row: `ev.Data[k]` in `OnAfterRender` → `key.In(ev.Values)`), then **Added** (`Key`, `NewKey`, `RenderValues`, `PluginConfig`, `ConfigReader`), **Removed** (`Get[T]`, `rc.Get/Set`, `SharedData`, the two mismatch errors, `Page.SEO`/`WithSEO`, `Host.Config`/`ConfigHost.Config`, `AfterRenderEvent.Data`).

- [ ] **Step 4: Verify and commit**

Run: `go vet ./... && go test -count=1 ./... && go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...` and the grep above (only the CHANGELOG's history and the migration table may still name removed APIs).

```bash
git status --short
git add <files by name>
git commit -m "docs: typed keys, PluginConfig and no SEO bag"
```

---

## After this plan

Not part of this plan; each follows once the branch is reviewed and merged:
- Tag v0.50.0 once `main`'s CI is green (staticcheck included).
- Re-release every plugin: 36 call `host.Config` → `PluginConfig`; validate exports its keys (`ErrorsKey`, `ValuesKey`) in place of string names; highlight, i18n and ogimage move their hook state to `Key.In`; honeypot, jsonld, meta, ogimage use keyed `Once`/`Cached`/`Set`. Then rerun the tag's CI until green.
- Docs site EN+TR, the VS Code extension's snippets, and the user's apps (pusula uses `collage.Get[T]` with string keys; Sen de Yaz uses `rc.Set`/`rc.Get`).
