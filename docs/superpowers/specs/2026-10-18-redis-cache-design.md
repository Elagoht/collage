# Shared cache for a fleet: data store interface, conformance kit, collage-redis

Status: designed 2026-10-18, awaiting approval. Target: collage v0.66.0 and a new
`Elagoht/collage-redis` v0.1.0.

## Why

collage caches well on one instance (memory, or disk with memory in front), but a
fleet behind a load balancer has no good answer today:

- `Cache.Store` accepts a custom page cache, and a shared Redis written against
  `Cache` + `TaggedCache` + `StaleCache` keeps pages consistent across instances.
  But nobody ships one: every user writes Redis code, Lua and tag indexing by hand,
  and there is no way to check they got the contract right.
- The `collage.Cached` data store is always per instance (`internal/core/cache.go`
  `buildDataCache`; in memory, plus `Dir/data/` for a disk cache). A webhook
  reaches one instance through the load balancer; that instance marks its own data
  stale, the others keep theirs fresh until its TTL. Instance B can then render a
  page from its old data and write it into the shared page cache as fresh, where it
  stays until the next invalidation. The window is the time until A's warm render
  lands, and it is unbounded for pages the warmer skips (warming off, past
  `WarmLimit`, a guarded page, a key A's tracker never saw).

The user wants Redis to be trivial: "add it, give it a Redis URL, the rest works".

## Decisions (the user's)

- **The user wires it in `Cache.Store`.** No plugin hook that installs a cache from
  `Configure`; no change to `ConfigHost`. The app writes:

  ```go
  store, err := redis.New(redis.Config{URL: os.Getenv("REDIS_URL")})
  // ...
  Cache: collage.CacheConfig{Enabled: true, Store: store},
  ```

- **`collage.Cached` uses the Store when the Store offers it.** A new optional
  interface in core, discovered by type assertion exactly like `TaggedCache` and
  `StaleCache`. Additive; nothing existing changes behaviour. With it, the data
  and the pages live in the same shared place, so no cross-instance broadcast is
  needed.
- **Every deploy starts with an empty Redis cache.** Old and new builds in a
  rolling deploy must not read each other's pages or data.
- **An overlapped render or fetch is never stored fresh, fleet-wide.** A render or
  data fetch on B that an invalidation on A overlapped lands stale, as it already
  does within one process (Part 1b).
- **The store bounds itself by age.** Every Redis key gets a maximum lifetime,
  stale and `Static` entries included; Redis is to run with `noeviction` (Part 3).
- **Invalidation reports the whole fleet's pages.** Warming and
  `CacheInvalidateEvent.Entries` (CDN purge, deploy adapter) see the pages any
  instance wrote, not only the invalidating instance's (Part 1c).
- **Out of scope:** a disk-only cache type, S3/NFS/any shared filesystem as a
  cache, pub/sub invalidation broadcast, a separate memory-tier size knob.

## Goals

1. A fleet sharing one Redis serves consistent pages *and* data after an
   invalidation on any one instance.
2. Setting it up is one constructor call with a URL and one `Cache.Store` line.
3. Anyone writing their own Store (Redis or not) can prove it meets the contract
   with one test call.

## Non-goals and anti-goals

- No per-instance memory copy in front of the shared store, page or data. That is
  the exact inconsistency this work removes; a faster but inconsistent cache is a
  failure even if benchmarks look good.
- Redis being down must never take the site down. A Redis failure is a cache miss
  (render, log, serve), as an unusable disk dir already falls back today.
- No new required configuration in core. An app that never sets `Cache.Store`
  sees no difference.

## Part 1 — core: a data store a Cache can provide (collage v0.66.0)

Add an optional interface (name is the builder's; `DataCache` is a suggestion),
exported from `pkg/collage` as an alias like the other cache interfaces. When
`Cache.Store` implements it, `buildDataCache` builds a `datacache.Store` backed by
it instead of process memory / `Dir/data/`.

What stays in process, unchanged: the per-key single fetch (concurrent loads of one
key wait for one fetch — per instance is fine), background refresh of stale values,
the failure cooldown (1s doubling to 1min), `ErrNotFound` dropping the value,
errors never stored, `PurgeTags` vs `InvalidateTags` semantics, stale renders
served `no-store` and marked degraded, export/`RenderFragment` waiting for fresh
data, the `CacheStale` metric and logs.

What moves to the backend: where entries live. The backend is byte-level — the
store already encodes values as JSON for the disk (`decode func([]byte)` on
`Load`), so values cross the interface as `[]byte` plus TTL/expiry, tags, and the
stale mark. It needs at least: get (value, stale, found), set with ttl and tags
(clears stale), mark stale by tags, delete by tags (purge), delete one key (for
`ErrNotFound`). The builder decides the exact method set from what
`internal/datacache/store.go` actually needs.

Constraints:

- **No in-process value cache in front of the backend.** Each `Load` reads the
  backend. The cost is one round trip and one JSON decode per load; that is the
  price of consistency and it is accepted. (Within one render, `Cached` values are
  already shared, so a render does not read the same key twice.)
- **The epoch/purge race rules must hold across instances** — see Part 1b. Today a
  fetch that started before an invalidation is stored stale, and one overtaken by
  a purge is not stored; those counters are in-process. With the backend, both
  rules hold for an invalidation or purge on any instance.
- **Values JSON can't round-trip** (unexported fields, `json:"-"`, interfaces —
  the existing disk rule) cannot be stored remotely. With a shared backend they
  are not cached across renders at all (behave like `Once`), and the existing
  one-log-per-type warning says so. Keeping them in local memory would bring back
  per-instance inconsistency.
- **Backend errors are misses.** A failed backend read fetches fresh; a failed
  write logs and still returns the fetched value.
- `InvalidateTags` / `PurgeTags` call the backend once per call, not once per key.
- Docs: `docs/caching.md` "Caching data, not only pages" and "Supplying your own
  cache" gain the new interface; the multi-instance warnings point to it.

## Part 1b — core: overlap guard across instances (user's decision)

Today `httpx.Handler.writeCache` declines to store a render an invalidation
overlapped (`invalidationsStarted`/`invalidationsFinished` in
`internal/core/app.go`), and `datacache` stores an overlapped fetch stale (its
`epoch`). Both are per process, so B cannot see A's invalidation. A page B wrote
fresh after A's invalidation would stay until the next one — forever for a
`Static` page.

Add an optional interface (pages and data both use it; may be one interface or
two, builder's call) backed by a **per-tag generation** in the store:

- Before a render or data fetch, core reads a token covering the tags it will be
  written with. For a page, the tags are known only after rendering (dependency
  tags gathered from fragments), so the token must still be checkable at write
  time: e.g. read the generations of the page's declared tags up front and the
  store re-checks the full tag set's generations at write, treating any tag whose
  generation advanced after the render started as overlapped. The builder picks
  the exact scheme; the requirement is that an invalidation on any instance that
  started after the render started makes the write land **stale** (not dropped —
  it is still the best copy for stale fallback), and a purge makes it **not
  stored**.
- `InvalidateTags` bumps the generation of each tag; `PurgeTags` bumps a purge
  generation. Atomically with the mark-stale / delete (one Lua script in Redis).
- Stores without the interface keep today's in-process guard, unchanged.
- The conformance kit tests it with two handles: start on handle 1, invalidate on
  handle 2, write on handle 1 → stale; purge on 2 → not stored.

## Part 1c — core: fleet-wide reach for warming and plugin events (user's decision)

`InvalidateTagsN` resolves the pages an invalidation reached from this instance's
in-memory tracker. That list drives warming, the returned count and
`plugin.CacheInvalidateEvent.Entries`, which `collage-cdnpurge` and the deploy
adapter act on. A page only B served would never be purged from a CDN.

Add an optional interface by which the store answers "which entries do these tags
cover" from its own tag index, returning what the event and warmer need per entry
(the builder reads what `Entries` and the warmer use today — at least the request
identity to re-render and the path/host to purge — and has the store keep that
with each tagged write). `InvalidateTags` merges the store's answer with the
tracker's, deduplicated. The merged set is bounded by `WarmLimit` for warming as
today; events get all of it. Ideally this is the same call that marks stale and
bumps generations, so one round trip does all three.

## Part 2 — conformance kit (collage v0.66.0)

In `pkg/collagetest` (the user-facing test helpers), something like
`collagetest.CacheSuite(t, func(t *testing.T) collage.Cache)`. The factory returns
a fresh, empty store per subtest. The suite type-asserts and runs the sections that
apply:

- `Cache`: get/set/etag round trip, TTL expiry (with real short TTLs; the suite
  must not need a fake clock the store can't accept), `InvalidateKey`, `Clear`,
  returned content not aliased to caller input.
- `TaggedCache`: `SetTagged` + `Invalidate` by any of several tags, keys with no
  tag untouched.
- `StaleCache`: mark stale by key and by tag, `Get` misses but `GetStale` hits
  with stale=true, a fresh write clears the mark, content/ETag unchanged by marking.
- The new data interface: everything Part 1 lists, including the purge race.
- **Two handles, one store.** The factory may optionally return a second handle to
  the same backing store (e.g. a second Redis client); when it does, the suite
  checks that a write, invalidation or purge through one is seen through the other.
  This is the property a fleet relies on and the in-memory stores can't show.
- Concurrency: parallel set/get/invalidate under `-race` without errors.

The built-in memory and tiered caches run it in core's own tests, which both
checks the kit and pins the built-ins' behaviour.

## Part 3 — `Elagoht/collage-redis` v0.1.0

A **library**, not a registered plugin — like `collage-paginate`: no `Plugin` type,
no `collage.json`, nothing for `app.Register`. Module
`github.com/Elagoht/collage-redis`, package `redis`, `go 1.26`, the other plugin-repo
conventions (`Version()`, README `# elagoht/redis`, tags `v0.x.y`). Added to
collage's CI matrix like the other libraries.

API (shape is the builder's within this):

- `redis.New(redis.Config{URL: "..."}) (*Store, error)` — URL in the standard
  `redis://` / `rediss://` form (password, db, TLS from the URL). Optional fields
  only for what a URL can't say: an existing client to reuse, a key prefix
  (default `collage:`), `Version` (below), timeouts.
- `*Store` implements `collage.Cache`, `TaggedCache`, `StaleCache` and the Part 1
  data interface. `Close()` closes a client it created.
- Client library: `github.com/redis/go-redis/v9` (my call).
- **Atomic writes.** A write with tags, a mark-stale by tags and a delete by tags
  each happen as one Lua script, so a concurrent writer and invalidator can't leave
  a page fresh that was invalidated, or a tag index pointing at nothing it should.
- **Fresh start per deploy (user's decision).** Every key lives under
  `<prefix><version>:`. `Version` defaults to a hash of the running executable,
  the same rule the disk cache uses (`internal/core/cache.go` `buildFingerprint`;
  the library computes it itself, since that function is internal). Two instances
  of one build share; a new build reads an empty namespace. No explicit namespace
  cleanup: the maximum age below expires an old build's keys on its own, and never
  deletes what an old instance in a rolling deploy is still serving from.
- **Bounded by age (user's decision).** Every key — page, data, stale or not,
  `Static` included, and the tag indexes and generations — gets a Redis TTL of at
  most `MaxAge` (config field, default 7 days). An entry's own TTL is
  `min(its TTL, MaxAge)` for freshness, but the key itself lives `MaxAge` so a
  stale copy stays available for fallback; a rewrite resets it. Tag-index and
  generation keys get their TTL extended to cover the longest-lived member, so an
  index never expires before the entries it points at. README says: run Redis
  with `maxmemory-policy noeviction` and size it for about `MaxAge` of writes; a
  full Redis then fails writes (logged, served as misses) rather than silently
  breaking an index.
- **Redis down = miss.** Commands get short timeouts; failures are logged
  (rate-limited, not once per request) and treated as a miss / failed write. The
  site keeps rendering.
- ETags: `collage.ETag(content)` unless there is a reason not to; stored with the
  entry so every instance serves the same one.
- Tests: the Part 2 kit, with two handles on one server. Default CI uses an
  in-process Redis (`github.com/alicebob/miniredis/v2`, which runs Lua); a real
  Redis integration run when `REDIS_URL` is set. My call.
- README: the three-line setup above, what is shared, what still is per instance
  (single fetch per key, warming — see below), and that every deploy starts cold.

## Known behaviour to document, not fix

- **The invalidating instance does all the warming** for the fleet's pages
  (Part 1c), within its `WarmLimit`.
- **`MaxAge` bounds stale fallback**: after 7 days (default) without a successful
  re-render a page or value is gone, even during an outage.
- **A deploy starts cold** across the fleet, by decision.
- **Single fetch per key is per instance**: N instances missing one key at once
  make up to N upstream fetches.

## Release

Core first (v0.66.0: interface, `buildDataCache` wiring, kit, docs, changelog),
then collage-redis v0.1.0 against the tag, then the docs site (EN + TR) gets the
fleet section and a collage-redis page. Run staticcheck v0.7.0 and govulncheck
before tagging. Nothing is tagged or pushed without the user's go-ahead.

## Left to the builder

Interface and method names, and whether 1, 1b and 1c are separate interfaces; the
data backend's exact methods; the generation-token scheme within Part 1b's
requirement; what per-entry identity 1c stores; Lua script layout and Redis key
schema; log rate limiting; how the kit structures subtests.

## Calls made by Claude, not the user

- `go-redis/v9` as the client; miniredis for default CI, real Redis when
  `REDIS_URL` is set.
- collage-redis is a library (no `Plugin`, no `collage.json`), like
  collage-paginate.
- Key prefix `collage:`; namespace version = executable hash, as the disk cache.
- `MaxAge` default 7 days.
- JSON-unencodable data values are not cached across renders with a shared
  backend (no local fallback).
- An overlapped write lands stale rather than being dropped.
