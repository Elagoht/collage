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
	Plugins: []collage.Plugin{minimizer.New(minimizer.Options{})},
})
```

Configuration, when a plugin takes any, comes from a JSON file keyed by plugin name
and handed to `Config.PluginConfig` — see `collage.LoadPluginConfig`, or `Env.PluginConfig`, which a scaffolded project's `main.go` already calls. A plugin with no entry runs on its defaults.

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
| `ConfigReader` (embedded) | What `collage.PluginConfig(host, defaults)` reads this plugin's section of `Config.PluginConfig` through — see [Configuration](#configuration) |
| `RegisterDocument(*Document) error` | Contribute a document |
| `Mount(prefix, fsys, opts...) error` | Serve a filesystem under a prefix |
| `Handle(prefix, http.Handler) error` | Serve a handler under a prefix ending in `/`, or at one exact path without it (`/metrics`), as `App.Handle` does — an event stream, a WebSocket — but always inside the middleware chain, as `App.Handle` does only with `WithMiddleware()` |
| `RenderFragment(r, FragmentRequest) (*FragmentRender, error)` | Render a fragment a page opened with `WithFragmentPath`, in parts — see below |
| `Use(middleware) error` | Wrap every request, where the plugin was registered: for one in `Config.Plugins`, outside the application's own middleware — but not one for a handler the application mounted with `App.Handle` without `WithMiddleware()`; see [http.md](http.md) |
| `URL(name, locale, params) (string, error)` | The path of a page or document, as `App.URL` builds it |
| `OriginFor(ctx, host) string` | The public origin of a host: an `OriginResolver`'s, else `Config.BaseURL` |
| `MultiOrigin() bool` | Whether an `OriginResolver` is registered, so origins vary by host |
| `Locales() (default, supported)` | The default locale and every supported one |
| `PageURLs(ctx, name) ([]PageURL, error)` | Every URL a page answers, in every locale, a pattern's `WithStaticParams` expanded — a sitemap's contents |
| `BuildID() string` | The build serving — `Config.Cache.Version`, or a fingerprint of the executable — for versioning what a browser keeps across deploys |
| `ServeStatus(w, r, status)` | Answer with a status and the site's own page for it: the not-found page for 404 and 410, the error page otherwise |

A plugin therefore has no way to reach the router, the cache, the render engine,
the template set, or any page it was not explicitly handed — it can add routes and
invalidate tags through these methods, but not reach the structures behind them. That is the structural
half of the framework's rule that plugins cannot mutate core state.

### Pushing fragments

`RenderFragment` is for a plugin that sends fragments over a connection it owns — an
event stream or a WebSocket — instead of waiting for the browser to ask for them. It
renders exactly what a request to the fragment's path renders, and returns the parts
instead of a response. One difference: values read through `collage.Cached` are
fresh. A request may be served a stale value while it is refreshed, but a pushed
fragment is not asked for again, so `RenderFragment` waits for the upstream, and
returns an error when it fails:

| Field | |
| --- | --- |
| `HTML` | The markup, with the reader's forgery token in any form it holds |
| `Head` | What the fragment hoisted into an area it placed no marker for, as `HoistItem`s with their area and key |
| `DependencyTags` | The tags the render depended on — match them against `CacheInvalidateEvent.Tags` to know what to push |
| `Shared` | The render is the same for every reader at this moment: the page is cached for everyone, or every handler in the subtree is declared `Static()` or `Shared()` and there is no slot resolver — and there is no form token |
| `ETag` | The ETag a request to the fragment's path would get for the same body, so a client can tell a pushed copy from one it already shows |
| `Cookie` | The forgery cookie the forms in `HTML` need, when the request carried none |

A `FragmentRequest` names the fragment either by `Page`, `Fragment`, `Locale` and
`Params`, or by `Path` — the URL a page linked with `{{fragmentURL}}`, query
included — which is resolved as a request to it would be. A client subscribing to
the elements it shows only knows their URLs, so `Path` is usually what a stream
has.

Only fragments the page opened are rendered: a stream reaches exactly what HTTP
reaches. A render that is not `Shared` may hold one reader's data, so it must be
rendered for each connection with that connection's request, never once for all.

### Streams and shutdown

A plugin's `Shutdown` runs after the server has stopped, and the server stops by
waiting for every open request to end — which an event stream or a WebSocket never
does by itself. A plugin serving one implements `StreamCloser`:

```go
func (p *Plugin) CloseStreams() { p.hub.close() }
```

`CloseStreams` runs when shutdown begins, before the server waits, and must end the
streams without waiting for them. A handler served through `Handle` can push its
write deadline forward with `http.NewResponseController(w).SetWriteDeadline`, and
take the connection over with `Hijack`, as it could on a bare `net/http` server.

A plugin that must know the server is about to stop taking traffic — readiness
turning false, new work refused — implements `DrainHook`:

```go
func (p *Plugin) OnDrain() { p.draining.Store(true) }
```

`OnDrain` is called once, when the drain starts: before `ServerConfig.DrainDelay`
is waited out and before `CloseStreams`. It must not block — the server goes on
serving while the drain lasts, and a slow hook delays every step after it. A
panic in it is contained and logged at Warn with the plugin's name. Like
`Shutdown`, it reaches every registered plugin, including one whose `Init` never
ran — a `Shutdown` before the application started — or failed and was rolled
back, so it must tolerate being called on a plugin that was never initialised.

A plugin that does work only while the application is serving — a scheduler, a
queue worker — implements `ServeHook`:

```go
func (p *Plugin) OnServe(ctx context.Context) { go p.run(ctx) }
```

`OnServe` is called once, in registration order, by `ListenAndServe`: after the
port is bound — when `collage: listening` is logged — and before it serves. It
is not called by `Start()` or `Handler()`, so not in a static build
(`collage build`), not in a plugin command, not by an application that runs its
own `http.Server` over `app.Handler()`, and not when `Shutdown` ran before
`ListenAndServe`. `collage dev` serves through `ListenAndServe`, so it is called
in development too, again on every restart.

`ctx` derives from `context.Background()` and is cancelled when the drain
starts, at the same moment `OnDrain` runs, whether the stop came from a signal
or from `App.Shutdown(ctx)`. It means "start no new work"; finishing the work
already running belongs in the plugin's `Shutdown`, which runs after the server
stops and is bounded by the shutdown ctx. The ctx may already be done when
`OnServe` is called — a drain that started a moment earlier — so check it
before starting work. `OnServe` must not block: start goroutines and return. A panic in it is contained and logged at Warn with the
plugin's name, and the server still serves.

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
plus its `Paths`, `Redirects`, and `DependencyTags` containers — so writing
through one of those cannot reach the framework's own page. But the fragment
pointers inside it (`LayoutChain`, `ContentFragment`, `NotFoundPage`,
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
`AfterRenderEvent.HTML`.

## The hooks

| Interface | Method | Fires | May change |
| --- | --- | --- | --- |
| `BeforeRenderHook` | `OnBeforeRender` | Immediately before a fresh render; **not** on a cache hit. **Pages only** — including an error page, and a page an action answers with | nothing |
| `BeforeActionHook` | `OnBeforeAction` | Before an action's handler: after the page's guards, the action's body limit and the forgery check. **Actions only**. `ev.Form()` parses the submission through the action's limit, once, for the handler too | `ev.Result`, which answers in the handler's place and stops dispatch |
| `RequestHook` | `OnRequest` | First, before collage's request span, middleware and routing — also for a request answered before routing (an encoded slash, a dirty path); returns the context to serve under and a function told the final status | the request's context |
| `AfterRenderHook` | `OnAfterRender` | After a successful render, with `ev.Fragments` (each fragment's time and failure), `ev.DependencyTags` and `ev.Values` (read with a key's `In`). **Pages only**, on the same terms | `ev.HTML`, `ev.Hoist(area, key, html)`; reports with `ev.Warn`, `ev.Error` |
| `DocumentRenderedHook` | `OnDocumentRendered` | After a document handler returns, before its body is cached or served. **Documents only** | `ev.Body` |
| `CacheInvalidateHook` | `OnCacheInvalidate` | After entries for some tags were invalidated — for a page or a document alike; `ev.Entries` names each invalidated entry's host and URL path | nothing |
| `ErrorHook` | `OnError` | On any failure while serving a request — a page, a document, or a mounted asset alike | nothing |
| `BuildFinishedHook` | `OnBuildFinished` | Once, when a static build has written every file | reports with `ev.Warn`, `ev.Error` |
| `StreamCloser` | `CloseStreams` | Once, when shutdown begins, before the server waits for open requests (see "Streams and shutdown") | nothing |
| `DrainHook` | `OnDrain` | Once, when the drain starts, before `DrainDelay` and `CloseStreams`; may reach a plugin whose `Init` never ran or failed (see "Streams and shutdown") | nothing |
| `ServeHook` | `OnServe` | Once, when `ListenAndServe` has bound its port, before it serves; never from `Start()`, `Handler()` or a build. Its ctx is cancelled when the drain starts | nothing |

Two consequences of where `OnAfterRender` sits are worth stating plainly:

- **A cached response does not run it again.** The hook's output is what got
  cached, so the post-processing is already baked into the stored bytes. A hook
  that needs to run per request — injecting a nonce, a per-visitor token — must
  not be combined with a cacheable strategy on that page.
- **A rendered error page runs it too**, and `OnBeforeRender` before it (since
  v0.7.0), so the 404 a server answers with is minified and annotated like the
  `404.html` an export writes. So does a page an action answers with — a form
  re-rendered with its validation errors is a page.

### Adding to the head after the render

A plugin that learns what a page needs only from its finished markup — the code
blocks it highlighted need a stylesheet — adds it with `ev.Hoist` in
`OnAfterRender`:

```go
if highlighted {
	ev.Hoist("head", "highlight:css", `<link rel="stylesheet" href="/_highlight/style.css">`)
}
```

It lands where the layout put `{{hoist "head"}}`, after what the render declared
there. A key the render already declared is left alone — the page is more specific
than a plugin afterwards — and so is a key hoisted twice. If an earlier plugin
replaced `ev.HTML`, the layout's place can no longer be found: `"head"` then lands
before `</head>`, and another area reports false.

### Which origin a host has: `OriginResolver`

A plugin that serves several sites from one process — a host per customer — says
which public origin absolute URLs for a host are built against by implementing
`OriginResolver`:

```go
func (p *Plugin) Origin(ctx context.Context, host string) (string, bool) {
	origin, ok := p.origins[host]
	return origin, ok
}
```

The host arrives lower-cased with its port stripped, and an IPv6 literal without
its brackets (`::1`). The first plugin in registration order that returns `ok`
wins. An origin must be a bare `scheme://host[:port]` with scheme `http` or
`https`, which `collage.ParseOrigin` checks and normalizes, and must name a host
(`https://:8080` does not, and `ParseOrigin` refuses it); an invalid one counts as not known, and dev mode logs it
once per plugin and origin. A host no plugin knows gets `Config.BaseURL`, and a
resolver that panics is logged and skipped. Application code reads the result with
`collage.BaseURL(rc)`, which is safe in a cached render because the host is in the
cache key. A site without a resolver sees no change.

