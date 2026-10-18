# Shared Cache for a Fleet — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A fleet sharing one Redis serves consistent pages and `collage.Cached` data after an invalidation on any instance, set up with `redis.New(redis.Config{URL: ...})` in `Cache.Store`.

**Architecture:** Core gains one optional interface, `SharedCache` (a `TaggedCache` + `StaleCache` that also keeps a global epoch with per-tag generations, writes conditionally on them, drops by tag in one step returning what it reached, and stores data values). The page handler, the data store and `dropTags` use it when `Cache.Store` implements it. `collagetest` gets a reference in-memory `SharedCache` and a conformance suite. `Elagoht/collage-redis` implements it with go-redis and Lua.

**Tech Stack:** Go 1.26, `github.com/redis/go-redis/v9`, `github.com/alicebob/miniredis/v2` (tests).

**Spec:** `docs/superpowers/specs/2026-10-18-redis-cache-design.md`

## Global Constraints

- Core work on branch `feat/shared-cache` in `~/Desktop/collage`; target collage v0.66.0. Nothing is tagged or pushed without the user's go-ahead.
- Additive only: an app that never sets `Cache.Store`, or sets a store that is not a `SharedCache`, behaves exactly as today. Existing tests must pass unchanged.
- Never use the type `any` in new code (user rule); the one existing `any` exception pattern (`// any: ...` comment) is not to be copied.
- No per-instance copy of shared pages or data in front of a `SharedCache`.
- A `SharedCache` error is a miss or a skipped write, never a failed request.
- collage-redis is a library (no `Plugin`, no `collage.json`), module `github.com/Elagoht/collage-redis`, package `redis`, `go 1.26`, `Version()`, README `# elagoht/redis`, tags `v0.x.y`, no LICENSE.
- collage-redis defaults: key prefix `collage:`, namespace version = sha256 of the running executable (same rule as `internal/core/cache.go` `buildFingerprint`), `MaxAge` 7 days.
- Before any tag: `go test -race -count=1 ./...`, `gofmt -l`, `go vet`, `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`, `govulncheck ./...`.
- Run tests on their own and read the result; never `go test … | tail && git tag`.
- Read `git status` before every commit; add files by name.

## Review Focus

- **Redis down at startup or mid-run:** `redis.New` with a valid URL and no server returns a store, and the site serves 200s (uncached) with a rate-limited warning — tested in Task 9.
- **Redis flushed or restarted mid-run:** the epoch must not restart below values in-flight renders hold, or an overlapped write lands fresh. The epoch key is initialised to the current Unix time in milliseconds when missing (`SET NX`), then `INCR`ed — tested in Task 8 (flush between `Epoch` and `DropTags`, write lands stale).
- **Wide invalidation:** one `DropTags` reaching 5,000 pages completes and returns all 5,000 — tested in Task 3 (suite) and run against Redis in Task 8.
- **Rolling deploy:** two stores with `Version` "a" and "b" on one Redis see none of each other's pages or data — tested in Task 9.
- **Data value JSON cannot round-trip** (unexported field) with a shared backend: each load fetches, one warning per type, no panic, no stored value — tested in Task 4.

---

### Task 1: The `SharedCache` interface

**Files:**
- Create: `internal/cache/shared.go`
- Modify: `pkg/collage/collage.go` (next to the `Cache`/`TaggedCache`/`StaleCache` aliases, ~line 428-442)
- Test: `pkg/collage/collage_test.go` (compile-time assertion only)

**Interfaces:**
- Produces (in `internal/cache`, aliased in `pkg/collage` under the same names):

```go
// WriteOutcome is what a conditional write did.
type WriteOutcome int

const (
	WriteFresh   WriteOutcome = iota // stored as a fresh entry
	WriteStale                       // stored, but marked stale: a tag was invalidated after since
	WriteSkipped                     // not stored: a tag was purged after since
)

// Reached is one page entry a DropTags reached, with the meta it was written with.
type Reached struct {
	Key  string
	Meta []byte
}

type SharedCache interface {
	TaggedCache
	StaleCache
	Epoch(ctx context.Context) (uint64, error)
	SetSince(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string, meta []byte, since uint64) (etag string, outcome WriteOutcome, err error)
	DropTags(ctx context.Context, tags []string, purge bool) ([]Reached, error)
	GetData(ctx context.Context, key string) (value []byte, stale, found bool, err error)
	SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (WriteOutcome, error)
	DeleteData(ctx context.Context, key string) error
}
```

