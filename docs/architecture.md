# Architecture

collage renders HTML on the server by composing *fragments* — a template plus an
optional data handler — into *pages*, and caching the result by dependency tag.
It also serves two things that are not composed HTML: *documents*, which are
routed, cacheable, non-HTML responses whose handler returns bytes, and *assets*,
which are mounted `fs.FS` file systems served with file semantics. This document
describes how the pieces fit together, why the load-bearing design decisions were
made, and where the implementation deliberately departs from the original
specification.

## Package layout

```
pkg/collage        the only package you import: builders, config, and type aliases
cmd/collage        the CLI binary
internal/types     the domain types: Fragment, Page, Document, Redirect, RenderContext
internal/template  html/template loading, the function map, per-render funcs
internal/render    the fragment tree walk, the failure policy, timeouts, panics
internal/cache     the Cache interface, the cache key, the in-memory cache
internal/dependency  tag -> cache key index
internal/router    radix routing per locale, redirects, locale resolution
internal/asset     mounted fs.FS assets: ServeContent, per-mount Cache-Control, ETags
internal/httpx     the HTTP request lifecycle
internal/plugin    the Plugin contract, the Host, hook dispatch
internal/observability  the Metrics and Tracer interfaces
internal/build     the static site builder
internal/core      the App: owns one of everything above, plus the server lifecycle
internal/cli       the CLI's commands and project scaffold
```

The dependency arrow points one way: `pkg/collage` depends on `internal/*`, and no
`internal` package ever imports `pkg/collage`. `pkg/collage` is almost entirely
type aliases, so a `*collage.Page` and an `*internal/types.Page` are literally the
same type — there is no conversion layer, no wrapper, and no `interface{}`
boundary between the public API and the engine.

The one place a struct is mirrored rather than aliased is `Config`:
`pkg/collage.Config` owns the defaults and the validation, then converts field by
field into `internal/core.Config`. That conversion is written out by hand
(`toCoreConfig`) precisely so that adding a field to one and forgetting the other
is visible in a diff.

## The three kinds of route

A request reaches exactly one of three things, and they are deliberately
different mechanisms rather than one generalised one.

| | `Page` | `Document` | Asset mount |
| --- | --- | --- | --- |
| Produces | Composed HTML | Whatever its handler returns | A file from an `fs.FS` |
| Content type | Always `text/html; charset=utf-8` | Static, declared, required | From the extension, else sniffed |
| Templates | Fragments and slots | None | None |
| Page cache | Yes | Yes — same key, same ETag, same tags | Never |
| `Range` requests | No | No | Yes, via `http.ServeContent` |
| Plugin hooks | All six | `OnCacheWrite`, `OnCacheInvalidate`, `OnError` | `OnError` only |
| A failure renders | The page's error page, in HTML | `text/plain` | `text/plain` |

A generated payload (sitemap, feed, JWKS) is small, computed from application
state and wants tag invalidation; a served file (audio, video, a zip, a
stylesheet) sits on disk or in an embed, is potentially large, and wants `Range`
and `Last-Modified`. Forcing the second through the first breaks in three
specific ways: a `[]byte`-returning API cannot do `Range` without hand-rolling
RFC 9110 range parsing; the page cache is bounded by entry count, so one 50 MB
zip would evict thousands of pages; and the framework's content-hash ETag is not
viable over 500 MB per request.

Documents register into the *same* radix tree as pages, so a collision between
`/sitemap.xml` and `/{slug}` is a startup error. Mounts claim a URL prefix
instead, checked before routing — which is safe only because a startup check
refuses a mount prefix that would shadow URL space the router already claims. It
asks the router for that, rather than enumerating the registries itself: page
paths, document paths and redirect sources alike, each in the locale-prefixed
spelling a visitor actually types.

See [documents.md](documents.md) and [assets.md](assets.md).

## The request lifecycle

For one GET on a page, in order — a mount claims the request before step 1 if its
prefix matches, and a document runs the same steps minus 4, 5 and 6:

1. **Route.** The router resolves the locale (path prefix, then `Accept-Language`,
   then cookie, then the default) and matches the remaining path in that locale's
   radix tree. A redirect match wins over a page or document match at the same
   path.
2. **`OnPageResolved`.** Plugins observe the resolved page.
3. **Cache lookup.** Only for a `GET`/`HEAD` on a page whose strategy is
   cacheable. On a hit, an `If-None-Match` that matches the stored ETag answers
   `304`; otherwise the stored bytes are served. Nothing below runs.
