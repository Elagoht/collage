# Stale page cache (v0.63.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The page cache keeps serving a page past its invalidation or expiry until a good render replaces it. With `Cache.Type: "disk"` it is tiered (memory in front of disk) and comes up warm after a restart.

**Architecture:** A new optional `cache.StaleCache` interface (GetStale/MarkStaleKey/MarkStale) is implemented by the memory and disk caches and by a new `cache.TieredCache` used for `Type: "disk"`. Core invalidation marks entries stale instead of dropping them when the store supports it, and bumps an invalidation epoch. On a stale hit the handler renders through the existing flight. It stores and serves a good render, and otherwise serves the stale bytes with `no-store`. Every cacheable response carries `X-Collage-Cache`.

**Tech Stack:** Go stdlib.

**Spec:** `docs/superpowers/specs/2026-10-15-stale-cache-design.md`, section "v0.63.0: page cache" (binding). The v0.62.0 sections describe the data store this builds on.

## Global Constraints

- Never use the type `any` (generic `[T any]` constraints are fine). No `sync.Map`.
- Additive public API only: `collage.StaleCache` (alias of `cache.StaleCache`) and `collage.CacheStatusHeader = "X-Collage-Cache"`. `cache.Cache` and `cache.TaggedCache` keep their exact method sets, and `Get` keeps reporting only fresh entries.
- A custom `Cache.Store` that does not implement `StaleCache` behaves exactly as in v0.62.0.
- No maximum stale age. Eviction (MaxEntries/MaxBytes) and `PurgeTags` are the only ways a stale page disappears, apart from a 404 render and a CSRF-marker mismatch.
- A disk failure never fails a request.
- A stale page is served with `Cache-Control: no-store`. A render that answers 404 drops the entry and serves the 404.
- `X-Collage-Cache` is `HIT`, `STALE` or `MISS`, on every cacheable GET/HEAD page or document response, and never in dev mode.
- Run tests with `go test -race -count=1` (the test cache hides scaffold breakage). Never `git add -A`/`.`; stage named files after reading `git status --short`. Every commit message ends with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Comments explain *why*, in full sentences, at the density of the surrounding code.
- Branch `feat/stale-page-cache` in worktree `/Users/furkan/Desktop/collage-pages`. CHANGELOG `## v0.63.0`.

## Review Focus

1. **Double-tier consistency.** After `MarkStaleKey`, `InvalidateKey`, `Clear` and eviction, the memory and disk tiers of `TieredCache` must agree. A disk-only stale entry must never be promoted as fresh. Pinned by `TestTiered_MarkStaleReachesBothTiers` and `TestTiered_PromotionKeepsStaleness` (Task 2).
2. **A render overtaken by an invalidation is never stored fresh**, whether on a plain miss or a stale hit. Pinned by `TestHandler_ARenderOverlappingAnInvalidationIsNotStored` (Task 4).
3. **CSRF marker on a stale entry.** A stale body carrying another key's marker is dropped, not served. Pinned by `TestHandler_AStaleEntryWithAForeignMarkerIsDropped` (Task 4).
4. **HEAD on a stale entry** serves headers like GET (STALE, no-store) and writes nothing. Pinned by `TestHandler_HeadOnAStalePage` (Task 4).
5. **Custom store without StaleCache** keeps the v0.62 behaviour end to end. Pinned by `TestStalePages_ACustomStoreKeepsDropping` (Task 4).

---

### Task 1: `StaleCache` in the memory and disk caches

**Files:** `internal/cache/cache.go` (interface), `internal/cache/memory.go`, `internal/cache/disk.go`, tests in `internal/cache/memory_test.go` and `internal/cache/disk_test.go`, `pkg/collage/collage.go` (alias `StaleCache = cache.StaleCache` with a doc comment beside `TaggedCache`), `pkg/collage/api_test.go` if it lists exported names.

**Interfaces produced:**
```go
// in internal/cache/cache.go, after TaggedCache
type StaleCache interface {
	Cache
	GetStale(ctx context.Context, key string) (content []byte, etag string, stale, found bool)
	MarkStaleKey(ctx context.Context, key string) error
	MarkStale(ctx context.Context, tags []string) error
}
```
Both `*MemoryCache` and `*DiskCache` satisfy `StaleCache` and `TaggedCache` (add `var _ StaleCache = ...`).