Doc comments carry the contract the suite (Task 3) tests, one sentence per rule:
- `Epoch` only grows, including across a restart or flush of the backing store.
- `DropTags` advances the epoch once, sets each tag's invalidation generation (purge false) or purge generation (purge true) to the new epoch, marks stale (or removes) every page **and data** entry under any tag, and returns the page entries reached (data entries are not returned) — as one atomic step.
- `SetSince`: `WriteSkipped` if any tag's purge generation > since; else `WriteStale` if any tag's invalidation generation > since; else `WriteFresh`. ttl ≤ 0 is the default TTL, as `Set`.
- `GetData`: an expired value is found and stale; ttl 0 on `SetData` means no expiry. `DeleteData` of a missing key is not an error.
- Keys and tags are opaque; the store owns its own namespacing.

- [ ] **Step 1:** Add `TestSharedCacheAliases` to `collage_test.go`: `var _ collage.StaleCache = collage.SharedCache(nil)`, `var _ collage.TaggedCache = collage.SharedCache(nil)`, and assert `collage.WriteFresh`, `collage.WriteStale`, `collage.WriteSkipped` are distinct; run `go test ./pkg/collage -run XXX` → FAIL (undefined).
- [ ] **Step 2:** Write `internal/cache/shared.go` and the aliases (`type SharedCache = cache.SharedCache`, `type Reached = cache.Reached`, `type WriteOutcome = cache.WriteOutcome`, consts re-declared as `const WriteFresh = cache.WriteFresh` etc.), each with a doc comment in the style of the neighbouring aliases.
- [ ] **Step 3:** `go build ./... && go test ./pkg/collage -run XXX` → PASS.
- [ ] **Step 4:** Commit `feat(cache): SharedCache, a store a fleet shares`.

---

### Task 2: Reference in-memory `SharedCache`

It lives in `internal/cache` so `internal/datacache` and `internal/httpx` tests can use it (both may import `internal/cache`: it imports no internal package), and `collagetest` re-exports it for users.

**Files:**
- Create: `internal/cache/sharedmem.go`
- Create: `pkg/collagetest/sharedcache.go`
- Test: `internal/cache/sharedmem_test.go` (direct behaviour tests; the full suite runs on it in Task 3)

**Interfaces:**
- Consumes: Task 1.
- Produces: `func cache.NewSharedMemory() *SharedMemory` — implements `SharedCache`, safe for concurrent use, so two `App`s given the same pointer behave as two instances on one Redis. `func (c *SharedMemory) Pages() int`, `func (c *SharedMemory) Data() int` (entry counts, for tests). ETag = `ETag(content)`. Expiry uses real time. In collagetest: `func NewSharedCache() collage.SharedCache` returning it.

- [ ] **Step 1: Failing tests** `TestSharedMemory_SetSinceAfterInvalidateIsStale` (epoch e := Epoch; DropTags([t], false); SetSince(k, …, [t], nil, e) → `WriteStale`; Get(k) not found; GetStale(k) found, stale) and `TestSharedMemory_SetSinceAfterPurgeIsSkipped` (same with purge → `WriteSkipped`, GetStale not found), `TestSharedMemory_DropTagsReturnsMeta` (SetSince with meta `[]byte("m")` → DropTags returns `[]Reached{{Key: k, Meta: []byte("m")}}`; a data key under the same tag is stale but not in the result).
- [ ] **Step 2:** `go test -race ./internal/cache -run TestSharedMemory` → FAIL.
- [ ] **Step 3:** Implement with one mutex, maps for pages, data, tag→keys index (pages and data separately), per-tag invalidation and purge generations, and an epoch starting at 1.
- [ ] **Step 4:** Same command → PASS.
- [ ] **Step 5:** Commit `feat(collagetest): an in-memory SharedCache`.

