# Cache warming (v0.64.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After an invalidation, affected pages are re-rendered in the background before anyone asks. An action's redirect target is rendered fresh before the redirect is sent, so an editor never needs a second refresh and no page view waits.

**Architecture:** The handler records a *warm recipe* per cache key when it writes a page or document: host, path, the query values the key varies on, the frozen vary-set values, and the kind. A warm render replays a recipe through the handler's own `ServeHTTP` (inside the application's and plugins' middleware, which it never passes through) with a discarding ResponseWriter and a `types.WithFreshData` context. The existing stale-hit flow then renders and stores the page. Core hands the keys resolved by every `dropTags` to a background warmer (4 workers, capped, stops on the first failure). The action handler warms the redirect target's recipes synchronously before it writes the redirect.

**Tech Stack:** Go stdlib.

**Spec:** `docs/superpowers/specs/2026-10-17-cache-warming-design.md` (binding).

## Global Constraints

- Never use the type `any` (generic `[T any]` constraints are fine). No `sync.Map`.
- Additive API only:
  - `CacheConfig.WarmLimit int`: 0 means the default 1000; a negative value turns warming off.
  - `observability.CacheWarm CacheEvent = "warm"`, re-exported as `collage.CacheWarm`.
- No visitor request ever waits on warming. Only an action with `InvalidateTags` and a same-origin `Location` waits, for its own target.
- Warm requests never pass through `App.Use` or plugin middleware. They are marked fresh-data, so they never render from stale data.
- Warming is off in dev mode, during a static build or capture, and when the cache is off. It stops on `Shutdown`.
- A run stops at its first failed or degraded warm render: one warn line, and the remaining keys are skipped and left stale.
- Comments explain *why*, in full sentences. Run tests with `go test -race -count=1`. Never `git add -A`/`.`. Every commit message ends with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Branch `feat/cache-warming` in the worktree `/Users/furkan/Desktop/collage-warm`. CHANGELOG `## v0.64.0`.

## Review Focus

1. **The warmed key equals the visitor's key** for vary-header and query variants. Pinned by `TestWarm_AVaryVariantIsWarmedUnderItsOwnKey` (Task 1).
2. **`renderSafeContext` keeps the fresh-data marker.** Otherwise a warm render uses stale data, is degraded, and stores nothing. Pinned by `TestWarm_RendersFromFreshData` (Task 1).
3. **A down upstream is asked once, not N times.** The first failure stops the run. Pinned by `TestWarmer_StopsAtTheFirstFailure` (Task 2).
4. **No double render** when the action warms a target that is also queued. Pinned by `TestAction_WarmsTheRedirectTargetOnce` (Task 3).
5. **Shutdown with a full queue** returns promptly and leaks no goroutines. Pinned by `TestWarmer_ShutdownDropsTheQueue` (Task 2).

---

### Task 1: Warm recipes and the warm render

**Files:** `internal/httpx/warm.go` (new: the recipe type, recipe store, `warmKey`), `internal/httpx/handler.go` (record a recipe in `writeCache`; `renderSafeContext` keeps the fresh-data marker), `internal/httpx/document.go` (record a recipe in `writeDocumentCache`), and tests in `internal/httpx/warm_test.go`.

**Interfaces produced (internal):**
```go
type recipe struct {
	host, path string
	query      url.Values // only the keys the cache key varies on, as written
	vary       []string   // requestVary(r) entries "Header=value", as written
	document   bool
}
// recipes: bounded (Deps-provided cap, default 10000), most-recently-written order,
// guarded by a mutex; methods: put(key, recipe), get(key) (recipe, bool),
// forPath(host, path) []string (keys), newestFirst(keys []string) []string.
func (h *Handler) warmKey(ctx context.Context, key string) (ok bool, err error)
```

**Behaviour:**
- `writeCache` and `writeDocumentCache` record the recipe after a successful write. Read how the cache key is built (`cache.Key` with `queryVary`, `requestVary`, and the lower-cased Host) so the recipe holds exactly what rebuilds that key.
- `warmKey`:
  1. Look up the recipe; a missing recipe returns `(false, nil)`.
  2. Build `GET http://<host><path>?<query>`.
  3. Give its context a vary set already holding the recipe's `vary` entries, frozen, the way `requestVary` expects (read `external.go` varySet).
  4. Wrap the context with `types.WithFreshData`.
  5. Serve it through `h.ServeHTTP` with a discarding ResponseWriter that records the status and `X-Collage-Cache`.
  6. `ok` is true only when the response was 200 and the key is now fresh in the store (check `GetStale` stale==false when the store is a `StaleCache`; otherwise trust status 200 with header MISS or HIT).
  7. A STALE or non-200 response is `(false, err-describing-it)`.
- `renderSafeContext` must carry the fresh-data marker (`types.WantsFreshData`) into the shared render context, alongside the route and vary set it already keeps. Check how `IsCapture` is carried, and do the same.
- `warmKey` must not trigger the dev-reload script or personalisation side effects that set cookies on the discarded writer; discarding is fine. It must not run in dev mode (return `(false, nil)`).

**Tests:** `TestWarm_RecipeRebuildsTheSameKey` (write via a real request, then warmKey writes the same key; assert through `X-Collage-Cache` HIT on a following real request and only one entry in the store), `TestWarm_AVaryVariantIsWarmedUnderItsOwnKey` (an `App.Use` middleware or test hook that declares a vary header, two variants served, both warmed under their own keys after invalidation), `TestWarm_RendersFromFreshData` (data via `Cached`; invalidate; warmKey gives fresh content and a stored entry, not STALE), `TestWarm_DocumentsToo`, `TestWarm_UpstreamDownReportsFailure` (returns ok=false with an error, and the stale entry stays). Use pkg/collage or internal/httpx test helpers as the existing stale-page tests do (`internal/httpx/stalepage_test.go`, `pkg/collage/stalepages_test.go`).

