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
| `Dynamic()` (the default) | Render every request, never serve from cache | `no-store` |
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
- the raw query string.

Every field is self-delimiting, so no two distinct inputs can collide by
concatenation, and the whole key is prefixed `v1:` so the scheme can change later
without colliding with keys made by this one.

Including the raw query string is a correctness decision with a cost: a data
handler receives the whole `*http.Request` and may legitimately render from
`r.URL.Query()`, so two queries against one path are two representations — but it
also fragments the cache across `utm_*` and other tracking variants, and it varies
on parameter order, since the query is not canonicalised. A per-page allowlist of
significant parameters would recover both; guessing which parameters matter is the
application's call, so it is deliberately not built in.

## ETags and conditional requests

Every cached entry stores the ETag its content hashed to, and the fresh response
advertises the value the *cache* stored rather than a locally recomputed one — so
a cache implementation that derives ETags its own way still answers conditional
requests consistently. A request whose `If-None-Match` matches gets a `304` with
no body and no `Content-Type`.

## `Vary`

On a publicly cacheable response the framework sends `Vary` built from the enabled
locale sources: `Accept-Language` when header-locale resolution is on, `Cookie`
when cookie-locale resolution is. collage's own key already carries the resolved
locale, but a shared cache between your server and the client — a CDN, a corporate
proxy — keys on the URL alone, and a locale negotiated from a header or a cookie
is not in the URL. Without `Vary`, such a cache hands one visitor's language to
the next.

Path-locale resolution contributes nothing to `Vary`: it is already in the URL.

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
client that cached it keeps the old copy until its `max-age` elapses — which is
what fingerprinted filenames exist to solve, and which this framework does not do
for you.

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
	Dir:     "/var/cache/mysite",
	Version: buildID,   // a git commit, a release tag, a build timestamp
}
```

`Version` is required, and the reason is the whole design. **A disk cache outlives
the process that filled it**, so without one a new binary serves HTML the old one
rendered — a changed template, a changed data handler, and a page nobody can
explain. Entries live under a subdirectory named for a hash of the version, so a
different version reads a different directory and finds nothing. There is no check
to forget and no sweep to schedule; the old directory simply stops being read.

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
