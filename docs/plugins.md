# Plugins

A plugin is an ordinary Go value that implements `collage.Plugin` and opts into
whichever hooks it needs. Nothing is registered by magic: you construct it and
hand it to the application.

```go
if err := app.RegisterPlugin(&stamp{}); err != nil {
	log.Fatal(err)
}
```

## Installing one

A plugin is a separate Go module. Two steps, and there is no third:

```
go get github.com/Elagoht/collage-minimizer
```

```go
import minimizer "github.com/Elagoht/collage-minimizer"

app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{minimizer.New()},
})
```

Configuration, when a plugin takes any, comes from a JSON file keyed by plugin name
and handed to `Config.PluginConfig` — see `collage.LoadPluginConfig`, which a scaffolded project's `main.go` already calls. A plugin with no entry runs on its defaults.

### A plugin that hoists needs somewhere to hoist to

Some plugins contribute to the document head — structured data, preload hints — and
they do it by hoisting. **Nothing appears unless the layout has the marker:**

```html
<head>
  {{hoist "head"}}
</head>
```

A scaffolded project has it. A layout written before this mattered may not, and the
symptom is a plugin that registers, runs, and produces nothing at all.

### And some of them only supply the mechanism

`collage-jsonld` is the clearest case: registering it emits nothing, because the
plugin cannot invent what a page is about. The page says so from its data handler,
which is where it already has the article it fetched:

```go
func articleData(ctx context.Context, rc *collage.RenderContext) (view, []string, error) {
	article, err := client.Article(ctx, rc.Param("slug"))
	// ...
	jsonld.Emit(rc, jsonld.Article{
		Headline:      article.Title,
		DatePublished: article.PublishedAt,
		AuthorName:    article.Author,
	})
	return view{Article: article}, nil, nil
}
```

`collage-minimizer` is the other kind: it works on what the render produced, so
registering it is the whole of it. Which kind a plugin is is the first thing its
README says.


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
half of the framework's rule that plugins cannot mutate core state.

`Host` has no `Documents` method, and that is deliberate rather than an
oversight: nothing outside the framework's own build step reads the document
registry today (see `internal/core/document.go`'s `Documents`, which
`App.RenderDocumentPath` and the static builder use directly, neither of which is
a plugin). `Pages` exists on `Host` because a plugin's per-request hooks receive a
live `*Page` a plugin may want to cross-reference against the full list; nothing
analogous currently reads a document through a hook, so there is no consumer to
build the method for yet.

**`Host` limits reachability, not mutability.** `*collage.Page` is a plain struct
of exported fields. `Pages` and `Page` hand back a defensive copy — the struct,
plus its `Paths`, `Redirects`, `SEO`, and `DependencyTags` containers — so writing
through one of those cannot reach the framework's own page. But the fragment
pointers inside it (`LayoutFragment`, `ContentFragment`, `NotFoundPage`,
`ErrorPage`) stay shared, and the per-request **event** types carry the live
`*Page` rather than a copy, deliberately: copying a page and its fragment tree on
every render would defeat a cache-first framework's hot path.

A plugin holding an event's `*Page` can write straight through it, and doing so
mutates the same page every other request and hook sees — concurrently with those
requests reading it. That is a data race in the precise sense: `go test -race`
reports it, and without the detector it corrupts whatever container was written to.
The framework does not defend against that and does not claim to. In-process Go plugins are trusted code,
not a sandbox: the goal is to make accidental mutation hard and deliberate
mutation obvious. Where mutation *is* intended it is explicit —
`AfterRenderEvent.HTML`, and `CacheWriteEvent`'s `Skip`, `TTL`, and `Tags`.

## The hooks

| Interface | Method | Fires | May change |
| --- | --- | --- | --- |
| `PageResolvedHook` | `OnPageResolved` | After routing, before anything else — including on a cache hit. **Pages only**, never a document (see "Documents dispatch three hooks, not six" below) | nothing |
| `BeforeRenderHook` | `OnBeforeRender` | Immediately before a fresh render; **not** on a cache hit. **Pages only** | nothing |
| `AfterRenderHook` | `OnAfterRender` | After a successful render. **Pages only** | `ev.HTML` |
| `CacheWriteHook` | `OnCacheWrite` | Before a render result is stored — for a page or a document alike | `ev.Skip`, `ev.TTL`, `ev.Tags` |
| `CacheInvalidateHook` | `OnCacheInvalidate` | After entries for some tags were invalidated — for a page or a document alike | nothing |
| `ErrorHook` | `OnError` | On any failure while serving a request — a page, a document, or a mounted asset alike | nothing |