---

### Task 3: Conformance suites

**Files:**
- Create: `pkg/collagetest/cachesuite.go`
- Create: `internal/cache/suite_test.go` (package `cache_test`, which may import `collagetest`: runs `CacheSuite` on `NewMemory`, `NewTiered(NewMemory, NewDisk(t.TempDir()))`, and `SharedCacheSuite` on `NewSharedMemory`, pair = same pointer twice)

**Interfaces:**
- Consumes: Tasks 1-2.
- Produces:

```go
func CacheSuite(t *testing.T, newCache func(t *testing.T) collage.Cache)
func SharedCacheSuite(t *testing.T, newPair func(t *testing.T) (a, b collage.SharedCache))
```

`CacheSuite` runs `t.Run` sections: `Cache` always; `TaggedCache` and `StaleCache` when the store implements them. `SharedCacheSuite` runs `CacheSuite` on `a`, then the shared sections, each writing through `a` and checking through `b` (and the reverse where it matters). Each subtest calls the factory for a fresh, empty store. TTL checks use a 50ms TTL and poll every 10ms up to 2s.

Required subtests (names and assertions):
- `Cache/RoundTrip`: Set returns etag; Get returns same bytes and etag. `Cache/NotAliased`: mutate the slice passed to Set after the call → Get unchanged. `Cache/TTLExpires`, `Cache/InvalidateKey`, `Cache/InvalidateMissingKey` (nil error), `Cache/Clear`.
- `Tagged/InvalidateAnyTag`: k1 [a,b], k2 [b], k3 [c]; Invalidate([b]) → k1,k2 gone, k3 present.
- `Stale/MarkKey`, `Stale/MarkTags`: Get misses, GetStale returns same content and etag with stale=true; `Stale/FreshWriteClears`.
- `Shared/EpochGrows`: Epoch before < after DropTags, seen through b.
- `Shared/OverlapStale`, `Shared/OverlapPurgeSkipped`: Epoch on a; DropTags on b; SetSince on a → `WriteStale` / `WriteSkipped`.
- `Shared/UnrelatedTagFresh`: DropTags([x]) on b after since → SetSince with [y] → `WriteFresh`.
- `Shared/DropReachesOtherHandle`: SetSince on a with meta; DropTags on b returns it with meta; Get through a misses; GetStale through a hits.
- `Shared/PurgeRemoves`: purge → GetStale through a not found; data under the tag `found == false`.
- `Shared/DataRoundTrip`, `Shared/DataExpiredIsStale`, `Shared/DataZeroTTLNeverExpires` (still fresh after 200ms), `Shared/DataInvalidateStale`, `Shared/DataDelete`, `Shared/DataOverlap` (same outcomes as pages for `SetData`).
- `Shared/WideDrop`: 5,000 pages under one tag; DropTags returns 5,000 distinct keys.
- `Shared/Concurrent`: 8 goroutines × 200 ops of SetSince/Get/DropTags/GetData across a and b, under `-race`, no error returned other than nil.

- [ ] **Step 1:** Write the suite file and the two runner test files.
- [ ] **Step 2:** `go test -race -count=1 ./internal/cache ./pkg/collagetest` → the built-ins and the reference store PASS. Any built-in failure is a real finding: stop and report it rather than weakening the suite.
- [ ] **Step 3:** Commit `feat(collagetest): CacheSuite and SharedCacheSuite`.

---

### Task 4: `collage.Cached` on a shared backend

**Files:**
- Modify: `internal/datacache/store.go` (new `Options.Backend`, backend branch of `load`, `refresh`, `settleLocked`, `Invalidate`, `Purge`, `Len`, `Close`)
- Create: `internal/datacache/backend.go` (the `Backend` interface and the backend-mode helpers, so `store.go` grows little)
- Modify: `internal/core/cache.go:179-191` (`buildDataCache`)
- Test: `internal/datacache/backend_test.go`