A plugin that builds absolute URLs itself asks the `Host` it was given:

```go
origin := host.OriginFor(ctx, "acme.test") // a resolver's, else Config.BaseURL
varies := host.MultiOrigin()               // is a resolver registered?
```

`CacheInvalidateHook` events carry `ev.Entries`, each a `Host` and a `Path`, so a
purge can build every URL against the right origin.

### Rewriting each response: `PersonaliseHook`

`OnAfterRender` runs once, before the cache, so it cannot give each reader
something of their own. A plugin that must — a Content-Security-Policy nonce, a
per-visitor token — implements `PersonaliseHook`, called for every HTML response
on its way to one reader:

```go
func (p *Plugin) OnPersonalise(_ context.Context, ev *collage.PersonaliseEvent) error {
	if !bytes.Contains(ev.Body, []byte(p.marker)) {
		return nil
	}
	nonce, err := newNonce()
	if err != nil {
		return err
	}
	ev.Body = bytes.ReplaceAll(ev.Body, []byte(p.marker), []byte(nonce))
	ev.Header.Set("Content-Security-Policy", "script-src 'nonce-"+nonce+"'")
	ev.Personal = true
	return nil
}
```

The event carries:

- `ev.Request`: the reader's own request, not a shared render's stripped one.
- `ev.Header`: the response header; set here what must match the body.
- `ev.Body`: what will be written, the reader's forgery token already in it. A hook
  replaces it (assigns a new slice); it must never write into it: the slice may be
  the cache's, shared with concurrent readers.
