# Host hardening after the 2026-10-27 audit (design)

Source: the Host/core-API audit of 2026-10-27 (report `~/Desktop/guvenlik-denetimi-2026-10-27.pdf`;
detailed findings and PoCs in the session scratchpad `…/scratchpad/audit/findings/*.md` and the
`audit1027_*_test.go` files in worktree `~/Desktop/collage-audit-1027`). The user ruled the first
decision; the rest are Claude's calls ("kararları sen ver"), listed at the end.

## Goal

A plugin's view of the site matches HTTP's, Host keeps the guarantees its doc comments state, and the
surface frozen at v1 is the surface plugins use. Ships as collage v0.78.0 (minor: API removals), with
plugin releases for the plugin-side findings.

## Core changes

### 1. RenderFragment is equivalent to an HTTP request for the fragment's URL
`internal/core/fragmentrender.go` (`App.RenderFragment`, `resolveFragmentRequest`).
- **Path values (HOST-07, High).** The request handed to the guard and the render carries every path
  parameter via `SetPathValue`, as `internal/httpx/handler.go` does for a real request. Path branch: on
  `forFragment`. By-name branch: clone r, point its URL at the fragment's URL built from the page's
  pattern + params (`App.URL` semantics), and refuse params the pattern does not declare or values the
  router could not produce (contain `/`, are `.`/`..`, empty) with `ErrUnknownFragmentPath`. The caller's
  own path values never leak through `r.Clone`.
- **Middleware (PLUGA-B01, High) — user's ruling: run the chain.** The fragment render runs inside the
  application's and every plugin's middleware chain, as a request to the fragment's URL would (same
  order as `ServeHTTP`; same prefix/skip semantics). The terminal handler of that chain performs the
  render. If a middleware answers instead of calling next (401 basicauth, 429 ratelimit, a redirect),
  RenderFragment returns `ErrGuardRefused` wrapping the status, and nothing the middleware wrote reaches
  the caller (it writes into a discarding recorder). Context values middleware sets (session user, tenant)
  are visible to the guard and render, as over HTTP. Headers/cookies middleware sets are dropped (the
  caller owns its own response). Document this in the Host and App doc comments.
- **Personalise (HOOKS-01 / PLUGA-01 / B03, Medium).** RenderFragment runs every `PersonaliseHook` on
  the fragment's HTML before returning, as the response path does (CSP nonce, honeypot signature). A
  personalised result is `Shared = false`.
- **`Personal` is sticky (HOOKS-07).** Once a PersonaliseHook sets `ev.Personal = true`, a later hook
  cannot clear it (the dispatcher ORs).

### 2. Host guarantees
- **Pages()/Page() copy (HOST-01, Medium):** use the existing deep `types.ClonePage` (or extend it)
  so `CacheParams`, `FragmentPaths` (outer and inner maps), `Actions` (each `*Action` and its
  `Methods`) are not shared. Update the doc comment to say exactly what is still shared (fragment
  pointers, if any).
- **ConfigHost closes after Configure (HOST-02, Medium):** `AddTemplateFunc`, `AddRenderFunc`,
  `WrapMount` called after that plugin's Configure returned return a new sentinel
  `ErrConfigureClosed` (exported in errors.go, documented) and change nothing.
