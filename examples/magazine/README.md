# The Wire — the production-shaped example

A news magazine built on collage, reading everything it renders from a fake JSON
API over HTTP.

`examples/blog` shows the framework's mechanisms one at a time against an
in-process store. This one puts them together against a backend that can be slow,
can fail, and is on the other end of a socket — which is where the interesting
decisions are.

```
make -C examples/magazine dev      # API on :8080, site on http://localhost:3000
make -C examples/magazine chaos    # the same, with the backend misbehaving
make -C examples/magazine test
```

Or without make, from anywhere:

```
go run ./examples/magazine/cmd/api &
go run ./examples/magazine/cmd/site
```

Both binaries embed everything they serve — templates, stylesheet and the article
corpus — so neither needs a working directory, a data volume, or anything beside it
in a container image.

## What is here

| | |
|---|---|
| `newsroom/` | the article model, 20 articles across 5 sections and 6 writers, and the API client |
| `cmd/api/` | the fake backend: read-only JSON, with deliberate failure injection |
| `cmd/site/` | the site itself |

`newsroom` imports no collage package, deliberately. The API server has no business
knowing what renders its JSON; the site translates newsroom's errors into the
framework's at its own edge, in `translate`.

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

**Locale comes from the URL and nowhere else.** The site sets
`DisableHeaderLocale` and `DisableCookieLocale`. It registers different paths per
locale, so a locale resolved from a browser header can disagree with the path
actually requested: an English link shared with someone whose browser asks for
Turkish would resolve to `tr`, look `/category/climate` up in the Turkish tree, and
404 on a URL that works perfectly for the person who sent it. With both disabled, a
URL means the same page for everyone — which is also what makes the pages shareable
and the cache sound.

**The layout performs no I/O.** Everything the chrome needs from the backend — the
section nav, the "most read" sidebar — lives in its own fragment with its own
fallback, bound into a slot. A layout that can fail is a layout that can take down
the error page explaining why the site is broken.

**Required is a claim about the page, not the fragment.** The article body is
`Required()`, so a missing piece is a 404 and an unreachable backend is a 500.
The sidebar is not, so its failure costs the reader a panel and not the article.
Deleting `Required()` from the article fragment makes both of those tests fail with
a 200 — chrome wrapped around a hole, which is how a broken article gets indexed as
an empty one.

**"No such article" and "cannot reach the backend" stay apart the whole way.** The
client keeps `ErrNotFound` and `ErrUnavailable` distinct, and retries only the
second. A site that conflates them serves 404s during an outage and teaches every
crawler that its archive was deleted.

**Degraded renders are not cached.** The framework declines to cache a render in
which any fragment failed. That is what stops a momentary outage from being frozen
into the page for the whole TTL: the sidebar returns on the next request rather
than fifteen minutes later. `TestSite_DegradedRendersAreNotCached` asserts it by
counting backend requests, since both responses are `200` with different bodies.

**Search is never cached, and `robots.txt` says so.** A page's cache key includes
the raw query string, so caching search would mint an entry per distinct `?q=` —
waste with an ordinary crawler, an eviction attack with an attacker-chosen
parameter. Rendering it fresh costs one API call. Disallowing it in `robots.txt` is
the other half of the same decision.

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

```
make -C examples/magazine chaos
```

That starts the API with `-fail-every 3 -latency 400ms`. Load a section page that
is not yet cached and the "most read" panel turns into its fallback, styled
differently on purpose — a degraded panel that looks like a working one is how a
partial outage goes unnoticed for a week.

With `-fail-every 2` you will see *nothing* degrade, which is also the point: the
client retries once, and with every second request failing the retry always lands
on a success. Failure injection that the retry can absorb is failure injection
working as intended.

## Deploying it

```
docker compose --project-directory ../.. -f compose.yaml up --build
```

The `Dockerfile` builds either binary (`--build-arg CMD=api|site`) onto `scratch`.
Nothing is copied into the runtime stage but the binary and the certificate roots,
because there is nothing else: both binaries carry their own content.

Configuration is environment variables with flag overrides — `PORT`,
`MAGAZINE_API_URL`, `PUBLIC_BASE_URL`, `CACHE_TTL`, `LOG_LEVEL`, `DEV_MODE` for the
site; `MAGAZINE_API_ADDR`, `MAGAZINE_API_LATENCY`, `MAGAZINE_API_FAIL_EVERY`,
`LOG_LEVEL` for the API. `PUBLIC_BASE_URL` has to be set behind a proxy: the site
cannot infer its public origin from the address it binds, and a feed with the wrong
origin is a feed nobody can follow.
