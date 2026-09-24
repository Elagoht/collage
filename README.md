# collage

A Go framework for server-side, component-based rendering.

A page is a **layout fragment** wrapped around a **content fragment**, and a
fragment is a template plus an optional data handler plus the slots it exposes to
its children. Fragments compose; pages are configuration. Output is cached by
dependency tag and invalidated by tag, not by guessing TTLs.

Not everything a site serves is HTML, so two things beside pages: **documents** —
routed, cacheable non-HTML responses such as a sitemap, a feed or a `robots.txt`,
whose handler returns bytes — and **assets**, a mounted `fs.FS` served with
`Range` support and its own `Cache-Control`.

No dependencies: the standard library, and nothing else. Go 1.26.

**Status: pre-1.0.** The API can still change between minor versions. Every
change that breaks an application is listed first, under **Breaking**, in
[CHANGELOG.md](CHANGELOG.md), with what to do instead; patch versions never break
anything. The aim for 1.0 is an API that then stays put.

## Getting started

Three commands and a site is running.

```
go install github.com/Elagoht/collage/cmd/collage@latest

collage new mysite
cd mysite && go mod tidy
cp .env.example .env.development
collage dev
```

http://localhost:3000 — a home page and a page of live demos: an API action, a form,
a fragment with its own URL and a JSON document, with the tests that drive them.
`collage dev` reads `.env.development`; `PORT=8080 collage dev` moves it. Go 1.26 is
the only requirement; `go install` puts `collage` in `$(go env GOPATH)/bin`, which is on your `PATH` if you have
installed any Go tool before.

Then, in that directory:

```
go test ./...    # the tests it came with
collage build    # -> bin/mysite, the binary you deploy
collage export   # -> dist/, static files, for a site that needs no server
collage serve    # serves dist/ the way a static host would
```

Under `collage dev`, saving a template reloads the page in the browser, and saving
Go code rebuilds and restarts the program and then reloads the page — no external
watcher, nothing to install. A change that does not compile leaves the last good
build running. `collage new mysite -minimal` starts without the demos.

Where to look next: `pages/` has one file per page, `fragments/` the layout and the
demos, `actions/` the API endpoint; `routes.go` registers them, and `main.go` has the
configuration. The rest of this file is what
those are made of, and [docs/](docs/) is the detail.

**Adding collage to a project you already have** is `go get
github.com/Elagoht/collage` and the application below.

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
		WithDataHandler(collage.DataHandler(func(_ context.Context, _ *collage.RenderContext) (homeData, []string, error) {
			return homeData{Title: "Welcome home"}, []string{"homepage"}, nil
		})).
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

### Plugins

A plugin is an ordinary Go module that implements `collage.Plugin`. Nothing about
one shipped by the framework's author would be privileged, which is why none ship
here: they are separate projects, fetched with `go get` and registered in
`Config.Plugins` like any other dependency.

```go
import optimage "github.com/Elagoht/collage-opti-image"

app, err := collage.New(&collage.Config{
	Plugins:      []collage.Plugin{optimage.New()},
	PluginConfig: pluginConfig,
})
```

A plugin can observe a request, rewrite a page's HTML or a document's bytes, and
contribute pages, documents, mounts and template functions of its own. See
[`docs/plugins.md`](docs/plugins.md).

## What it guarantees

- **No silent failures.** A fragment naming a template that does not exist, a
  page that does not validate, a page referencing an error page that was never
  registered — all of these fail at startup, by name, not on the first request.
- **Deterministic output.** Templates execute in order, depth-first; sibling
  fragments fetch their data concurrently, but what they hoist is placed by
  declaration order, not by which finished first. So the same page rendered twice
  from the same inputs is byte-identical. That is what makes caching and ETags
  sound.
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
- **A URL means one thing.** The locale comes from the path and nothing else, so
  a link, a crawler and a CDN all see what the reader sees. Content that depends on
  a header is declared with `collage.Vary`, which keys the cache on what the
  application resolved and sends the `Vary` header downstream caches need.