- **Template function names (HOST-03, Medium):** reserve every text/template and html/template builtin
  (`and call html index slice js len not or print printf println urlquery eq ge gt le lt ne` plus
  html/template's escaper names) for AddTemplateFunc/AddRenderFunc → `ErrDuplicateTemplateFunc`. The
  application's own `Config.Template.Funcs` win over a plugin's AddRenderFunc of the same name, as
  `app.go` already promises (PLUGB-05); a collision is logged once at startup.
- **Config isolation (HOST-08, Low):** the host values no longer embed an exported `ConfigSource`
  holding the whole map. Each host holds only its own plugin's section (copied `json.RawMessage`) in an
  unexported field. No sealed-API change (ConfigSource is internal).
- **Body limit under Handle (HOST-10, Low):** `Host.Handle` prefixes (inside the middleware chain) get
  `MaxBodyBytes` like every other route. `App.Handle` keeps its documented behaviour (bare: none).
- **RegisterCommand (HOST-04, Low):** refuses names the dispatcher answers itself (`collage-check`,
  `collage-inspect`, `collage-routes`, the built-in CLI commands) and names that are not one shell word,
  and is refused after Start like every other registrar.
- **Invalidation dispatch (HOOKS-04, Low):** every CacheInvalidateHook is called even when one fails or
  panics (panic recovered and logged); errors are joined.
- **Fragment ETag (HOOKS-02, Low):** a fragment response with a form gets an ETag over the unmasked
  body, consistent with RenderFragment's.
- **RedirectSource (HOOKS-08, Low):** core validates plugin redirect rules (status 301/302/303/307/308,
  `From` starts with `/` and not `//`, `To` is a site path or http(s) URL, no control characters) and
  drops invalid ones with a build finding.

### 3. API narrowing (Claude's calls, measured zero users)
Remove from the sealed surface before v1 (`pkg/collage` and internal aliases; api_test allowlists):
- `Host.InvalidateTags` (0 users; request-time CDN/indexnow fan-out). `App.InvalidateTags` stays.
- `ConfigHost.BaseURL`, `ConfigHost.OriginFor`, `ConfigHost.MultiOrigin` (0 users; MultiOrigin was
  order-dependent at Configure). Host keeps all three.
- `ApplicationPlugins` (plugin.Lister) on the **Init** host. It stays on ConfigHost, so `FindPlugin`
  works from Configure (both users, live and websocket, call it there); FindPlugin's doc says so.
- `ErrorEvent.Page`, `BeforeActionEvent.Page` (0 readers; live `*Page`).
Kept: `FragmentRequest`'s by-name mode (apps may use App.RenderFragment; it is made guard-equivalent by
§1 instead).

## Plugin changes (separate releases)
- **offline (PLUGB-01, Medium):** `storable()` in sw.js also refuses `Cache-Control: private`; on
  logout-type navigation nothing changes (documented).
- **i18n (PLUGB-04, Medium):** `{{th}}` / `Translator.Funcs()["th"]` filter substituted values that land
  in URL attributes (href/src/action/formaction…) the way html/template does (`#ZgotmplZ` for
  non-http(s)/mailto/relative), and quote-aware attribute substitution; README corrected.
- **session (PLUGA-02, Medium):** the session cookie is bound to the host it was issued on (the host
  goes into the signed/encrypted payload and is checked), so a cookie replayed on another tenant's host
  is refused.
- Lows (one patch each, when touched): basicauth README (app.Handle not covered), opti-image output
  dimension cap and recipe/disk bounds, tenant negative-cache for unknown hosts, ratelimit/fail2ban
  IPv6 /64 grouping option, otel untrusted traceparent option + method bucketing, honeypot learned-path
  cap behaviour, flash cookie size, validate trim-before-check, oauth anonymous login cookie, accesslog
  coverage notes. Scheduled after the core release; not part of v0.78.0.

## Not changed (Claude's calls)
- Mount serving `.html`/`.svg` (CORE-10): legitimate for static sites; documented as an uploads warning
  instead.
- Cache key Host/query axes (CORE-03): needs its own design (allowlisted query keys); tracked.
- Open redirect via app-supplied Location (CORE-01), XFP trust without TrustedProxies (CORE-02): by
  design, documented (SafeRedirect, TrustedProxies).

## Tests
Each audit PoC that demonstrated a finding becomes a regression test asserting the fixed behaviour
(the `audit1027_*` tests and `poc/*` modules are the inputs). The live/websocket end-to-end PoCs
(HOST-07, PLUGA-B01, HOOKS-01) run in CI via the plugin matrix after release; locally via go.work.

## Release
collage v0.78.0 (CHANGELOG: Breaking = API removals; Fixed = the rest) → plugin re-releases whose
tests or code touch removed API (expected none) → offline, i18n, session → docs site (EN+TR) →
extension if the catalog/schema changes. Lint (staticcheck v0.7.0, govulncheck with go1.26.10) and the
plugin matrix (`gh workflow run ci.yml`) before tagging.