**Behaviour:**
- `Get` returns only entries that are neither marked stale nor expired. It no longer deletes an expired entry: the entry is kept as stale. Memory's hits/misses counters keep counting `Get` as today.
- `GetStale` returns any present entry; `stale` is true when the entry is marked stale or expired.
- `MarkStaleKey` on a missing key is not an error. `MarkStale(tags)` marks every entry carrying any of the tags. Neither changes expiry or content.
- `SetTagged`/`Set` always write a fresh entry, which clears any stale mark.
- Memory: add `Stale bool` to the stored record (not to the exported `Entry` struct unless it is needed; if it is added there, document it). Eviction is unchanged (FIFO, MaxEntries/MaxBytes).
- Disk: `header` gains `Stale bool json:"stale,omitempty"`. `MarkStaleKey`/`MarkStale` rewrite the file atomically (temp file + rename, as `SetTagged` does) with the same ETag, expiry, tags and content and `stale:true`, under `c.mu`. An unreadable entry met during `MarkStale` is removed, as `Invalidate` does today. `Get` no longer deletes expired files. Eviction still bounds the directory.
- Existing tests that assert "expired → deleted/miss" keep asserting a miss from `Get`. Where they assert deletion, change them to assert `GetStale` finds the entry as stale, and say why in the test comment.

**Tests (write first, RED then GREEN):**
- Memory and disk, each: `Test<Impl>_MarkStaleKeyKeepsTheEntry` (Get misses, GetStale finds it stale with the same content and etag); `Test<Impl>_MarkStaleByTag`; `Test<Impl>_ExpiredIsStaleNotGone` (controlled clock); `Test<Impl>_SetClearsStale`; `Test<Impl>_MarkStaleOnMissingKeyIsNoError`. Disk only: `TestDisk_StaleSurvivesReopen` (NewDisk again on the same dir and version → GetStale stale), and `TestDisk_EvictionStillBoundsStaleEntries`.

**Verify:** `go test -race -count=1 ./internal/cache/ ./pkg/collage/ ./internal/core/ ./internal/httpx/`. Core and httpx tests that relied on expired entries disappearing may fail. Fix only tests whose premise is the old deletion, and explain each in the report. Commit.

---

### Task 2: `TieredCache` and wiring for `Type: "disk"`

**Files:** create `internal/cache/tiered.go` and `internal/cache/tiered_test.go`; modify `internal/cache/memory.go` and `internal/cache/disk.go` for the small internal helpers below; modify `internal/core/cache.go` (`buildCache` disk branch); add tests in `internal/core` as needed.

**Interfaces produced:**
```go
// NewTiered puts front (memory) before back (disk). It warms front from back.
func NewTiered(front *MemoryCache, back *DiskCache) *TieredCache
// TieredCache implements Cache, TaggedCache and StaleCache.
func (t *TieredCache) Dir() string // back.Dir(), for the existing logging/inspection that reads DiskCache.Dir
```
Internal helpers (unexported, same package):
- `(*DiskCache) load(key string) (Entry, stale bool, found bool)`: full entry with tags and expiry.
- `(*DiskCache) newestFirst() []string`: keys are not recoverable from file names, so return the entry file paths sorted by ModTime, newest first. Read each header plus content for warm-up. If the disk header does not store the key, add `Key string json:"key,omitempty"` to the header so warm-up can index memory by key. Files without a key (written by v0.62 or earlier) are skipped by warm-up, but are still served through `Get` promotion.
- `(*MemoryCache) put(key string, e Entry, stale bool)`: store with an explicit expiry and stale flag, respecting the limits.

**Behaviour:**
- `GetStale`: front first, then back. On a back hit, `put` into front with the back's expiry, tags and stale flag, then return. `Get`: same, fresh only.
- `SetTagged`/`Set`: back first, then front. A back error is returned after front is still written, so the request is served and the caller reports it; it must not stop the memory write.
- `MarkStaleKey`, `MarkStale`, `InvalidateKey`, `Invalidate`, `Clear`: apply to both tiers. Join errors and return them; front is always applied.
- Warm-up in `NewTiered`: walk `back.newestFirst()` and `put` each entry, stale ones included, until front reports it is full (MaxEntries or MaxBytes). Stop at the first that would evict. Log nothing per file; a corrupt file is skipped.
- `buildCache` for `"disk"`: build the `DiskCache` as today, then a `MemoryCache` with the memory defaults, and the tier. `MaxBytes` and `MaxEntries` from config apply to the disk tier, as today. The memory tier uses `MemoryConfig{DefaultTTL, MaxEntries: cfg.Cache.MaxEntries}`, with MaxBytes left at its default (256 MiB). Keep every existing `buildCache` log line (it logs `dir`, which comes from `Dir()`).
- Anything that type-asserts the built store to `*cache.DiskCache` must keep working. grep `*cache.DiskCache` and `.(*cache.DiskCache)` in internal/ and pkg/ and adapt.

