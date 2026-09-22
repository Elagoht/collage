# Collage-Core Implementation Plan

## Context

`collage-core` is a production-grade Go framework for server-side, component-based
rendering. It is cache-first, plugin-driven, and built on explicit contracts with no
magic. This plan is the argument; the architecture spec pasted by the project owner is
the authority. Where this plan and the spec conflict, the spec wins.

Repository: empty greenfield, Go 1.26.4, module `github.com/Elagoht/collage`, branch
`feat/collage-core`.

### Architectural shape

```
collage/
├── cmd/collage/              CLI entrypoint (new, dev, build)
├── pkg/collage/              Public API (type aliases + builders + App facade)
├── internal/
│   ├── types/                Shared domain types (Fragment, Page, RenderContext, ...)
│   ├── core/                 App orchestrator & lifecycle
│   ├── render/               Render engine (slot-based, recursive)
│   ├── router/               Radix tree, locale resolution, redirects
│   ├── cache/                Cache interface + memory implementation
│   ├── dependency/           Dependency tracking & tag invalidation
│   ├── plugin/               Plugin registry & hooks
│   ├── template/             Template engine abstraction (html/template)
│   ├── httpx/                HTTP handler & middleware
│   ├── build/                Static site generation
│   ├── observability/        Metrics & tracing interfaces
│   └── cli/                  CLI implementation
├── examples/blog/            Example project
└── docs/                     Documentation
```

### Import direction (no cycles, enforced)

`internal/types` is the leaf: it imports only the standard library. Every other
`internal/*` package may import `internal/types`. `pkg/collage` imports `internal/*`
and re-exports the domain types as **type aliases**, so user-facing code writes
`*collage.Page` while internal code writes `*types.Page` and they are the same type.

**`internal/*` MUST NEVER import `pkg/collage`.** This is what lets every internal
interface take concrete types instead of `interface{}`.

The HTTP package is named `httpx` (directory `internal/httpx`) so it never shadows the
standard library `net/http` at a call site.

## Global Constraints

These bind every task. A violation is a review defect regardless of what a task says.

1. **Module path is `github.com/Elagoht/collage`.** Every import uses it verbatim.
2. **Go 1.26. Standard library only.** No third-party dependencies in `go.mod` —
   framework, CLI, tests, and examples alike. `go.sum` stays empty.
3. **No `any` / `interface{}`, with three documented exceptions.** The user's global
   instruction is "never use type any". The permitted exceptions, each of which MUST
   carry a `// any: <reason>` comment on the declaring line:
   - template data payloads (`html/template.Execute` accepts `any`),
   - `Page.SEO` metadata values (opaque by spec),
   - `RenderContext.SharedData` values (fragment inter-communication).
   Nothing else. Internal interfaces take concrete types — never
   `Register(page interface{})`. No `map[string]interface{}` outside the three.
4. **No reflection.** `reflect` is not imported anywhere.
5. **Every exported identifier has a doc comment** starting with its own name.
6. **Verification gate, run in the repo root before reporting DONE:**
   `gofmt -l . ` (must print nothing), `go build ./...`, `go vet ./...`,
   `go test ./...`. All four must be clean. Paste the real output into the report.
7. **Tests are table-driven** where more than one case exists, use only the standard
   `testing` package, and assert real behaviour. A test that asserts nothing, or that
   restates the implementation, is a defect.
