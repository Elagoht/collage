# Layout chains and guards

Date: 2026-09-27
Status: approved design, pending implementation

## Motivation

Two developer-experience gaps, both reported from a real application:

1. **Auth guarding requires a hand-rolled middleware plugin.** The only
   interception point before this design is `Host.Use` middleware, which runs
   before routing. A guard written there re-derives routing knowledge the
   framework already has: it lists protected paths by hand (exact match only —
   no patterns, no locale prefixes), and the policy lives far from the page it
   protects. The desired model is the Next.js one: a layout declares itself
   private, and every page it wraps inherits the protection.
2. **Nested layouts force builder-returning helpers.** A `Page` has exactly one
   `LayoutFragment`, filled by registration. A layout with a hole to fill per
   page (an auth shell around the login form) therefore cannot be a finished
   `*Fragment`; helpers must return `*FragmentBuilder` and every page composes
   the nesting by hand. N levels of layout mean N nested `WithSlotFragment`
   chains.

Pre-1.0: no backward compatibility is preserved. `WithLayout` is removed
outright and `InspectedPage.Layout` is replaced, rather than kept alongside
their successors.

## Goals

- `WithLayouts(outermost ...innermost)`: a page declares an ordered layout
  chain; registration folds content into the innermost layout and wraps each
  layout into the next outer one.
- Guards declared on layout fragments (and the content fragment), dispatched by
  the framework after routing resolves the page and before cache is read.
- The policy — "is this reader logged in" — stays in `collage-session`; the
  core learns only the dispatch mechanism.
- `collage inspect` reports the chain and the guards.

## Non-goals

- Personalization. A guard gates access; it does not vary the render per
  reader. Per-reader variation stays with the existing `Vary`/personalise
  machinery.
- Role or permission hierarchies. `collage-session` may grow them later; the
  core's `GuardFunc` is deliberately policy-free.
- Guards on documents, standalone actions, or non-spine fragments.
- Any session or credential handling in core.

## API

### `PageBuilder.WithLayouts`

```go
func (b *PageBuilder) WithLayouts(outermost ...*Fragment) *PageBuilder
```

- Arguments are ordered outermost first, innermost last. The content fragment
  lands in the innermost layout's `content` slot (`DefaultContentSlot`).
- `WithLayout` is removed. A single layout is `WithLayouts(master)`.
- Builder rules, recorded on the builder and surfaced at registration as
  existing builder errors are:
  - zero arguments — `ErrMissingLayout` (a page wanting no layout simply does
    not call the method);
  - a nil entry — `ErrNilFragment`;
  - the same fragment pointer twice in one chain — `ErrFragmentCycle`;
  - calling `WithLayouts` twice — `ErrConflictingLayout`, following the
    convention of `WithData` + `WithDataHandler` → `ErrConflictingData`.