4. **`OnBeforeRender`.** Plugins observe a render that is actually about to
   happen — this is what distinguishes the hook from `OnPageResolved`.
5. **Render.** The engine walks the page's fragment tree depth-first, running each
   fragment's data handler under its timeout, executing its template, and
   expanding `{{slot "name"}}` into the rendered children.
6. **`OnAfterRender`.** Plugins may replace the HTML. The replacement is what is
   served *and* what is cached.
7. **Cache write.** Skipped for a degraded render, a `HEAD`, or when a
   `OnCacheWrite` hook sets `Skip`. The key's dependency tags are recorded with
   the tracker.
8. **Respond.** `Content-Type`, `ETag`, `Cache-Control`, and — on a publicly
   cacheable response — `Vary`.

A failure anywhere in 1–6 goes to the error path: log, dispatch `OnError`, then
render the page's own `NotFoundPage`/`ErrorPage`, falling back to the site-wide
one and then to the framework's built-in page. An error response is always
`no-store` and is never cached. A failure in step 7 is the exception — it is
logged and reported to plugins, but the page has already rendered, so it is
served uncached rather than turned into a 500.

## Rendering is sequential, on purpose

`internal/render` walks the fragment tree strictly depth-first, one fragment at a
time, on the request goroutine. Fragments are not rendered in parallel.

The reason is determinism: the same page rendered twice from the same inputs must
produce byte-identical output, because that output is cached, ETagged, and
conditionally re-served. Concurrent fragment rendering buys latency only when
several fragments each block on I/O, and pays for it with output that can differ
between renders, error ordering that depends on scheduling, and a fragment tree
whose shared `RenderContext.SharedData` would need locking.

`render.Execute`, which wraps every data handler and every template execution,
runs `fn` on the calling goroutine for a related reason. Running it on a spawned
goroutine would let `Execute` return the instant a deadline passed — but the
spawned goroutine could not be killed, so a handler that blocks forever would leak
one goroutine per request, permanently. The cost of the in-line choice is that a
fragment's `Timeout` bounds the *context* the handler is given, not the handler
itself: a handler that never consults `ctx.Done()` can run past its deadline.
Handlers are expected to honour their context.

## Failure is a policy, not an accident

Each fragment carries its own policy, applied in `renderFragment`:

| Fragment | On failure |
| --- | --- |
| `Required()` | The error propagates; the whole page render fails |
| optional, with `WithFallback(f)` | `f` renders in its place; if `f` also fails, the fragment emits nothing and the page still succeeds |
| optional, no fallback | The fragment emits nothing and the page still succeeds |

A required fragment's failure is marked internally so that an optional ancestor
with a fallback cannot quietly absorb it — otherwise "required" would only hold
until the error reached the first ancestor willing to swallow it.

Either way the failure is recorded in the render's metadata, which is what makes
`Result.Degraded()` true — and a degraded render is served but never cached, so
one request's transient failure is not pinned in front of every later request.

A panic in a data handler or a template function is recovered by `render.Execute`
and converted into an ordinary error carrying the recovered value and the stack,
so the fragment fails instead of the process.

## No silent failures

Everything that can be checked at startup is checked at startup, and
`RegisterPage` is where most of it happens:

- a fragment naming a template the engine did not load is rejected by name, so a
  typo in a template path is a startup error rather than a first-request 500;
- a page that does not validate — no content fragment, a path not starting with
  `/`, `Incremental` with no TTL, a fragment cycle, an unfilled required slot — is
  rejected by name;
- a page that references a `NotFoundPage` or `ErrorPage` which was never
  registered in its own right fails the *startup*, because an unregistered error
  page never has its content bound into its layout and would render empty at the
  one moment it was needed;
- registration closes once the handler is built: `RegisterPage` and friends return
  `ErrAppStarted` afterwards, since plugin `Init` has by then already seen the
  pages.

When the handler build fails, `App.Handler()` returns a handler that answers every
request with `503` and logs the reason once; `App.ListenAndServe()` returns the
error instead, so a program that starts a server never loses it.

## Why plugins get a `Host`, not the `App`

The specification says a plugin "cannot mutate core state directly". `Plugin.Init`
therefore receives a `collage.Host` — `DevMode`, `Pages`, `Page`,
`InvalidateTags`, `Logger`, `RegisterCommand` — and not the `*App`. A plugin has
no way to reach the router, the cache, the render engine, the template set, or any
page it was not explicitly handed.