**Tests:** `TestTiered_ReadsMemoryThenDiskAndPromotes`, `TestTiered_PromotionKeepsStaleness`, `TestTiered_MarkStaleReachesBothTiers`, `TestTiered_InvalidateAndClearReachBothTiers`, `TestTiered_WarmsFromDiskNewestFirstWithinMemoryLimits`, `TestTiered_ADiskWriteFailureStillWritesMemory` (make the disk dir read-only after NewDisk; skip as root). Core: an app with `Type: "disk"` restarted on the same Dir and Version serves its first request from cache (use the existing disk-cache test helpers in internal/core or pkg/collage).

**Verify:** the full suite with `-race -count=1`. Commit.

---

### Task 3: Core invalidation marks pages stale; invalidation epoch

**Files:** `internal/core/app.go` (`dropTags`, the `httpx.Deps` construction), `internal/httpx/handler.go` (`Deps` field only), tests in `internal/core` / `pkg/collage`.

**Interfaces produced:**
- `httpx.Deps.InvalidationEpoch func() uint64`: nil means no epoch rule. Core passes a closure over an `atomic.Uint64` on `App`, which is incremented once per `dropTags` call **before** it touches the store.
- Behaviour of `dropTags(ctx, tags, purge)`:
  - When `purge` is false and `a.store` implements `cache.StaleCache`: call `MarkStaleKey` per resolved key instead of `InvalidateKey`; skip the tracker `Forget` loop for those keys, so `PurgeTags` can still reach them; and call `MarkStale(tags)` instead of `TaggedCache.Invalidate(tags)`. Metrics (`CacheInvalidate` per key), the plugin event, and the count stay exactly as today.
  - When `purge` is true, or the store is not a `StaleCache`: today's behaviour, unchanged.
- Update `InvalidateTagsN`'s doc comment (core and `pkg/collage/app.go`): pages are marked stale when the store supports it, served until a good render replaces them. Update `PurgeTags`'s doc: removes pages outright.

**Tests:** `TestInvalidateTags_MarksPagesStaleOnAStaleCache` (built-in memory store: after invalidation `GetStale` finds the page stale and `Get` misses); `TestPurgeTags_RemovesStalePages` (invalidate, then purge: `GetStale` finds nothing); `TestInvalidateTags_ACustomStoreStillDrops` (a store implementing only `cache.Cache`); `TestInvalidationEpoch_AdvancesPerCall`.

**Verify:** the full suite. Some pkg/collage tests that assert a re-render right after `InvalidateTags` may now see the stale page served first; they are fixed in Task 4. Note which tests fail, but do not change handler behaviour here. If tests outside Task 4's scope fail, report them. Commit.

---

### Task 4: Handler serves stale pages and documents

**Files:** `internal/httpx/handler.go` (lookup, the stale-hit flow, `writeCache`, headers), `internal/httpx/document.go` (the same for documents), `pkg/collage/collage.go` (`CacheStatusHeader` constant, doc comment), tests in `internal/httpx` and `pkg/collage`.

**Interfaces consumed:** `cache.StaleCache` (Task 1), `Deps.InvalidationEpoch` (Task 3).

**Behaviour (pages; documents mirror it):**
- `cacheGet` returns `(content, etag, stale, found)`. With a `StaleCache` store it uses `GetStale`; otherwise `Get` with `stale=false`. The foreign-CSRF-marker check applies to both fresh and stale entries; a mismatch drops the entry (`InvalidateKey`) and is a miss.
- Fresh hit: today's `serveCached` plus the header `X-Collage-Cache: HIT`.
- Stale hit: do not serve yet; render through the flight as on a miss, keeping the stale bytes and ETag. Emit `CacheMiss` as today, or a new event only if the spec requires it (it does not); emit `CacheStale` only when the stale page is actually served. Then:
  - `out.fail == nil`, not degraded: serve the render with `X-Collage-Cache: MISS`. The write happens inside `renderPage` as today, subject to the epoch rule.
  - `out.fail` with status 404 (or the render marked not-found): `InvalidateKey(key)`, then serve the failure.
  - Any other `out.fail`, or `out.degraded`: serve the stale bytes through `serveCached` with `Cache-Control: no-store` (override after `setCacheHeaders`, as the degraded path does), `X-Collage-Cache: STALE`, metric `observability.CacheStale` with the key, and a warn log through the handler's logger: "collage: serving a stale page, its render failed" with page, path and error (or "degraded" when there was no error).
