# Stale-on-failure cache

Status: v0.62.0 (data store) designed and approved 2026-10-15. v0.63.0 (page
cache) is outlined at the end and gets its own brainstorm and spec.

## Why

ose-landing (collage v0.61.5) replaces a Go site whose cache crashes after CMS
webhooks. The user has wanted a *fallback* cache from the start: when the
upstream fails, keep serving what we had. Today collage fails the same way the
old site did:

1. `InvalidateTags` deletes data-store entries (`internal/datacache/store.go`
   `Invalidate`) and page entries (`internal/cache`, tracker keys in
   `internal/core/app.go` `InvalidateTagsN`).
2. The next request renders cold. If the CMS is slow or down, the layout's data
   fragment errors, the optional layout renders nothing, the content slot goes
   with it, and the result is `ErrEmptyRender`: a **500 on every page**
   (`internal/httpx/handler.go`). A required fragment's error is a 500 directly.
3. The default fragment data timeout is 5s (`internal/render/engine.go`
   `DefaultTimeout`). After a wide invalidation every page fetches cold at once.
4. The data store is in-process only, so a restart or deploy leaves nothing to
   fall back on.

## Requirements (from the user)

- **R1:** Write to memory and disk together. Memory is the read path and disk is
  the durable copy.
- **R2:** On startup, load disk into memory so a restart comes up warm.
- **R3:** Invalidation marks entries stale. It does not delete them.
- **R4:** If the replacement cannot be produced, keep serving the stale entry.
  Only a successful result replaces it.

## Decisions

- **Two releases.** v0.62.0 is the data store. On its own it closes the 500
  path, because pages render from stale data while the upstream is down.
  v0.63.0 is the page cache.
- **Stale-while-revalidate.** A stale hit returns the stale value immediately
  and refreshes in the background. There is no blocking grace period. Editors
  see a change one request late.
- **No maximum stale age.** A value disappears only through LRU eviction
  (`MaxEntries`), `ErrNotFound`, or `PurgeTags`. Every stale serve is logged and
  counted, so a long outage stays visible.
- **A render that used stale data is degraded.** It is served but never stored
  in the page cache.

## v0.62.0: data store

### Load semantics (`internal/datacache`)

Each entry is fresh or stale. An entry becomes stale when one of its tags is
invalidated or when its TTL passes. Expiry no longer removes it.

- **Fresh hit:** return the value, as today.
- **Stale hit:** return the stale value with a nil error, and tell the caller it
  was stale (see "Degraded render" below). If no refresh is running for the
  key, and the key is not cooling down after a failed refresh, start one:
  - single-flight, one per key;
  - detached from the request: `context.WithoutCancel(ctx)` with its own fixed
    timeout of 30s (`refreshTimeout`, not configurable);
  - on success: replace the entry with a fresh one, applying the epoch rule
    below;
  - on `types.ErrNotFound` (matched with `errors.Is`): remove the entry and its
    file. The next load misses, fetches, gets the error, and the page answers
    404;
  - on any other error, or a panic: keep the stale entry, emit
    `observability.CacheStale`, and log at warn with the key name, how long the
    entry has been stale, and the error. The key then cools down: no refresh
    starts for it until `retryAt`, 1s after the first consecutive failure and
    doubling with each further one, capped at 60s. Stale hits inside the
    cooldown are still served and still emit `CacheStale`. A success replaces
    the entry, which ends the cooldown, and logs one info line with the failure
    count. A fresh-data load (`types.WantsFreshData`) ignores the cooldown. If
    the entry is gone when the refresh fails (purged or evicted), nothing is
    served stale and the failure is logged at debug only;
  - Emit `CacheStale` once for each stale value served.
- **Miss (no entry):** fetch and block, as today, including today's
  abandoned-starter retry and panic handling. There is nothing to fall back to,
  so errors reach the caller as today.
- **Epoch rule.** A fetch, blocking or background, that started before an
  invalidation of the store is stored stale, not fresh. Its callers still get
  its value.