That is enforced by *what is passed*, not by the parameter's type. A method set
travels with a value through an interface, so handing `Init` the `*App` under a
`Host` parameter would leave `Shutdown`, `ListenAndServe`, `Handler`, and
`RenderPath` one type assertion away — an assertion that needs no name for the
concrete type and no import of `pkg/collage`, so narrowing what the public package
exports would not have closed it either. `Init` receives a narrow forwarding value
that has Host's six methods and nothing else, so there is nothing for the
assertion to find.

**`Host` limits reachability, not mutability, and the framework does not pretend
otherwise.** `*collage.Page` is a plain struct of exported fields. `Host.Pages`
and `Host.Page` return a defensive copy (the struct, plus its `Paths`,
`Redirects`, `SEO`, and `DependencyTags` containers), but the per-request event
types deliberately carry the framework's *live* `*Page` — copying a page and its
fragment tree on every render would defeat a cache-first framework's hot path. A
plugin holding an event's `*Page` can write straight through it, and doing so
mutates the same page every other request sees — concurrently with every other
request reading it. That is a data race, not merely something the framework leaves
undefined: `go test -race` will report it, and without the detector it silently
corrupts whatever container was written to. The framework does not defend against
it, so do not do it.

This is deliberate rather than an oversight: in-process Go plugins are trusted
code, not a sandbox boundary. The goal is to make accidental mutation hard and
deliberate mutation obvious, not to make mutation impossible — which Go's type
system cannot give us without copying everything. Where mutation *is* intended it
is explicit: `AfterRenderEvent.HTML`, and `CacheWriteEvent`'s `Skip`, `TTL`, and
`Tags`.

## Deviations from the original specification

Each of these is a deliberate departure, with the reason it was made.

- **`interface{}` at internal boundaries replaced by concrete types, and
  `Plugin.Init(ctx, app interface{})` by a narrow `Host` interface.** An untyped
  parameter is not a capability boundary and gives the compiler nothing to check;
  `Host` states exactly what a plugin may reach.
- **`internal/http` renamed `internal/httpx`.** A package named `http` shadows
  `net/http` at every call site that imports both.
- **`TaggedCache` added as an optional extension interface.** `Cache.Set` carries
  no tags, so a cache that *can* index tags at write time needs a way to say so
  without forcing every implementation to.
- **`MatchResult.RedirectStatus` added.** The router knew which status a matched
  redirect wanted; without carrying it, the handler could not write it.
- **`Result.Degraded()` added.** "A fragment failed but the page still rendered"
  has to be answerable, because that is exactly the render that must not be
  cached.
- **`render.Execute` runs in-line, not in a goroutine.** A spawned goroutine
  cannot be killed on timeout, so every blocked handler would leak one
  permanently.
- **`types.ErrNotFound` added.** Without a sentinel meaning "this content does not
  exist", every missing record is a 500 and a page-specific 404 page is
  unreachable.
- **`LocaleConfig` booleans inverted to `Disable*` form.** A bool documented
  "default true" can never be turned off: its zero value is indistinguishable from
  "caller left it unset", so defaulting would flip it back on every time.
- **`Vary` on public responses, and the query string in the cache key.** A locale
  negotiated from a header or a cookie is not in the URL, so a shared cache would
  hand one visitor's language to the next; and a data handler may render from
  `r.URL.Query()`, so two queries against one path are two representations.
- **`Document` and asset mounts added; the specification described only HTML
  pages.** The spec had one response shape — composed HTML with a constant
  content type — so `robots.txt`, `sitemap.xml`, a feed, a JWKS document and any
  static file were not merely unimplemented but unexpressible. `Document` adds a
  routed, cacheable non-HTML response that reuses every piece of a page's
  machinery except rendering; `App.Mount` adds an `fs.FS` served with
  `http.ServeContent`. They are two mechanisms rather than one because a
  generated payload and a served file want opposite things — see "The three kinds
  of route".
- **Registration binds into a per-page copy of the layout.** `Bind` appends to a
  slot's fill, and a shared layout is one object — binding three pages into it
  would render all three pages' content on every one of them. Copying the slot
  table per page is what makes a layout shareable at all.

## Further reading

- [Fragments and pages](fragments.md)
- [Documents](documents.md)
- [Assets](assets.md)
- [Caching and invalidation](caching.md)
- [Plugins](plugins.md)
- [Routing, locales, redirects](routing.md)
- [The CLI and static builds](cli.md)
