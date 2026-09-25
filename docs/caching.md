# Caching and invalidation

collage caches *rendered page output* and *document bodies*, keyed by request
identity and indexed by the dependency tags the render declared. Nothing is
cached until you turn it on:

```go
app, err := collage.New(&collage.Config{
	Template: collage.TemplateConfig{Root: "templates"},
	Cache: collage.CacheConfig{
		Enabled:    true,
		Type:       "memory",
		DefaultTTL:    5 * time.Minute,
		MaxEntries:    10000,
		MaxKeysPerTag: 10000,
	},
})
```

A disabled cache is a nil cache: every request renders and nothing is stored.

## Render strategies

| Builder call | Meaning | `Cache-Control` |
| --- | --- | --- |
| `Dynamic()` | Render every request, never serve from cache | `no-store` |
| `Static()` | Render once, serve from cache until explicitly invalidated | `public, max-age=0, must-revalidate` |
| `Incremental(ttl)` | Serve from cache until `ttl` elapses since the render | `public, max-age=<ttl seconds>` |

```go
page := collage.NewPage("blog-post").
	WithLayout(layout).
	WithContent(postContent).
	WithPath("en", "/blog/{slug}").
	Incremental(10 * time.Minute).
	WithDependency("blog:posts").
	Build()
```

### A page that declares none

A page that calls none of the three is resolved when it is registered:

- **Dynamic** if anything it renders fetches per render — a data handler
  (`WithDataHandler`, including `Load` and `Effect`) or a slot resolver, in its
  layout, its content, anything bound into their slots, their fallbacks, or a
  fragment opened with `WithFragmentPath`.
- **Static** otherwise: a page rendering templates and fixed values —
  `WithData(v)`, `WithTitle(s)` — renders the same for every reader.

A handler means dynamic because it may read the request, a cookie, the clock, and
nothing outside the function can tell whether it does. The guess costs a render
when it is wrong, never a reader served another's page. A handler whose output is
the same for everyone — a post read from a file — is a page that says `Static()`
or `Incremental(ttl)` itself.

A document resolves the same way: dynamic with a handler, static with a fixed
body (`WithBody`). A declared strategy is never second-guessed, in either
direction, and a registered page's `Strategy` is always the resolved one.

`Incremental` without a positive TTL is a registration error
(`collage.ErrMissingTTL`), not a page that silently never expires.

`Static()` means what it says: its cache entry is written with no practical
expiry, so `DefaultTTL` does not apply to it. Only `InvalidateTags`, or eviction
at `MaxEntries`, causes a static page to render again. If you want a page to
expire on a clock, that is `Incremental`.

Only `GET` and `HEAD` are ever served from cache, and a `HEAD` never populates it:
it produced no body to store.

[Documents](documents.md) use exactly this machinery: the same three strategies,
the same builder calls, the same key, the same ETag, the same tags. Only the body
is cached — a document's content type is static, read from the matched document
at serve time, so it never has to be stored beside the bytes and the `Cache`
interface did not change to accommodate documents at all.

## The cache key

The key is the SHA-256 of an unambiguous, length-prefixed serialisation of:

- the request path,
- the resolved locale,
- the captured path parameters,
- the raw query string — or only the parameters the page named with
  `WithCacheParams`, when it named any (see
  [below](#which-query-parameters-are-part-of-the-key)),
- any values middleware declared with `collage.Vary` (see [`Vary`](#vary)).

Every field is self-delimiting, so no two distinct inputs can collide by
concatenation, and the whole key is prefixed `v1:` so the scheme can change later
without colliding with keys made by this one.

Including the raw query string is a correctness decision with a cost: a data
handler receives the whole `*http.Request` and may legitimately render from
`r.URL.Query()`, so two queries against one path are two representations — but it
also fragments the cache across `utm_*` and other tracking variants, and it varies
on parameter order, since the query is not canonicalised. Guessing which parameters
matter is the application's call, so the framework does not guess: a page that
names its parameters with `WithCacheParams` recovers both, and one that names none
keeps the whole query in its key.

## ETags and conditional requests

Every cached entry stores the ETag its content hashed to, and the fresh response
advertises the value the *cache* stored rather than a locally recomputed one — so
a cache implementation that derives ETags its own way still answers conditional
requests consistently. A request whose `If-None-Match` matches gets a `304` with
no body and no `Content-Type`.

## `Vary`

The locale is in the URL, so a page needs no `Vary` for it: a shared cache already
tells `/about` from `/tr/hakkinda`.

A page whose content depends on a request header does need one, and the
application says so from middleware with `collage.Vary(r, header, value)`. The value
it resolved enters collage's own cache key, and the header name goes into the
response's `Vary` header, so a CDN or proxy between your server and the reader
keeps the versions apart too. See [http.md](http.md#collagevary-content-that-depends-on-the-request).

## Dependency tags

A tag is any string. Two sources contribute to a page's tags, and they are
unioned, de-duplicated, and sorted:

```go
// From the page:
WithDependency("blog:posts")

// From a data handler, per render:
return data, []string{"post:" + slug}, nil
```

The union is stored with the cache entry and recorded in the dependency tracker,
which maps tag → the keys built from it.

### The tracker is in-process, and bounded

Two properties of the tracker are worth knowing before you rely on tag
invalidation.

**It is per process, and in memory.** It records the keys *this* instance wrote,
and nothing else. Behind a load balancer with a shared store, instance A's
tracker cannot name the keys instance B wrote; after a restart, no tracker can
name anything written before it. The framework closes this as far as it can by
also calling `Cache.Invalidate(ctx, tags)` whenever your store implements
`collage.TaggedCache` — a store that indexes tags itself can resolve what the
tracker never saw — but with a store that does not, tag invalidation reaches only
what the running process happens to remember.

**It is bounded by `MaxKeysPerTag`.** Nothing removes a key from the tracker when
the cache evicts or expires it, and the cache key carries the request's query
string, so any client can mint unlimited distinct keys for one page. Without a
cap the tracker would grow without bound behind a cache that stays at
`MaxEntries`. When a tag reaches the cap, recording a new key under it drops the
oldest key recorded under that tag: the cache entry itself is untouched and keeps
being served until it expires, but `InvalidateTags` no longer reaches it. The
default is 10000, matching `MaxEntries`; a negative value means unlimited, which
is a deliberate choice to accept unbounded growth rather than ever drop.

## Invalidating

```go
// Fire and forget.
if err := app.InvalidateTags(ctx, "post:"+slug, "blog:posts"); err != nil {
	log.Printf("invalidate: %v", err)
}

// Or find out what it reached.
reached, err := app.InvalidateTagsN(ctx, "blog:posts")
```

`InvalidateTagsN` returns the number of keys the tags resolved to and the cache
accepted a removal for. It is not a count of entries that were live: invalidating
a key that holds nothing is not an error and reports no distinction, so the count
is an upper bound on live entries removed.

A key the cache fails to drop is not counted and does not stop the rest — every
other key is still invalidated and the failures are joined into the returned
error, because a partial invalidation that reports success is how stale pages
survive a deploy.

When your store implements `collage.TaggedCache`, `Cache.Invalidate(ctx, tags)` is
called as well as the per-key removals the tracker resolved. Entries it drops are
not included in the returned count — the `Cache` contract gives `Invalidate` no
count to report, and inventing one would make the number mean different things for
different caches.

Plugins observe invalidation through `OnCacheInvalidate`, and a plugin triggers
one through `Host.InvalidateTags`.

An action that asks for invalidation declaratively, through
`ActionResult.InvalidateTags`, has it run before its response is written — so a
redirect to a page built from what it changed is not answered from the old entry.
A failure there does not fail the action: the change the action made has already
happened, so the error is logged (`collage: action invalidation failed`, with the
action and the tags) and the response goes out as the handler asked. Call
`app.InvalidateTags` yourself from the handler when a failed invalidation should
change the answer.

## Caching data, not only pages

The page cache stores what a render produced. It does nothing for thirty different
pages that each fetch the same author: each is its own render, so each asks. An
export of those pages, which renders every one of them, asks thirty times.

`collage.Cached` stores what renders are made from:

```go
func authorCard(ctx context.Context, rc *collage.RenderContext) (Author, []string, error) {
	id := rc.Param("author")
	author, err := collage.Cached(rc, "author:"+id, time.Hour, []string{"author:" + id},
		func(ctx context.Context) (Author, error) { return api.Author(ctx, id) })
	return author, nil, err
}
```

Thirty pages by two authors now fetch twice, served or exported.

- **One set of tags for both caches.** The tags are added to the page's own, so
  `InvalidateTags("author:" + id)` drops the stored author *and* every cached page
  that showed them, together. There is no second cache to keep in step by hand — the
  thing that goes wrong when an application memoises in its own API client and then
  invalidates only the pages.
- **One fetch per key at a time.** Requests that need the same key while it is being
  fetched wait for that fetch rather than starting their own.
- **Errors are not stored**, and a fetch that was still running when its tags were
  invalidated hands its result to whoever was waiting but does not store it: what it
  brought back is what the invalidation was meant to replace.
- **Bounded and in-process.** Values are kept in memory, up to `Cache.MaxEntries`,
  least recently used first to go — unlike the page memory cache, which evicts in
  insertion order (see [below](#the-built-in-memory-cache)). Each instance of a multi-instance deployment keeps
  its own, so N instances ask N times — still once each rather than once per page.
- **Where it keeps nothing.** With `Cache.Enabled` false, in development, for a
  request that called `SkipCache`, and in an action's own handler, `Cached` behaves
  as `Once`: shared within the render, fetched fresh by the next. A preview therefore
  sees fresh data as well as a fresh page.

`ttl` bounds how long a value is kept when nothing invalidates it; zero keeps it
until something does. It is independent of the page's TTL — a page that renders
every minute can still reuse an author fetched an hour ago, which is the point.

## Concurrent misses render once

When a cached page expires, every request that arrives before the first re-render
finishes is a cache miss. Without anything in the way, each of them renders: the
same page, the same upstream calls, at the same moment — and the number of them
grows with traffic, which is the shape of an outage rather than of a slow page.

So the first request for a key renders and the rest wait for it. They are handed the
same bytes and each writes its own response. Nothing is configurable here and
nothing needs to be: it is how the handler serves a miss.

Two things follow that are worth knowing.

**Only cacheable pages coalesce.** A page declared `Dynamic()` has no cache key, and
two requests for it are two renders by the page's own declaration.

**A coalesced request is reported as its own cache event**, `CacheCoalesced`, rather
than as a hit or a miss. It is not a hit — nothing was cached when the request asked
— and calling it a miss would suggest it cost a render. Watch it: a count that
climbs steadily is a page expiring faster than it can be re-made, which is what a
too-short `Incremental` TTL looks like from the outside.

A request whose own connection goes away stops waiting. And a render that fails
because the *first* request was cancelled is not passed on to the requests behind
it — they try again — so one reader pressing stop cannot turn into an error page for
everyone who happened to ask at the same moment.

## What is never cached

- **A degraded render.** If any fragment failed — even one a fallback covered for
  — the output is served but not stored. Caching it would pin one request's
  transient failure in front of every later request.
- **Any error response.** 404s and 500s are written `no-store`, and a rendered
  error page's own dependency tags are dropped. That includes a document's
  plain-text failure and an asset mount's plain-text 404.
- **Anything but `GET`.** An unsafe method's response is never a cached page.
- **A mounted asset.** See below.

## Mounted assets never enter the page cache

A file served from an [asset mount](assets.md) is not stored by `collage.Cache`,
not keyed by the cache key above, not tagged, and not reachable by
`InvalidateTags`. `MaxEntries` does not apply to it and it cannot evict a page.

That separation is the whole reason assets are a separate mechanism rather than a
`Document` returning file bytes, and it follows from two properties of this
cache. It is **in memory and bounded by entry count, not by bytes**: one 50 MB zip
stored in it would displace thousands of pages under a `MaxEntries` that was
chosen for pages. And its ETag is a **content hash of the whole body**, which is
the right thing for a rendered page and not viable to recompute over a 500 MB
file on every request.

Mounted files are served with `http.ServeContent` instead, which brings `Range`,
`If-Range`, `206` and `Last-Modified` with it, and with a lazily computed,
memoised content-hash ETag — the hash alone is remembered, never the body, so the
memory cost is bounded by file count.

**The freshness of a mounted file is the mount's `Cache-Control` and the client's
business, not the framework's.** There is no server-side entry to expire and no
invalidation call that reaches one. If a file changes and its URL does not, every
client that cached it keeps the old copy until its `max-age` elapses. That is what
content-addressed URLs solve, and the framework does give you those: link a file
with `{{asset "/static/app.css"}}` (or `rc.Asset`) and the URL carries a hash of
the file's contents, served `immutable`, so a changed file is a new URL — see
[assets](assets.md). A file linked by its plain path gets no such help.

## Adjusting a write from a plugin

```go
// OnCacheWrite may extend the TTL, add tags, or suppress the write entirely.
func (p *myPlugin) OnCacheWrite(ctx context.Context, ev *collage.CacheWriteEvent) error {
	if ev.Page.Name == "home" {
		ev.TTL = time.Minute
		ev.Tags = append(ev.Tags, "homepage")
	}
	return nil
}
```

A failure in the cache-write path never fails the request: the page has already
rendered, and serving it uncached beats turning a cache problem into a 500.

Note that the `Cache-Control` header is derived from the page, not from a TTL a
hook adjusted, so the value a client sees does not change from request to request.

## The built-in memory cache

`Type: "memory"` is a process-local `map` with:

- **FIFO eviction** at `MaxEntries` — oldest by insertion, *not* least recently
  used. Reading an entry does not make it younger.
- **Lazy expiry** — an expired entry is dropped when it is next looked up.
- `MaxEntries: 0` means the default (10000); a negative value means unlimited.

The same `MaxEntries` bounds the `collage.Cached` data store, which evicts the
other way: least recently used, so a value read on every page stays. Two caches,
one number — an application that sizes it sizes both, each counted separately. A
`"disk"` cache is not bounded by it; its entries leave by expiry and
invalidation.

## Supplying your own cache

Set `CacheConfig.Store`. A non-nil `Store` is used exactly as given, and
`Cache.Type` is then ignored (including by validation). `Enabled` stays the master
switch: a `Store` on a disabled cache is not silently turned on.

> **Do not assign a nil pointer to `Store`.** A nil `*myCache` assigned to a
> `Cache`-typed field is *not* a nil interface — it is a non-nil interface holding
> a nil pointer, and `store != nil` is true for it. The framework cannot tell that
> apart from a real implementation without reflection, which it does not use, so
> it will call straight through and panic on the first cache lookup. This bites
> most often via a constructor that returns a concrete pointer type:
>
> ```go
> func newRedisCache(c *redis.Client) *redisCache { ... } // may return nil
>
> Cache: collage.CacheConfig{Enabled: true, Store: newRedisCache(client)} // nil is now "non-nil"
> ```
>
> Leave the field unset when you have no cache, or check for nil before assigning.

```go
app, err := collage.New(&collage.Config{
	Template: collage.TemplateConfig{Root: "templates"},
	Cache: collage.CacheConfig{
		Enabled:    true,
		Store:      newRedisCache(client),
		DefaultTTL: 5 * time.Minute,
	},
})
```

The interface is `collage.Cache`:

```go
type Cache interface {
	Get(ctx context.Context, key string) (content []byte, etag string, found bool)
	Set(ctx context.Context, key string, content []byte, ttl time.Duration) (etag string, err error)
	Invalidate(ctx context.Context, tags []string) error
	InvalidateKey(ctx context.Context, key string) error
	Clear(ctx context.Context) error
}
```

The contract:

- `Get` reports `found == false` for an expired entry, and callers must not mutate
  the returned slice.
- `Set` returns the ETag the entry is stored under; that value is what the fresh
  response advertises, so it must be the same one `Get` will report later.
  `collage.ETag(content)` computes the framework's own if you have no reason to
  derive your own.
- A `ttl <= 0` means "use your own default".
- The only defined failure mode is context cancellation.
- It is called from request goroutines, so it must be safe for concurrent use.

A minimal implementation, using nothing but the public API:

```go
// memoStore is a Cache over a map, for illustration.
type memoStore struct {
	mu      sync.Mutex
	entries map[string][]byte
}

// Get returns the stored bytes for key, if any.
func (s *memoStore) Get(_ context.Context, key string) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	content, ok := s.entries[key]
	if !ok {
		return nil, "", false
	}
	return content, collage.ETag(content), true
}

// Set stores content under key and returns its ETag.
func (s *memoStore) Set(_ context.Context, key string, content []byte, _ time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[key] = content
	return collage.ETag(content), nil
}

// Invalidate leaves tag resolution to the framework's tracker.
func (s *memoStore) Invalidate(_ context.Context, _ []string) error { return nil }

// InvalidateKey removes one entry.
func (s *memoStore) InvalidateKey(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.entries, key)
	return nil
}

// Clear removes every entry.
func (s *memoStore) Clear(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = make(map[string][]byte)
	return nil
}

var _ collage.Cache = (*memoStore)(nil)
```

### Tag-aware writes

Implement `collage.TaggedCache` as well when your store can index tags at write
time. The framework type-asserts for it and calls `SetTagged` in place of `Set`:

```go
// SetTagged stores content under key and indexes it under each of tags.
func (s *memoStore) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	// ... index tags, then store as Set does.
	return s.Set(ctx, key, content, ttl)
}

var _ collage.TaggedCache = (*memoStore)(nil)
```

The framework's own dependency tracker keeps working either way, and remains the
authority for resolving tags to keys: the two indexes are deliberately redundant.

## Which query parameters are part of the key

By default, the whole raw query string is a cache dimension. That is the
conservative reading and it is correct: a data handler receives the whole request
and may render from `r.URL.Query()`, so the framework cannot know which parameters
matter without being told.

It is also expensive. A newsletter link carrying `?utm_source=` caches a second copy
of the page, and a crawler walking variants evicts real entries from a bounded cache
without ever asking for a distinct page. Say which parameters the page reads:

```go
collage.NewPage("listing").
	WithPath("en", "/articles").
	WithCacheParams("page", "sort").
	Incremental(time.Minute)
```

Naming none at all drops the query from the key entirely — how a page states that it
renders the same whatever the query says:

```go
WithCacheParams()
```

Naming a parameter the page does not read is harmless. Failing to name one it does
read is not: two representations then share an entry, and one visitor is served
another's page.

Declaring an allowlist also canonicalises what survives, so `?page=2&sort=new` and
`?sort=new&page=2` stop being two entries for one representation. That is only safe
once the page has said which parameters matter, which is why it is not the default.

An allowlist does not make every page cacheable. A search page's discriminating
parameter is the search term, whose values are chosen by whoever is asking — keying
a cache on that is an eviction attack with a text field for a trigger. Such a page
wants `Dynamic()`.

`DocumentBuilder.WithCacheParams` is the same knob for documents.

## Keeping the cache across restarts

The default cache is in memory, so a restart renders everything again. A disk cache
keeps it:

```go
Cache: collage.CacheConfig{
	Enabled: true,
	Type:    "disk",
	Dir:     ".cache/pages",
}
```

`Dir` has no default, because a framework that picks a place to write files is a
framework that writes them somewhere nobody looked. An application that wants the
build-tool convention says `.cache/pages` and adds a line to `.gitignore`.

A directory is all it takes. **A disk cache outlives the process that filled it**, so
something has to stop a new binary serving HTML the old one rendered — a changed
template, a changed handler, and a page nobody can explain. Entries therefore live
under a subdirectory named for the build, and `Version` names it.

Leave `Version` empty and it is a hash of the running executable, which changes
exactly when the rendered output might. Two runs of an unchanged program derive the
same value — including under `go run`, whose build cache hands back the same binary
— and so does every machine in a fleet running the same build, so they share a
cache. It costs one to two milliseconds at startup.

Set it when something outside the binary decides what the output looks like: a
content revision, a configuration digest. Not to identify the build, which the
framework does better than a string anyone has to remember to update.

Not the VCS revision from `debug.ReadBuildInfo`, for what it is worth: `go run`
usually omits it, and it says nothing about uncommitted edits — which are exactly
the edits a developer is looking at when a page comes back stale.

**The namespace is the build, and only the build.** A stored page carries a marker
where each reader's token goes, derived from `Security.CSRFKey`, so a page stored
under one key cannot be served under another — its form would carry a marker
nothing replaces and be refused on submission. That is checked on every hit
instead: a stored body carrying another key's marker (or any marker, with forgery
protection off) is a miss, dropped and rendered again. Pages without a form carry
no marker and survive a key change, and a restart of a site with no key — which
generates a new one every run — no longer starts the cache empty.

Two consequences worth knowing before they surprise you:

- **Everything that shares a `Dir` and a build shares entries** — two
  `App`s in one process included. A test that builds a fresh application per test
  reads what the previous test, or the previous `go test` run, rendered. Give each
  its own `Dir` (a test's `t.TempDir()`) or its own `Version`; the scaffolded
  project's tests do the first.
- **A cached page outlives every process-local decision that shaped it.** A plugin
  that rewrites HTML to point at something only its own process remembers — a name
  it made up, a file it has not written yet — must keep that state as durably as the
  cache keeps the HTML, or a restart serves pages that refer to what no longer
  exists. `collage-opti-image` writes its recipes to disk for this reason.

**A cached page is never served in development.** `Config.DevMode` or
`Template.DevMode` turns off cache *lookups*: every request renders again. Templates
reload from disk in development, and a cached page hides that reload for as long as
its TTL — on exactly the pages someone is most likely to be editing. The write path
is untouched, so entries are still stored, tags still tracked and `CacheWrite` hooks
still fire; what dev mode removes is serving a page that was rendered before the
edit.

**A disk cache is never used in development.** `Config.DevMode` or
`Template.DevMode` substitutes an in-memory one and logs that it did. Development is
exactly where the output changes between runs, and nobody bumps a version to save a
file — the version guard catches a released build, not a developer.

Each entry is one file: a JSON header line carrying the ETag, the expiry and the
dependency tags, then the content. Written through a temporary file and a rename, so
a reader sees either the whole previous entry or the whole new one. There is no
separate tag index — `Invalidate` reads the headers instead, which costs one small
read per entry on a call that happens when content is published, and buys having no
second structure that can disagree with the first or be left behind by a crash.

Keys are hashed before they become filenames. `Cache` is a public interface and a
caller can store under anything, so no key can name a path, a parent directory, or a
filename longer than the filesystem accepts.