Two consequences of where `OnAfterRender` sits are worth stating plainly:

- **A cached response does not run it again.** The hook's output is what got
  cached, so the post-processing is already baked into the stored bytes. A hook
  that needs to run per request — injecting a nonce, a per-visitor token — must
  not be combined with a cacheable strategy on that page.
- **A rendered error page does not run it at all.** The error path renders the
  error page directly rather than through the request's render pipeline.

### Documents dispatch three hooks, not six

A document (`collage.NewDocument`) dispatches `OnCacheWrite`,
`OnCacheInvalidate`, and `OnError`. It does **not** dispatch `OnPageResolved`,
`OnBeforeRender`, or `OnAfterRender` — those three are about a render, and a
document handler is not one: `AfterRenderEvent.HTML` would be a lie for a zip
file or a JPEG, and there is no `*Page` for `PageResolvedEvent.Page` to carry.
The accepted consequence is explicit: **a plugin cannot post-process a document
body.** A plugin that stamps every page from `OnAfterRender` stamps nothing on a
sitemap. `ErrorEvent.Page` is `nil` for a document failure — the event's `Path`
already identifies the route, and the framework's own log line names the
document. See `docs/documents.md` for the full reasoning; this table and that
page are kept in agreement on it.

**`CacheWriteEvent.Page` is `nil` for a document too**, and for the same reason:
nothing rendered it from a page. `OnCacheWrite` is one of the three hooks a
document *does* dispatch, so this is the one nil a plugin written for pages will
actually meet:

```go
func (p *myPlugin) OnCacheWrite(_ context.Context, ev *collage.CacheWriteEvent) error {
	if ev.Page == nil {
		// A document: ev.Key, ev.TTL and ev.Tags are all still valid.
		return nil
	}
	ev.TTL = p.ttlFor(ev.Page.Name)
	return nil
}
```

Getting that check wrong is worse than it looks. `safeCall` contains the panic,
so the request still completes — but the cache write it was dispatched for is
abandoned, so the document is **never cached**, on any request, and the only
symptom is one error line per request.

A mounted asset request dispatches only `OnError`, and only for a 4xx or 5xx
response, or for a panic recovered while serving it: an asset is neither a page
nor a document, so none of the cache or render hooks apply to it either.

