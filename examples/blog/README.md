# The blog example

This is the advanced example from collage's specification, implemented for real.
It serves actual requests, and `main_test.go` is the framework's end-to-end test.

```
cd examples/blog && go run .     # serves on http://localhost:3000
cd examples/blog && go run . -port 8099   # if 3000 is taken
go test ./examples/blog          # the end-to-end test, from anywhere
```

Run it from **this directory**, not from the repository root: the templates are
loaded from the relative path `templates`, so the process's working directory has
to be the one holding them. `-host` and `-port` override the defaults; the test
needs neither, because it drives `app.Handler()` through `httptest` and never
binds a port.

It imports nothing but the standard library and
`github.com/Elagoht/collage/pkg/collage`. That is a deliberate constraint: if the
example ever needed an `internal/` import, the public surface would be missing
something.

## What to look at

| File | What it shows |
| --- | --- |
| `main.go` | The whole application: config, fragments, pages, registration |
| `store.go` | An in-memory post store, and the two ways a data fetch can fail |
| `documents.go` | `/sitemap.xml` and `/robots.txt`: routed, cached, non-HTML responses |
| `assets.go` | The `/static/` mount, served from an `embed.FS` |
| `plugin.go` | A plugin that post-processes every render and adds a CLI subcommand |
| `templates/` | A shared layout, two page templates, four error templates |
| `static/` | The stylesheet the layout links, embedded into the binary |
| `main_test.go` | `httptest` against `app.Handler()` |

## The routes

| Path | What happens |
| --- | --- |
| `/` | The post index, incrementally cached for a minute, tagged `blog:posts` |
| `/blog/{slug}` | One post, incrementally cached for ten minutes, tagged `post:<slug>` and `blog:posts` |
| `/tr/`, `/tr/blog/{slug}` | The same pages, resolved to the `tr` locale from the path prefix |
| `/old-blog/{slug}` | `301` to `/blog/{slug}` |
| `/temp-blog/{slug}` | `302` to `/blog/{slug}` |
| `/sitemap.xml` | A document: `application/xml`, incrementally cached for an hour, tagged `blog:posts` |
| `/robots.txt` | A document: `text/plain`, static, its body a constant |
| `/static/app.css` | A mounted file: served from the `embed.FS`, with a content-hash `ETag` |
| `/static/nope.css` | `404` in `text/plain` — a missing stylesheet is not answered with a web page |
| `/blog/no-such-post` | `404`, rendered by the blog's own not-found page |
| `/blog/storage-outage` | `500`, rendered by the blog's own error page |
| anything else | `404`, rendered by the site-wide not-found page |

## The bit worth copying

The store reports two different failures, and the difference decides the response:

```go
// The post does not exist. Wrapping collage.ErrNotFound is what makes this a 404
// served by the page's NotFoundPage.
return Post{}, fmt.Errorf("blog: no post with slug %q: %w", slug, collage.ErrNotFound)

// The post exists but could not be loaded. An ordinary error: a 500, served by
// the page's ErrorPage.
return Post{}, fmt.Errorf("blog: load post %q: %w", slug, ErrStorageUnavailable)
```

The content fragment is `Required()`, so either failure fails the whole page
rather than rendering the layout around a hole. Without the `ErrNotFound` wrap,
a missing post would be a 500 and the page's 404 page would be unreachable.

## Sharing one layout

All four of the layout-bearing pages — home, post, blog 404, blog 500 — are built
from the same `layout` fragment value. That is safe: registration gives each page
a private copy of the layout's slot table before binding that page's content into
it, so the pages do not render each other's content out of one shared slot.

## Documents are not pages

`/sitemap.xml` and `/robots.txt` are `collage.Document`s, not pages. They share a
page's routing, cache key, ETag, conditional responses and tag invalidation, and
share none of its rendering: a document's handler returns bytes.

That is deliberate, and `documents.go` builds its sitemap with `encoding/xml` to
show why. `html/template` applies HTML escaping rules, which are wrong for XML
and for JSON, so a sitemap generated through the page pipeline would be silently
malformed at exactly the characters that need escaping most.

The sitemap declares `WithDependency("blog:posts")` — the same tag the home page
and the post page declare — so publishing a post invalidates all three with one
call. `main_test.go` asserts that by counting handler executions rather than
status codes: a cache hit and a re-render are both `200` with the same body.

## Assets are neither

`/static/` is a mount: an `fs.FS` served with `http.ServeContent`, which brings
`Range`, `If-Range`, `206` and `Last-Modified` with it. A mounted file never
enters the page cache — its freshness is the mount's `Cache-Control` and the
client's business — and a missing one answers `text/plain`, never HTML.

The example mounts an `embed.FS`, so the **static files** travel with the binary
and need no working directory. The templates do not: `TemplateConfig.Root` is a
filesystem path, so the example still has to be run from its own directory. An
application serving files from disk should use
`os.OpenRoot("./static")` and its `FS()`, never `os.DirFS`, which Go's own
documentation states does not prevent symlink traversal. See
[docs/assets.md](../../docs/assets.md).

## Error pages must be registered

`blog404Page` and `blog500Page` have no paths of their own, but they are still
passed to `RegisterPage`. Startup rejects a page that references an unregistered
error page, because an unregistered one never has its content bound into its
layout: it would render empty at the one moment it was needed.