- `ev.Personal`: set it when the body became particular to this reader.

It runs after the forgery token goes into the shared body, inside every
middleware, so before compression, and before the dev reload script. On a page it
runs before the dev overlay; on an error page, or a page an action answers with,
the overlay is already in the body.
Plugins run in registration order and each sees the previous body. It covers a
page (from the cache or fresh), a fragment path or fragment read, an action's HTML
answer and an error page, never a document.

`Personal` treats the response as a forgery token does: its ETag is recomputed
from the body sent, a page answers `private, no-store`, and neither a page from
the cache nor a fragment read is answered `304`, not even to `If-None-Match: *`. A fragment read answers `private, no-cache`
with the recomputed ETag and an action's HTML `private, no-store`, overriding even
a `Cache-Control` the handler set. A compressing middleware that keeps compressed
bodies per ETag, such as elagoht/compress, gains nothing from a personal
response: its ETag is new every time, so it is compressed for each reader. A body changed without `Personal`
still gets an ETag naming the bytes sent. A static build's header capture
(`collage.IsCapture`) runs the hook too, but sets its `Personal` aside: the
exported file carries no nonce, so it gets the `Cache-Control` the page's strategy
gives, not `private, no-store` (see [deployment](deployment.md)). A hook that returns an error or panics is
logged and the page, fragment or action answers 500; on an error page the built-in
page for that status is sent instead. A site without such a plugin sends the same
responses as before.

