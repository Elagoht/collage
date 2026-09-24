# Documents

A **document** is a routed, cacheable response that is not HTML: a sitemap, an
RSS feed, a `robots.txt`, a `/.well-known/jwks.json`, a JSON endpoint.

It shares almost everything with a page — the same router, the same cache key,
the same content-hash ETag, the same conditional requests, the same render
strategies, the same dependency tags and the same `InvalidateTags` — and shares
none of the rendering. A document has no template, no layout, no fragments and no
slots. Its handler returns bytes.

```go
sitemap := collage.NewDocument("sitemap", "application/xml").
	WithPath("en", "/sitemap.xml").
	WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
		body, err := buildSitemap(store.List())
		return body, []string{"blog:posts"}, err
	}).
	Incremental(time.Hour).
	WithDependency("blog:posts").
	Build()

if err := app.RegisterDocument(sitemap); err != nil {
	log.Fatal(err)
}
```

## Why not a page

Two reasons, and the second is the one that bites.

A page's content type is a constant. Every page response is `text/html;
charset=utf-8`, including its error pages, because that is what a page *is*.

And `html/template` — which is what a page renders through — applies HTML
escaping rules. Those rules are wrong for XML and wrong for JSON: it escapes on
HTML's terms, in HTML's contexts, and a sitemap or a feed generated through it is
silently malformed at exactly the characters that most need escaping. Not an
error, not a warning — a file that a crawler rejects and a developer stares at.

So a document renders nothing. If you want templating, use `text/template`
yourself, or, better, an encoder that knows the format:

```go
// buildSitemap marshals posts into a sitemaps.org document.
func buildSitemap(posts []Post) ([]byte, error) {
	set := urlSet{
		Namespace: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:      make([]sitemapURL, 0, len(posts)),
	}
	for _, post := range posts {
		set.URLs = append(set.URLs, sitemapURL{Location: "https://example.com/blog/" + post.Slug})
	}

	encoded, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sitemap: %w", err)
	}
	return append([]byte(xml.Header), encoded...), nil
}
```

## The builder

| Call | Effect |
| --- | --- |
| `collage.NewDocument(name, contentType)` | Starts the builder. Both are required |
| `WithPath(locale, pattern)` | The URL pattern that reaches this document in `locale` |
| `WithHandler(fn)` | The function that produces the body. Required |
| `Dynamic()` | Execute on every request, never serve from cache. The default |
| `Static()` | Execute once, serve from cache until explicitly invalidated |
| `Incremental(ttl)` | Serve from cache until `ttl` elapses since the last execution |
| `WithDependency(tags...)` | Tags every response from this document carries |
| `WithRedirect(from, to, status)` | A source pattern that redirects here |
| `WithPermanentRedirect(from, to)` | The same, as a 301 |
| `Build()` / `BuildErr()` | The document, and whatever errors the chain accumulated |

`ContentType` is static, required, and written verbatim on every response —
including responses served from cache. That is why it is not stored with the
cached body: the framework reads it from the matched document at serve time, so
adding documents did not change the `Cache` interface at all.

`Build` records `collage.ErrNoDocumentHandler` when no handler was ever set.
Unlike a page, a document has no template to fall back on, so the handler is
mandatory; `BuildErr` reports it, and `RegisterDocument` refuses the document by
name regardless of whether you checked.

## The handler contract

```go
type DocumentHandlerFunc func(ctx context.Context, rc *RenderContext) (body []byte, tags []string, err error)
```

`rc` is the same `*collage.RenderContext` a fragment's data handler receives:
`rc.Param("slug")` for a captured path parameter, `rc.Request` for the request,
`rc.Locale` for the resolved locale. One field differs: `rc.Page` is `nil`, since
no page is being rendered.

The returned `tags` are unioned with the document's own `WithDependency` tags,
de-duplicated and sorted, exactly as a page's are — and, exactly as with a page,
the tags are collected *before* the error is checked, so a handler that resolved
what it depends on and then failed has still said what would invalidate this
response.

Three things about the contract are worth stating plainly, because each is a
deliberate choice rather than an accident:

**An empty body with a nil error is a failure, not an empty document.** A page
may legitimately render nothing — an optional root fragment with no fallback
produces an empty page on purpose — but a document's return value *is* the whole
response, so an empty success is indistinguishable from a handler that forgot to
populate it. It is served as a 500, reported to plugins as
`collage.ErrEmptyDocumentBody`, and refused by the static build. **A handler that
genuinely wants to serve an empty document returns a single newline.**

**An error wrapping `collage.ErrNotFound` is a 404; anything else is a 500.** The
same distinction a page's data handler draws, reached the same way:

```go
// jwksHandler serves the signing keys, or a 404 before any key exists.
func jwksHandler(store *KeyStore) collage.DocumentHandlerFunc {
	return func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
		keys, err := store.Public(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("jwks: load keys: %w", err)
		}
		if len(keys) == 0 {
			return nil, nil, fmt.Errorf("jwks: no keys published: %w", collage.ErrNotFound)
		}

		body, err := json.Marshal(keys)
		if err != nil {
			return nil, nil, fmt.Errorf("jwks: marshal: %w", err)
		}
		return body, []string{"jwks"}, nil
	}
}
```

**A panic is contained.** A panic in a document handler is recovered the same way
a panic in a data handler is: it becomes an ordinary error carrying the recovered
value and the stack, so the request fails rather than the process.

A document handler has no per-document `Timeout` field — `Fragment` has one,
`Document` does not — so it runs under **`Config.Template.Timeout`** (5s by
default), the same knob that sets the default fragment data-handler timeout. For
a fragment that field is a default something can override; for a document it is
the entire budget, so raising it for one slow fragment raises it for every
sitemap and feed too. As everywhere else in the framework, it bounds the
*context* the handler is given, not the handler itself: a handler that never
consults `ctx.Done()` can still run past its deadline.

## Errors are plain text, never HTML

A failing document does not render an error page. Returning an HTML page to a
crawler that asked for `sitemap.xml`, or to a client that asked for JSON, is the
same mistake in both directions, so a document's failure is answered with
`text/plain`:

- the correct status — 404 for a handler error wrapping `collage.ErrNotFound`,
  500 for anything else;
- a single generic line in production, and the route name plus the full error
  chain in development mode;
- `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`, always.

If you want a format-specific error body — a JSON `{"error": ...}`, say — handle
the error inside your handler and return a body. Whatever the handler returns is
what is served.

## Caching

Everything in [caching.md](caching.md) applies unchanged. The same cache key
(path, locale, path parameters, raw query), the same content-hash ETag, the same
`If-None-Match` → 304, the same `Cache-Control` per strategy, the same rule that
only `GET` and `HEAD` are served from cache and that a `HEAD` never populates it.

Tag invalidation is the same call:

```go
// A new post drops the cached home page, the cached post page, and the cached
// sitemap — all three declared "blog:posts".
if err := app.InvalidateTags(ctx, "blog:posts"); err != nil {
	log.Printf("invalidate: %v", err)
}
```

## Plugins see three hooks, not six

A document dispatches `OnCacheWrite`, `OnCacheInvalidate` and `OnError`. It does
**not** dispatch `OnPageResolved`, `OnBeforeRender` or `OnAfterRender`.

The reason is that those three events are about a render, and no render happens.
`AfterRenderEvent.HTML` would be a lie for a zip file or a JPEG, and
`PageResolvedEvent.Page` has no value to carry. The accepted consequence is
explicit: **a plugin cannot post-process a document body.** A plugin that stamps
every page from `OnAfterRender` stamps nothing on a sitemap.

`ErrorEvent.Page` is `nil` for a document failure — the event's `Path` already
identifies the route, and the framework's own log line names the document.

## Locales

`Paths` is locale-keyed, like a page's, so a per-locale document needs no new
machinery:

```go
feed := collage.NewDocument("feed", "application/rss+xml").
	WithPath("en", "/feed.xml").
	WithPath("tr", "/feed.xml").
	WithHandler(feedHandler).
	Incremental(15 * time.Minute).
	Build()