- `ErrNotFound` from a blocking fetch is returned and nothing is stored, as
  today.

### Invalidation

- `Store.Invalidate(tags)` marks matching entries stale and increments the
  epoch. It returns the number of entries marked.
- New `Store.Purge(tags)` removes matching entries and their files, and
  increments the epoch.

### Typed values without `any`

The store's `any` values are replaced by a small interface declared in
`internal/types` (or `datacache`):

```go
type Value interface {
	Encode() ([]byte, error)
}
```

`Cached[T]` wraps its result in a generic `typed[T]` that implements `Value`
with `encoding/json`. The store passes a decode function into `Load`, built by
`Cached[T]`, with the shape `func([]byte) (Value, error)`. Once the store has
decoded an entry loaded from disk, the entry keeps the decoded value, so the
bytes are decoded at most once per process. The `DataCache` interface in
`internal/types/cached.go` changes accordingly. It is internal, so this is not
an API change.

### Stable store key

`storeKey` now uses a stable type identity:
`name + "\x00" + typ.PkgPath() + "\x00" + typ.String()`. The current `%p` of
the `reflect.Type` changes between processes. If a package is renamed or moved,
the old entries simply stop matching and are treated as misses.

### Persistence (R1, R2)

- **Enabled when** `Cache.Enabled`, not dev mode, `Cache.Store == nil`, and
  `Cache.Type == CacheDisk`. In every other case the store is memory-only, as
  today.
- **Directory:** `<Cache.Dir>/data/`. It is **not** namespaced by build
  fingerprint, so a new deploy with the CMS down still renders from yesterday's
  data. The type identity in the key guards against shape changes.
- **File:** one per entry, named by the hex sha256 of the store key. It holds a
  small header (format version, store key, tags, expiry, stale flag, write
  time) followed by the encoded value. Writes are atomic (temp file + rename),
  using the same pattern as `internal/cache/disk.go`.
- **Writes:** these happen outside `s.mu`, on one background writer goroutine
  per store, so a caller such as `Invalidate` returns without waiting on the
  disk. `Store.Close(ctx)` waits for running refreshes and the write queue,
  then stops the writer; `App.Shutdown` calls it before the plugins shut down.
  - Every successful store (fresh or stale) writes the file.
  - `Invalidate` rewrites each marked entry's file with the stale flag set, so
    staleness survives a restart.
  - Eviction, `ErrNotFound`, and `Purge` delete the file.
  - Writes to one key must not reorder. Use a per-entry generation number and
    skip a write whose generation has been superseded.
- **Failures:** a disk failure never fails a load or a request. It is logged.
  A value whose `Encode` fails stays memory-only, logged once per type. `Encode`
  fails for a type JSON would not restore exactly: an unexported or `json:"-"`
  field, or an interface, anywhere in it, unless that part implements both
  `json.Marshaler` and `json.Unmarshaler`.
- **Startup:** `datacache.Open(dir, opts)` reads the directory. It loads entries
  newest first, up to `MaxEntries`, as raw bytes with their tags, expiry and
  stale flag, and deletes files beyond that limit. Unreadable or corrupt entry
  files, or files with an unknown format version, are deleted, as are leftover
  `.tmp-` files; files whose name is not 64 lowercase hex characters are not
  the store's and are left alone. Every entry loads **stale** (stale since its
  stale time, or else its write time): what changed upstream while the process
  was down is unknown. The site still starts warm and never blocks: the first
  load serves the persisted value at once and refreshes it in the background.
  Values are decoded lazily on first read.
- **Decode failure:** this happens when the type changed between builds. The
  entry and its file are removed and the load proceeds as a miss. It is never an
  error to the page.
- **Bound:** the number of entries on disk is bounded by `MaxEntries`, the same
  as in memory. There is no separate byte cap for the data store.

### Degraded render