### Before collage starts on a request

`RequestHook.OnRequest` runs before anything else — before collage starts its
request span, before middleware, before routing — and returns the context the
request is served under, and a function called with the status when the response is
written — once, unless serving the request panics. It is what a tracing plugin needs: a trace carried in from the caller has
to be the parent of collage's own span, and middleware runs inside it.

It also sees the requests collage answers before routing: a path with an encoded
slash (`/a%2f..%2fb`), answered 404, and a dirty path (`/a/../b`, `//b`),
redirected to its clean spelling with 301, or 308 for a method other than GET and
HEAD. These are a scanner's probes more often than not, so a plugin counting them —
a ban, a log line — must see them. For such a request `RouteInfo` is the zero `Route` and
`r.URL` is the raw path as it came in; `Metrics.HTTPResponse` and collage's request
span are not called for it. The development reload stream is the one request no
hook sees.

`collage.RouteInfo(ctx)` reports what the request resolved to — a `Route` whose
`Kind` is `collage.RouteKindPage`, `RouteKindDocument`, `RouteKindAction`,
`RouteKindMount` or `RouteKindHandler` (`"page"`, `"document"`, `"action"`,
`"mount"`, `"handler"`; the set may grow) and whose
`Name` is the registered name or prefix — from that context, which
`Metrics.HTTPResponse` also receives: a metric or a span can be labelled with the
route rather than the raw path. It carries the pattern the route was registered
with — `/blog/{slug}`, without a locale prefix — and the locale, for pages,
documents and actions alike.

`collage.ClientIP(r)` is the client the request comes from, behind the proxies
the application lists in `Server.TrustedProxies` (see
[deployment.md](deployment.md)) — from `OnRequest` on, middleware included. A
plugin keying anything on the client, a rate limit or a ban, should use it rather
than `RemoteAddr`, which behind a proxy is the proxy for every request. It is the
zero `netip.Addr` when there is no address to give: in a shared page render,
whose request has no `RemoteAddr`, and behind a trusted proxy whose
`X-Forwarded-For` holds no usable client (`unknown`). Never count that one.

`collage.IsHTTPS(r)` is whether the request arrived over TLS: on its own
connection, or at a proxy that says so in `X-Forwarded-Proto`. With
`Server.TrustedProxies` listed, the header is believed only from one of them, so a
client cannot claim a TLS connection it does not have; with none listed it is
believed from anyone, which is what an application behind a proxy it has not
described needs. It is the decision the forgery guard makes for its own cookie's
`Secure` flag. A plugin setting `Secure` on a cookie, or sending
`Strict-Transport-Security`, uses it rather than reading the header itself.

### Checking the output: findings

A plugin that checks what a page renders — a heading level skipped, an image
without `alt`, a form field without a label — reports what it finds rather than
failing the render:

```go
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if !bytes.Contains(ev.HTML, []byte("<h1")) {
		ev.Error("one-h1", "the page has no <h1>")
	}
	return nil
}
```

A finding (`collage.Finding`) has a level, the rule that found it, a message, the
plugin — filled in by the framework — and the page's path. Where it goes depends
on where the page was rendered:

- **In development** it is shown over the page, in the panel a failed fragment
  uses, and the page is served as it is — a page an action answers with and an
  error page included.
- **In a static build** it is listed in the report under the page it is about. An
  error-level finding fails the build with `collage.ErrBuildFindings`; the pages are
  written either way.
- **In production** nothing is done with it. A check re-run on every render would
  spend a server's time on what the build already knew; a checking plugin should
  turn itself off there — `ev.Static` says a render is a static build's, and
  `host.DevMode()` says the server is a development one.

What no single render can tell — two pages with one title, a link to a page the
build did not write — is checked in `OnBuildFinished`, which runs once every file
is written. `ev.Files` lists each file with its kind (`collage.BuiltPage`,
`collage.BuiltDocument`, `collage.BuiltAsset` — `"page"`, `"document"`, `"asset"`),
the URL path it answers and its place on disk; `ev.Warn(path, rule, message)` and
`ev.Error` report against a page.

### Documents dispatch three hooks, not five

