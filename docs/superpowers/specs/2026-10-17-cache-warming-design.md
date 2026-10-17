# Cache warming after invalidation (v0.64.0)

Status: design agreed with the user on 2026-10-17 in chat. This is the written
spec, for the user to review before the plan.

## Why

Since v0.63 an invalidated page is served stale on its first request, and that
request's render replaces it. Visitors never wait, but an editor who saves and
is redirected sees the old page, and has to refresh a second time. The user does
not want that second refresh. The user also does not want any page view to wait
(a 2s grace was rejected).

## Goal

After an invalidation, the affected pages are re-rendered in the background
right away, before anyone asks. The one request that must see the change at
once, an action's redirect after `ActionResult.InvalidateTags`, waits inside
the action (the form submit), never on the page view.

| Who                              | Waits?                                     |
|----------------------------------|--------------------------------------------|
| A visitor                        | Never: served the stale page or the warmed one |
| An editor's form submit (action) | +1 render of the redirect target           |
| A CMS webhook                    | Never: warming runs in the background      |

## Measured cost (blogbench, 2026-10-17)

- A 500-post blog: re-rendering every page with its data cached takes about
  75 ms CPU in total.
- An ose-landing-like site: 1–5 ms per page, so 500 pages is about 0.5–2.5 s
  CPU, or roughly 0.2–0.7 s of wall time with 4 workers.
- Upstream calls are only the changed data: one for `blogs:last5`.
- There is no extra total work. These renders happen today anyway, and warming
  only moves them earlier.

## Design

### Warm recipes

- When the handler writes a page or document to the cache, it also records how
  to rebuild that response. This is the **warm recipe**, kept in memory per
  cache key:
  - host;
  - path;
  - the query values the key varies on;
  - the request-vary header values (`requestVary`), meaning the values
    middleware declared through the vary set;
  - whether it is a page or a document.
- Recipes are bounded the way the tracker is (`MaxEntries`, default 10000), and
  are evicted oldest first.
- Recipes are in memory only. After a restart there are none until pages are
  written again. The tracker is in memory too, so this matches what an
  invalidation can resolve.
- Warming renders from the recipe, so it writes exactly the cache key a visitor
  produced. Every variant that was actually served (i18n, tenant, query) is
  warmed. A variant nobody requested is not warmed.

### The warmer

- Core passes the resolved keys of every `InvalidateTags` and `PurgeTags` call
  to the handler's warmer. It does this after `dropTags` finishes, so the
  two-counter epoch lets warm writes through.
- A single background warmer with a fixed pool of 4 workers takes keys from a
  queue.
  - Keys already queued are not queued again.
  - Each run is capped at `Cache.WarmLimit` keys (default 1000; a negative value
    turns warming off), taking the most recently written keys first. Keys over
    the cap are left stale and are rendered on their first request, as in v0.63.
- A warm render goes through the inner page and document handler, not the
  application's or the plugins' middleware. Rate limits, access logs and auth
  never see it.
  - It uses a synthesized GET request built from the recipe.
  - Its context carries the recipe's vary set, already frozen, and is marked
    `types.WithFreshData`, so it waits for the upstream and never uses stale
    data.
  - This is the same view a shared render already gets
    (`renderSafeContext`: route plus vary set only).
- The outcome follows the normal write rules. A good render is stored (fresh
  `HIT` from then on). A failed or degraded render stores nothing and leaves the
  stale page.
  - **On the first failed warm render, the run stops**, so a down upstream is
    not hammered. One warn line is logged, naming the error and how many keys
    were skipped.
  - If a visitor asks for a page while it is being warmed, they join the same
    flight and share the render.
- Warming never runs in dev mode, in a static build, or when the cache is off.
- It stops on `Shutdown`: the queue is dropped, and running renders finish
  under the shutdown context.
- Observability:
  - a new `observability.CacheWarm` event for each warmed key, re-exported as
    `collage.CacheWarm`;
  - one info line per run with the number warmed, skipped and failed, and the
    duration.

### Action redirect: warm the target first

- When an action returns `InvalidateTags` together with a same-origin
  `Location`, the action handler warms that target before it writes the
  response:
  1. It runs the invalidation, as today.
  2. It looks up the recipes whose host equals the request host and whose path
     equals the `Location` path.
  3. It renders those keys synchronously, with the same rules as the warmer.
  4. It then sends the redirect.
- The editor's redirected page view is then a fresh `HIT`.
- The wait is bounded by the fragments' own data timeouts. If the upstream is
  slow or down, the redirect is still sent and the page is stale, as in v0.63.
- These keys are also removed from the background queue, so they are not
  rendered twice.

### API (additive)

- `CacheConfig.WarmLimit int`: the maximum number of pages warmed after one
  invalidation. 0 means the default (1000), and a negative value turns warming
  off.
- `collage.CacheWarm` metrics event.
- Nothing else is exported. The recipe store and the warmer are internal.

## Acceptance tests

1. Invalidate `blogs:last5` with the upstream up. After the warmer drains, every
   affected page is a `HIT` with the new content, and nobody requested it in
   between. There is exactly one upstream call for the list.
2. An action with `InvalidateTags` and a `Location`: the first request to the
   redirect target is a `HIT` with the new content.
3. The same action with the upstream down: the redirect is still sent within
   the fragment timeout, and the target is served `STALE`.
4. Upstream down during warming: the run stops after the first failure, one
   warn is logged, and the pages stay stale.
5. A variant keyed by a vary header (for example a middleware-declared
   `Accept-Language`) is warmed under its own key.
6. `WarmLimit` caps a run, and the most recently written keys are warmed first.
   A negative value warms nothing.
7. A visitor during warming joins the flight, so the page is rendered once.
8. No warming in dev mode, in a static build, or with the cache off.
   `Shutdown` stops the warmer.
9. Warm requests never pass through `App.Use` middleware or plugin middleware.
10. `PurgeTags` warms too: the page is rendered fresh, with no stale copy in
    between.

End to end on ose-landing: warm the site, mark the CMS data and pages stale
through an invalidation, wait about 1 s, and every page is a `HIT` with fresh
content and no request in between.

## Constraints

- No new `any`. Additive API only (`WarmLimit`, `CacheWarm`).
- Branch `feat/cache-warming` in a worktree; CHANGELOG `## v0.64.0`.
- Tag and push only after the user has seen the spec and the plan, as for
  v0.62. The `/goal` go-ahead covered v0.63 only.
- Docs site after the release.
