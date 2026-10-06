# Typed keys, no SEO bag, typed plugin config: the last `any`s of the public API

Date: 2026-10-06
Status: approved design, pending implementation
Target: collage v0.50.0, after v0.49.0
([typed fragment data and template type checking](2026-10-06-template-typecheck-design.md))

## Motivation

v0.49.0 takes `any` out of fragment data. A scan of `pkg/collage`'s exported
surface (every exported identifier, the fields and methods of the internal types
it aliases, walked with `go/types`) found the rest:

| Where | Verdict |
| --- | --- |
| `TemplateConfig.Funcs` (`template.FuncMap`), `ConfigHost.AddTemplateFunc`, `AddRenderFunc` | stays: html/template defines the type |
| `PanicError.Value` | stays: `recover` defines the type |
| `RenderContext.Get/Set`, `RenderContext.SharedData`, `AfterRenderEvent.Data` | **this spec**: typed keys |
| `Page.SEO`, `PageBuilder.WithSEO` | **this spec**: removed |
| `Host.Config(v any)`, `ConfigHost.Config(v any)` | **this spec**: `collage.PluginConfig[T]` |

`Once[T]`, `Cached[T]` and `Get[T]` are generic, but their keys are strings: one
key read as two types is caught at runtime (`ErrOnceTypeMismatch`,
`ErrCachedTypeMismatch`) or not at all (`Get` returns `false`, and a section
renders empty). In practice values are written with `rc.Set("k", v)` and read
with `collage.Get[T](rc, "k")`, and nothing ties the two types together.

## Decisions

- **One key type, `collage.Key[T]`, for the render bag, `Once` and `Cached`.**
- **A key's identity is its name and its type.** `NewKey[A]("x")` and
  `NewKey[B]("x")` are different keys. A key may be created anywhere, any number
  of times; two keys equal in name and type are the same key.
- **`Page.SEO` and `WithSEO` are removed**, not typed: nothing reads them.
- **Plugin config is read with `collage.PluginConfig(host, defaults)`.**

## Design

### 1. `collage.Key[T]`

```go
type Key[T any] struct{ /* name string; unexported */ }

func NewKey[T any](name string) Key[T]
func (k Key[T]) With(part string) Key[T]          // name + ":" + part, same T
func (k Key[T]) Name() string                     // for logs and errors
func (k Key[T]) Get(rc *RenderContext) (T, bool)
func (k Key[T]) Set(rc *RenderContext, v T)

func Once[T any](rc *RenderContext, key Key[T], fetch func(context.Context) (T, error)) (T, error)
func Cached[T any](rc *RenderContext, key Key[T], ttl time.Duration, tags []string, fetch func(context.Context) (T, error)) (T, error)
```

```go
var (
	confirmKey = collage.NewKey[confirmView]("confirm")
	articleKey = collage.NewKey[Article]("article")
)

confirmKey.Set(rc, view)
view, ok := confirmKey.Get(rc)

a, err := collage.Once(rc, articleKey.With(slug), fetchArticle)
a, err := collage.Cached(rc, articleKey.With(slug), time.Hour, []string{"article:" + slug}, fetchArticle)
```

- `Key[T]` is a small comparable value. `With` chains:
  `boardKey.With(id).With("filter")`.
- Storage is keyed by `(name, reflect.Type of T)`. The render bag, `Once`'s
  per-render memo and `Cached`'s store all use that pair, so no lookup can find a
  value of another type. `ErrOnceTypeMismatch` and `ErrCachedTypeMismatch` can no
  longer happen and are removed.
- `Get` is `false` only when nothing was set under the key in this render.
- The bag and `Once` stay separate namespaces, as today: a value `Once` fetched is
  not readable with `Get`. Merging them is a design of its own (in-flight
  fetches) and out of scope.
- An empty name is a programming error: `NewKey` panics on it, at the line that
  declared the key.

**Removed:** `RenderContext.Get`, `RenderContext.Set`, `collage.Get[T]`, the
string-keyed `Once`/`Cached`, `ErrOnceTypeMismatch`, `ErrCachedTypeMismatch`.
`RenderContext.SharedData` becomes unexported. `AfterRenderEvent.Data` is
removed: a plugin reads only what it holds a key for.

**Reading a render's values after it.** An `AfterRender` hook has no
`RenderContext`, yet elagoht/highlight, i18n and ogimage hand state from their
render functions to their hook through `ev.Data`. The event carries the render's
values instead, opaque, and a key reads from them:

```go
type RenderValues struct{ /* unexported */ }        // collage.RenderValues
func (k Key[T]) In(v *RenderValues) (T, bool)       // nil v: false

// AfterRenderEvent gains:
Values *RenderValues

st, ok := stateKey.In(ev.Values)
```

A `RenderContext` holds its values in one `*RenderValues` (its own lock), shared
by every copy the render makes, exactly as `SharedData` was.

**Plugins** that share values export their keys, for example
`validate.ErrorsKey` and `validate.ValuesKey`, and the application reads them
with those keys. Actions that hand a form its errors write through keys the same
way.

### 2. `Page.SEO` and `WithSEO`: removed

Nothing in the framework reads `Page.SEO`; the registry only copies it
(`internal/core/registry.go:631`), and no app, plugin or document uses it. Titles
and head content already go through `HoistTitle` and `{{hoist}}`. A real SEO need
later gets its own design, likely a plugin's own typed fields, as
elagoht/ogimage does.

### 3. `collage.PluginConfig`

```go
func PluginConfig[T any](h ConfigReader, defaults T) (T, error)
```

```go
cfg, err := collage.PluginConfig(host, Config{Limit: 10, Window: time.Minute})
```

- `T` is inferred from `defaults`.
- No section for the plugin: `defaults` comes back unchanged. A section: it is
  decoded over a copy of `defaults`, so unset fields keep their defaults. Today's
  meaning, without the pointer.
- `Host.Config` and `ConfigHost.Config` are removed. `ConfigReader` is an
  interface in `internal/plugin` with one unexported method; `Host` and
  `ConfigHost` embed it. collage's hosts satisfy it by embedding
  `plugin.ConfigSource`, a struct in that package whose method reads the plugin's
  section, so only collage can implement it. `collage.ConfigReader` is an alias.
  A map or slice inside `defaults` is decoded into in place, as `Config(&opts)`
  did: defaults are a literal per call in practice.
- Malformed JSON and unknown keys are reported as today (`CheckConfigKeys`
  unchanged).

## Testing

- **Keys:** `With` builds names; same name and different types are separate
  values; `Get`/`Set` stay inside one render (no leak to the next, none between
  concurrent renders).
- **`Once`, `Cached`:** today's suites move to keys: in-flight sharing, errors,
  tags, `SkipCache`, the panicking fetch. The mismatch tests are replaced by
  "same name, different type, separate values".
- **Compile-time guarantee:** a testdata package that writes `Set` with the wrong
  type and a `fetch` returning the wrong type; the test runs `go build` on it and
  expects both type errors.
- **`PluginConfig`:** no section, partial section over defaults, malformed JSON,
  unknown key.
- `go test -count=1`, and a grep of `.go.tmpl` for every removed name: the
  scaffold is not compiled by the test cache's idea of what changed.

## Migration

| Before | After |
| --- | --- |
| `rc.Set("k", v)` / `collage.Get[T](rc, "k")` | `k := collage.NewKey[T]("k")`, then `k.Set(rc, v)` / `k.Get(rc)` |
| `collage.Once(rc, "a:"+id, fetch)` | `collage.Once(rc, aKey.With(id), fetch)` |
| `collage.Cached(rc, "a:"+id, ttl, tags, fetch)` | `collage.Cached(rc, aKey.With(id), ttl, tags, fetch)` |
| `ev.Data["x"]` in `OnAfterRender` | the key the plugin that wrote it exports |
| `WithSEO(k, v)` | delete; head content goes through `HoistTitle` or `{{hoist}}` |
| `host.Config(&cfg)` | `cfg, err := collage.PluginConfig(host, defaults)` |

## Release

- collage **v0.50.0**, breaking; the changelog leads with the table above.
- Plugins: every plugin reading its config moves to `PluginConfig` — 36 of the
  39 call `host.Config` — and the ones sharing values export keys (validate) or
  move their hook state to `In` (highlight, i18n, ogimage). Every one is
  re-released. Order as in every release: tag collage, then each
  plugin drops its `replace`, `go get`s the tag, tests, tags. The tag's CI run
  builds each plugin's latest *tag* against collage, so it fails until the
  plugins are re-released; rerun it (`gh workflow run ci.yml --ref v0.50.0`)
  once they are, and the release is done when that run is green.
- Framework `docs/`, the docs site (EN, TR), the extension's snippets and
  `collage.json` schemas (`schemagen -manifests`) follow.
- The test apps are the user's to move; furkanbaytekin.dev moves when its owner
  decides.

## Out of scope

Merging the render bag with `Once`; typing `Cached`'s tags; template functions'
and `PanicError`'s `any`, which html/template and `recover` define.
