# Plugins

A plugin is an ordinary Go value that implements `collage.Plugin` and opts into
whichever hooks it needs. Nothing is registered by magic: you construct it and
hand it to the application.

```go
if err := app.RegisterPlugin(&stamp{}); err != nil {
	log.Fatal(err)
}
```

## The contract

```go
type Plugin interface {
	Name() string
	Version() string
	Init(ctx context.Context, host Host) error
	Shutdown(ctx context.Context) error
}
```

Everything beyond lifecycle is opt-in. The registry discovers hooks by **type
assertion**, which has one practical consequence: a misspelled or renamed hook
method is not a compile error, it is a hook that silently never fires. Assert the
interfaces you mean to implement:

```go
var (
	_ collage.Plugin          = (*stamp)(nil)
	_ collage.AfterRenderHook = (*stamp)(nil)
)
```

## A complete plugin

```go
// stamp injects a marker comment into every rendered page and contributes one
// CLI subcommand.
type stamp struct {
	logger *slog.Logger
}

// Name identifies the plugin.
func (s *stamp) Name() string { return "blog-stamp" }

// Version reports the plugin's own version.
func (s *stamp) Version() string { return "1.0.0" }

// Init takes what it needs off the Host and registers a subcommand.
func (s *stamp) Init(_ context.Context, host collage.Host) error {
	s.logger = host.Logger()
	return host.RegisterCommand(collage.Command{
		Name:  "pages",
		Usage: "pages",
		Short: "List the registered pages",
		Run: func(_ context.Context, _ []string) error {
			for _, page := range host.Pages() {
				fmt.Println(page.Name)
			}
			return nil
		},
	})
}

// Shutdown releases what Init acquired.
func (s *stamp) Shutdown(_ context.Context) error { return nil }

// OnAfterRender post-processes the rendered page.
func (s *stamp) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	marker := []byte("<!-- rendered by blog-stamp -->")
	stamped := make([]byte, 0, len(ev.HTML)+len(marker))
	stamped = append(stamped, ev.HTML...)
	ev.HTML = append(stamped, marker...)
	return nil
}
```

`examples/blog/plugin.go` is this plugin, and `examples/blog/main_test.go`
asserts on its output end to end.

## The `Host`

`Init` receives a `collage.Host`, not the `*collage.App`:

| Method | What it does |
| --- | --- |
| `DevMode() bool` | Whether the application is in development mode |
| `Pages() []*Page` | Every registered page, each a defensive copy |
| `Page(name) (*Page, bool)` | One page by name, a defensive copy |
| `InvalidateTags(ctx, tags...) error` | Invalidate cache entries by tag |
| `Logger() *slog.Logger` | The application's structured logger |
| `RegisterCommand(Command) error` | Contribute a CLI subcommand |

A plugin therefore has no way to reach the router, the cache, the render engine,
the template set, or any page it was not explicitly handed. That is the structural
half of the specification's "plugins cannot mutate core state".

**`Host` limits reachability, not mutability.** `*collage.Page` is a plain struct
of exported fields. `Pages` and `Page` hand back a defensive copy — the struct,
plus its `Paths`, `Redirects`, `SEO`, and `DependencyTags` containers — so writing
through one of those cannot reach the framework's own page. But the fragment
pointers inside it (`LayoutFragment`, `ContentFragment`, `NotFoundPage`,
`ErrorPage`) stay shared, and the per-request **event** types carry the live
`*Page` rather than a copy, deliberately: copying a page and its fragment tree on
every render would defeat a cache-first framework's hot path.

A plugin holding an event's `*Page` can write straight through it, and doing so
mutates the same page every other request and hook sees. The framework does not
defend against that and does not claim to. In-process Go plugins are trusted code,
not a sandbox: the goal is to make accidental mutation hard and deliberate
mutation obvious. Where mutation *is* intended it is explicit —
`AfterRenderEvent.HTML`, and `CacheWriteEvent`'s `Skip`, `TTL`, and `Tags`.

## The hooks