- **Invalidation reports what it did.** `InvalidateTagsN` returns the number of
  keys it reached, and a partial failure is an error rather than a silent success.
- **A non-HTML route fails as a non-HTML route.** A document's failure, and a
  missing asset, answer `text/plain` — a crawler asking for `sitemap.xml` and a
  browser asking for a stylesheet never get a web page back.
- **Assets are files, not cache entries.** A mount is served with
  `http.ServeContent`, so `Range` and `206` work and audio seeks; it never enters
  the page cache, so a 50 MB download cannot evict thousands of pages.
- **A mount cannot silently shadow a route.** A mount or handler prefix that would
  swallow a registered route is refused at startup, naming both, in either
  registration order.

## Extending the framework

Four seams, all reachable from `pkg/collage` alone.

**Your own HTTP.** Collage renders pages; an API, auth or language negotiation is
written the way you already would. `app.Use` takes standard `net/http` middleware,
which runs before routing and inside the framework's panic guard and metrics, and
whatever it puts in the request context is what data handlers read. `app.Handle`
mounts any `http.Handler` under a prefix — collage applies no forgery check, body
limit or cache to it.

```go
app.Use(requireAuth)
app.Handle("/api/", apiRouter) // chi, echo, http.ServeMux, a gRPC gateway
```

[docs/http.md](docs/http.md) has both, and `collage.Vary` for a cached page whose
content depends on a header.

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

**Documents and assets.** Neither is an extension point so much as a second and
third kind of route, reachable from `pkg/collage` alone.

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

err := app.RegisterDocument(sitemap)

// os.OpenRoot, not os.DirFS: os.DirFS does not prevent symlink traversal.
root, err := os.OpenRoot("./static")
if err != nil {
	log.Fatal(err)
}
err = app.Mount("/static/", root.FS(), collage.WithCacheControl("public, max-age=3600"))
```

[docs/documents.md](docs/documents.md) and [docs/assets.md](docs/assets.md) cover
both, including what they deliberately do not do.

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
| [docs/architecture.md](docs/architecture.md) | Package layout, the request lifecycle, why data is fetched concurrently but markup written in order, why plugins get a `Host` |
| [docs/fragments.md](docs/fragments.md) | Fragments, slots, layouts, data handlers, timeouts, the failure policy, template functions |
| [docs/documents.md](docs/documents.md) | `Document`: non-HTML responses, the handler contract, plain-text errors, the four hooks, static builds |
| [docs/assets.md](docs/assets.md) | `App.Mount`: `fs.FS` assets, `Range` and ETags, why `os.OpenRoot` rather than `os.DirFS`, and the limitations |
| [docs/caching.md](docs/caching.md) | Render strategies, the cache key, ETags, `Vary`, dependency tags, invalidation, custom caches |
| [docs/plugins.md](docs/plugins.md) | The `Plugin` contract, the `Host`, every hook, dispatch and error semantics, lifecycle |
| [docs/routing.md](docs/routing.md) | Path patterns, locales, redirects, error-page resolution, registration errors |
| [docs/http.md](docs/http.md) | `app.Use` middleware, `app.Handle` for your own handlers, `collage.Vary` |
| [docs/actions.md](docs/actions.md) | `Action`: methods, forms, `ActionResult`, request-forgery tokens, fragments at their own URLs |
| [docs/deployment.md](docs/deployment.md) | Building a binary, containers, signals, TLS, the cache in production, health checks |
| [docs/cli.md](docs/cli.md) | `collage new`/`dev`/`build`, plugin subcommands, and the static site builder |

## The CLI

```
collage new myblog    # scaffold a runnable project
collage dev           # run it, rebuilding and restarting on every Go change
collage build         # compile the binary you deploy
collage export        # render the statically-buildable pages to dist/
collage serve         # serve dist/ the way a static host would
```

## Contributing and security

[CONTRIBUTING.md](CONTRIBUTING.md) has what a change needs to be merged. A
vulnerability is reported privately — see [SECURITY.md](SECURITY.md), not an
issue.

## License

MIT — see [LICENSE](LICENSE).