8. **Errors are wrapped with `%w`** and package-level sentinel errors
   (`var ErrFoo = errors.New("collage: foo")`) are used for conditions callers branch
   on. Error strings are lowercase, prefixed `collage: `.
   **One sentinel per distinct failure *mode*, not per field.** Two checks share a
   sentinel only when a caller would handle them identically and the difference is
   purely which field tripped — in that case the field name goes in the wrapped message
   (`fmt.Errorf("%w: %s", ErrX, field)`), and the sentinel's doc comment says it is
   deliberately shared. Two checks need separate sentinels when they mean different
   things to a caller: `ErrInvalidTimeout` (a fragment's data-fetch deadline) and
   `ErrInvalidTTL` (a page's cache lifetime) are distinct concepts and were correctly
   split in Task 1, whereas six config duration fields that are all simply "negative
   duration" are one mode. When in doubt, ask: would any caller write different code for
   these two cases? If not, share the sentinel and name the field in the message.
9. **No panics escape a public API.** Panic recovery happens at the fragment execution
   boundary and converts to an error. `recover()` appears only where the plan says.
10. **No goroutine leaks.** Anything started has a documented stop path. Rendering is
    sequential and deterministic by spec.
11. **Context is honoured.** Any function doing I/O or calling user code takes
    `context.Context` as its first parameter and respects cancellation.
12. **Never cache an error or 404 response.** This is a spec invariant.
13. Do not create files the task does not name. No speculative abstraction (YAGNI).

## Task 1: Module bootstrap and shared domain types

Create the Go module and the leaf package every other package depends on.

### Files

- `go.mod` — `module github.com/Elagoht/collage`, `go 1.26`. No requires.
- `internal/types/fragment.go`
- `internal/types/page.go`
- `internal/types/context.go`
- `internal/types/strategy.go`
- `internal/types/errors.go`
- `internal/types/fragment_test.go`
- `internal/types/page_test.go`
- `internal/types/context_test.go`
- `internal/types/strategy_test.go`

### `internal/types/strategy.go`

```go
// RenderStrategy selects how a page's output is cached and regenerated.
type RenderStrategy int

const (
	// StrategyDynamic renders on every request and never serves from cache.
	StrategyDynamic RenderStrategy = iota
	// StrategyStatic renders once and serves from cache until explicitly invalidated.
	StrategyStatic
	// StrategyIncremental serves from cache until the page's CacheTTL elapses.
	StrategyIncremental
)
```

Add `func (s RenderStrategy) String() string` returning `"dynamic"`, `"static"`,
`"incremental"`, or `"unknown(<n>)"` for an out-of-range value. Add
`func (s RenderStrategy) Cacheable() bool` — true for Static and Incremental only.

### `internal/types/fragment.go`

```go
// DataHandlerFunc fetches the data a fragment renders with. It returns the template
// data, the dependency tags the data was derived from, and an error.
type DataHandlerFunc func(ctx context.Context, rc *RenderContext) (data any, tags []string, err error) // any: html/template renders arbitrary data

// SlotDefinition declares a named position inside a fragment's template and the
// fragments bound to it.
type SlotDefinition struct {
	// Name is the slot's identifier, matched by {{slot "name"}} in the template.
	Name string
	// Required reports whether rendering fails when no fragment is bound.
	Required bool
	// AllowMultiple reports whether more than one fragment may be bound. When false,
	// binding a second fragment is a validation error.
	AllowMultiple bool
	// Fill holds the fragments bound to this slot, rendered in binding order.
	Fill []*Fragment
}

// Fragment is the framework's unit of composition: a template, an optional data
// contract, and the slots it exposes to child fragments.
type Fragment struct {
	Name         string
	TemplatePath string
	DataHandler  DataHandlerFunc
	Slots        map[string]*SlotDefinition
	Required     bool
	Fallback     *Fragment
	Timeout      time.Duration
}
```

Methods on `*Fragment`:

- `Slot(name string) (*SlotDefinition, bool)` — nil-safe lookup.
- `SlotNames() []string` — sorted, for deterministic iteration and error messages.
- `Bind(slotName string, child *Fragment) error` — appends to `Fill`. Returns
  `ErrUnknownSlot` wrapped with the slot name if undeclared, `ErrSlotOccupied` if the
  slot already has a fill and `AllowMultiple` is false, `ErrNilFragment` if child is nil.
- `Validate() error` — `Name` non-empty, `TemplatePath` non-empty, every declared
  `Required` slot has at least one fill, `Timeout >= 0`, no slot name is empty, each
  slot's map key equals its `Name` field. Recurses into `Fill` and `Fallback`. Detects
  cycles (a fragment reachable from itself) and returns `ErrFragmentCycle` with the
  path — use an explicit `map[*Fragment]bool` on the recursion stack, not a depth
  counter. Returns `nil` for a valid tree. Nil receiver returns `ErrNilFragment`.
- `EffectiveTimeout(def time.Duration) time.Duration` — returns `Timeout` when > 0,
  otherwise `def`.

### `internal/types/page.go`

```go
// Redirect describes a source path pattern that redirects to a destination.
type Redirect struct {
	From       string
	To         string
	StatusCode int
	Permanent  bool
}

// Page is a render configuration: the fragments to compose, the paths that reach it,
// and how its output is cached.
type Page struct {
	Name            string
	LayoutFragment  *Fragment
	ContentFragment *Fragment
	Paths           map[string]string
	Redirects       []*Redirect
	Strategy        RenderStrategy
	CacheTTL        time.Duration
	SEO             map[string]any // any: SEO metadata is opaque to the framework
	DependencyTags  []string
	NotFoundPage    *Page
	ErrorPage       *Page
}
```

- `Redirect.EffectiveStatus() int` — returns `StatusCode` when it is one of 301, 302,
  307, 308; otherwise 301 when `Permanent`, else 302.
- `Redirect.Validate() error` — `From` and `To` non-empty and both start with `/`;
  `StatusCode` is 0 or one of the four allowed codes (`ErrInvalidRedirectStatus`).
- `Page.Root() *Fragment` — `LayoutFragment` when set, else `ContentFragment`.
- `Page.Locales() []string` — sorted keys of `Paths`.
- `Page.PathFor(locale string) (string, bool)`.
- `Page.Validate() error` — `Name` non-empty; `ContentFragment` non-nil
  (`ErrMissingContent`); every path pattern starts with `/`; `CacheTTL >= 0`; when
  `Strategy == StrategyIncremental`, `CacheTTL > 0` (`ErrMissingTTL`); every redirect
  validates; `Root().Validate()` passes. Error pages are NOT recursed into here (they
  are validated when registered in their own right) — but `NotFoundPage` and
  `ErrorPage` must not be the page itself (`ErrSelfErrorPage`).

**Layout/content binding rule:** when `LayoutFragment` is non-nil, the content
fragment is bound to the layout's slot named `"content"`. This binding is performed
once, by the registration path in Task 11 — `types.Page` itself never mutates. Add
`Page.ContentSlotName() string` returning the constant `DefaultContentSlot = "content"`
so no other package hard-codes the string.

### `internal/types/context.go`

```go
// RenderContext carries request-scoped state to every fragment in one render.
type RenderContext struct {
	Request    *http.Request
	Locale     string
	PathParams map[string]string
	Page       *Page
	SharedData map[string]any // any: fragments exchange arbitrary values
}
```

The embedded `context.Context` is an unexported field `ctx`, per spec, with:

- `NewRenderContext(ctx context.Context, req *http.Request, page *Page, locale string, params map[string]string) *RenderContext` — copies `params` into a fresh map
  (never aliases the caller's), initialises `SharedData`, defaults a nil `ctx` to
  `context.Background()`.
- `Context() context.Context` — returns `ctx`.
- `WithContext(ctx context.Context) *RenderContext` — returns a **shallow copy** with
  the new ctx; used for per-fragment timeouts. Maps are shared with the original by
  design (document this).
- `Param(name string) string` — empty string when absent.
- `Get(key string) (any, bool)` / `Set(key string, value any)` — `SharedData` access,
  nil-map safe on `Set`.

`RenderContext` is request-scoped and MUST NOT be retained past a render. Say so in the
doc comment.

### `internal/types/errors.go`

Sentinel errors, all prefixed `collage: `: `ErrNilFragment`, `ErrUnknownSlot`,
`ErrSlotOccupied`, `ErrFragmentCycle`, `ErrMissingContent`, `ErrMissingTTL`,
`ErrInvalidRedirectStatus`, `ErrSelfErrorPage`, `ErrEmptyName`, `ErrEmptyTemplatePath`,
`ErrInvalidPath`, `ErrInvalidTimeout`, `ErrInvalidTTL`, `ErrRequiredSlotUnfilled`,
`ErrInvalidSlotDefinition`.

**One sentinel per distinct failure — never reuse one for an unrelated condition.**
Callers branch with `errors.Is`, so overloading a sentinel silently merges two failures a
caller needs to tell apart. Specifically: `ErrInvalidTimeout` for `Fragment.Timeout < 0`;
`ErrInvalidTTL` for `Page.CacheTTL < 0` (distinct from `ErrMissingTTL`, which is the
incremental-strategy-without-TTL case); `ErrRequiredSlotUnfilled` for a `Required` slot
with no fill (NOT `ErrMissingContent`, which is a page with no content fragment);
`ErrInvalidSlotDefinition` for a malformed declaration (empty slot name, or a map key
that does not equal the definition's `Name`). Every failure mode in `Validate` must be
reachable by `errors.Is` with its own sentinel — a test asserting only `err != nil` is a
defect.

### Tests

Cover: strategy `String`/`Cacheable` across all values including out-of-range; `Bind`
success, unknown slot, occupied slot, nil child, multiple allowed; `Validate` accepting
a valid nested tree and rejecting each failure mode with `errors.Is`; a deliberate
fragment cycle (A binds B, B binds A) returning `ErrFragmentCycle` rather than
recursing forever — this test MUST complete, so it proves the cycle guard; redirect
status resolution for all combinations; `PathFor`; `NewRenderContext` not aliasing the
caller's param map (mutate the caller's map after construction, assert the context is
unaffected); `WithContext` preserving fields.

### Verification

Run the Global Constraints gate. `go test ./... -run . -count=1` passes.

## Task 2: Public API — aliases, builders, configuration

`pkg/collage` is the only package users import. It re-exports `internal/types` via
aliases and provides the fluent builders from the spec's examples.

### Files

- `pkg/collage/types.go` — aliases and re-exported sentinels/constants.
- `pkg/collage/fragment.go` — `FragmentBuilder`.
- `pkg/collage/page.go` — `PageBuilder`.
- `pkg/collage/config.go` — `Config` and nested config types + defaults + validation.
- `pkg/collage/fragment_test.go`, `pkg/collage/page_test.go`, `pkg/collage/config_test.go`

### Aliases (`pkg/collage/types.go`)

```go
type Fragment = types.Fragment
type SlotDefinition = types.SlotDefinition
type DataHandlerFunc = types.DataHandlerFunc
type Page = types.Page
type Redirect = types.Redirect
type RenderContext = types.RenderContext
type RenderStrategy = types.RenderStrategy
```

Re-export the strategy constants (`StrategyDynamic`, `StrategyStatic`,
`StrategyIncremental`) and every sentinel error from `internal/types` as
`var ErrX = types.ErrX`, so users can `errors.Is` without importing an internal package.

### Builder error handling

The spec's examples chain calls and ignore errors:

```go
f := collage.NewFragment("home", "pages/home.html").WithDataHandler(h).Build()
```

So builders accumulate rather than returning `(T, error)` at every step:

- Each `WithX` method that can fail appends to the builder's unexported `errs []error`
  and returns the builder unchanged in that respect.
- `Build() *Fragment` (resp. `*Page`) returns the constructed value. It never panics and
  never returns nil for a non-nil builder.
- `BuildErr() error` returns `errors.Join(b.errs...)`, or nil. Its existence MUST be
  named in `Build()`'s doc comment so the escape hatch is discoverable.

Ignored builder errors are not lost, because **registration is the enforcement point**:
`App.RegisterPage` (Task 11) calls `Page.Validate()` and rejects a malformed page with a
named error. A builder mistake therefore surfaces at startup, loudly, rather than at the
first request. Say this explicitly in the package doc comment.

### `FragmentBuilder`

`NewFragment(name, templatePath string) *FragmentBuilder`, then:

- `WithDataHandler(h DataHandlerFunc) *FragmentBuilder`
- `WithSlot(name string, required, allowMultiple bool) *FragmentBuilder` — declares a
  slot. Re-declaring the same name records an error.
- `WithSlotFragment(slotName string, child *Fragment) *FragmentBuilder` — binds a child
  into a declared slot via `Fragment.Bind`, recording any error.
- `Required() *FragmentBuilder` — sets `Required = true`.
- `WithFallback(f *Fragment) *FragmentBuilder`
- `WithTimeout(d time.Duration) *FragmentBuilder` — negative records an error.
- `Build() *Fragment`, `BuildErr() error`

### `PageBuilder`

`NewPage(name string) *PageBuilder`, then `WithLayout(*Fragment)`,
`WithContent(*Fragment)`, `WithPath(locale, pattern string)`,
`WithRedirect(from, to string, status int)`, `WithPermanentRedirect(from, to string)`,
`WithNotFoundPage(*Page)`, `WithErrorPage(*Page)`, `Static()`, `Dynamic()`,
`Incremental(ttl time.Duration)`, `WithDependency(tags ...string)`,
`WithSEO(key string, value any)` (`// any:` comment required), `Build()`, `BuildErr()`.

Default strategy when none is chosen: `StrategyDynamic`.

### `Config`

```go
type Config struct {
	DevMode       bool
	Server        ServerConfig
	Template      TemplateConfig
	Cache         CacheConfig
	Locale        LocaleConfig
	Observability ObservabilityConfig
}

type ServerConfig struct {
	Host            string        // default "localhost"
	Port            int           // default 3000
	ReadTimeout     time.Duration // default 15s
	WriteTimeout    time.Duration // default 30s
	IdleTimeout     time.Duration // default 60s
	ShutdownTimeout time.Duration // default 10s
}

type TemplateConfig struct {
	Root      string        // default "./templates"
	Extension string        // default ".html"
	DevMode   bool          // reload templates per request
	Timeout   time.Duration // default DataHandler timeout, default 5s
}

type CacheConfig struct {
	Enabled    bool
	Type       string        // "memory" (default when empty and Enabled)
	DefaultTTL time.Duration // default 5m
	// MaxEntries caps stored entries. Zero means "use the default" (10000); a
	// negative value means unlimited. Zero cannot mean unlimited — ApplyDefaults could
	// not then distinguish it from an unset field.
	MaxEntries int
}

type LocaleConfig struct {
	Default   string   // default "en"
	Supported []string // default []string{Default}
	// DisablePathLocale turns off the /tr/blog/post prefix source. The zero value
	// keeps it enabled, which is the default behaviour.
	DisablePathLocale bool
	// DisableHeaderLocale turns off the Accept-Language source. The zero value keeps
	// it enabled.
	DisableHeaderLocale bool
	// CookieName is the cookie consulted for a locale. Empty means "locale"; set
	// DisableCookieLocale to turn the source off entirely.
	CookieName          string
	DisableCookieLocale bool
}
```

**Boolean defaults must be expressed as negative flags, as above.** A `bool` field
documented as "default true" is unusable: `ApplyDefaults` cannot distinguish "the caller
left it zero" from "the caller explicitly set false", so the caller can never turn the
feature off. Inverting the flag makes the zero value the desired default and leaves the
opt-out expressible. The same rule applies anywhere else in the project.

```go

type ObservabilityConfig struct {
	Metrics observability.Metrics // nil -> no-op
	Tracer  observability.Tracer  // nil -> no-op
}
```

`ObservabilityConfig` references Task 6's interfaces; if Task 6 has not landed, declare
the field types as the interfaces you will create there and note it in the report — but
prefer to leave `ObservabilityConfig` out of this task entirely and add it in Task 6.
**Ruling: leave `ObservabilityConfig` out of Task 2.** Task 6 adds the field.

Methods: `(*Config).ApplyDefaults()` filling every zero value above, and
`(*Config).Validate() error` — port in 1..65535, `Template.Root` non-empty,
`Cache.Type` in {"memory"} when caching is enabled, `Locale.Default` non-empty and
present in `Supported`, all durations `>= 0`. `DevMode` effective value is
`cfg.DevMode || cfg.Template.DevMode`; expose it as `(*Config).IsDevMode() bool` and
use that everywhere else in the codebase.

### Tests

Builder happy paths mirroring the two canonical usage examples in
`docs/spec/usage-examples.md` (minimal app and the blog example) and asserting the
resulting struct fields — those examples are the API's authority and must keep
compiling verbatim; `BuildErr` populated for
duplicate slot, unknown slot binding, negative timeout, missing content;
`ApplyDefaults` idempotence (apply twice, same result); `Validate` rejecting each bad
field; `IsDevMode` true when either flag is set.

### Verification

Global Constraints gate. Additionally `go doc ./pkg/collage` lists the aliases.

## Task 3: Template engine abstraction

### Files

- `internal/template/engine.go` — the `Engine` interface and shared types.
- `internal/template/html.go` — `html/template` implementation.
- `internal/template/funcs.go` — the default `FuncMap` including `slot`.
- `internal/template/engine_test.go`, `internal/template/html_test.go`,
  `internal/template/funcs_test.go`
- Test fixtures under `internal/template/testdata/`.

### Interface

```go
// Engine renders a named template with the supplied data.
type Engine interface {
	// Render executes the template at path with data and writes to w.
	Render(ctx context.Context, w io.Writer, path string, data any) error // any: template data
	// Lookup reports whether a template exists.
	Lookup(path string) bool
	// RenderWithFuncs executes the template at path with data, after overlaying funcs
	// onto the engine's FuncMap for this render only. Implementations MUST do this on a
	// clone, never on the shared template set, so concurrent renders cannot see each
	// other's functions. This is how the render engine binds a per-render slot function.
	RenderWithFuncs(ctx context.Context, w io.Writer, path string, data any, funcs template.FuncMap) error // any: template data
	// Reload discards and reparses all templates. Used in dev mode.
	Reload() error
	// Names returns every loaded template path, sorted.
	Names() []string
}
```

### `html/template` implementation

`NewHTML(cfg HTMLConfig) (*HTMLEngine, error)` where

```go
type HTMLConfig struct {
	Root      string
	Extension string
	DevMode   bool
	Funcs     template.FuncMap
}
```

- Walks `Root` recursively with `filepath.WalkDir`, collecting files whose extension
  matches `Extension`. Template names are **slash-separated paths relative to Root**
  (`layouts/default.html`), produced with `filepath.ToSlash` so Windows and macOS agree.
- Parses everything into one `*template.Template` set so templates can reference each
  other with `{{template "other/file.html" .}}`.
- Refuses to escape the root: a resolved path outside `Root` is `ErrTemplateEscapesRoot`.
- Missing root directory → `ErrTemplateRootMissing` wrapped with the path.
- `Render` on an unknown path → `ErrTemplateNotFound` wrapped with the path.
- `DevMode: true` → `Render` calls `Reload()` first, guarded by a mutex. `DevMode: false`
  parses once at construction; `Reload` still works when called explicitly.
- Concurrency: guard the template set with a `sync.RWMutex`. `Render` takes the read
  lock, `Reload` the write lock. This MUST be race-clean under `go test -race`.
- `Render` honours `ctx`: check `ctx.Err()` before executing and return it if non-nil.
- `Render` writes into a `bytes.Buffer` first and only copies to `w` on success, so a
  template error never emits half a page.
- `RenderWithFuncs` calls `Clone()` on the parsed set, applies `Funcs(funcs)` to the
  clone, and executes that. Because `html/template` requires every function *name* to be
  known at parse time, every name the render engine will later override — `slot` above
  all — MUST already be present in `DefaultFuncs()` at construction. A name absent at
  parse time cannot be added by `RenderWithFuncs`; state this in the doc comment and
  cover it with a test that overrides `slot` successfully and asserts a never-declared
  name fails at parse.
- `Render(ctx, w, path, data)` is exactly `RenderWithFuncs(ctx, w, path, data, nil)`.

### Slot function

`slot` is registered in the default FuncMap but its real implementation is
**per-render**, supplied by the render engine in Task 7 — the engine builds a FuncMap
clone bound to the current render state. In this task:

- `DefaultFuncs() template.FuncMap` provides: `slot` (a placeholder that returns
  `ErrSlotOutsideRender` when called, so a stray `{{slot "x"}}` outside a render is a
  clear error, not a nil-map panic), `safeHTML`, `safeURL`, `dict`, `default`, `upper`,
  `lower`, `title`, `join`, `formatTime`.
- `dict(pairs ...any) (map[string]any, error)` — `// any:` comment required; odd
  argument count is an error; non-string key is an error.
- `safeHTML(s string) template.HTML` and `safeURL(s string) template.URL` are documented
  as trust-the-caller escapes hatches.
- The engine MUST allow the caller's `Funcs` to override any default.

### Tests

Fixture templates in `testdata/` (a layout, a page, one with a syntax error under a
directory excluded from the default root). Cover: loading and `Names()` sorted output;
rendering with data; unknown template error; dev-mode reload picking up a file
rewritten mid-test (write the file, call Render, assert new output); non-dev-mode NOT
picking it up until `Reload()`; parse error surfaced at construction; root escape
rejected; each FuncMap function including `dict` error cases; `ctx` already cancelled
returns `ctx.Err()`; partial output not written when execution fails mid-template.
Run the package with `-race`.

### Verification

Global Constraints gate plus `go test ./internal/template/... -race -count=1`.

## Task 4: Cache interface, key generation, memory implementation

### Files

- `internal/cache/cache.go` — interface, `Entry`, errors.
- `internal/cache/key.go` — key generation and ETag.
- `internal/cache/memory.go` — in-memory implementation.
- `internal/cache/key_test.go`, `internal/cache/memory_test.go`

### Interface (spec-mandated shape)

```go
// Cache stores rendered pages keyed by request identity and tagged for invalidation.
type Cache interface {
	Get(ctx context.Context, key string) (content []byte, etag string, found bool)
	Set(ctx context.Context, key string, content []byte, ttl time.Duration) (etag string, err error)
	Invalidate(ctx context.Context, tags []string) error
	InvalidateKey(ctx context.Context, key string) error
	Clear(ctx context.Context) error
}
```

`Set` as specified carries no tags, so tag association is a separate concern owned by
the dependency tracker (Task 5). To let a cache implementation that *can* store tags do
so without widening the interface every caller depends on, add an optional extension
interface in the same package:

```go
// TaggedCache is implemented by caches that can associate tags with an entry at write
// time. Callers type-assert for it; a cache that does not implement it relies on the
// dependency tracker for invalidation.
type TaggedCache interface {
	Cache
	SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (etag string, err error)
}
```

`MemoryCache` implements both.

### Key generation (`key.go`)

```go
// KeyInput identifies one cacheable render.
type KeyInput struct {
	Path   string
	Locale string
	Params map[string]string
	Vary   []string // extra discriminators, e.g. a plugin's cache dimension
}
```

- `Key(in KeyInput) string` — SHA-256 over a canonical serialisation, hex-encoded,
  prefixed `"v1:"` so the scheme can change later. Canonical form: path, then locale,
  then params sorted by key, then sorted `Vary`, each field length-prefixed
  (`fmt.Fprintf(h, "%d:%s", len(s), s)`) so no two distinct inputs can produce the same
  byte stream. **Test this collision resistance explicitly**: `{Path: "/ab", Locale: "c"}`
  and `{Path: "/a", Locale: "bc"}` must produce different keys.
- `ETag(content []byte) string` — strong ETag, `"` + hex SHA-256 (first 16 bytes) + `"`,
  including the surrounding quotes as HTTP requires.
- `ETagMatch(ifNoneMatch, etag string) bool` — handles `*`, comma-separated lists,
  surrounding whitespace, and the `W/` weak prefix on the request side.

### Memory cache

`NewMemory(cfg MemoryConfig) *MemoryCache` with `MemoryConfig{DefaultTTL time.Duration; MaxEntries int; Now func() time.Time}`.
`Now` defaults to `time.Now` and exists so tests control expiry without sleeping — the
tests MUST use it rather than `time.Sleep`.

- Entries hold content, etag, expiry, tags, and an insertion sequence number.
- `Get` returns `found=false` for an expired entry and deletes it (lazy expiry).
- `Set` with `ttl <= 0` uses `DefaultTTL`; a `DefaultTTL` of 0 means no expiry.
- `MaxEntries > 0`: on insert past the limit, evict the **oldest by insertion sequence**
  (FIFO). Document that this is FIFO, not LRU.
- Tag index: `map[string]map[string]struct{}` tag → keys, maintained on `SetTagged`,
  `InvalidateKey`, `Invalidate`, eviction, and lazy expiry. **No orphan entries**: a
  test must assert the tag index is empty after every key it referenced is gone.
- `sync.RWMutex` throughout; `Get` must not hold the write lock except for lazy delete.
- `Stats() Stats` returning `Hits, Misses, Sets, Evictions, Entries uint64` for Task 6.

### Tests

Key determinism and collision resistance (above); ETag stability and `ETagMatch` across
`*`, weak, list, and whitespace forms; memory get/set/miss; expiry via injected clock;
FIFO eviction at `MaxEntries`; tag invalidation removing exactly the tagged keys and
leaving others; tag index cleanliness after invalidation, expiry, and eviction; `Clear`;
concurrent `Get`/`Set`/`Invalidate` under `-race`.

### Verification

Global Constraints gate plus `go test ./internal/cache/... -race -count=1`.

## Task 5: Dependency tracker and tag invalidation

### Files

- `internal/dependency/tracker.go`
- `internal/dependency/tracker_test.go`

### Interface

```go
// Tracker records which cache keys were produced from which dependency tags and
// resolves tags back to the keys they invalidate.
type Tracker interface {
	Track(ctx context.Context, key string, tags []string) error
	Resolve(ctx context.Context, tags []string) ([]string, error)
	Forget(ctx context.Context, key string) error
	ForgetTags(ctx context.Context, tags []string) error
	Tags(ctx context.Context, key string) ([]string, error)
	Clear(ctx context.Context) error
}
```

`NewMemory() *MemoryTracker` maintains both directions — tag → keys and key → tags — so
`Forget` can remove a key from every tag it belongs to without scanning. Both maps are
kept consistent under one `sync.RWMutex`.

- `Track` replaces a key's previous tag set (a re-render may emit different tags) and
  removes it from tags it no longer belongs to. **Test the re-track case explicitly.**
- `Resolve` returns a de-duplicated, sorted slice — determinism matters for tests and
  for the invalidation order. Unknown tags contribute nothing and are not an error.
- Empty tag lists are no-ops, never errors — **except `Track`**, whose `tags` argument is
  a *replacement* set rather than a target set. `Track(ctx, key, nil)` means "this key now
  depends on nothing" and must actively clear the key's previous tags. Treating it as a
  no-op would let a page that stopped depending on a tag keep resolving from it forever,
  which is precisely the stale-invalidation failure the re-tracking property guards
  against.
- `MaxKeysPerTag int` on `MemoryTracker` (0 = unlimited) guards unbounded growth; when
  exceeded, the oldest key for that tag is dropped and a counter incremented, exposed as
  `Stats() Stats{Keys, Tags, Dropped uint64}`.

### Tests

Track/resolve round trip; multi-tag keys; re-tracking a key narrowing its tag set (the
key must not resolve from the dropped tag); `Forget` removing from all tags;
`ForgetTags`; resolve determinism (sorted, de-duplicated) asserted across repeated
calls; unknown tag resolving empty; `MaxKeysPerTag` dropping the oldest and counting it;
concurrent access under `-race`.

### Verification

Global Constraints gate plus `-race` on this package.

## Task 6: Observability interfaces

### Files

- `internal/observability/metrics.go`
- `internal/observability/tracing.go`
- `internal/observability/noop.go`
- `internal/observability/observability_test.go`
- Edit `pkg/collage/config.go` to add the `ObservabilityConfig` field deferred from Task 2.

### Interfaces

```go
// Metrics receives framework counters and timings. Implementations must be safe for
// concurrent use and must not block.
type Metrics interface {
	RenderDuration(ctx context.Context, page string, d time.Duration, cacheHit bool)
	FragmentDuration(ctx context.Context, page, fragment string, d time.Duration, err error)
	CacheEvent(ctx context.Context, event CacheEvent, key string)
	HTTPResponse(ctx context.Context, status int, path string, d time.Duration)
	Invalidation(ctx context.Context, tags []string, keys int)
}

// Tracer starts spans around framework operations.
type Tracer interface {
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// Span is a single traced operation.
type Span interface {
	SetAttribute(key, value string)
	RecordError(err error)
	End()
}
```

`CacheEvent` is a string-backed named type with constants `CacheHit`, `CacheMiss`,
`CacheSet`, `CacheEvict`, `CacheInvalidate`.

`noop.go` provides `NoopMetrics{}`, `NoopTracer{}`, `NoopSpan{}` — zero-allocation,
value receivers — and `MetricsOrNoop(m Metrics) Metrics` / `TracerOrNoop(t Tracer) Tracer`
helpers so no caller ever nil-checks.

Also provide `Timing` — a small struct the render engine fills:

```go
// Timing records where a render spent its time.
type Timing struct {
	Total     time.Duration
	Data      time.Duration
	Template  time.Duration
	CacheHit  bool
	Fragments int
}
```

### Tests

Noop implementations satisfy the interfaces (compile-time `var _ Metrics = NoopMetrics{}`)
and do nothing observable; `MetricsOrNoop(nil)` returns a usable value; a recording
fake used by later tasks lives here as `RecordingMetrics` (exported, concurrency-safe,
with a `Snapshot()` method) so Tasks 7 and 10 do not each write their own.

### Verification

Global Constraints gate. `pkg/collage` still builds with the new config field.

## Task 7: Render engine

The core of the framework. Depends on Tasks 1, 3, 6.

### Files

- `internal/render/engine.go` — `Engine` interface, `Result`, `Metadata`, options.
- `internal/render/fragment.go` — recursive fragment rendering, slot resolution.
- `internal/render/execute.go` — panic-safe data handler execution with timeout.
- `internal/render/errors.go`
- `internal/render/engine_test.go`, `internal/render/fragment_test.go`,
  `internal/render/execute_test.go`
- `internal/render/testdata/` fixtures.

### Public shape

```go
// Engine renders a page into HTML.
type Engine interface {
	Render(ctx context.Context, rc *types.RenderContext) (*Result, error)
}

// Result is one page's rendered output.
type Result struct {
	HTML           []byte
	DependencyTags []string
	Metadata       *Metadata
}

// Metadata describes how a render performed.
type Metadata struct {
	Page      string
	Locale    string
	Timing    observability.Timing
	Fragments []FragmentMetadata
}

// FragmentMetadata records one fragment's contribution to a render.
type FragmentMetadata struct {
	Name     string
	Duration time.Duration
	Failed   bool
	UsedFallback bool
	Err      error
}
```

`New(tmpl template.Engine, opts Options) *SlotEngine` with

```go
type Options struct {
	MaxDepth       int           // default 32
	DefaultTimeout time.Duration // default 5s
	Metrics        observability.Metrics
	Tracer         observability.Tracer
	DevMode        bool
}
```

### Algorithm

1. `Render` resolves the root fragment via `rc.Page.Root()`. Nil root → `ErrNoRootFragment`.
2. Render a fragment:
   a. Depth check against `MaxDepth` → `ErrMaxDepthExceeded` naming the fragment chain.
   b. Run `DataHandler` if set, through `execute.go` (below). Collect its tags.
   c. Build a per-render `slot` function bound to this fragment's `Slots` and the
      current depth, clone the engine's FuncMap, and render the fragment's template.
      `{{slot "name"}}` renders every fragment in that slot's `Fill` **in binding
      order**, concatenates the output, and returns `template.HTML`. An unknown slot
      name in a template is an error, not silent empty output.
   d. A slot declared `Required` with no fill is `ErrRequiredSlotEmpty`.
3. Tag collection: the root's tags, every descendant's tags, plus `rc.Page.DependencyTags`,
   de-duplicated and **sorted** before returning — determinism is a spec invariant.
4. Timing: total, data time, template time, fragment count, reported to `Metrics`.

**Per-fragment failure policy (spec section 4):**

```
fragment error
  ├── Fragment.Required == true  → propagate; the whole render fails
  └── Fragment.Required == false
        ├── Fallback != nil → render the fallback; if the fallback ALSO fails, emit
        │                     empty output and log — a fallback failure never
        │                     escalates to a page failure
        └── Fallback == nil → emit empty output, record the error in
                              FragmentMetadata, and continue
```

**A required fragment's failure is non-absorbable.** Once a `Required` fragment fails,
no optional ancestor may swallow the error via its own fallback — otherwise "Required
fails the whole render" silently stops holding for every fragment below the first
fallback in the tree, which is the opposite of what `Required` promises. Mark such an
error as non-absorbable as it propagates and let it pass through fallback handling
untouched. `ErrMaxDepthExceeded` and a nil entry in a slot's `Fill` propagate the same
way: they indicate a malformed tree, not a runtime data failure, and a fallback must not
mask them.

**Timing metadata survives failure.** The spec guarantees every render has timing
metadata, so a fatal render error must not discard it. `Render` returns a non-nil
`*Result` on **every** path: on failure its `HTML` is nil and its `Metadata` is populated
with whatever was gathered. Callers must check the error before reading `HTML`; say so in
the doc comment. `Metrics.RenderDuration` is reported on the failure path too.

A non-required fragment that fails still contributes the tags its handler returned
before failing (if any), and its `FragmentMetadata.Failed` is true. This is how the
caller can decide not to cache a degraded page — expose `Result.Degraded() bool`
returning true when any fragment failed. **The HTTP layer (Task 10) must not cache a
degraded result.**

### `execute.go`

```go
// Execute runs fn with a timeout and converts a panic into an error.
func Execute(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error
```

- Wraps `ctx` with `context.WithTimeout` when `timeout > 0`.
- `defer recover()` converting a panic into `PanicError{Value any, Stack []byte}`
  (`// any:` comment required — a recovered panic value is `any` by language definition).
  `PanicError` implements `error` and `Unwrap() error` when the recovered value is an
  error.
- Runs `fn` **on the calling goroutine**, not a spawned one: a spawned goroutine cannot
  be killed on timeout and would leak. The timeout therefore bounds the context the
  handler receives, and a handler that ignores its context can still overrun — document
  this limitation explicitly in the doc comment. Do NOT spawn a goroutine per handler.
- Returns `context.DeadlineExceeded` wrapped when the context expired and `fn` returned
  a context error.

Template execution is wrapped in `Execute` too, since a template `FuncMap` function can
panic.

### Tests

Single fragment with data; layout + content through a slot; nested slots three deep;
binding order preserved with two fragments in one `AllowMultiple` slot; unknown slot
name in a template errors; required-slot-empty errors; required fragment failure fails
the render; optional fragment failure renders the fallback; fallback failure yields
empty output and a non-nil `FragmentMetadata.Err` without failing the page; a
`DataHandler` that panics produces `PanicError` and does not crash the test binary; a
template FuncMap panic is likewise contained; timeout honoured by a handler that
respects `ctx.Done()`; depth limit trips at `MaxDepth`; tags de-duplicated and sorted
across a tree; `Degraded()` true exactly when a fragment failed; metadata timings
non-zero; `-race` clean with concurrent renders of the same page.

### Verification

Global Constraints gate plus `go test ./internal/render/... -race -count=1`.

## Task 8: Router — radix tree, locales, redirects

### Files

- `internal/router/router.go` — `Router` interface, `MatchResult`.
- `internal/router/radix.go` — the tree.
- `internal/router/locale.go` — locale resolution.
- `internal/router/pattern.go` — pattern parsing and validation.
- `internal/router/radix_test.go`, `internal/router/locale_test.go`,
  `internal/router/router_test.go`

### Shape

```go
// MatchResult describes what a request resolved to.
type MatchResult struct {
	Page       *types.Page
	Locale     string
	PathParams map[string]string
	RedirectTo string
	RedirectStatus int
	IsNotFound bool
}

// Router resolves requests to pages.
type Router interface {
	Match(req *http.Request) (*MatchResult, error)
	Register(page *types.Page) error
	RegisterNotFound(page *types.Page) error
	RegisterError(page *types.Page) error
	NotFoundPage() *types.Page
	ErrorPage() *types.Page
}
```

Note the concrete `*types.Page` — the spec's `interface{}` is replaced per Global
Constraint 3. `MatchResult.RedirectStatus` is added so the handler does not re-derive it.

### Patterns

- `/blog/{slug}` — a named dynamic segment matching one path segment.
- `/files/{path...}` — a catch-all, valid only as the final segment. A catch-all
  anywhere else is `ErrInvalidPattern`.
- Static segments beat dynamic segments beat catch-all, at every level — the classic
  radix priority. **Test that `/blog/new` chooses a registered static route over
  `/blog/{slug}`.**
- Duplicate registration of the same pattern for the same locale is `ErrDuplicateRoute`
  naming both pages.
- Trailing slashes: `/blog/` and `/blog` match the same route. Normalise by trimming a
  single trailing slash except for the root `"/"`.
- Percent-decoding: decode each segment with `url.PathUnescape` before matching, so
  `/blog/hello%20world` yields the param `hello world`. A decode failure is a 404, not
  a 500.

### Locale resolution (priority order from the spec)

1. An explicit locale prefix in the path (`/tr/blog/post`) when `LocaleConfig.FromPath`
   and the prefix is in `Supported`. The prefix is stripped before route matching.
2. `Accept-Language`, when `FromHeader` — parse q-values properly, pick the highest-q
   supported language, fall back on a primary-subtag match (`tr-TR` matches `tr`).
3. The cookie named by `LocaleConfig.FromCookie`, when non-empty.
4. `LocaleConfig.Default`.

Every request resolves to exactly one supported locale — a spec invariant. An
unsupported value at any stage falls through to the next stage rather than failing.
`New(cfg LocaleOptions)` takes the resolved config from `pkg/collage`, passed as a
small router-owned struct (do NOT import `pkg/collage`).

### Redirects

Registering a page also registers its `Redirects`. A matched redirect sets `RedirectTo`
and `RedirectStatus` and leaves `Page` nil. `{param}` placeholders in a redirect's `To`
are substituted from the params captured by `From` — an unsubstituted placeholder is a
registration-time error, not a runtime surprise. **Redirect matching takes priority over
page matching** for the same path, and a redirect whose `From` equals a registered page
path is `ErrRedirectShadowsPage`.

### No match

`Match` returns `IsNotFound: true` with the resolved locale and a nil `Page` — it never
returns an error for "no route". Errors are reserved for malformed input the caller
must know about.

### Tests

Static, dynamic, catch-all, and priority ordering; nested dynamic segments;
percent-decoding and its failure mode; trailing-slash equivalence; duplicate route
rejection; catch-all in a non-final position rejected; locale from path, header
(including q-values and primary-subtag fallback), cookie, and default, plus the
precedence between them; unsupported locale falling through; redirect matching with
parameter substitution; redirect shadowing a page rejected at registration; no-match
returning `IsNotFound` with a locale; a table of at least 20 route/request pairs.

### Verification

Global Constraints gate.

## Task 9: Plugin system

### Files

- `internal/plugin/plugin.go` — `Plugin` interface and hook interfaces.
- `internal/plugin/registry.go` — registration, ordering, dispatch.
- `internal/plugin/hooks.go` — hook payload types.
- `internal/plugin/registry_test.go`, `internal/plugin/hooks_test.go`

### Interfaces

```go
// Plugin is the minimum contract. Hooks are opt-in via the interfaces below.
type Plugin interface {
	Name() string
	Version() string
	Init(ctx context.Context, host Host) error
	Shutdown(ctx context.Context) error
}
```

`Host` replaces the spec's `app interface{}` (Global Constraint 3). It is the narrow,
read-mostly surface the core offers plugins — this is also how "plugins cannot mutate
core state" is enforced structurally:

```go
// Host is the capability surface a plugin receives. It exposes what plugins may read
// and the few things they may do; it deliberately offers no way to mutate pages,
// fragments, or the router after startup.
type Host interface {
	DevMode() bool
	Pages() []*types.Page
	Page(name string) (*types.Page, bool)
	InvalidateTags(ctx context.Context, tags ...string) error
	Logger() *slog.Logger
	RegisterCommand(cmd Command) error
}
```

`Command` is `struct{ Name, Usage, Short string; Run func(ctx context.Context, args []string) error }`,
consumed by the CLI in Task 13.

Hook interfaces, each optional and type-asserted by the registry:

```go
type PageResolvedHook interface { OnPageResolved(ctx context.Context, ev *PageResolvedEvent) error }
type BeforeRenderHook  interface { OnBeforeRender(ctx context.Context, ev *BeforeRenderEvent) error }
type AfterRenderHook   interface { OnAfterRender(ctx context.Context, ev *AfterRenderEvent) error }
type CacheWriteHook    interface { OnCacheWrite(ctx context.Context, ev *CacheWriteEvent) error }
type CacheInvalidateHook interface { OnCacheInvalidate(ctx context.Context, ev *CacheInvalidateEvent) error }
type ErrorHook         interface { OnError(ctx context.Context, ev *ErrorEvent) error }
```

Event structs carry pointers to the live objects where mutation is intended and copies
where it is not. Specifically: `AfterRenderEvent` has an `HTML []byte` field the plugin
MAY replace (this is the spec's "can add post-process steps"), and a `Page *types.Page`
field documented as read-only. `CacheWriteEvent` has `Skip bool` a plugin may set to
suppress the write, plus `TTL` and `Tags` it may adjust.

### Registry

`NewRegistry(logger *slog.Logger) *Registry` with:

- `Register(p Plugin) error` — duplicate `Name()` is `ErrDuplicatePlugin`; empty name is
  an error; registration after `Init` is `ErrRegistryStarted`.
- `Init(ctx, host) error` — calls each plugin's `Init` **in registration order**. A
  failure aborts and calls `Shutdown` on the already-initialised plugins in reverse
  order, wrapping both errors.
- `Shutdown(ctx) error` — reverse order, collects every error with `errors.Join`, never
  stops early.
- One dispatch method per hook, e.g.
  `PageResolved(ctx context.Context, ev *PageResolvedEvent) error`. Dispatch is
  sequential in registration order. A hook error is wrapped with the plugin's name.
  **`OnError` hook failures are logged and swallowed** — an error handler that errors
  must not recurse.
- Hook dispatch is panic-safe: a panicking plugin yields an error naming it, and the
  host survives. Reuse the render package's `Execute`? No — `internal/plugin` must not
  depend on `internal/render`. Implement a small local `safeCall` with the same
  recover-to-error shape and note the deliberate duplication in a comment.
- `Plugins() []Plugin` returns a copy of the slice.

The core must be fully usable with zero plugins: every dispatch on an empty registry is
a no-op returning nil, and a nil `*Registry` receiver is safe.

### Tests

Registration, duplicate rejection, ordering; `Init` failure rolling back initialised
plugins in reverse; `Shutdown` collecting all errors; each hook dispatched in order; a
panicking hook contained and named; `AfterRender` HTML replacement observed by the
caller; `CacheWrite` `Skip` honoured by the test's inspection of the event; `OnError`
hook error swallowed; nil registry and empty registry no-ops; a plugin that implements
no hooks is registered and never called.

### Verification

Global Constraints gate.

## Task 10: HTTP handler

Wires router, cache, render engine, plugins, and observability into the request
lifecycle from spec section 3. Depends on Tasks 1, 4, 5, 6, 7, 8, 9.

### Files

- `internal/httpx/handler.go`
- `internal/httpx/errorpage.go` — error page resolution and the built-in fallbacks.
- `internal/httpx/handler_test.go`, `internal/httpx/errorpage_test.go`

### Shape

```go
// Handler serves rendered pages over HTTP.
type Handler struct { ... }

// Deps are the collaborators a Handler needs. Every field is required except Cache,
// Metrics, Tracer, and Plugins.
type Deps struct {
	Router   router.Router
	Renderer render.Engine
	Cache    cache.Cache
	Tracker  dependency.Tracker
	Plugins  *plugin.Registry
	Metrics  observability.Metrics
	Tracer   observability.Tracer
	Logger   *slog.Logger
	DevMode  bool
	DefaultTTL time.Duration
}

func New(d Deps) (*Handler, error)
```

`Handler` implements `http.Handler`.

### Lifecycle (exactly the spec's diagram)

1. `Router.Match`. Error → 500 path. `RedirectTo` non-empty → write `Location` and the
   redirect status, done. `IsNotFound` → 404 path.
2. Plugin `OnPageResolved`. A hook error → 500 path.
3. Cache lookup when `page.Strategy.Cacheable()` and the cache is non-nil and the
   request method is GET or HEAD. Build the key with `cache.Key`.
   - Hit and `If-None-Match` matches → `304`, no body, ETag header set.
   - Hit → `200` with the cached body, `ETag`, and `Cache-Control` derived from the
     strategy (`Static` → `public, max-age=0, must-revalidate`; `Incremental` →
     `public, max-age=<ttl seconds>`; `Dynamic` → `no-store`).
4. Plugin `OnBeforeRender`.
5. Render. Error → 500 path.
6. Plugin `OnAfterRender` (may replace the HTML).
7. Cache write when: the strategy is cacheable, the method is GET, the result is not
   degraded, and no `CacheWrite` hook set `Skip`. Dispatch `OnCacheWrite` first. Write
   through `TaggedCache.SetTagged` when available, otherwise `Set` plus
   `Tracker.Track`.
8. `Tracker.Track(key, tags)` regardless of which write path was used, so invalidation
   works for both.
9. Write `200` with `Content-Type: text/html; charset=utf-8`, `ETag`, `Cache-Control`,
   and in dev mode an `X-Collage-Render-Time` header.

**HEAD requests** are served by rendering (or cache) and writing headers with no body.

### Error pages

`resolveNotFound(page *types.Page) *types.Page` → the matched page's `NotFoundPage`, else
the router's registered global, else nil. Same for `resolveError`. When the resolution
yields nil OR rendering the error page itself fails, write the **built-in minimal page**:
a self-contained HTML document with no external references, status set correctly.

- In dev mode the built-in 500 page includes the failing fragment's name and the full
  error chain (`%+v` style: error text plus the `PanicError` stack when present).
- In production it contains a generic message and nothing else. **Test that a production
  500 body contains none of: the error text, the fragment name, any file path.** This is
  a security property, not a nicety.
- 404 and 500 responses set `Cache-Control: no-store` and are never written to the
  cache. A test must assert the cache is empty after a 500.
- An error while rendering the error page is logged once and never retried — no
  recursion.

Plugin `OnError` is dispatched for every 4xx/5xx the handler produces.

### Tests

Use `httptest`. Cover: 200 render; cache hit served without re-rendering (assert the
render engine was called once across two requests with a counting fake); 304 on
`If-None-Match`; redirect status and `Location`; 404 with the global page; 404 with a
page-specific override; 500 with the page-specific error page; 500 falling back to the
built-in when no error page is registered; **dev vs prod 500 body contents**; degraded
render not cached; `Dynamic` strategy never cached; POST not cached; HEAD returning
headers and an empty body; `Cache-Control` per strategy; plugin hooks called in order
with a recording plugin; `OnAfterRender` HTML replacement reaching the wire;
`CacheWrite` `Skip` preventing the write; tracker receiving the tags; `OnError`
dispatched on 404 and 500; `-race` with concurrent requests.

### Verification

Global Constraints gate plus `go test ./internal/httpx/... -race -count=1`.

## Task 11: Application orchestrator

Ties every subsystem together and provides the public `App`. Depends on all prior tasks.

### Files

- `internal/core/app.go`
- `internal/core/registry.go` — page registration and validation.
- `internal/core/app_test.go`, `internal/core/registry_test.go`
- `pkg/collage/collage.go` — the public `New`, `App` alias, and facade methods.

### `internal/core`

```go
// App owns every subsystem and the server lifecycle.
type App struct { ... }

// New builds an App from a validated configuration.
func New(cfg Config) (*App, error)
```

`core.Config` is the internal mirror of `pkg/collage.Config` — `pkg/collage` converts
its public `Config` into `core.Config` so `internal/core` never imports `pkg/collage`.
Keep the conversion in `pkg/collage/collage.go` and keep the two structs field-aligned;
a comment on each points at the other.

`App` methods:

- `RegisterPage(p *types.Page) error` — validates, performs the layout/content slot
  binding (`p.LayoutFragment.Bind(types.DefaultContentSlot, p.ContentFragment)`) exactly
  once per page, checks that every referenced template exists in the template engine
  (`ErrTemplateNotFound` naming the fragment and path — this catches typos at startup
  rather than on the first request), and registers routes. Registration after
  `ListenAndServe` has started is `ErrAppStarted`.
- `RegisterNotFoundPage(p *types.Page) error`, `RegisterErrorPage(p *types.Page) error`.
- `RegisterPlugin(p plugin.Plugin) error`.
- `InvalidateTags(ctx context.Context, tags ...string) error` — resolve via the tracker,
  `InvalidateKey` each, dispatch `OnCacheInvalidate`, `Forget` the keys, report the count
  to `Metrics`. Returns the number invalidated via a sibling
  `InvalidateTagsN(ctx, tags...) (int, error)`; `InvalidateTags` delegates to it.
- `Handler() http.Handler` — lazily builds and memoises the HTTP handler, running plugin
  `Init` on first call so `Handler()` is usable in tests without a server.
- `ListenAndServe() error` — builds the `http.Server` from `ServerConfig` with all four
  timeouts set, and traps `SIGINT`/`SIGTERM` for graceful shutdown using
  `ShutdownTimeout`. Returns nil on a clean shutdown, not `http.ErrServerClosed`.
- `Shutdown(ctx context.Context) error` — stops the server, then the plugin registry,
  joining errors.
- `Pages() []*types.Page`, `Page(name string) (*types.Page, bool)`, `DevMode() bool`,
  `Logger() *slog.Logger`, and `RegisterCommand(cmd plugin.Command) error` — together
  with `InvalidateTags` these make `*App` satisfy `plugin.Host`. Assert it with
  `var _ plugin.Host = (*App)(nil)`. `RegisterCommand` rejects a duplicate or empty name.
- `Commands() []plugin.Command` — the commands plugins contributed, for the CLI in
  Task 13 to list and dispatch. Returns a copy.
- `RenderPath(ctx context.Context, path, locale string, params map[string]string) (*render.Result, error)`
  — renders one page outside the HTTP path, resolving the page by path through the
  router. This is the entry point the static builder in Task 12 consumes through its own
  narrow `Renderer` interface; it bypasses the cache and returns the raw result.

Validation at registration is where the framework's "no silent failures" invariant is
enforced: a page whose template is missing, whose required slot is unfilled, or whose
strategy/TTL combination is incoherent must fail at `RegisterPage`, with an error that
names the page and the specific problem.

### `pkg/collage/collage.go`

- `type App = core.App` — alias, so users call the methods above directly.
- `func New(cfg *Config) (*App, error)` — nil config is an error; applies defaults,
  validates, converts, delegates to `core.New`.
- `type Plugin = plugin.Plugin`, `type Host = plugin.Host`, `type Cache = cache.Cache`,
  `type Metrics = observability.Metrics`, `type Tracer = observability.Tracer` aliases so
  users can implement these without importing internal packages.

### Tests

Build an App over a temporary template directory; register the spec's minimal example
and assert the page serves 200 through `Handler()`; missing template rejected at
registration naming the fragment; duplicate page name rejected; layout/content binding
happening exactly once (register, then assert the layout's content slot has exactly one
fill); `InvalidateTags` clearing exactly the matching cache entries end to end (render,
assert cached, invalidate, assert re-rendered); registration after start rejected;
`Shutdown` idempotent; `var _ plugin.Host = (*App)(nil)` compiles; graceful shutdown
returning nil.

### Verification

Global Constraints gate plus `-race`.

## Task 12: Static site generation

### Files

- `internal/build/builder.go`
- `internal/build/builder_test.go`

### Shape

```go
// Builder renders pages to static files.
type Builder struct { ... }

// Options configures a static build.
type Options struct {
	OutDir      string
	Locales     []string   // empty = every locale each page declares
	Clean       bool       // remove OutDir's contents first
	Concurrency int        // default 1 (sequential, per the determinism invariant)
	PathProvider PathProvider
}

// PathProvider supplies concrete paths for pages with dynamic segments. A page whose
// pattern contains {param} cannot be built without one.
type PathProvider interface {
	Paths(ctx context.Context, page *types.Page, locale string) ([]PathInstance, error)
}

// PathInstance is one concrete URL for a dynamic page.
type PathInstance struct {
	Path   string
	Params map[string]string
}
```

`New(app Renderer, opts Options) (*Builder, error)` where `Renderer` is a narrow local
interface satisfied by `*core.App` (`Pages()`, and a render entry point) — declared in
`internal/build` so the dependency points the right way.

`Build(ctx) (*Report, error)`:

- Skips `StrategyDynamic` pages (they cannot be static) and records them in the report
  as skipped, with the reason.
- For each page × locale: resolve concrete paths (static pattern → itself; dynamic →
  `PathProvider`, and with no provider record a skip with `ErrDynamicPathUnresolved`).
- Renders through the same render engine as the server, so output is identical.
- Writes `<OutDir>/<path>/index.html`, creating directories. The root path writes
  `<OutDir>/index.html`. **Path safety: reject any resolved path that escapes `OutDir`
  after `filepath.Clean`** — a `PathProvider` is user code and `..` in a param must not
  write outside the output directory. This is a required test.
- `Clean: true` removes only the contents of `OutDir`, and refuses to run when `OutDir`
  is `/`, empty, or the repository root.
- `Report` carries `Written []string`, `Skipped []SkipRecord`, `Errors []error`,
  `Duration`. A page that fails to render records an error and the build continues; the
  final error is `errors.Join` of all of them, so one bad page does not hide the rest.

### Tests

Static pages written to the right paths; the root path; multiple locales writing to
their prefixed directories; dynamic page with a `PathProvider`; dynamic page without one
skipped with the right reason; a `PathProvider` returning `../escape` rejected and
nothing written outside `OutDir`; `Clean` refusing dangerous targets; a failing page
recorded without aborting the build; report contents. Use `t.TempDir()`.

### Verification

Global Constraints gate.

## Task 13: CLI

### Files

- `cmd/collage/main.go` — thin: parse, delegate, exit with a code.
- `internal/cli/cli.go` — command dispatch.
- `internal/cli/new.go` — project scaffolding.
- `internal/cli/dev.go` — dev server.
- `internal/cli/build.go` — static build.
- `internal/cli/templates.go` — embedded scaffold files via `embed.FS`.
- `internal/cli/cli_test.go`, `internal/cli/new_test.go`, `internal/cli/build_test.go`
- `internal/cli/scaffold/` — the embedded scaffold tree.

### Commands

- `collage new <name> [-dir path] [-module path]` — scaffolds a runnable project:
  `go.mod` (module path defaulting to `<name>`), `main.go` wiring one page, a layout and
  a home template, a `.gitignore`, and a README. Refuses a non-empty target directory
  unless `-force`. After scaffolding, print the exact commands to run it.
- `collage dev [-config path] [-port n]` — builds the app with `DevMode: true` and
  serves it. Template reload is per-request via `TemplateConfig.DevMode`, so no file
  watcher is needed — say so in the help text rather than implying hot reload of Go code.
- `collage build [-out dir] [-clean]` — runs the static builder and prints the report:
  files written, pages skipped with reasons, total duration.
- `collage version`, `collage help`, and `collage help <command>`.
- Plugin-contributed commands from `plugin.Host.RegisterCommand` are listed in help and
  dispatched by name. A plugin command name colliding with a built-in is rejected.

### Conventions

- Use `flag.NewFlagSet` per command, never the global `flag.CommandLine`.
- Everything writes to injected `io.Writer`s (`Stdout`, `Stderr` on a `CLI` struct), so
  tests capture output without touching the process streams.
- `Run(ctx context.Context, args []string) int` returns the exit code; `main` calls
  `os.Exit` with it and nothing else. `main` is the only place `os.Exit` appears.
- Unknown command → usage on stderr, exit 2. No arguments → usage, exit 2.

Because `dev` and `build` need a user's compiled app, they operate on the **current
directory's Go project**: `dev` runs `go run .` with `COLLAGE_DEV=1` set, and `build`
runs `go run . -collage-build -out <dir>`. Document this contract in the scaffolded
README so a generated project's `main.go` honours the flags the CLI passes. The
scaffolded `main.go` MUST implement that contract.

### Tests

`Run` with each command and with bad input, asserting exit codes and captured output;
`new` scaffolding into `t.TempDir()` and the result compiling (`go build ./...` in the
scaffold via `os/exec`, skipped with `t.Skip` when the `go` binary is unavailable);
non-empty directory refused without `-force`; `help <command>`; unknown command exit 2;
version output. Do not start a real server in tests — assert `dev` constructs its
command correctly through an injected runner.

### Verification

Global Constraints gate. `go run ./cmd/collage help` prints usage.

## Task 14: Example application and documentation

### Files

- `examples/blog/main.go`, `examples/blog/templates/**`, `examples/blog/README.md`
- `README.md` at the repository root.
- `docs/architecture.md`, `docs/fragments.md`, `docs/caching.md`, `docs/plugins.md`,
  `docs/routing.md`, `docs/cli.md`
- `examples/blog/main_test.go`

### Example

The blog example implements the spec's advanced example for real: a layout, a home page
listing posts, a `/blog/{slug}` post page with `Required()` content and a page-specific
404 and 500, permanent and temporary redirects from old URL shapes, incremental caching
with dependency tags (`post:<slug>`, `blog:posts`), an in-memory post store, global 404
and 500 pages, and a small plugin that injects a `X-Powered-By` style post-process step
through `OnAfterRender` so the plugin path is exercised end to end. It must be a
separate module or use the repository module — use the repository module, with the
example importing `github.com/Elagoht/collage/pkg/collage`.

`examples/blog/main_test.go` starts the app's `Handler()` with `httptest` and asserts:
home renders, a post renders, an unknown slug produces the blog-specific 404, a post
whose handler fails produces the blog-specific 500, a redirect returns 301, and
`InvalidateTags("post:x")` causes a re-render. This is the framework's end-to-end test.

### Documentation

`README.md`: what it is, the invariants it guarantees, install, the minimal example from
the spec (which MUST compile — keep it identical to `examples/blog` conventions), and a
map of the docs. The `docs/*.md` files cover their subsystems with real, compiling code
snippets and the design decisions from the spec's section 6, including why rendering is
sequential and why plugins get a `Host` rather than the `App`.

Document the deliberate deviations from the original spec in `docs/architecture.md`
under "Deviations from the original specification", one line each with the reason:
`interface{}` replaced by concrete types and `Host`; `internal/http` renamed `httpx`;
`TaggedCache` extension interface; `MatchResult.RedirectStatus` added; `Result.Degraded`
added; `Execute` running in-line rather than in a goroutine.

### Verification

Global Constraints gate over the whole repository, plus:
`go test ./... -race -count=1` green, `go vet ./...` clean, `gofmt -l .` empty, and
`go run ./examples/blog` starting and serving `/` (verify with a short-lived `curl` or,
preferably, the `httptest`-based test instead of starting a real server).
