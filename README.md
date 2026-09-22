# collage

A Go framework for server-side, component-based rendering.

A page is a **layout fragment** wrapped around a **content fragment**, and a
fragment is a template plus an optional data handler plus the slots it exposes to
its children. Fragments compose; pages are configuration. Output is cached by
dependency tag and invalidated by tag, not by guessing TTLs.

No dependencies: the standard library, and nothing else. Go 1.26.

## Install

```
go get github.com/Elagoht/collage
go install github.com/Elagoht/collage/cmd/collage@latest   # optional CLI
```

## A minimal application

```go
package main

import (
	"context"
	"log"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// homeData is what the home page's template renders with.
type homeData struct {
	Title string
}

func main() {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{
			Host: "localhost",
			Port: 3000,
		},
		Template: collage.TemplateConfig{
			Root:      "templates",
			Extension: ".html",
			DevMode:   true,
		},
		Cache: collage.CacheConfig{
			Enabled:    true,
			Type:       "memory",
			DefaultTTL: 5 * time.Minute,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	layout := collage.NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		Build()

	homeContent := collage.NewFragment("home-content", "pages/home.html").
		WithDataHandler(func(_ context.Context, _ *collage.RenderContext) (any, []string, error) { // any: collage.DataHandlerFunc renders arbitrary template data
			return homeData{Title: "Welcome home"}, []string{"homepage"}, nil
		}).
		Build()

	homePage := collage.NewPage("home").
		WithLayout(layout).
		WithContent(homeContent).
		WithPath("en", "/").
		Incremental(5 * time.Minute).
		WithDependency("homepage").
		Build()

	if err := app.RegisterPage(homePage); err != nil {
		log.Fatal(err)
	}

	log.Println("serving on http://localhost:3000")
	if err := app.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
```

```html
<!-- templates/layouts/default.html -->
<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>My site</title></head>
<body>{{slot "content"}}</body>
</html>
```

```html
<!-- templates/pages/home.html -->
<h1>{{.Title}}</h1>
```

Later, when the homepage's content changes:

```go
err := app.InvalidateTags(ctx, "homepage")
```

For the full version of this — shared layouts, `/blog/{slug}`, page-specific 404
and 500 pages, redirects, incremental caching, and a plugin — see
[`examples/blog`](examples/blog), which is also the framework's end-to-end test.

## What it guarantees

- **No silent failures.** A fragment naming a template that does not exist, a
  page that does not validate, a page referencing an error page that was never
  registered — all of these fail at startup, by name, not on the first request.
- **Deterministic output.** Fragments render strictly sequentially, depth-first,
  so the same page rendered twice from the same inputs is byte-identical. That is
  what makes caching and ETags sound.
- **A failure has a defined blast radius.** A `Required()` fragment's failure
  fails the page; an optional one falls back or renders nothing. A panic in a data
  handler or a template fails that fragment, not the process.
- **A broken render is never cached.** If any fragment failed — even one a
  fallback covered for — the page is served but not stored. Error responses are
  `no-store` and are never cached either.
- **"Missing" and "broken" are different.** A data handler that wraps
  `collage.ErrNotFound` produces a 404 and the page's own not-found page; anything
  else produces a 500 and its error page.
- **Production error pages leak nothing.** Fragment names, error chains, and
  stacks appear only in development mode.
- **Caches downstream are told the truth.** `Vary` on every publicly cacheable
  response, derived from the locale sources actually enabled, so a CDN cannot hand
  one visitor's language to the next.
- **Invalidation reports what it did.** `InvalidateTagsN` returns the number of
  keys it reached, and a partial failure is an error rather than a silent success.

## Extending the framework

Three seams, all reachable from `pkg/collage` alone.

**A cache of your own.** Implement `collage.Cache` and set it on
`CacheConfig.Store`; `Cache.Type` is then ignored. Implement
`collage.TaggedCache` too and the framework calls `SetTagged`, so your store
indexes the dependency tags itself.

```go
app, err := collage.New(&collage.Config{
	Template: collage.TemplateConfig{Root: "templates"},
	Cache: collage.CacheConfig{
		Enabled: true,
		Store:   newRedisCache(client), // implements collage.Cache
	},
})
```

Five methods, no framework internals, and `collage.ETag(content)` if you have no
reason to derive your own ETags. [docs/caching.md](docs/caching.md) has a complete
implementation.

**Template functions of your own.** `TemplateConfig.Funcs` is merged over the
built-ins at construction, so an entry under a built-in name replaces it. It has
to be set before `New`, because that is where templates are parsed.

```go
app, err := collage.New(&collage.Config{
	Template: collage.TemplateConfig{
		Root: "templates",
		Funcs: template.FuncMap{
			"money": func(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) },
		},
	},
})
```

**A plugin.** Implement `collage.Plugin` and whichever hooks you need; the
registry finds them by type assertion, so assert the interfaces you meant to
implement.

```go
// stamp post-processes every rendered page.
type stamp struct{}

func (s *stamp) Name() string                                 { return "stamp" }
func (s *stamp) Version() string                              { return "1.0.0" }
func (s *stamp) Init(_ context.Context, host collage.Host) error { return nil }
func (s *stamp) Shutdown(_ context.Context) error             { return nil }

func (s *stamp) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	ev.HTML = append(ev.HTML, "<!-- stamped -->"...)
	return nil
}

var (
	_ collage.Plugin          = (*stamp)(nil)
	_ collage.AfterRenderHook = (*stamp)(nil)
)

err := app.RegisterPlugin(&stamp{})
```

Details in [docs/caching.md](docs/caching.md),
[docs/fragments.md](docs/fragments.md) and [docs/plugins.md](docs/plugins.md).

## Documentation

| Document | Covers |
| --- | --- |
| [docs/architecture.md](docs/architecture.md) | Package layout, the request lifecycle, why rendering is sequential, why plugins get a `Host` — and the deviations from the original specification |
| [docs/fragments.md](docs/fragments.md) | Fragments, slots, layouts, data handlers, timeouts, the failure policy, template functions |
| [docs/caching.md](docs/caching.md) | Render strategies, the cache key, ETags, `Vary`, dependency tags, invalidation, custom caches |
| [docs/plugins.md](docs/plugins.md) | The `Plugin` contract, the `Host`, every hook, dispatch and error semantics, lifecycle |
| [docs/routing.md](docs/routing.md) | Path patterns, locales, redirects, error-page resolution, registration errors |
| [docs/cli.md](docs/cli.md) | `collage new`/`dev`/`build`, plugin subcommands, and the static site builder |

`docs/spec/usage-examples.md` holds the canonical examples the public API is
defined against.

## The CLI

```
collage new myblog        # scaffold a runnable project
collage dev               # go run . with COLLAGE_DEV=1
collage build -out dist   # render the statically-buildable pages to files
```

## Tests

```
go test ./... -race -count=1
```