- Miss: `X-Collage-Cache: MISS` on the success response.
- Header only when the request was cacheable (`key != ""`), never in dev mode, and never on failure responses other than the stale serve.
- **Epoch rule:** `renderPage` (and the document produce function) read `InvalidationEpoch()` before rendering, and `writeCache`/`writeDocumentCache` skip the write when it changed, still returning `cache.ETag(content)`. A nil func disables the rule.
- `collage.CacheStatusHeader = "X-Collage-Cache"`, exported from pkg/collage beside the other header constants (`FetchHeader`, `LocationHeader`). httpx uses its own internal constant of the same value.

**Tests (pkg/collage, through the public API, unless noted):**
- `TestStalePages_ServedWhileTheUpstreamIsDown`: a page whose required fragment reads plain data (not `Cached`), so the page cache alone is under test. Warm, then make the data handler fail. `InvalidateTags`. The response is 200 with the old HTML, STALE and no-store. Restore the data and the next request is MISS with new HTML, then HIT.
- `TestStalePages_ADegradedRenderServesTheStalePage` (an optional fragment fails → the old HTML, not the holed one).
- `TestStalePages_NotFoundDropsTheEntry` (required fragment returns ErrNotFound → 404; then GetStale through a test store or a second request is still 404).
- `TestStalePages_AnExpiredPageIsStale` (short TTL; data down → STALE).
- `TestStalePages_ACustomStoreKeepsDropping` (a custom cache.Cache store → after invalidation a failing render is an error page, never STALE).
- `TestStalePages_PurgeLeavesNothingStale`.
- `TestStalePages_Documents` (a cached document: stale served when its handler fails; 404 drops).
- `TestStalePages_HitMissHeaders` (MISS then HIT; no header in dev mode; none on an uncacheable page).
- `TestHandler_ARenderOverlappingAnInvalidationIsNotStored` (internal/httpx or pkg/collage: block the render on a channel, invalidate, release → the response is served but the next request renders again).
- `TestHandler_AStaleEntryWithAForeignMarkerIsDropped`.
- `TestHandler_HeadOnAStalePage`.
- Update the pkg/collage tests from v0.62 that expect a fresh page right after `InvalidateTags` (e.g. TestCached_InvalidatesWithThePages, TestCached_TheRequestAfterAStaleOneIsFresh, TestCached_NotFoundUpstreamBecomesA404, TestCached_AStaleDocumentIsNotCached). Stale data inside a stale page now serves the stale page, which is the same body. Keep their intent: eventually fresh, and a 404 when not found. Adjust only the mechanics, and explain each change in the report.

**Verify:** the full suite with `-race -count=1`, plus `-count=10` for the new stale page tests. Commit.

---

### Task 5: Docs and changelog

**Files:** `docs/caching.md` (invalidation semantics for pages; stale page serving; `X-Collage-Cache`; tiered disk cache and warm restart; StaleCache for custom stores in "Supplying your own cache"), `docs/actions.md` (the redirect after `InvalidateTags` now shows the stale page once, the same note as for data), `docs/deployment.md` if it describes the disk cache, `CHANGELOG.md` `## v0.63.0` (Changed: InvalidateTags marks pages stale on built-in stores; Added: StaleCache, CacheStatusHeader, tiered disk cache with warm restart; note for custom stores).

Write from the code as it now is. grep the docs for "removes every cached render", "drops", "InvalidateKey" and fix every statement the change made wrong. Commit.

---

### Task 6: Verification, e2e, release

Done by the controller: full suite `-race -count=1`; a scaffold check (`go test -count=1 ./internal/cli/...`); no new `any`; e2e on ose-landing via a temporary GOWORK (warm, mark stale, CMS down → 200 STALE; restart with the CMS down → 200, HIT from the warmed tier); whole-branch review; merge; tag v0.63.0; push; docs site (EN+TR).