```

**A pattern must not repeat the locale prefix.** With `Supported: {"en", "tr"}`
the URL `/tr/feed.xml` reaches the `tr` entry above, because path-locale
resolution strips the `/tr` segment *before* the router matches — so the `tr`
tree must hold `/feed.xml`, not `/tr/feed.xml`. Writing `WithPath("tr",
"/tr/feed.xml")` registers the `tr` tree's `/tr/feed.xml`, which is reached only
by `/tr/tr/feed.xml`, and `/tr/feed.xml` answers 404. This is the same rule
[routing.md](routing.md) states for pages; documents share the page tree and are
not an exception to it.

The two URLs that reach the document above are therefore `/feed.xml`, in the
default locale, and `/tr/feed.xml`. A locale that wants a genuinely different URL gets one by
writing a different pattern (`WithPath("tr", "/akis.xml")`), not by prefixing.

The handler reads the resolved locale from `rc.Locale`. The locale is part of the
cache key, so the two feeds are two entries.

One consequence for the static build: two locales pointing at the *same* literal
pattern resolve to one output file, since a document writes to its literal path.
The builder detects that collision and records the later locale in
`Report.Skipped` rather than letting two goroutines race to write one file. Give
each locale its own pattern when the build needs to emit both.

## Registration

Documents register into the *same* radix tree as pages. That is the point: a
collision between `/sitemap.xml` and a page's `/{slug}` is a startup error, not a
coin toss at request time.

| Error | Cause |
| --- | --- |
| `collage.ErrNilDocument` | A nil `*Document` |
| `collage.ErrEmptyName` | No name |
| `collage.ErrEmptyContentType` | No content type |
| `collage.ErrNoDocumentHandler` | No handler |
| `collage.ErrInvalidPath` | A path pattern not starting with `/` |
| `collage.ErrMissingTTL` | `Incremental` with no positive TTL |
| `collage.ErrDuplicateDocument` | A name another document already holds |
| `collage.ErrDuplicateRoute` | A path a page or another document already claims |
| `collage.ErrAppStarted` | Registration after the application started |

`App.Documents()` returns every registered document in registration order.
`App.RenderDocumentPath(ctx, path, locale, params)` executes one outside the HTTP
path, bypassing the cache, and returns its raw result; it reports
`collage.ErrDocumentNotFound` when the path resolves to a page, a redirect, or
nothing.

## The static build

A document is written to its **literal path**: `/sitemap.xml` becomes
`<OutDir>/sitemap.xml`, not `<OutDir>/sitemap.xml/index.html`, because a crawler
asking for `/sitemap.xml` must not receive a directory.

- A `Static()` or `Incremental(ttl)` document is built. A `Dynamic()` one is
  recorded in `Report.Skipped` — it exists to execute per request.
- A document whose pattern for a locale contains a `{param}` needs
  `BuildOptions.DocumentPathProvider`, or it is skipped with
  `collage.ErrDynamicPathUnresolved`. That is a separate interface from
  `PathProvider`, not a widening of it, so an existing `PathProvider` keeps
  compiling.
- A handler that returns an empty body is refused with
  `collage.ErrEmptyDocumentBody` and no file is written — the same condition the
  live server answers with a 500, rather than a zero-byte file and an exit status
  of zero.
- A panic in a handler is recovered as `collage.ErrBuildPanic`, recorded against
  that document, and the rest of the build continues.

```go
// documentPaths expands "/feeds/{category}.xml" into one path per category.
type documentPaths struct {
	categories []string
}

