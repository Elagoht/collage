# The Wire — a magazine built with collage

A news site reading everything it renders from a JSON backend over HTTP.

This is a **separate module**. `go.mod` requires `github.com/Elagoht/collage` the
way any project would, and the code imports nothing from the framework's internals.
It is here to be read as an application, not as part of the framework.

```
cd ../newsroom-api && go run .   # the backend, on localhost:8080
cd ../magazine     && go run .   # the site, on http://localhost:3000
```

If port 3000 is taken: `go run . -port 8080` — or any other, and point the backend
elsewhere with `go run . -api http://localhost:9000`.

```
go test ./...
```

`go.mod` here is exactly what yours would be — a plain `require`, no `replace`. The
framework's source is not in this directory and cannot be reached from it: collage
comes from the module cache like any other dependency, and Go does not let one
module import another's `internal` packages.

The `go.work` at the repository root is what builds this against the checkout beside
it rather than the released version, so a change to the framework is exercised here
immediately. It is a developer's tool and nothing in this module refers to it —
delete it, or copy this directory somewhere else, and the `require` above resolves
from the module cache as usual.

`examples/blog` is the other example, and it answers a different question: it shows
the framework's mechanisms one at a time against an in-process store, and it is the
framework's end-to-end test, so it lives inside the framework's own module. This one
is about what the mechanisms are *for* once a backend is involved.

## Routes

| Path (en) | Path (tr) | Strategy |
|---|---|---|
| `/` | `/tr/` | Incremental, 1m |
| `/category/{slug}` | `/tr/kategori/{slug}` | Incremental, 2m |
| `/author/{slug}` | `/tr/yazar/{slug}` | Incremental, 5m |
| `/{year}/{month}/{slug}` | `/tr/{year}/{month}/{slug}` | Incremental, 15m |
| `/search?q=` | `/tr/arama?q=` | **Dynamic — never cached** |
| `/rss.xml`, `/sitemap.xml` | same | Incremental, 15m / 1h |
| `/robots.txt` | same | Static |
| `/healthz` | same | Dynamic |
| `/static/` | same | mounted `embed.FS`, outside the page cache |

## The decisions worth reading the code for

**The API's types are declared again in `newsroom.go`, not imported.** A backend
does not hand its structs to its callers — you read its documentation and declare
what you need — and a client that imports them is coupled to a repository layout
that would not exist outside this one. It also makes the contract visible:
everything the site depends on the backend for is in one file.

**Locale comes from the URL and nowhere else.** The site sets
`DisableHeaderLocale` and `DisableCookieLocale`. It registers different paths per
locale, so a locale resolved from a browser header can disagree with the path
actually requested: an English link shared with someone whose browser asks for
Turkish would resolve to `tr`, look `/category/climate` up in the Turkish tree, and
404 on a URL that works perfectly for the person who sent it. With both disabled, a
URL means the same page for everyone — which is also what makes the pages shareable
and the cache sound.

**The document head is a slot, not part of the layout.** A layout's data handler
runs before its content slot renders — fragments render depth-first, and a slot is
filled during the parent's template execution — so the layout cannot see the
headline the content fragment is about to fetch. Building `<title>` from the
layout's own data gives every page the site name and nothing else. Each page binds
its own head fragment instead, and the article and section lookups are memoised into
`RenderContext.SharedData` so the head and the content share one request rather than
making two.

**The layout performs no I/O.** Everything the chrome needs from the backend — the
section nav, the "most read" sidebar — lives in its own fragment with its own
fallback, bound into a slot. A layout that can fail is a layout that can take down
the error page explaining why the site is broken.

**Required is a claim about the page, not the fragment.** The article body is
`Required()`, so a missing piece is a 404 and an unreachable backend is a 500. The
sidebar is not, so its failure costs a panel and not the article. Deleting
`Required()` from the article fragment makes both of those tests fail with a 200 —
chrome wrapped around a hole, which is how a broken article gets indexed as an empty
one.

**"No such article" and "cannot reach the backend" stay apart the whole way.** The
client keeps `ErrNotFound` and `ErrUnavailable` distinct and retries only the
second. A site that conflates them serves 404s during an outage and teaches every
crawler that its archive was deleted.

**Degraded renders are not cached.** The framework declines to cache a render in
which any fragment failed. That is what stops a momentary outage from being frozen
into the page for the whole TTL: the sidebar returns on the next request rather than
fifteen minutes later. `TestSite_DegradedRendersAreNotCached` asserts it by counting
backend requests, since both responses are `200` with different bodies.

**Search is never cached, and `robots.txt` says so.** A page's cache key includes
the raw query string, so caching search would mint an entry per distinct `?q=` —
waste with an ordinary crawler, an eviction attack with an attacker-chosen
parameter. Rendering it fresh costs one API call.

**The feed and the sitemap are marshalled, not rendered.** `html/template` applies
HTML escaping rules, which are wrong for XML at exactly the characters that most
need escaping: an apostrophe in a headline becomes `&#39;` in a reader's title bar,
and a naked ampersand makes the document unparseable. Documents return bytes, so
`encoding/xml` does the work.

**`/healthz` reports a degraded backend with a 200.** Liveness, not readiness: the
site is still serving cached pages, error pages and static files, and an
orchestrator that restarts it for an upstream outage is restarting the wrong
process. The body says which it is, so a probe that does care can read it.

## Watching it degrade

Start the backend with failure injection:

```
cd ../newsroom-api && go run . -fail-every 3 -latency 400ms
```

Load a section page that is not yet cached and the "most read" panel turns into its
fallback, styled differently on purpose — a degraded panel that looks like a working
one is how a partial outage goes unnoticed for a week.

With `-fail-every 2` you will see *nothing* degrade, which is also the point: the
client retries once, and with every second request failing the retry always lands on
a success.

## Configuration

Flags, or the environment: `HOST`, `PORT`, `MAGAZINE_API_URL`, `PUBLIC_BASE_URL`,
`CACHE_TTL`, `LOG_LEVEL`, `DEV_MODE`.

`PUBLIC_BASE_URL` has to be set behind a proxy. The site cannot infer its public
origin from the address it binds, and a feed whose links carry the wrong origin is a
feed nobody can follow.