**Interfaces:**
- Consumes: the data half of `SharedCache` (`datacache` imports `internal/cache`; acyclic, `internal/cache` imports no internal package):

```go
type Backend interface {
	Epoch(ctx context.Context) (uint64, error)
	GetData(ctx context.Context, key string) (value []byte, stale, found bool, err error)
	SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (cache.WriteOutcome, error)
	DeleteData(ctx context.Context, key string) error
}
```
- Produces: `datacache.Options{…, Backend Backend}`; `buildDataCache` sets it when `cfg.Cache.Store` implements `cache.SharedCache` (checked before the existing `Store != nil` early return).

Behaviour with a backend (everything else in `Load`'s doc stays true):
- No values in `s.entries`. Kept in process: `inflight`, and a cooldown map key → {failures, retryAt, tags} for keys whose refresh failed.
- Read: `GetData`. Error → miss. Found fresh → `decode`; decode error → `DeleteData`, miss. Found stale and not `WantsFreshData` → serve it, start a background refresh unless cooling down. Otherwise fetch (single flight per key).
- Before a fetch: `since, err := Epoch`; on error the fetch result is returned but not written.
- After a fetch: success → `encode`; encode error → warn once per type (reuse `unencodable`; message `collage: data cache value not shared, it cannot be encoded`), not written. Else `SetData(…, since)`; error → warn, value still returned. `ErrNotFound` → `DeleteData`. Other error → cooldown as today.
- `Invalidate`/`Purge`: bump the local epoch, clear cooldowns whose tags intersect; return 0. They do not touch the backend — `dropTags` (Task 6) does, in one call with the pages.
- `Len` returns 0; `Close` waits for refreshes, as today.

- [ ] **Step 1: Failing tests**, with `cache.NewSharedMemory()` as the backend; "marks it stale in the backend" is its `DropTags([tag], false)`:
  - `TestBackend_TwoStoresSeeOneInvalidation`: stores s1, s2 on one fake; s1.Load fetches "v1"; backend `DropTags`-equivalent marks it stale; s2.Load returns "v1" stale=true and its background refresh stores "v2"; after `s2.Close`, s1.Load returns "v2" fresh.
  - `TestBackend_OverlappedFetchStoredStale`: fetch blocks on a channel; mark stale in backend; release → backend got `SetData` returning stale; next load serves it stale and refreshes.
  - `TestBackend_NotFoundDeletes`, `TestBackend_ErrorsAreMisses` (GetData and SetData return errors → Load returns the fetched value, nil error), `TestBackend_UnencodableNotShared` (value with an unexported field: two loads → two fetches, one warning logged, backend holds nothing).
- [ ] **Step 2:** `go test -race ./internal/datacache -run TestBackend` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race -count=1 ./internal/datacache ./internal/core ./pkg/collage` → PASS (existing tests untouched).
- [ ] **Step 5:** Commit `feat(datacache): collage.Cached keeps values in a SharedCache`.

---

### Task 5: Page and document writes through `SetSince`

**Files:**
- Modify: `internal/httpx/handler.go:1358-1383` (`invalidationMark` gains the shared epoch), `:1480-1518` (`writeCache`), `:1557` (`dropIfOverlapped`)
- Modify: `internal/httpx/document.go:91,271-306` (same for documents)
- Modify: `internal/httpx/warm.go` (recipe JSON; `AdoptShared`)
- Create: `internal/httpx/sharedmeta.go`
- Test: `internal/httpx/shared_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:
  - `invalidationMark` gets `shared uint64` and `sharedOK bool`; `func (h *Handler) invalidationMark(ctx context.Context) invalidationMark` reads `Epoch` when `h.cache` is a `cache.SharedCache` (error → `sharedOK` false). Update every caller.
  - `func encodeSharedMeta(host, path string, rcp recipe) []byte` and `func DecodeSharedMeta(meta []byte) (host, path string, ok bool)` — JSON `{"host":…,"path":…,"recipe":{…}}`; `recipe` gets `MarshalJSON`/`UnmarshalJSON` covering every field.
  - `func (h *Handler) AdoptShared(reached []cache.Reached) []string` — for each reached key the recipe store lacks, decodes the meta's recipe and `put`s it with `read` true; returns the keys whose recipes it now holds.
- Write path when `h.cache` is a `SharedCache`: `!mark.sharedOK` → no write (return ""). Else `SetSince(ctx, key, content, ttl, tags, encodeSharedMeta(r.Host, r.URL.Path, recipe), mark.shared)`. Tags passed are `withRouteTags(tags, r.Host, r.URL.Path)` so the store's index carries path/host tags too. `WriteSkipped` → `h.overlapped(ctx, mark)`, return "". `WriteStale` → `h.overlapped`, `wroteForWarm(…, dropped=true)`, return "" (the response is still served). `WriteFresh` → as today (metrics, recipe, `Track`, `dropIfOverlapped`). In `dropIfOverlapped`, a `SharedCache` is marked stale (`MarkStaleKey`) instead of `InvalidateKey`, so the overlapped copy stays available for fallback.
- Non-shared stores: byte-for-byte today's path.

- [ ] **Step 1: Failing tests** (package `httpx`, handler built the way the existing `internal/httpx` tests build one, with `cache.NewSharedMemory()` as its cache):
  - `TestShared_WriteCarriesMetaAndRouteTags`: one GET; the store saw `SetSince` with tags including the path tag and host tag, and `DecodeSharedMeta(meta)` returns the host and path.
  - `TestShared_OverlappedRenderLandsStale`: a fragment data handler blocks; mid-render the store's `DropTags([page tag], false)` runs; the response is 200 with the render; the store holds the key stale (`GetStale` stale=true, `Get` misses).
  - `TestShared_EpochErrorSkipsWrite`: `Epoch` returns an error → 200, no `SetSince` call.
  - `TestShared_RecipeRoundTrip`: a recipe with vary, header, query, tls, document, cacheParams (nil and empty) survives `encodeSharedMeta` → `AdoptShared` → `request(ctx)` producing the same URL, headers and vary set as the original.
- [ ] **Step 2:** `go test -race ./internal/httpx -run TestShared` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race -count=1 ./internal/httpx ./internal/core ./pkg/collage` → PASS.
- [ ] **Step 5:** Commit `feat(httpx): page writes weigh a shared store's invalidations`.

---

### Task 6: `dropTags` through `DropTags`, fleet-wide reach

**Files:**
- Modify: `internal/core/app.go:1368-1495` (`dropTags`), and the warmer interface `a.warmer` holds (add `AdoptShared`)
- Modify: the `InvalidateTagsN` / `PurgeTags` doc comments (the "TaggedCache ... only thing that makes a shared store invalidatable" paragraph gains the `SharedCache` path)
- Test: `pkg/collage/shared_fleet_test.go`

**Interfaces:**
- Consumes: Tasks 1, 4, 5 (`httpx.DecodeSharedMeta`, `Handler.AdoptShared`).
- When `a.store` is a `cache.SharedCache`: the per-key `MarkStaleKey`/`InvalidateKey` loop and the `MarkStale`/`Invalidate` tag calls are replaced by one `shared.DropTags(ctx, tags, purge)`. Its error joins `failures`; the rest still runs. `keys` = tracker keys ∪ reached keys (deduplicated, tracker order first). `entries` = today's tracker-derived entries ∪ `DecodeSharedMeta` of each reached meta, deduplicated and sorted as today. Count = `len(keys)`; one `CacheInvalidate` metrics event per key. Warming: `handler.AdoptShared(reached)` then `Warm`/`WarmPurged(keys)` (still bounded by `WarmLimit`). The local `a.data.Invalidate/Purge` still runs first.

- [ ] **Step 1: Failing tests** — two apps (`collage.New` twice over the same templates) sharing one `collagetest.NewSharedCache()` value; a page with a `collage.Cached` author fetched from a variable the test changes; serve with `httptest`:
  - `TestFleet_DataInvalidatedOnOneReachesTheOther` (the spec's Why): GET on A and B (both see "ada"); author → "grace"; `A.InvalidateTagsN("author")`; with warming off (`WarmLimit: -1`) GET on B → body contains "grace" (stale page served first is acceptable only if the second GET on B within 2s polling shows "grace").
  - `TestFleet_OverlapOnOtherInstanceNotFresh`: B's render blocks inside the fetch; A invalidates; release; GET on A → not a fresh `HIT` of B's render (`X-Collage-Cache` is not `HIT`, or body is the new value).
  - `TestFleet_EventCoversPagesOnlyOtherServed`: a plugin on A records `CacheInvalidateEvent.Entries`; only B served `/p/x`; `A.InvalidateTagsN(tag)` → entries include `{Host: …, Path: "/p/x"}`; return value counts it.
  - `TestFleet_WarmsPagesOnlyOtherServed`: warming on; only B served `/p/x`; A invalidates; within 2s the store holds `/p/x` fresh (`Get` hits) without any further request.
- [ ] **Step 2:** `go test -race -count=1 ./pkg/collage -run TestFleet` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race -count=1 ./...` → PASS; then `-count=200 -run TestFleet` → PASS (no flake; see `TestCacheConfigStore` history — a test that races the warmer must turn warming off or wait for it).
- [ ] **Step 5:** Commit `feat(core): invalidation reaches the whole fleet through a SharedCache`.

---

### Task 7: Core docs and changelog

**Files:**
- Modify: `docs/caching.md` — "The tracker is in-process, and bounded", "Caching data, not only pages" (multi-instance bullet), "Supplying your own cache" (new `### A cache a fleet shares` section: the interface, what core does with it, the conformance suites, link to collage-redis), "Keeping the cache across restarts" (fleet paragraph points to `SharedCache`).
- Modify: `CHANGELOG.md` (v0.66.0 entry, in the file's existing style)
- Modify: `README.md` caching section: the Redis example becomes `redis.New(redis.Config{URL: …})` from collage-redis.

- [ ] **Step 1:** Write the docs. Known behaviour from the spec goes in: the invalidating instance warms for the fleet; single fetch per key is per instance; JSON-unencodable values are not shared.
- [ ] **Step 2:** `go test ./...` (doc tests, if any, e.g. reference checks) → PASS; full lint per Global Constraints → clean.
- [ ] **Step 3:** Commit `docs: SharedCache, a cache a fleet shares (v0.66.0)`.

---

### Task 8: collage-redis — the store

**Files (new repo `~/Desktop/collage-redis`, `git init`, module with `replace github.com/Elagoht/collage => ../collage` until core is tagged):**
- Create: `go.mod`, `.gitignore`, `version.go` (`Version() string` = "0.1.0", `const Name = "elagoht/redis"`), `config.go`, `store.go`, `scripts.go` (Lua sources as `redis.NewScript`), `store_test.go`

**Interfaces:**
- Consumes: Task 1 interface; Task 3 suites.
- Produces:

```go
type Config struct {
	URL     string        // redis:// or rediss://; required unless Client is set
	Client  goredis.UniversalClient // optional: reuse; Close then leaves it open
	Prefix  string        // default "collage:"
	Version string        // default: sha256 of os.Executable(), hex, first 16 chars
	MaxAge  time.Duration // default 7 * 24 * time.Hour
	Timeout time.Duration // per command; default 250ms
	Logger  *slog.Logger  // default slog.Default()
}
func New(cfg Config) (*Store, error) // errors only for a bad config/URL; never pings
func (s *Store) Close() error
var _ collage.SharedCache = (*Store)(nil)
```

Key schema (suggested; builder may refine): `<prefix><version>:` then `p:<key>` hash {c, etag, exp, stale, meta}, `d:<key>` hash {v, exp, stale}, `tp:<tag>` / `td:<tag>` sets of page / data keys, `gi:<tag>` and `gp:<tag>` generations, `epoch`. Every write, mark and drop is one Lua script. Epoch initialisation: `SET epoch <unix ms> NX` then `INCR` (Review Focus). TTLs: every key `PEXPIRE` ≤ `MaxAge`; an entry's freshness expiry is `min(ttl, MaxAge)` in its `exp` field while the key itself lives `MaxAge`; index and generation keys are re-`PEXPIRE`d to `MaxAge` on every touch, so they outlive any member. ETag: `collage.ETag(content)` stored in the hash.

- [ ] **Step 1:** Tests: `TestStore_CacheSuite` (`collagetest.CacheSuite` on a miniredis-backed store, fresh miniredis per subtest), `TestStore_SharedCacheSuite` (pair = two `Store`s with separate clients on one miniredis), `TestStore_EpochSurvivesFlush` (Epoch e; `FLUSHALL`; DropTags([t]); SetSince(…, [t], …, e) → `WriteStale`), `TestStore_WideDrop5000`.
- [ ] **Step 2:** `go test -race ./...` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race -count=1 ./...` → PASS.
- [ ] **Step 5:** Commit `feat: a SharedCache on Redis`.

---

### Task 9: collage-redis — failure, age and deploy behaviour

**Files:**
- Modify: `store.go`, `config.go`
- Create: `failure_test.go`, `site_test.go`

- [ ] **Step 1: Failing tests:**
  - `TestStore_RedisDownIsMiss`: miniredis closed → `Get` found=false, `SetSince` returns an error, no panic; `New` against `redis://127.0.0.1:1` returns a store.
  - `TestStore_LogsRateLimited`: 1,000 failing calls in one second → at most 1 warning per 10s (log captured via `slog` handler).
  - `TestStore_MaxAge`: MaxAge 1h; Set with ttl 0 (default 100y for `Static`) → miniredis `TTL` of the key ≤ 1h; `FastForward(61m)` → `GetStale` not found; tag set expired no earlier than its members.
  - `TestStore_VersionsIsolated`: stores with Version "a" and "b" on one miniredis: a write through "a" is invisible to "b", data too.
  - `TestSite_ServesWithRedisDown`: a `collagetest` site with `Cache.Store` = store on a closed miniredis → GET returns 200 with the page.
  - `TestSite_TwoAppsOneRedis`: the Task 6 Why scenario end to end through this store.
  - `TestStore_RealRedis` (skipped unless `REDIS_URL` set): runs `SharedCacheSuite` against it with a random `Prefix`, cleaned up after.
- [ ] **Step 2:** FAIL → implement → `go test -race -count=1 ./...` PASS.
- [ ] **Step 3:** Commit `feat: Redis down is a miss; keys bounded by MaxAge; a namespace per build`.

---

### Task 10: collage-redis README and collage CI matrix

**Files:**
- Create: `~/Desktop/collage-redis/README.md` (`# elagoht/redis`: the three-line setup; what is shared; what stays per instance; every deploy starts cold; `MaxAge` and that stale fallback ends with it; run Redis with `maxmemory-policy noeviction` and size it for about `MaxAge` of writes; `rediss://` for TLS)
- Modify: `~/Desktop/collage/.github/workflows/ci.yml` — add collage-redis to the plugins matrix in the libraries' position (as collage-paginate), noting it has no `collage.json` so `collage plugin check` does not apply — follow how paginate is handled.

- [ ] **Step 1:** Write both; `go vet ./... && staticcheck` clean in collage-redis.
- [ ] **Step 2:** Commit each repo separately (`docs: README`, `ci: collage-redis in the plugin matrix`).

---

## Release (needs the user's go-ahead at each step; not part of execution)

1. Merge `feat/shared-cache`, tag collage v0.66.0, push; check `gh run list`.
2. collage-redis: drop `replace`, `GOPROXY=direct GONOSUMDB=github.com/Elagoht go get github.com/Elagoht/collage@v0.66.0`, tidy, test, `gh repo create Elagoht/collage-redis --public`, `git push`, tag v0.1.0.
3. Docs site (`~/Desktop/collage-docs`, EN + TR): fleet section in caching, a collage-redis page.