`Cached` records on the render state that it served a stale value, and the
render result reports it (`render.Result.Degraded()` becomes true; today it
looks only at failed fragments). The handler then treats the render like any
other degraded render: it is served, never written to the page cache, and sent
with `Cache-Control: no-store`, so no CDN pins it either. The next request renders again, by which time the background
refresh has usually finished, and that render is cached. Without this rule, a
render built from stale data would be stored as fresh for a full page TTL right
after an invalidation, and editors would not see their change.

### API (additive only)

- `InvalidateTags` / `InvalidateTagsN` keep their signatures. For data they now
  mark stale. For pages, in v0.62.0, they still delete. The returned count keeps
  its documented meaning (page keys the tracker resolved).
- New `func (a *App) PurgeTags(ctx context.Context, tags ...string) error`:
  removes data entries (memory and disk) and pages for the tags. It is the hard
  delete for the rare case an app wants entries gone. It is not added to `Host`,
  which is sealed.
- `ActionResult.InvalidateTags` follows `InvalidateTags`.
- New `observability.CacheStale CacheEvent = "stale"`. The `Metrics` interface
  is unchanged.

### Docs

- `docs/caching.md`: stale semantics, stale-while-revalidate, persistence
  directory, `ErrNotFound`, `PurgeTags`, the degraded-render rule.
- `docs/actions.md`: the webhook recipe, explaining that the old content stays
  up while the CMS is down.
- CHANGELOG entry for v0.62.0.
- The docs site (`~/Desktop/collage-docs`) is updated after the release.

### Acceptance tests

Data store (`internal/datacache`):

1. Invalidate, then the fetch errors: the stale value is returned with a nil
   error, and the entry stays.
2. Stale entry, then the refresh returns `ErrNotFound`: the entry and its file
   are gone, and the next load returns the error.
3. Expired entry, then the fetch errors: the stale value is returned.
4. Stale hit returns without waiting on the fetch. When the background fetch
   finishes, the next load sees the new value.
5. A fetch started before `Invalidate` is stored stale, not fresh.
6. Concurrent loads on a stale key start one background fetch.
7. Persist, then open a new store on the same directory: the value is served
   without fetching, and the stale flag survives.
8. A persisted value whose type changed is a miss with no error, and its file is
   dropped.
9. A value that cannot be encoded is served from memory and not persisted.
10. A background refresh outlives the cancelled request that triggered it.
11. `Purge` removes memory and disk entries.

Framework (`internal/core`, `internal/httpx`, `pkg/collage`):

12. A render that read a stale value is served but not stored in the page cache.
    The next render, with the refresh finished, is stored.
13. Warm page, then `InvalidateTags`, then the upstream is down: 200, built from
    stale data (layout present, no `ErrEmptyRender`).
14. Restart (new app, same `Cache.Dir`): entries load stale; the first render
    makes no blocking upstream call and serves the persisted value.
15. A disk write failure never fails the request.

All tests run with `go test -race -count=1 ./...`.

End to end: ose-landing via `go.work`, using `collage build` because caches are
off in dev. Warm the site, invalidate `cms`, stop the CMS, and check that every
page answers 200. Restart the binary with the CMS still down and check that
every page still answers 200.

### Constraints

- No new `any`, and the store's existing `any` goes away.
- Branch `feat/stale-data-cache`, in a worktree (other sessions share the tree).
  Minor bump to v0.62.0. **No tag or push without the user's go-ahead.**

## v0.63.0: page cache (outline, to be brainstormed)

- Tiered page cache: memory in front, disk behind, write-through on `SetTagged`.
  On startup, warm memory from the current build's disk namespace.
- `InvalidateTags` marks pages stale. On a stale hit the handler renders. If the
  render succeeds and is not degraded, it is stored and served. Otherwise the
  stale HTML is served with a short `Cache-Control`. A 404 drops the entry.
- `writeCache` gets the epoch rule.
- An optional interface for staleness, so custom `Cache.Store`s keep today's
  behaviour.
- `X-Collage-Cache: HIT | STALE | MISS` header.
- Open: block vs serve-stale on page hits (v0.62 chose serve-stale for data),
  and whether the previous build's pages are a last resort across deploys.
