# Caching and invalidation

collage caches *rendered page output*, keyed by request identity and indexed by
the dependency tags the render declared. Nothing is cached until you turn it on:

```go
app, err := collage.New(&collage.Config{
	Template: collage.TemplateConfig{Root: "templates"},
	Cache: collage.CacheConfig{
		Enabled:    true,
		Type:       "memory",
		DefaultTTL: 5 * time.Minute,
		MaxEntries: 10000,
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

Only `GET` and `HEAD` are ever served from cache, and a `HEAD` never populates it:
it produced no body to store.

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

Plugins observe invalidation through `OnCacheInvalidate`, and a plugin triggers
one through `Host.InvalidateTags`.

## What is never cached

- **A degraded render.** If any fragment failed — even one a fallback covered for
  — the output is served but not stored. Caching it would pin one request's
  transient failure in front of every later request.
- **Any error response.** 404s and 500s are written `no-store`, and a rendered
  error page's own dependency tags are dropped.
- **Anything but `GET`.** An unsafe method's response is never a cached page.

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