| Interface | Method | Fires | May change |
| --- | --- | --- | --- |
| `PageResolvedHook` | `OnPageResolved` | After routing, before anything else — including on a cache hit | nothing |
| `BeforeRenderHook` | `OnBeforeRender` | Immediately before a fresh render; **not** on a cache hit | nothing |
| `AfterRenderHook` | `OnAfterRender` | After a successful render | `ev.HTML` |
| `CacheWriteHook` | `OnCacheWrite` | Before a render result is stored | `ev.Skip`, `ev.TTL`, `ev.Tags` |
| `CacheInvalidateHook` | `OnCacheInvalidate` | After entries for some tags were invalidated | nothing |
| `ErrorHook` | `OnError` | On any failure while serving a request | nothing |

Two consequences of where `OnAfterRender` sits are worth stating plainly:

- **A cached response does not run it again.** The hook's output is what got
  cached, so the post-processing is already baked into the stored bytes. A hook
  that needs to run per request — injecting a nonce, a per-visitor token — must
  not be combined with a cacheable strategy on that page.
- **A rendered error page does not run it at all.** The error path renders the
  error page directly rather than through the request's render pipeline.

An `ErrorHook` can classify what it receives with `errors.Is`:
`collage.ErrNoRoute` (no route matched — a link problem), `collage.ErrNotFound`
(a required fragment's content does not exist — a content problem),
`collage.ErrEmptyErrorPage` (a registered error page rendered nothing),
`collage.ErrMaxDepthExceeded`, `collage.ErrRequiredSlotEmpty`,
`collage.ErrNoRootFragment`, and `collage.PanicError` through `errors.As`.

`ErrorEvent.Stage` names where in the pipeline the failure happened (`"route"`,
`"not_found"`, `"page_resolved"`, `"before_render"`, `"render"`,
`"after_render"`, `"cache_write"`, `"error_page"`). It is caller-defined rather
than an enum. `"error_page"` is the one worth alerting on: it means the page that
reports failures failed, which nobody finds out about otherwise, because the
client still receives a plausible-looking error page.

## Dispatch and error semantics

Hooks are dispatched in **registration order**. Every hook call is panic-guarded:
a panicking hook fails like a returning-an-error hook rather than taking the
process down.

- `OnPageResolved`, `OnBeforeRender`, `OnAfterRender` — the first error stops
  dispatch and fails the request with a 500.
- `OnCacheWrite` — an error (or `ev.Skip`) suppresses the cache write, and the
  request still succeeds. The page has already rendered; serving it uncached beats
  turning a cache problem into a 500.
- `OnCacheInvalidate` — dispatched from `InvalidateTags`, not from a request; an
  error is joined into what `InvalidateTags` returns.
- `OnError` — an error is logged and swallowed, and dispatch continues to the
  remaining plugins. An error handler that itself errors must not recurse into
  another round of error handling.

For `OnAfterRender` and `OnCacheWrite`, later plugins see what earlier ones
changed.

## Lifecycle

1. `RegisterPlugin` — before the application starts. Afterwards it returns
   `collage.ErrAppStarted`, since `Init` has already run and already seen the
   registered pages. A nil plugin, an empty name, and a duplicate name are
   rejected with `collage.ErrNilPlugin`, `collage.ErrEmptyPluginName`, and
   `collage.ErrDuplicatePlugin`.
2. `Init` — once, in registration order, when the handler is built (which is what
   `Handler()` and `ListenAndServe()` both do first). An `Init` that fails aborts
   startup and rolls back: every already-initialised plugin gets `Shutdown`, in
   reverse order, and the failing plugin does not (it never finished
   initialising).
3. `Shutdown` — in reverse registration order, *after* the HTTP server has
   stopped, so a plugin is never torn out from under an in-flight request. Unlike
   `Init`, it never stops early: every plugin is given its chance and the failures
   are joined.

`RegisterCommand` is the one registration call deliberately exempt from
`ErrAppStarted`: plugins register commands from inside `Init`, which by definition
runs as part of starting.

## Commands

A registered command reaches `app.Commands()`, and the CLI dispatches it by name
alongside the built-ins. A command must not shadow a built-in (`new`, `dev`,
`build`, `version`, `help`): the CLI refuses the whole invocation rather than
silently dropping the offender. See [the CLI](cli.md).

```go
for _, cmd := range app.Commands() {
	fmt.Printf("%s\t%s\n", cmd.Name, cmd.Short)
}
```
