# Routing, locales, redirects, and error pages

A page declares the path that reaches it, per locale:

```go
page := collage.NewPage("blog-post").
	WithLayout(layout).
	WithContent(postContent).
	WithPath("en", "/blog/{slug}").
	WithPath("tr", "/blog/{slug}").
	Build()
```

A [document](documents.md) declares its paths the same way, with the same
`WithPath(locale, pattern)`, and everything on this page applies to it unchanged
— patterns, locales, redirects, and the registration errors below.

There is one radix tree per locale, holding **page and document paths together**,
and one tree shared across locales for redirects. Sharing one tree is the point:
a collision between `/sitemap.xml` and a page's `/{slug}` is a startup error
rather than a coin toss at request time.

[Mounted assets](assets.md) are not in that tree at all. A mount claims its whole
URL prefix and is checked **before** the router runs, so nothing under
`/static/` ever reaches a route — which is safe only because a startup check
refuses a mount prefix that would swallow a registered page or document path
(`collage.ErrMountShadowsRoute`), in either registration order. A request under a
mount prefix that names no file is the mount's own plain-text 404, not the
site-wide not-found page, and a method other than `GET` or `HEAD` is a 405.

## Path patterns

| Segment | Matches |
| --- | --- |
| `blog` | Exactly that text |
| `{slug}` | Exactly one segment, captured as `slug` |
| `{rest...}` | The remainder of the path, captured as `rest`. Must be the final segment |

At every level, a static child is tried before the dynamic edge, which is tried
before the catch-all — with backtracking, so a static match always beats a dynamic
one even when the dynamic branch would have matched further down.

`/blog` and `/blog/` are the same route. A pattern must start with `/`, must not
contain an empty segment or an empty placeholder name, and must not put a
catch-all anywhere but last; anything else is `collage.ErrInvalidPattern` at
registration.

Two different parameter names at the same position — `/blog/{slug}` and
`/blog/{id}/edit` — are rejected with `collage.ErrAmbiguousParameterName`: a node
holds one dynamic edge, so the two names cannot both be right.

Captured values reach the data handler through the render context:

```go
slug := rc.Param("slug")          // or rc.PathParams["slug"]
```

Matching happens on the *escaped* path, split first and percent-decoded one
segment at a time, so a `%2F` inside a segment cannot silently erase a segment
boundary. A segment that fails to decode is a 404, not a 500.

Any HTTP method reaches the page and renders it; only `GET` and `HEAD` are
eligible to be served from, or (for `GET`) to populate, the cache.

## Locales

```go
Locale: collage.LocaleConfig{
	Default:   "en",
	Supported: []string{"en", "tr"},
},
```

Sources are consulted in priority order, and an unsupported or malformed value at
any stage falls through to the next:

1. **Path prefix** — `/tr/blog/hello` resolves `tr` and matches `/blog/hello` in
   the `tr` tree.
2. **`Accept-Language`** — standard quality-value parsing.
3. **Cookie** — named by `Locale.CookieName`, `"locale"` by default.
4. **`Locale.Default`**, which is always treated as supported.

Each source is on by default; turn one off with the matching `Disable*` field:

```go
Locale: collage.LocaleConfig{
	Default:             "en",
	Supported:           []string{"en", "tr"},
	DisableHeaderLocale: true,
	DisableCookieLocale: true,
},
```

The fields are negative on purpose. A bool documented "default true" can never be
turned off, because its zero value is indistinguishable from "the caller left it
unset" and defaulting flips it back on every time.

Enabling the header or cookie source adds `Vary: Accept-Language, Cookie` to
publicly cacheable responses — see [caching](caching.md).

A path registered for one locale only is reachable only in that locale. A request
resolving to a locale with no tree simply does not match.

## Redirects

```go
page := collage.NewPage("blog-post").
	WithContent(postContent).
	WithPath("en", "/blog/{slug}").
	WithRedirect("/old-blog/{slug}", "/blog/{slug}", 301).
	WithRedirect("/temp-blog/{slug}", "/blog/{slug}", 302).
	WithPermanentRedirect("/archive/{slug}", "/blog/{slug}").
	Build()
```

- Allowed status codes are `301`, `302`, `307`, and `308`. `0` means "decide from
  `Permanent`": `301` when permanent, `302` otherwise. Anything else is
  `collage.ErrInvalidRedirectStatus` at registration.
- A placeholder in the destination must be captured by the source pattern, or
  registration fails with `collage.ErrUnsubstitutedPlaceholder` — such a
  placeholder could never be substituted at match time.
- A redirect whose source collides with a registered page *or document* path is
  rejected in either registration order (`collage.ErrRedirectShadowsPage`): one
  of the two would be unreachable. The sentinel's name predates documents
  sharing the tree; it covers both.
