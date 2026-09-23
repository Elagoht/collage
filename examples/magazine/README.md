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

## Plugins

The site uses three, fetched with `go get` like any other dependency:

| | |
|---|---|
| [`collage-minimizer`](https://github.com/Elagoht/collage-minimizer) | strips whitespace and comments from pages, documents and the stylesheet |
| [`collage-jsonld`](https://github.com/Elagoht/collage-jsonld) | emits an Article and a BreadcrumbList on every piece |
| [`collage-opti-image`](https://github.com/Elagoht/collage-opti-image) | resizes the article images and serves them itself |

Nothing about them is special because they were written alongside the framework.
They require `github.com/Elagoht/collage` exactly as this module does.

Two of them arrive through `Config.Plugins` rather than `RegisterPlugin`, because
they need the `Configure` phase: the minifier wraps every mounted filesystem, and
the image optimiser registers the route it serves from, and both happen while the
application is built.

Their settings come from `plugins-config.json`, which the site loads itself — the
one line tying it to JSON is its own `LoadPluginConfig` call, so moving the settings
to YAML or the environment changes nothing else.

One setting does not come from that file. The image optimiser may only fetch from
hosts it is told about, and the only host this site uses is wherever `-api` pointed,
which the file cannot know. `main.go` merges it in. That is what `PluginConfig`
being a plain map is for: the application composes it, from a file and from anything
else it knows.

### Seeing the image optimiser work

The newsroom API generates a 1600×900 image per article. The templates declare
`width` and `height` on every `<img>` — which is what makes them eligible, since the
declared size is the only statement of how large the picture will actually be drawn
— so the plugin rewrites each `src` to a signed URL under `/_image/` and serves a
copy at that size:

```
source              1600x900   92,906 bytes   (from the API)
article, PNG         760x428   32,177 bytes
article, WebP        760x428    6,162 bytes   (with -tags webp)
```

`plugins-config.json` asks for WebP, and it arrives in a binary built with
`-tags webp` — the tag is what links the encoder:

```
go run -tags webp .
```

Without the tag the same configuration serves PNG, which is the right fallback: a
missing encoder is a reason to serve the source format, not to refuse to start. It
is not a silent one, though — the plugin says so at startup:

```
WARN opti-image: WebP is configured but no encoder is linked; serving the source
     format instead  fix="build with -tags webp, ..."
```

That encoder is lossless, which is why it wins so heavily here and would lose on a
photograph — this site's images are generated gradients. See the plugin's README for
the numbers either way.

Nothing is fetched while the page renders. The rewrite happens during the render;
the fetch and the resize happen the first time a browser asks for the rewritten URL,
and the result is cached for thirty days behind a strong ETag.

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

**Every page says which query parameters it reads.** By default the whole query
string discriminates, which is correct and expensive: a newsletter link carrying
`?utm_source=` caches a second copy of the front page, and a crawler walking
variants evicts the real archive from a bounded cache without ever asking for a
distinct page. The listings declare `WithCacheParams("page")`; an article declares
`WithCacheParams()` with nothing at all, because it renders the same whatever the
query says.

**Search is still never cached, and `robots.txt` says so.** The allowlist does not
rescue it: the offending parameter is the one the page is about. `q` has to
discriminate, and its values are chosen by whoever is asking — a cache keyed on
arbitrary reader input is an eviction attack with a text field for a trigger.
Rendering it fresh costs one API call.

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

## Building it to files

```
go run . -build ./out -api http://localhost:8080
```

The backend has to be running: a static build asks it which articles, sections and
writers exist, and renders each one. 68 files for this corpus.

```
out/index.html                                    the front page
out/tr/index.html                                 and in Turkish
out/category/climate/index.html                   a section
out/2026/09/seawalls-buy-time-not-safety/index.html
out/rss.xml  out/sitemap.xml  out/robots.txt
out/static/magazine.css                           the mount, copied
```

A live server answers `/category/{slug}` for whatever arrives; a directory of files
has to be told which ones exist. `build.go` answers that with a `PathProvider`
reading the same API the site renders from — which is the application's job, because
only it knows where the content comes from.

What is skipped, and why, is printed rather than left to be discovered:

- `search`, `/healthz` and the error pages use the `Dynamic` strategy, which has no
  static answer by definition;
- `rss.xml`, `sitemap.xml` and `robots.txt` are one file serving both locales, so
  the Turkish copies would write over the English ones.

The plugins run. A built page is byte-for-byte what the server sends — minified,
carrying its structured data, with the image URLs rewritten — with one exception
below.

**The image URLs will not work in the built site.** Two things are true at once: the
optimiser signs its URLs with a key generated per process, so a build's URLs are not
valid for any later process; and the route that would serve them is skipped by the
build anyway, because its path is dynamic and no `DocumentPathProvider` can enumerate
signatures that do not exist yet. A statically built site needs its images somewhere
a file server can reach — the origin directly, or a CDN — which means leaving the
optimiser off for the build. It is a real limit of combining the two, not a setting.

## Configuration

Flags, or the environment: `HOST`, `PORT`, `MAGAZINE_API_URL`, `PUBLIC_BASE_URL`,
`CACHE_TTL`, `LOG_LEVEL`, `DEV_MODE`.

`PUBLIC_BASE_URL` has to be set behind a proxy. The site cannot infer its public
origin from the address it binds, and a feed whose links carry the wrong origin is a
feed nobody can follow.
