# The blog example

This is the advanced example from collage's specification, implemented for real.
It serves actual requests, and `main_test.go` is the framework's end-to-end test.

```
go run ./examples/blog      # serves on http://localhost:3000
go test ./examples/blog     # the end-to-end test
```

It imports nothing but the standard library and
`github.com/Elagoht/collage/pkg/collage`. That is a deliberate constraint: if the
example ever needed an `internal/` import, the public surface would be missing
something.

## What to look at

| File | What it shows |
| --- | --- |
| `main.go` | The whole application: config, fragments, pages, registration |
| `store.go` | An in-memory post store, and the two ways a data fetch can fail |
| `plugin.go` | A plugin that post-processes every render and adds a CLI subcommand |
| `templates/` | A shared layout, two page templates, four error templates |
| `main_test.go` | `httptest` against `app.Handler()` |

## The routes

| Path | What happens |
| --- | --- |
| `/` | The post index, incrementally cached for a minute, tagged `blog:posts` |
| `/blog/{slug}` | One post, incrementally cached for ten minutes, tagged `post:<slug>` and `blog:posts` |
| `/tr/`, `/tr/blog/{slug}` | The same pages, resolved to the `tr` locale from the path prefix |
| `/old-blog/{slug}` | `301` to `/blog/{slug}` |
| `/temp-blog/{slug}` | `302` to `/blog/{slug}` |
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

## Error pages must be registered

`blog404Page` and `blog500Page` have no paths of their own, but they are still
passed to `RegisterPage`. Startup rejects a page that references an unregistered
error page, because an unregistered one never has its content bound into its
layout: it would render empty at the one moment it was needed.