**Verify:** `go test -race -count=1 ./internal/httpx/ ./pkg/collage/`. Commit.

---

### Task 2: The background warmer and core wiring

**Files:** `internal/httpx/warm.go` (warmer), `internal/httpx/handler.go` (`Deps.WarmLimit int`, start and stop), `internal/core/app.go` (call the warmer with resolved keys after `dropTags` finishes; stop it in `Shutdown` before `data.Close`), `internal/core` config (`CacheConfig.WarmLimit`), `pkg/collage/config.go` (`WarmLimit` with doc), `internal/observability/metrics.go` + `pkg/collage/collage.go` (`CacheWarm`), tests.

**Interfaces produced:**
```go
func (h *Handler) Warm(keys []string)            // non-blocking enqueue
func (h *Handler) StopWarming(ctx context.Context) // drop queue, wait running renders or ctx
```

**Behaviour:**
- `Warm` enqueues a run: deduplicate against keys already queued, order newest first by recipe, and cap at WarmLimit (0 means 1000; negative disables, so `Warm` is a no-op). Keys over the cap are dropped from the run.
- One coordinator goroutine and 4 workers (constant `warmWorkers = 4`), started lazily on the first `Warm`.
- A run processes its keys with the workers. On the first `ok=false` with a non-nil error:
  - stop handing out the rest of that run;
  - log one warn: "collage: warming stopped, a page failed to render", with the error, the remaining count and the run size;
  - drop the rest.
- After each run, one info line: "collage: warmed pages", with warmed, skipped, failed and the duration.
- `observability.CacheWarm` with the key for each successful warm.
- No warming in dev mode, during a static build or capture, or with the cache off (the handler has no store). Core does not call `Warm` then.
- Core: in `dropTags`, collect the resolved keys and call `h.Warm(keys)` **after** the deferred finished-counter bump, so the warm renders are storable. A `defer` ordered before the counter's `defer` works; alternatively, call it after `dropTags` returns, in `InvalidateTagsN` and `PurgeTags`. Purge warms too.
- `Shutdown`: `StopWarming(ctx)` before `data.Close(ctx)`.

**Tests:**
- `TestWarmer_WarmsAfterInvalidate`: 50 pages using `Cached` list data; invalidate the list tag; wait for the warmer to drain (add an unexported test hook such as `h.warmIdle()`); then every page is a HIT with new content, with exactly one upstream list fetch and no request in between.
- `TestWarmer_StopsAtTheFirstFailure`: upstream down; one failed render, one warn, the rest skipped, pages STALE.
- `TestWarmer_LimitAndOrder`.
- `TestWarmer_NegativeLimitDisables`.
- `TestWarmer_DedupesQueuedKeys`.
- `TestWarmer_VisitorDuringWarmJoinsTheFlight`: a render counter shows 1.
- `TestWarmer_ShutdownDropsTheQueue`: blocked renders; Shutdown with a short ctx returns; goroutines finish.
- `TestWarmer_OffInDevAndBuild`.
- `TestWarmer_SkipsMiddleware`: an `App.Use` counter is not incremented by warm renders.
- `TestPurgeTags_Warms`.

**Verify:** the full suite with `-race -count=1`, and `-count=10` for `Warm`. Commit.

---

### Task 3: An action warms its redirect target first

**Files:** `internal/httpx/action.go`, `internal/httpx/warm.go` (`forPath`, plus removing keys from the queue), tests in pkg/collage.

**Behaviour:**
- In the action flow, after `h.invalidator(ctx, result.InvalidateTags)` and before `writeActionResult`: if `result.Location` is a same-origin path (relative, or an absolute URL whose host equals the request's), resolve its path. Then, for each recipe key from `forPath(lower(r.Host), path)`:
  1. Remove it from the background queue.
  2. Call `warmKey` synchronously with a context derived from the request: detached from the client's cancellation, but bounded by the fragments' own timeouts.
- Failures are logged at debug (the redirect still goes out). The page stays STALE, as in v0.63.
- Only when warming is enabled (WarmLimit ≥ 0, not dev, cache on).

**Tests:**
- `TestAction_RedirectTargetIsFreshOnFirstLoad`: an action invalidates and redirects to `/p/x`; the next GET of `/p/x` is a HIT with the new content.
- `TestAction_UpstreamDownStillRedirects`: the redirect is sent and the target is STALE.
- `TestAction_WarmsTheRedirectTargetOnce`: a render counter; the target is also in the background run, and is rendered once.
- `TestAction_CrossOriginLocationIsNotWarmed`.

**Verify:** the full suite. Commit.

---

### Task 4: Docs and changelog

**Files:** `docs/caching.md` (a new "Warming after an invalidation" section: what is warmed, recipes, variants, WarmLimit, stop-on-failure, middleware skipped, observability), `docs/actions.md` (the redirect after `InvalidateTags` is now fresh on first load; the stale case only when the upstream is slow or down), `docs/configuration.md` if it lists CacheConfig fields, and `CHANGELOG.md` `## v0.64.0`. Fix every v0.63 statement that says the first load after an invalidation is stale. Commit.

---

### Task 5: Verification, e2e, review, merge

Done by the controller:
- the full suite with `-race -count=1`;
- the scaffold tests with `-count=1`;
- no new `any`;
- e2e on the scratch blogbench app (ose-landing has no runtime invalidation entry point, and recipes do not survive a restart): invalidate, and after about 1 s every page is a HIT with fresh content and no request in between;
- the whole-branch review, the fix wave and the merge.

Then **ask the user before the tag and the push.** The docs site follows the release.