- All layouts in a chain use the `content` slot convention. A named-slot chain
  (layout B wrapping into layout A's `"body"` slot) is out of scope.

### Guard types

```go
// GuardFunc decides whether a request may reach what it guards. A nil
// decision with a nil error allows it.
type GuardFunc func(ctx context.Context, r *http.Request) (*GuardDecision, error)

// GuardDecision is a guard's answer: a redirect (Status 3xx with Location) or
// a bare refusal status such as 401 or 403.
type GuardDecision struct {
    Status   int
    Location string
}
```

`FragmentBuilder.WithGuard(g)` sets `Fragment.Guard`. A nil `Guard` — the vast
majority of fragments — means no guard.

## Registration folding

`bindContent` generalizes from one layout to a chain:

1. Each layout in the chain gets its per-page slot-table copy (`copyLayout`,
   unchanged): pages sharing a layout value never share slot bindings.
2. The content fragment is bound into the innermost copy's `content` slot. If
   the innermost layout already holds the content fragment in that slot (the
   existing hand-bound escape hatch, covered by
   `TestRegisterPage_HandBoundLayoutStillGetsItsOwnCopy`), the bind is skipped.
3. Each copy is bound, inner to outer, into the next outer copy's `content`
   slot. A `content` slot already filled on an outer layout is a registration
   error — the existing rule ("registration fills the content slot itself, so
   a layout must not have it filled already") extended to every chain entry.
4. `p.LayoutFragment` ends as the folded root; `p.LayoutChain` (new field)
   holds the original fragments outermost-first, for inspection and guard
   collection. `Root()` is unchanged.

Guard collection is a `Page` helper, not stored state: it walks `LayoutChain`
then `ContentFragment` and returns the non-nil `Guard` values in that order.

## Guard dispatch

The spine rule, within a page render: guards run only on the page's spine —
its layout chain and its content fragment. Guards on slot children,
resolver-returned fragments, and fallbacks are ignored. Access policy belongs
to the route's spine, not to rendering parts. A fragment path is not a page
render; it is its own route, and the fragment's own `Guard` is its whole
policy — nothing inherited, nothing underneath.

Ordering and short-circuit: outermost layout first, then inwards, the content
fragment last. The first decision that blocks wins; no further guard runs. A
guard returning an error fails the request (500) immediately, through the
existing `serveFailure` path, under a new `stageGuard` metric stage.

Dispatch points in the HTTP handler:

- **Page render (GET/HEAD):** after the router resolves the page, before the
  cache is read, and before `PageResolved` fires. Blocked readers never reach
  the cache; a `Static()` private page is safe because the guard — not the
  cache — is the gate. A blocked request does not appear to
  `PageResolvedHook` plugins: it never touched the page.
- **Page-attached action:** the owning page's spine guards run before the
  action handler. (The router node already holds both the action and its
  page.) A form on a private page cannot be submitted logged-out. Actions
  registered standalone have no spine and run no guards.
- **Fragment path:** the fragment served at its own URL runs only its own
  `Guard`. It inherits nothing from the page that declared it — a fragment
  path is its own route with its own policy.
- **Documents:** unaffected.
- **NotFound/ErrorPage renders:** guards are skipped. These are framework
  fallbacks; running guards on them invites loops (a private error page →
  redirect → error → …).
- **Router redirects, 405, OPTIONS:** no page is served; no guards run.

Redirect answers are written the way the router writes its own redirects:
`Location` header plus status, no body.

Guard ≠ personalization: allowed readers share the page's cache entry. A page
whose render differs per reader keeps using `Vary`/personalise; a guarded page
may be `Static()` when its content is identical for every allowed reader.

## collage-session

Separate repository (`github.com/Elagoht/collage-session`), released after the
core release per the usual plugin order:

```go
// RequireUser returns a guard that redirects a logged-out reader to
// loginPath with a "next" parameter carrying the current URI.
func RequireUser(loginPath string) collage.GuardFunc
```

Logged out: `303`, `loginPath + "?next=" + url.QueryEscape(r.URL.RequestURI())`.
Logged in: allowed. The application's hand-rolled guard plugin is deleted
outright.

## Inspect

`InspectedPage`:

- `Layout string` is removed; `Layouts []string` (outermost-first) replaces it.
- `Guards []string` lists the spine fragment names that carry a guard.

The VS Code extension's catalog/schema is regenerated after the core release.

## Documentation

- `docs/fragments.md`: the pages/layouts section gains the chain, and
  `WithLayout` examples become `WithLayouts`.
- Guard dispatch: a section in `docs/routing.md` (access control sits with
  routing) — spine rule, ordering, cache relationship, fallback skips.
- `CHANGELOG.md`: unreleased entry.
- Docs site (`~/Desktop/collage-docs`, EN+TR) after the release.

## Testing

TDD throughout; the builder and registration tests come first.

- Builder: zero-arg `WithLayouts`, nil entry, duplicate pointer, double call —
  each records its sentinel; `BuildErr` surfaces them; registration refuses a
  page carrying one.
- Folding: a two-layout chain renders content inside the inner layout inside
  the outer one; two pages sharing layout values keep private slot tables
  (existing invariant extended to chains); hand-bound innermost still gets its
  copy; a pre-filled outer `content` slot is refused.
- Guards: run before cache (a cached private page still redirects a logged-out
  reader); page-attached action inherits; standalone action does not;
  fragment path runs its own guard only; NotFound/ErrorPage skips; ordering
  short-circuits at the first block; a guard error serves 500 at `stageGuard`;
  the redirect answer carries `Location` and no body; guard order is
  outermost→innermost→content.
- Inspect: `Layouts` and `Guards` reported.
