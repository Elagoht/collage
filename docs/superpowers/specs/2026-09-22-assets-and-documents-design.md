# Assets and Documents — design

Date: 2026-09-22
Status: approved for planning

## Problem

`collage` can serve exactly one thing: HTML pages. Two gaps follow.

**Non-HTML responses are impossible.** `contentTypeHTML` is a constant
(`internal/httpx/handler.go:79`) written unconditionally on every 200 and every
error response (`handler.go:423`, `:450`, `errorpage.go:167`). `Page` carries no
content-type concept. So `robots.txt`, `llms.txt`, `sitemap.xml`, `rss.xml`,
`/.well-known/jwks.json` and JSON endpoints cannot be produced at all.

**Static assets have no support.** The framework contains no `http.FileServer`,
`http.Dir`, `ServeFile` or `embed.FS` anywhere. A user can compose their own mux
around `app.Handler()` — that works today — but it is undocumented and integrates
with nothing: not the cache, not `collage build`, not startup route-conflict
detection.

## Approach

Two separate mechanisms, because the two needs are genuinely different.

A **generated, cacheable payload** (sitemap, RSS, jwks) is computed from
application state, is small, and wants tag invalidation. A **served file**
(audio, zip, CSS) sits on disk or in an embed, is potentially large, and wants
Range requests and `Last-Modified` semantics.

Forcing the second through the first breaks in three specific ways:

- **Range.** Seeking in an audio file *is* a `Range` request. `http.ServeContent`
  provides 206 / `If-Range` / multipart handling. A `[]byte`-returning API cannot
  without hand-rolling RFC 9110 range parsing.
- **Memory.** The page cache is in-memory, FIFO, bounded by `MaxEntries`. One
  50 MB zip would evict thousands of pages.
- **ETag.** Generated content should be hashed (the framework already does this).
  Hashing a 500 MB file per request is not viable.

Rejected alternatives: a single `io.Reader`-returning `Document` (loses Range,
makes cacheability ill-defined, invites large payloads into the cache); and
documenting mux composition only (nothing integrates with the cache, tag
invalidation, plugin hooks or the static build, abandoning the framework's
cache-first claim outside HTML).

## Part 1 — `Document`

A routed, cacheable, non-HTML response.

```go
// internal/types/document.go
type Document struct {
	Name           string
	Paths          map[string]string // locale -> URL pattern
	ContentType    string            // required, static
	Handler        DocumentHandlerFunc
	Strategy       RenderStrategy
	CacheTTL       time.Duration
	DependencyTags []string
	Redirects      []*Redirect
}

type DocumentHandlerFunc func(ctx context.Context, rc *RenderContext) (body []byte, tags []string, err error)
```

It reuses Page's machinery unchanged: the same router, the same cache key
(path + locale + params + query), the same content-hash ETag, the same tag
invalidation, the same strategies, HEAD and 304.

### Decisions

**D1 — One router tree, two fields on `MatchResult`.** Documents register into
the *same* radix tree as pages, so a collision between `/sitemap.xml` and
`/{slug}` is caught at startup. `MatchResult` gains `Document *types.Document`
alongside `Page *types.Page`; exactly one is non-nil. Rejected: a shared `Route`
interface — explicit fields match the project's no-magic stance and keep the
consumer's switch obvious.

**D2 — No fragments, no slots, no templating.** The handler returns bytes. This
is not only simplicity: `html/template` applies HTML escaping rules, which are
wrong for XML and JSON, so generating a sitemap through it yields silently
malformed output. A handler wanting templates uses `text/template` itself.
Stated as an explicit non-goal.

**D3 — Failures render `text/plain`, never HTML.** Returning an HTML error page
to a crawler requesting `sitemap.xml`, or to a client expecting JSON, is the same
mistake. A failed document produces plain text with the correct status, generic in
production and detailed in dev mode, and a handler error wrapping
`types.ErrNotFound` produces 404. A handler wanting a format-specific error body
handles the error itself and returns a body.

**D4 — Only three plugin hooks fire: `OnCacheWrite`, `OnCacheInvalidate`,
`OnError`.** The render hooks do not, because no render occurs and
`AfterRenderEvent.HTML` would be a lie for a zip. This keeps the hook surface from
doubling. Consequence, accepted: a plugin cannot post-process a document body.

**D5 — `ContentType` is static and is not cached.** Only the body enters the
cache; the content type is read from the matched `Document` at serve time. This
means `cache.Cache` — now public API — does not change.

### Static build

Documents write to the literal path: `/sitemap.xml` becomes
`<OutDir>/sitemap.xml`, not `sitemap.xml/index.html`. Documents with a dynamic
pattern require a `PathProvider`; `StrategyDynamic` documents are skipped and
recorded.

This forces two existing signatures to widen, and the plan must say so explicitly
rather than discovering it mid-task:

- `build.PathProvider.Paths(ctx, page *types.Page, locale string)` currently takes
  a page. It becomes route-kind-agnostic — either a second method for documents or
  a parameter that carries both — and the choice belongs in the plan, not here.
- `build.Renderer`, the narrow interface `*core.App` satisfies, exposes `Pages()`.
  It gains `Documents()` and a document render entry point alongside `RenderPath`.