- Redirects are matched **before** pages and documents, and the redirect tree is
  shared across locales, since a `Redirect` carries no locale of its own.

The response is written by hand rather than through `http.Redirect`: a `Location`
header and the status, with no body. A redirect carries its destination in the
header, and a body only makes `GET` and `HEAD` behave differently for no benefit.

## Error pages

Error pages are a *page* mechanism. A [document](documents.md) answers a failure
with `text/plain` and a [mounted asset](assets.md) answers a missing file the
same way, both bypassing everything in this section: an HTML error page handed to
a crawler fetching `sitemap.xml`, or to a browser fetching a stylesheet, is the
same mistake in either direction.

For pages, two levels, and the more specific one wins:

```go
// Page-specific.
postPage := collage.NewPage("blog-post").
	WithContent(postContent).
	WithPath("en", "/blog/{slug}").
	WithNotFoundPage(blog404Page).
	WithErrorPage(blog500Page).
	Build()

// Site-wide.
app.RegisterNotFoundPage(globalNotFound)
app.RegisterErrorPage(globalError)
```

Resolution order for a failure:

1. the failing page's own `NotFoundPage` (for a 404) or `ErrorPage` (for a 500);
2. the site-wide page registered with `RegisterNotFoundPage` /
   `RegisterErrorPage`;
3. the framework's built-in page.

**Every error page must itself be registered**, with `RegisterPage`, even though
it has no path of its own:

```go
if err := app.RegisterPage(blog404Page); err != nil {
	log.Fatal(err)
}
```

Startup fails with `collage.ErrUnregisteredErrorPage` otherwise. The reason is not
bookkeeping: registration is what binds a page's content fragment into its layout,
so an unregistered error page would render its layout around an empty slot — a
custom error page that silently never appears, at the one moment it was needed.
Identity counts, not just the name: the registered page must be the same object
the field points at.

A page cannot be its own error page (`collage.ErrSelfErrorPage`).

An error page's own render is never retried and never cached, and its dependency
tags are dropped. If it fails, or renders empty, that is logged once, reported to
plugins under the stage `"error_page"`, and the built-in page is served instead —
a 500 that can 500 into itself is an outage rather than a bad response.

### Which failures reach which page

| Situation | Status | Page |
| --- | --- | --- |
| No route matched | 404 | site-wide not-found (there is no page to ask) |
| Required fragment's handler wrapped `collage.ErrNotFound` | 404 | the page's own not-found, else site-wide |
| Required fragment failed any other way | 500 | the page's own error page, else site-wide |
| Routing, hook, or render-engine failure | 500 | the page's own error page, else site-wide |

### The built-in page

Self-contained HTML with no external stylesheet, script, image, or font, so it
renders identically on a deployment whose assets are exactly what just broke. In
development mode it shows the failing fragment and the full error chain, including
a panic's stack. In production it shows one generic sentence and nothing else — no
error text, no fragment name, no path. That is a security property, not taste: an
error message routinely carries a DSN, an internal hostname, or a filesystem path,
and an error page is the response most likely to hand all three to an anonymous
client.

## Registration errors worth knowing

| Error | Cause |
| --- | --- |
| `collage.ErrDuplicatePage` | Two pages registered under one name |
| `collage.ErrTemplateNotFound` | A fragment names a template the engine did not load |
| `collage.ErrMissingContent` | A page with no content fragment |
| `collage.ErrMissingTTL` | `Incremental` with no positive TTL |
| `collage.ErrInvalidPath` | A path pattern not starting with `/` |
| `collage.ErrUnregisteredErrorPage` | An error page referenced but never registered |
| `collage.ErrAppStarted` | Registering after the handler was built |
| `collage.ErrInvalidPattern` | A malformed path or redirect pattern |
| `collage.ErrDuplicateRoute` | Any two of {page, document} at one path, or two redirects at one source |
| `collage.ErrAmbiguousParameterName` | Two parameter names at one position |
| `collage.ErrRedirectShadowsPage` | A redirect source that is also a page or document path |
| `collage.ErrUnsubstitutedPlaceholder` | A redirect destination placeholder the source does not capture |
| `collage.ErrTemplateRootMissing` | `New`: `Template.Root` does not exist |

Every one of them is matchable with `errors.Is`, and the wrapped message names the
page or document, the pattern, and the locale. A document adds a few of its own —
`collage.ErrNilDocument`, `collage.ErrEmptyContentType`,
`collage.ErrNoDocumentHandler`, `collage.ErrDuplicateDocument` — listed in
[documents.md](documents.md); a mount adds `collage.ErrInvalidPrefix`,
`collage.ErrNilFS`, `collage.ErrMountShadowsRoute` and
`collage.ErrMountConflict`, listed in [assets.md](assets.md).