// Paths implements collage.DocumentPathProvider.
func (p documentPaths) Paths(ctx context.Context, doc *collage.Document, locale string) ([]collage.PathInstance, error) {
	pattern, ok := doc.PathFor(locale)
	if !ok || !strings.Contains(pattern, "{category}") {
		return nil, nil
	}

	instances := make([]collage.PathInstance, 0, len(p.categories))
	for _, category := range p.categories {
		instances = append(instances, collage.PathInstance{
			Path:   strings.Replace(pattern, "{category}", category, 1),
			Params: map[string]string{"category": category},
		})
	}
	return instances, nil
}
```

**A static build renders documents without plugins**, exactly as it renders pages
without them. The builder goes through `App.RenderDocumentPath`, not through
`App.Handler()`, so plugin `Init` never runs and no hook fires — including the
three a live document request does dispatch. What reaches the file is what the
handler returned, and nothing the plugin layer would have added on top.

## Non-goals

Stated so you do not go looking for them:

- **No templating, no fragments, no slots.** By design — see "Why not a page".
- **No render hooks.** See "Plugins see three hooks, not six".
- **No `Range` requests.** A document's body is produced in full, in memory, and
  served in full. Files that want seeking — audio, video, large downloads — are
  [assets](assets.md), which are served with `http.ServeContent` and do support
  `Range`.
- **No per-document timeout.** The engine's default applies.

A document body is held entirely in memory and, when its strategy is cacheable,
stored in the page cache — which is bounded by `MaxEntries`, not by bytes. A
50 MB document would evict thousands of pages. Serve anything large as an
[asset](assets.md) instead.