### Usage

```go
sitemap := collage.NewDocument("sitemap", "application/xml").
	WithPath("en", "/sitemap.xml").
	WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
		return buildSitemap(store.All()), []string{"blog:posts"}, nil
	}).
	Incremental(1 * time.Hour).
	Build()

app.RegisterDocument(sitemap)
```

## Part 2 — `Assets`

A mounted `fs.FS` served with file semantics.

```go
func (a *App) Mount(prefix string, fsys fs.FS, opts ...MountOption) error

collage.WithCacheControl("public, max-age=31536000, immutable")
collage.WithoutBuildCopy()
```

`fs.FS` covers `os.DirFS` and `embed.FS` through one interface. Object storage
does not fit it; the escape hatch there is composing a handler at the mux level,
which already works. Stated as a limitation.

### Decisions

**D6 — A thin handler over `fs.Open` + `http.ServeContent`, not
`http.FileServer`.** `FileServer` serves directory listings (an information leak),
imposes its `/index.html` → `/` redirect, cannot take a per-mount `Cache-Control`,
and returns HTML 404s — inconsistent with D3. `ServeContent` still provides Range,
`If-Range`, 206 and `Last-Modified` for free, which is what makes audio seeking
work.

**D7 — ETag is a lazily computed, memoised content hash.** `embed.FS` files have a
**zero `ModTime`**, so size+mtime validation silently fails there. The hash is
computed on first serve and the *hash alone* is memoised, never the body, so the
memory cost is bounded by file count rather than file size. Rejected: hashing the
whole FS at mount time, which makes startup proportional to total asset bytes.

**D8 — Assets never enter the page cache.** This is the entire reason the
mechanism is separate. Freshness is handled by `ServeContent` and the mount's
`Cache-Control`.

**D9 — Mounts match by prefix, before routing.** An empty prefix or `/` is
rejected. A mount prefix that shadows a registered page or document path, or
another mount, fails at startup naming both — the same rule as
`ErrRedirectShadowsPage`. Shadow detection runs at the same close-out point as the
existing error-page check, so registration order does not matter, and `Mount`
after the server has started is `ErrAppStarted`, like every other registration.

**D10 — `os.DirFS` is documented as not a security boundary.** Go's own
documentation states it does not prevent symlink traversal, so a symlink planted
inside the mounted directory escapes it. This project has already shipped and
fixed this exact bug class twice (the template root, the static build output).
Go 1.24 added `os.Root`, which *is* enforced by the kernel, and this module targets
Go 1.26. The docs and the scaffold use:

```go
root, err := os.OpenRoot("./static")
app.Mount("/static/", root.FS())
```

and state plainly why `os.DirFS` is the wrong choice here.

### Other behaviour

GET and HEAD only; anything else is 405. Content type from
`mime.TypeByExtension`, falling back to `ServeContent`'s 512-byte sniff. No
directory listings, no implicit `index.html`. Paths are `path.Clean`ed and checked
with `fs.ValidPath` after prefix stripping; directories are refused.

## Part 3 — Cross-cutting

**Static build.** Mounted assets are copied under their prefix into `OutDir`,
using the containment checks already in `internal/build`. Copying is the default
and `WithoutBuildCopy()` disables it — a media mount served from a CDN in
production should not be duplicated into the build output.

**Error surface.** The error's content type follows the *route kind*, not the
request: pages produce the HTML error page, documents and asset 404s produce
`text/plain`. All three are never cached, carry `Cache-Control: no-store`, and
dispatch `OnError`.

**Preserved invariants.** Documents use the same cache key, the same `Vary`
handling, the same tracker bound and the same `InvalidateTags` path as pages.
`Paths` is locale-keyed, so `/tr/sitemap.xml` works without new machinery.

## Non-goals

Templating for documents; render hooks for documents; Range requests for
documents; asset fingerprinting (hashed filenames); object-storage asset sources;
image processing.

## Public API added

`Document`, `DocumentHandlerFunc`, `DocumentBuilder`, `NewDocument`,
`App.RegisterDocument`, `App.Mount`, `MountOption`, `WithCacheControl`,
`WithoutBuildCopy`, and sentinels for the new failure modes (empty content type,
missing handler, invalid mount prefix, mount shadowing a route, mount conflict).
Every sentinel an application can receive is re-exported through `pkg/collage` —
the rule established when the final review found four reachability gaps.

## Testing

Document: routing alongside pages including a startup collision; cache hit, 304,
HEAD; tag invalidation regenerating; `ErrNotFound` producing a plain-text 404; a
failing handler producing a plain-text 500 that leaks nothing in production; the
three hooks firing and the render hooks not; static build writing the literal path.

Assets: Range and 206 against a real `httptest.Server` (a `ResponseRecorder`
cannot show this); `If-Range`; HEAD; 405; `embed.FS` with zero `ModTime` still
validating via ETag; a mount shadowing a route refused at startup; a symlink
inside an `os.DirFS` mount escaping — demonstrating why `os.OpenRoot` is the
documented choice; build copy and `WithoutBuildCopy`.