A document (`collage.NewDocument`) dispatches `OnDocumentRendered`,
`OnCacheInvalidate`, and `OnError`. It does **not** dispatch `OnBeforeRender` or
`OnAfterRender` — those are about a page render, and a document handler is not
one: `AfterRenderEvent.HTML` would be a lie for a zip file or a JPEG, and there is
no `*Page` for `BeforeRenderEvent.Page` to carry. A plugin that post-processes output implements `OnDocumentRendered` for
documents (see [Documents](#documents) below); one that stamps every page from
`OnAfterRender` alone stamps nothing on a sitemap. `ErrorEvent.Page` is `nil` for
a document failure — the event's `Path` already identifies the route, and the
framework's own log line names the document. See `docs/documents.md` for the full
reasoning; this table and that page are kept in agreement on it.

A mounted asset request dispatches only `OnError`, and only for a 4xx or 5xx
response, or for a panic recovered while serving it: an asset is neither a page
nor a document, so none of the cache or render hooks apply to it either.

An `ErrorHook` can classify what it receives with `errors.Is`:
`collage.ErrNoRoute` (no route matched — a link problem), `collage.ErrNotFound`
(a required fragment's content does not exist — a content problem),
`collage.ErrEmptyErrorPage` (a registered error page rendered nothing),
`collage.ErrAssetFailed` (a mounted asset request completed with a 4xx or 5xx —
one sentinel for every such status), `collage.ErrMaxDepthExceeded`,
`collage.ErrRequiredSlotEmpty`, and
`collage.PanicError` through `errors.As`.

`ErrorEvent.Stage` names where in the pipeline the failure happened (`"route"`,
`"not_found"`, `"guard"` (a guard that failed or answered with a decision it
cannot write), `"before_action"`, `"before_render"`, `"render"`,
`"after_render"`, `"cache_write"`, `"error_page"`, `"panic"`, `"asset"`,
`"plugin"`, and `"handler"` for a handler mounted with `app.Handle` answering
5xx). Each has a constant — `collage.StageRoute`, `collage.StageNotFound`,
`collage.StageGuard`, … `collage.StageHandler` — that an error hook compares
against instead of the literal. The set may grow, so a hook names the stages it
handles rather than assuming it has seen them all. `"error_page"` is the one worth alerting on:
it means the page that reports failures failed, which nobody finds out about
otherwise, because the client still receives a plausible-looking error page.

A panic recovered while serving a request — stage `"panic"` — matches
`errors.Is(ev.Err, collage.ErrPanic)` and also holds a `*collage.PanicError`:
`errors.As` reaches the panic value and the stack it was raised on, and
`errors.Is` reaches the value itself when it panicked with an error. The stack is
not in the error's message; the framework's log line carries it as a separate
`stack` attribute.

`ErrorEvent.Status` is the HTTP status the failure is answered with: 404 for a
missing asset, 500 for a failed render or a panic. It is 0 when no response is
written for the failure, as for a cache write that failed after the page was
served. An `"error_page"` event always says 500, even when the failure it was
answering was a 404: the event reports the error page breaking, which is a
server error, while the reader still receives the original status.

`ErrorEvent.Request` is the reader's request, for reading only — its URL, its
headers, its method. Its body may already have been read. It is nil when the
failure did not come from a request. It is the live request, not a copy: read what
you need during `OnError`, and do not keep it or hand it to another goroutine.

A page's failed cache write (`"cache_write"`) happens inside the render shared by
every reader waiting on the same cache key, so its `Request` is that shared
render's stripped request, not a particular reader's: no cookies, only the headers
middleware declared with `collage.Vary`, no `RemoteAddr`. A document's cache write
carries the request of the reader whose miss started the shared render. A
`collage.SharedCache` store failing a write sends no event: the store's own
failures are logged, rate-limited, instead (see
[A cache a fleet shares](caching.md#a-cache-a-fleet-shares)).

## Dispatch and error semantics

Hooks are dispatched in **registration order**. Every hook call is panic-guarded:
a panicking hook fails like a returning-an-error hook rather than taking the
process down.

- `OnBeforeRender`, `OnAfterRender`, `OnDocumentRendered` — the
  first error stops dispatch and fails the request with a 500.
- `OnBeforeAction` — the first error stops dispatch and fails the request: `413`
  when it wraps `*http.MaxBytesError`, as a body past the action's limit read
  through `ev.Form()` does, and `500` otherwise. The first plugin to set
  `ev.Result` is the last one asked, and the handler never runs.
- `OnCacheInvalidate` — dispatched from `InvalidateTags`, not from a request; an
  error is joined into what `InvalidateTags` returns.
- `OnError` — an error is logged and swallowed, and dispatch continues to the
  remaining plugins. An error handler that itself errors must not recurse into
  another round of error handling.

For `OnAfterRender` and `OnDocumentRendered`, later plugins see
what earlier ones changed.

## Lifecycle

1. `RegisterPlugin` — before the application starts. Afterwards it returns
   `collage.ErrAppStarted`, since `Init` has already run and already seen the
   registered pages. A nil plugin, an empty name, and a duplicate name are
   refused with an error, and a duplicate name with
   `collage.ErrDuplicatePlugin`.
2. `Init` — once, in registration order, when the application starts: what
   `Start()`, `Handler()`, `ListenAndServe()`, `RenderPath` and
   `RenderDocumentPath` — and so a static build — all do first, memoised. An `Init` that fails aborts
   startup and rolls back: every already-initialised plugin gets `Shutdown`, in
   reverse order, and the failing plugin does not (it never finished
   initialising).
3. `OnServe` — once, for a `ServeHook`, when `ListenAndServe` has bound its
   port and before it serves. Only `ListenAndServe` calls it; its ctx is
   cancelled when the drain starts.
4. `OnDrain` — once, when a shutdown starts, for a `DrainHook`: before
   `DrainDelay`, before `CloseStreams` and before the server stops. A `Shutdown`
   before the application started calls it too, without waiting, on plugins whose
   `Init` never ran.
5. `Shutdown` — in reverse registration order, *after* the HTTP server has
   stopped, so a plugin is never torn out from under an in-flight request. Unlike
   `Init`, it never stops early: every plugin is given its chance and the failures
   are joined. It runs once per plugin, and only for a plugin whose `Init`
   succeeded: a start that fails *after* every `Init` succeeded — a mount that
   swallows a page, an error page never registered — shuts the plugins down
   there and then, and a later `app.Shutdown` does not shut them down again, nor
   a plugin whose `Init` never ran or failed.

`RegisterCommand` is the one registration call deliberately exempt from
`ErrAppStarted`: plugins register commands from inside `Init`, which by definition
runs as part of starting.

## Commands

A registered command reaches `app.Commands()`, and your own `main` dispatches it
by name with `collage.DispatchCommands` — run as `go run . <command>`, because the
`collage` binary never loads your plugins, and so never sees their commands. A
scaffolded `main.go` already does this. A command with an empty name, or a name
another command already holds, is refused at `RegisterCommand`
(`collage.ErrDuplicateCommand` for a repeated name). Name it something
other than the binary's built-ins (`new`, `dev`, `build`, `export`, `serve`,
`version`, `help`) all the same: `go run . build` and `collage build` doing
different things is a trap for whoever types the wrong one. See
[the CLI](cli.md#plugin-commands).

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
	Plugins: []collage.Plugin{minimizer.New(minimizer.Options{})},
})
```

### What each phase offers

| | `ConfigHost` (Configure) | `Host` (Init) |
|---|---|---|
| `DevMode`, `Logger`, `collage.PluginConfig(host, …)` | yes | yes |
| `AddTemplateFunc`, `AddRenderFunc` | yes | — |
| `WrapMount` | yes | — |
| `Pages`, `Page`, `InvalidateTags` | — | yes |
| `URL`, `Locales`, `PageURLs`, `OriginFor`, `MultiOrigin` | — | yes |
| `RegisterDocument`, `Mount`, `Handle`, `Use` | — | yes |
| `RenderFragment` | — | yes |
| `RegisterCommand` | — | yes |

`AddTemplateFunc`'s function is one value for the life of the application.
`AddRenderFunc` takes a factory instead, called for each render with its
`*RenderContext`, so the function it returns can read what that render holds — a
nonce a `BeforeRender` hook set, the render's locale:

```go
var nonceKey = collage.NewKey[string]("csp:nonce")

host.AddRenderFunc("nonce", func(rc *collage.RenderContext) any {
	nonce, _ := nonceKey.Get(rc)
	return func() string { return nonce }
})
```

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

The render hooks fire during a static build too — `OnBeforeRender` and
`OnAfterRender` around every page, `OnDocumentRendered` on every document — and
plugin `Init` runs before the first page is rendered. `RenderPath` goes through startup, memoised, so a build
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

`BeforeRender` and `AfterRender` fire as a pair, so a plugin that sets something up
in one and uses it in the other is not handed half of each.

The header capture that follows the writes is different: when a
`BuildFinishedHook` plugin is registered to read what it finds, it asks the
application's handler for every written file's path, twice, so middleware and the
request hooks do see those requests. A plugin that
counts or limits traffic skips them with `collage.IsCapture(r.Context())`. A plugin
whose redirects a static host should carry implements `collage.RedirectSource`;
its rules reach `BuildFinishedHook` in `ev.Redirects`. See
[deployment](deployment.md#static-hosts).

### The render's own data

`AfterRenderEvent.Values` are the values the page's fragments exchanged through
keys while producing the HTML. It is how a plugin reaches what the page was built
*from* rather than what it was rendered *into*: a structured-data plugin wants the
article, not the markup it would otherwise parse back.

A hook has no `RenderContext`, so it reads them with the key's `In`:

```go
var ArticleKey = collage.NewKey[Article]("jsonld:article")

func (p *Plugin) OnAfterRender(ctx context.Context, ev *collage.AfterRenderEvent) error {
	article, ok := ArticleKey.In(ev.Values)
	if !ok {
		return nil // this page stored no article
	}
	ev.Hoist("head", "jsonld", p.script(article))
	return nil
}
```

The values are opaque: a plugin reads only what it holds a key for, and there is no
listing them. A plugin that wants the application to hand it something exports the
key, and the application stores under it; a plugin that carries state from its own
render functions to its hook keeps the key unexported. The framework puts nothing
there.

Begin a key's name with the plugin's own name — `"jsonld:article"`,
`"validate:errors"`. A key is only its name and its type, so two plugins that each
declare `NewKey[string]("errors")` hold one value between them, and whichever sets
it last wins. The prefix keeps a plugin's keys its own.

## Configuration

`Config.PluginConfig` is a `map[string]json.RawMessage`, keyed by plugin name:

```go
app, err := collage.New(&collage.Config{
	Plugins:      []collage.Plugin{minimizer.New(minimizer.Options{})},
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
	cfg, err := collage.PluginConfig(host, Config{HTML: true}) // defaults, overlaid by the application's section
	p.cfg = cfg
	return err
}
```

`PluginConfig` takes `Host` or `ConfigHost` alike, and the type it returns is the
type of the defaults. An absent, empty or `null` section returns the defaults
unchanged, and a section is decoded over a copy of them, so what it leaves out keeps
its default: "not configured" and "configured to the zero value" are different
statements, and only this can tell them apart. A malformed section is an error naming
the plugin and, when one member's value is what fails, its key —
`collage: you/greeting: "limit": json: cannot unmarshal string …` — returned with
the defaults. A section that is not JSON at all leaves them untouched. A value of
the wrong type (`"limit": "three"`) fails while the section is being decoded,
into a copy, so the defaults come back as they were.

A list in the section replaces the default list whole: `"fonts": [{"family":
"B", "file": "b.ttf"}]` over a Go default of one bold font gives one font, B,
with the weight it was given in JSON (none), not the Go font's. A map in the
section adds its entries to a copy of the default map. Neither writes into the
caller's slices or maps. Below the top level, a section's object is decoded over
the default's, so a nested map is still filled in place: write the defaults as
a literal in the call rather than sharing one value between calls.

**A key the plugin's options do not have is an error too**, at whatever depth,
and so is one named after a field tagged `json:"-"`: `collage: elagoht/robots:
unknown key "dissallow" in plugins-config.json; remove it, or rename it to a key
the plugin documents`. It stops the application at `Configure` or `Init`, where a
misspelt or renamed key used to be ignored and the option left at its default.
The error names the key, not its path, so a nested one is found by its name.
Keys match fields case-insensitively, as `encoding/json` matches them:
`"Disallow"` is `disallow`, not an unknown key. Decode the full `Options` type in
one `PluginConfig` call — the check is made against the type it decodes into, and
a type with its own `UnmarshalJSON` checks its own keys, or none.

**A duration in options is a `collage.Duration`**, not a `time.Duration`:

```go
type Options struct {
	BanTime collage.Duration `json:"banTime"` // "1h", "90s", "1h30m"
}

ban := time.Duration(p.opts.BanTime)
```

It reads a string `time.ParseDuration` understands and writes one back. A bare
number is refused with an error naming it — `collage: elagoht/fail2ban:
"banTime": write a duration as a string such as "10s", not 3600`, where
`time.Duration` would have read 3600 nanoseconds, 3.6µs, without a word — and so
is a negative duration. `null` leaves a `Duration` field at its default; a
`*collage.Duration` field set to `null` becomes nil, as `encoding/json` makes any
pointer.

**A key matching no registered plugin is a startup error** (`ErrUnknownPluginConfig`).
Ignoring `"elagoht/minimzer"` would leave the plugin running on defaults and the
operator certain it was configured.

Plugin names should read like module paths — `elagoht/minimizer` — so the
configuration key and the plugin are the same identifier rather than two that have
to be kept in step.

## Editor support: `collage.json`

A plugin can describe itself to editors in a `collage.json` at its module's root.
An editor extension finds it through `go list -m -json all` — the file ships in the
module like any other — and offers what it lists: the template functions with their
signatures and documentation, the attributes, the snippets, and the schema of the
plugin's section of `plugins-config.json`.

```json
{
  "$schema": "https://raw.githubusercontent.com/Elagoht/collage-snippets-highlighter/main/schemas/collage-plugin-manifest.schema.json",
  "name": "you/greeting",
  "description": "Greets the reader by name.",
  "templateFunctions": [
    {
      "name": "greet",
      "signature": "greet name",
      "insert": "greet ${1:.Name}",
      "doc": "\"Hello, name\" in the render's locale."
    }
  ],
  "attributes": [],
  "snippets": {
    "greeting": { "language": "html", "prefix": "cgreet", "body": ["<p>{{greet .Name}}</p>"], "description": "A greeting" }
  },
  "config": {
    "type": "object",
    "properties": { "formal": { "type": "boolean", "description": "Use the formal greeting." } }
  }
}
```

`name` is the plugin's `Name()`, the key its configuration is found under. `insert`
and snippet bodies use VS Code's snippet syntax. Every field but `name` is optional.

The editor learns which of those functions the application actually has, and every
page, fragment, slot and mounted file by name, from `collage inspect` (see the CLI
guide) — which is `go run . collage-inspect`, answered by `DispatchCommands` with
the application's description as JSON.

## Testing a plugin

A plugin's tests need an application to run it in, and nothing about that
application in particular. `collagetest.NewSite` builds one — a real collage
application, not a stand-in for the `Host` — with the plugin in it, starts it so
`Configure` and `Init` run at once (their errors fail the test), and shuts it down
when the test ends:

```go
import (
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collagetest"
)

func TestRobots(t *testing.T) {
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(robots.New(robots.Options{})),
		collagetest.WithPluginConfig("elagoht/robots", `{"disallow":["/admin"]}`),
	)

	res := site.Get("/robots.txt").WantStatus(http.StatusOK)
	if !strings.Contains(res.Body, "Disallow: /admin") {
		t.Errorf("robots.txt = %q", res.Body)
	}
	if site.Logs.Has(slog.LevelError) {
		t.Errorf("the plugin logged an error:\n%s", site.Logs)
	}
}
```

A `Site` is the started `App`, a `collagetest.Client` on its handler — so `Get`,
`Submit`, `Follow` and every other `Client` method work on it — and `Logs`, every
record the application wrote at debug level and above. `Logs.Has(level, parts...)`
reports whether a record at that level or above contains every part, and it is
safe to call while the application serves requests from other goroutines.

With no options, the site is as plain as an application gets: a layout with
`{{hoist "head"}}` and `{{slot "content"}}`, a `home` page at `/`, no cache, no
development mode, no `BaseURL` and only the default locale. A test adds what its
plugin needs:

| Option | Effect |
| --- | --- |
| `WithPlugins(plugins...)` | Adds plugins, in order |
| `WithPluginConfig(name, json)` | Sets one plugin's section of the plugin configuration, as `plugins-config.json` would. Invalid JSON fails the test |
| `WithTemplates(files)` | Adds or replaces templates by name under the root: `"layout.html"`, `"home.html"`, or one a registered page renders |
| `WithRegister(items...)` | Registers pages, documents and actions after the home page |
| `WithoutHome()` | Leaves the home page out, so `/` is the test's |
| `WithConfig(func(*collage.Config))` | Edits the configuration before the application is built: locales, a cache, `BaseURL`, development mode, anything |
| `WithSetup(func(*collage.App) error)` | Registers on the application itself before it starts — `Handle`, `Mount`, `Use`, `RegisterNotFoundPage`, `RegisterErrorPage`. Runs after the default registrations; several run in order; an error fails the site |

`collagetest.TrySite` is `NewSite` for a test that expects building or starting
to fail — a plugin refusing a bad configuration in `Init` — and returns the error
instead of failing the test.

A test that shuts the application down itself — to check what a plugin's
`Shutdown` returns — calls `site.Shutdown(ctx)`, not `site.App.Shutdown(ctx)`.
`App.Shutdown` runs once and returns its first error on every later call, so the
cleanup would report the error the test expected; `site.Shutdown` closes the site
and the cleanup does nothing more.

## Third-party plugins need no mechanism

Go compiles plugins in. `plugin.Open` is Linux and macOS only, demands an identical
toolchain and identical dependency versions, and is not a basis for an ecosystem. A
third-party plugin is an ordinary Go module:

```go
import optimage "github.com/Elagoht/collage-opti-image"

app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{optimage.New(optimage.Options{})},
})
```

No plugin ships with the framework, and that is the point rather than an omission: a
plugin bundled here would be built against internals nobody else can reach, and the
first third-party author would discover the difference the hard way. Everything a
plugin needs is on this page.