An `ErrorHook` can classify what it receives with `errors.Is`:
`collage.ErrNoRoute` (no route matched — a link problem), `collage.ErrNotFound`
(a required fragment's content does not exist — a content problem),
`collage.ErrEmptyErrorPage` (a registered error page rendered nothing),
`collage.ErrAssetFailed` (a mounted asset request completed with a 4xx or 5xx —
one sentinel for every such status), `collage.ErrMaxDepthExceeded`,
`collage.ErrRequiredSlotEmpty`, `collage.ErrNoRootFragment`, and
`collage.PanicError` through `errors.As`.

`ErrorEvent.Stage` names where in the pipeline the failure happened (`"route"`,
`"not_found"`, `"page_resolved"`, `"before_render"`, `"render"`,
`"after_render"`, `"cache_write"`, `"error_page"`, `"asset"`). It is
caller-defined rather than an enum. `"error_page"` is the one worth alerting on:
it means the page that reports failures failed, which nobody finds out about
otherwise, because the client still receives a plausible-looking error page.

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

## Contributing, not only observing

The hooks above let a plugin watch a request and rewrite what it produced. A plugin
can also contribute things the framework then serves.

### The two phases

`Init` runs at startup, after the application has registered its own pages. A
plugin that wants to read those pages, or add one, belongs there.

Some contributions have to happen earlier. A template function must exist before
templates are parsed — `html/template` resolves a name at execution time but can only
call one that was in the `FuncMap` at parse time — and parsing happens while the
application is built. So there is a second, earlier phase:

```go
type Configurer interface {
	Configure(ctx context.Context, host collage.ConfigHost) error
}
```

`Configure` is optional and discovered by type assertion, exactly as the hooks are.
It runs inside `New`, and it receives a narrower host: at that point the application
has registered nothing, so there are no pages to read.

**A plugin implementing `Configurer` must be supplied in `Config.Plugins`.**
`RegisterPlugin` is called after `New` has already parsed the templates, so it
refuses such a plugin by name (`ErrConfigurerRegisteredLate`) rather than skipping
its `Configure` silently — a plugin whose template function never arrived would
otherwise leave you wondering why no template can call it.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{minimizer.New()},
})
```

### What each phase offers

| | `ConfigHost` (Configure) | `Host` (Init) |
|---|---|---|
| `DevMode`, `Logger`, `Config` | yes | yes |
| `AddTemplateFunc` | yes | — |
| `WrapMount` | yes | — |
| `Pages`, `Page`, `InvalidateTags` | — | yes |
| `RegisterPage`, `RegisterDocument`, `Mount` | — | yes |
| `RegisterCommand` | — | yes |

`WrapMount` wraps the mounted filesystem rather than transforming a response,
because a mount serves through `http.ServeContent` and therefore supports `Range`.
Transforming bytes per request shifts every offset, and a range request then returns
the wrong slice of a file whose advertised length no longer matches. A minifier
returns an `fs.FS` whose files are already minified.

### Documents

`DocumentRenderedHook` is `AfterRenderHook`'s counterpart for the non-HTML routes:

```go
func (p *Plugin) OnDocumentRendered(ctx context.Context, ev *collage.DocumentRenderedEvent) error {
	if strings.HasSuffix(ev.ContentType, "/json") {
		ev.Body = compact(ev.Body)
	}
	return nil
}
```

Without it a plugin that post-processes output covers pages and silently skips every
sitemap, feed and JSON endpoint.

### Static builds

The render hooks fire during a static build too, and plugin `Init` runs before the
first page is rendered. `RenderPath` goes through startup, memoised, so a build
renders in the state a served render renders in.

That is not a nicety. Before it, a built site was not what the server served:
unminified where the server minified, unannotated where it annotated, and — because
a plugin reading its configuration in `Init` never got it — running on defaults in
one and configured in the other. One source, two sites, with nothing saying so.

A plugin that produces files, rather than only transforming them, should serve them
from a mount rather than a route. A build copies every mounted filesystem into its
output *after* every page has rendered, so a filesystem that records what the pages
asked for can hand the builder exactly the right set — and the built site needs
nothing running behind it. A routed document cannot be enumerated that way when its
path is dynamic, because a build has no way to guess what would be requested.

`PageResolved` deliberately does not fire. Its contract is "once per request,
immediately after the router resolves it", and a build is not a request; firing it
would make every plugin counting requests count renders nobody asked for.
`BeforeRender` and `AfterRender` fire as a pair, so a plugin that sets something up
in one and uses it in the other is not handed half of each.

### The render's own data

`AfterRenderEvent.Data` is the render's `SharedData` — whatever the page's fragments
exchanged while producing the HTML. It is how a plugin reaches what the page was
built *from* rather than what it was rendered *into*: a structured-data plugin wants
the article, not the markup it would otherwise parse back.

What is in it is entirely the application's convention; the framework puts nothing
there.

## Configuration

`Config.PluginConfig` is a `map[string]json.RawMessage`, keyed by plugin name:

```go
app, err := collage.New(&collage.Config{
	Plugins:      []collage.Plugin{minimizer.New()},
	PluginConfig: pluginConfig,
})
```

The framework reads no file and imposes no format. The application fills the map
however it likes — `collage.LoadPluginConfig(path)` is a convenience for a JSON
file, and nothing depends on it, so an application whose configuration lives in YAML
or the environment is not shut out.

A plugin reads its own section into its own typed struct:

```go
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	p.cfg = Config{HTML: true} // defaults
	return host.Config(&p.cfg) // overlaid by the application's section, if any
}
```

An absent section leaves the value alone, so defaults survive: "not configured" and
"configured to the zero value" are different statements, and only this can tell them
apart.

**A key matching no registered plugin is a startup error** (`ErrUnknownPluginConfig`).
Ignoring `"elagoht/minimzer"` would leave the plugin running on defaults and the
operator certain it was configured.

Plugin names should read like module paths — `elagoht/minimizer` — so the
configuration key and the plugin are the same identifier rather than two that have
to be kept in step.

## Third-party plugins need no mechanism

Go compiles plugins in. `plugin.Open` is Linux and macOS only, demands an identical
toolchain and identical dependency versions, and is not a basis for an ecosystem. A
third-party plugin is an ordinary Go module:

```go
import optimage "github.com/Elagoht/collage-opti-image"

app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{optimage.New()},
})
```

No plugin ships with the framework, and that is the point rather than an omission: a
plugin bundled here would be built against internals nobody else can reach, and the
first third-party author would discover the difference the hard way. Everything a
plugin needs is on this page.
