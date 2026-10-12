# Plugin sealing inventory — reviewer 3

Plugins: errortrack, paginate, oauth, flash, search, otel, meta, htmlcheck, prometheus, cdnpurge, bundle, devtoolbar.
Read-only review of the local clones at the HEADs tagged for collage v0.60.0 (flash is still on collage v0.50.0, see Bugs).

The "lifecycle" row in each table covers `Plugin.Name/Version/Init/Shutdown/Configure`. It also covers the hook methods the core interfaces require (OnError, OnBeforeRender, OnAfterRender, OnBuildFinished, OnCacheInvalidate, OnRequest, StartSpan, the Metrics methods). These signatures belong to core, so the plugin cannot change them.

"v0.61 constants" means `collage.RouteKind*`, `collage.Built*` and `collage.Stage*` from docs/superpowers/specs/2026-10-12-v061-design.md. They are not released yet.

---

## collage-errortrack (v0.1.7)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| `Name` | const | KEEP | | convention | |
| `New(opts Options) *Plugin` | func | KEEP | | matches the proposed convention | |
| `Plugin` | type | KEEP | It has no exported fields. The TSV's "1 exported field" is the embedded unexported `sender`, and `go doc` shows none. | | |
| lifecycle + `OnError` | methods | KEEP | | core interfaces | |
| `Plugin.Capture(ctx, err)` | method | KEEP | | the only way to report from a background job | |
| `Options` | type | KEEP | | | |
| `Options.DSN` (`json:"-"`) + `dsnEnv` | field/key | KEEP | This is the model for the proposed secret convention: a `<x>Env` key plus a Go-only value. | | |
| `Options.SendIP` / `sendIP` | field/key | CHANGE | The key stays. The value it sends changes from `r.RemoteAddr`'s host to `collage.ClientIP(r)`. | Behind `Server.TrustedProxies` it currently sends the proxy's address, not the reader's. | none (behaviour fix) |
| `Options.HTTPClient` | field | KEEP | This is the name to standardise on; cdnpurge and indexnow say `Client`. | | |
| `Duration` (+`UnmarshalJSON`) | type | ASK | Either core adds `collage.Duration` in v0.61.0 (additive) and errortrack uses it, or every plugin keeps its own copy. | 8 plugins carry identical copies: errortrack, cdnpurge, fail2ban, health, live, ogimage, opti-image, tenant. | the type of `Options.Timeout`; README |
| `Event`, `Exception`, `Frame`, `RequestInfo`, `User` | types | KEEP | | `BeforeSend` and `User` hand them to user code | |
| `Event.Tags["route.kind"]`, `["stage"]` | tag values | CHANGE | Compare and emit through the v0.61 constants (`RouteKind*`, `Stage*`). Keep `"capture"` as the plugin's own stage, documented. | Use core's names, not raw strings. | none |
| `requestInfo` URL scheme | behaviour | CHANGE | Build the scheme from the proposed `collage.IsHTTPS(r)` (see Cross-cutting). | Behind a TLS-terminating proxy every event says `http://`. | none |

plugin verdict: patch-level (only the Duration type changes if core adopts one; everything else is behaviour)

---

## collage-paginate (v0.1.1, a library: no manifest or config)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Version` | const | REMOVE | Users read the version from go.mod or `debug.ReadBuildInfo`. | It is not a plugin, nothing reads the const, and it is hand-maintained, so it can drift from the tag. | none found |
| `New(total, perPage, current) Pager` | func | KEEP | It returns a `Pager`, not a `*Plugin`. Exempt it from the plugin convention as a library. | This is the idiomatic name for a value constructor. | |
| `Pager` (+ 9 fields), `Pager.WithWindow`, `Slot` | type/method | KEEP | | | |
| `Items[T any]` | func | KEEP | `any` here is a type-parameter constraint, not an `any` value. | | |
| `Linker`, `Linker.URL`, `Linker.For`, `Links`, `LinkSlot` | types/methods | KEEP | | | |
| `Path(first, pattern)` | func | KEEP | | | |
| `PathNamed(rc, first, rest)` | func | KEEP | It hard-codes the path parameter `"n"`. | | |
| `Query(rc, key)` | func | KEEP | | | |
| `StaticParams(count, perPage)` | func | KEEP | It hard-codes `"n"`. | | |
| `FromPath(rc, param, last)` | func | CHANGE | Drop `param` and read `"n"`: `FromPath(rc, last)`. | `PathNamed` and `StaticParams` only ever produce `n`, so any other `param` gives a listing whose links and static params do not match its reader. One fact should have one spelling. | collage-docs static-export.md EN line 304 and TR line 324; README line 57 |
| `FromQuery(rc, key, last)` | func | KEEP | | It pairs with `Query(rc, key)`. | |

plugin verdict: API changes (Version removed, FromPath signature)

---

## collage-oauth (v0.1.3)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| lifecycle | methods | KEEP | | | |
| `Plugin.ServeHTTP` | method | UNEXPORT | Init mounts an unexported handler: `host.Handle(prefix+"/", http.HandlerFunc(p.serve))`. | It is exported only so that `host.Handle(p)` compiles. Users could mount it elsewhere, bypassing Configure's checks. | none found |
| `Plugin.Client(ctx, userID, provider)` | method | KEEP | | | |
| `Plugin.Revoke(ctx, userID, name)` | method | CHANGE | Rename the parameter to `provider`, matching `Client`. | Same fact, same word. A parameter name is not an API break, but it shows up in go doc. | none |
| `ErrNotLinked`, `Identity`, `LoginFunc`, `TokenStore` | var/types | KEEP | | | |
| `Options`, `Provider` | types | KEEP | | | |
| `Options.Key` + `KeyHex` (`key`) | fields/key | CHANGE | Follow the secret convention: keep `Key []byte json:"-"` for Go, replace `KeyHex`/`key` with `KeyEnv`/`keyEnv`, which names an env var holding hex. | A sealing key in plugins-config.json gets committed. `clientSecretEnv` next to it already uses the Env form. Today there are two Go fields for one fact and "KeyHex wins". | session, secure, honeypot and flash have the same pair (other reviewers) |
| `Options.PreviousKeys` + `PreviousKeysHex` (`previousKeys`) | fields/key | CHANGE | Becomes `previousKeysEnv` (a list of env var names), same rule as above. | as above | |
| `prefix` key | config | CHANGE | Accept it with or without a trailing `/` and normalise. bundle demands the trailing `/` and oauth strips it. | One rule for every prefix key. | none |
| error codes `state/expired/denied/exchange/unavailable/token/rejected` | `?error=` values | KEEP | They are documented in the README table. | | |
| template func `oauthLogin` | func name | KEEP | | | |
| route `{prefix}/{name}/login`, `/callback`, default `/auth` | routes | KEEP | | | |
| manifest | collage.json | CHANGE | Add `"requires": ["elagoht/session"]`. Add a `setup` note that `OnLogin` must be set in Go and a provider configured. | It imports collage-session and fails without it. `collage plugin add` writes `oauth.New(oauth.Options{})`, which can never start (OnLogin is required). | snippets extension catalog |

plugin verdict: API changes

---

## collage-flash (v0.1.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| lifecycle | methods | KEEP | | | |
| `Success/Info/Warning/Error` | consts | KEEP | | | |
| `Message` (`Kind`, `Text`) | type | KEEP | | The template sees it. | |
| `Add(rc, kind, text)`, `AddTo(ctx, kind, text)`, `Take(rc)` | funcs | KEEP | | | |
| `Options.Key` + `KeyHex` (`key`) | fields/key | CHANGE | Becomes `Key []byte json:"-"` plus `KeyEnv`/`keyEnv`. | secret convention | docs plugins.md flash section |
| `maxAge` (int seconds) | key | ASK | Either keep cookie-style integer seconds (as session, honeypot and consent do), or switch every cookie lifetime to a duration string like `"5m"` (as errortrack, cdnpurge and live do). | There are two duration encodings across plugins. | session, honeypot, consent |
| cookie `collage_flash` / key `cookie` | cookie | KEEP | | | |
| template func `flashes` | func name | KEEP | | | |
| `Secure` flag from a raw `X-Forwarded-Proto` | behaviour | CHANGE | Use the proposed `collage.IsHTTPS(r)`, which honours `TrustedProxies`. | It is the same hand-rolled check as secure and session. | |
| manifest snippets | collage.json | CHANGE | One class scheme: the snippets use `flash--{{.Kind}}` and `flash-{{.Kind}}`. | inconsistent | extension snippets |

plugin verdict: API changes (config key)

---

## collage-search (v0.1.6)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| lifecycle + `OnBuildFinished` | methods | KEEP | | | |
| `IndexFile`, `IndexPath` | consts | KEEP | | The index's location is part of the API (users' own JS and tests). | |
| `ScriptPath` | const | UNEXPORT | | It is a mount detail. `{{searchBox}}` resolves it through `rc.Asset`, and no user needs it. | none found |
| `Entry`, `Heading` | types | KEEP | | They are the documented index format, readable from Go. | |
| `Options`, `Text` | types | KEEP | | | |
| `Options.Text` (embedded; keys `label`, `placeholder`, `results`, `oneResult`, `noResults`, `unavailable` at top level) | config | KEEP | Keep the flat shape. The bug is in the manifest schema, which lacks these six keys and sets `additionalProperties:false`, so it rejects the README's own example. | | extension schema regenerated |
| `exclude` (path prefixes) | key | CHANGE | Make it page names, as sitemap and analytics read `exclude`, matched on `BuiltFile.Name`. | `BuiltFile.Path` carries the locale prefix, so `"/admin/"` misses `/tr/admin/`. The same key means names in two plugins and prefixes in two. | README, docs plugins.md; indexnow has the same issue (other reviewer) |
| `f.Kind != "page"` | code | CHANGE | Use `collage.BuiltPage` (v0.61). | use core | |
| mount `/_search/`, template func `searchBox` | route/func | KEEP | | | |

plugin verdict: API changes

---

## collage-otel (v0.2.10)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `ScopeName` | consts | KEEP | | Users filter by instrumentation scope. | |
| `New(opts Options)` | func | KEEP | | | |
| `NewTracer(tracer)` | func | REMOVE | Users write `otel.New(otel.Options{Tracer: t})`. | It is exactly that call (one constructor per plugin). | collage-docs plugins.md EN line 2220 and TR line 2313; README lines 8, 109 and 135 |
| `Options` (`Tracer`, `Propagator` Go-only, `skip`) | type | KEEP | | | |
| lifecycle + `StartSpan`, `OnRequest` | methods | KEEP | | | |
| `Span` (+`End`, `RecordError`, `SetAttribute`, embedded `trace.Span`) | type | UNEXPORT | Users who need the OTel span call `trace.SpanFromContext(ctx)`. | `StartSpan` returns `collage.Span`. The adapter is exported only because it was convenient, and the embedded `trace.Span` exposes it a second way. | none found |
| `info.Kind == "page"` | code | CHANGE | Use `collage.RouteKindPage` (v0.61). | use core | |
| span attr `url.scheme` | behaviour | CHANGE | Use the proposed `collage.IsHTTPS`. | It reads `http` behind a proxy. | |
| manifest | collage.json | CHANGE | Add a `setup` note: hand the same value to `Config.Observability.Tracer`. | `collage plugin add` only writes it into Plugins, so collage's own spans are never converted. | |

plugin verdict: API changes

---

## collage-meta (v0.2.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| lifecycle + `OnBeforeRender` | methods | KEEP | | | |
| `Website`, `Article`, `Summary`, `SummaryLargeImage` | consts | KEEP | | | |
| `Page` (10 fields), `Set(rc, page)` | type/func | KEEP | | | |
| `Options` | type | KEEP | | | |
| `baseURL` key / `Options.BaseURL` | config | ASK | Either REMOVE it and use `collage.BaseURL(rc)` (Config.BaseURL or OriginResolver), or keep it as an override. | It is the same fact as `Config.BaseURL`. The override also beats per-host origins on a tenant site, which is wrong there. The manifest's setup text already steers users to `BASE_URL`. | cdnpurge, sitemap, indexnow (same key); docs plugins.md around line 313 |
| `ErrNoBaseURL` | var | KEEP | The message says "BaseURL is required", although the key is optional. Reword it to "no origin: set Config.BaseURL …". | | |
| `noAlternates`, `locales`, `siteName`, `defaultImage`, `defaultImageAlt`, `twitterSite` | keys | KEEP | `noAlternates` has a false zero value, which is the default. | | |
| hoist keys `property:og:*`, area `head` | hoist | KEEP | | They are core's HoistMeta/HoistProperty keys. | |

plugin verdict: patch-level (API changes if the ASK goes REMOVE)

---

## collage-htmlcheck (v0.1.6)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| lifecycle + `OnAfterRender`, `OnBuildFinished` | methods | KEEP | | | |
| `Level`, `Off`, `Warn`, `Error` | type/consts | KEEP | | | |
| `RuleInfo`, `Rules()` | type/func | KEEP | | | |
| `Options` | type | KEEP | | | |
| `inProduction` / `Options.InProduction` | key | REMOVE | Users who want production checks read findings in a static build or in dev. | Its findings "go nowhere" in production, so all it does is spend CPU on every live render. The docs never use it. | none found |
| `titleMax`, `descriptionMax`, `pageBudget`, `rules`, `ignoreLinks` | keys | KEEP | `descriptionMax` has no schema description (see Bugs). | | |
| rule names (22, kebab-case) | names | KEEP | | Consistent naming. | |
| `f.Kind != "page"` (build.go:36) | code | CHANGE | Use `collage.BuiltPage` (v0.61). | use core | |

plugin verdict: API changes (one key removed)

---

## collage-prometheus (v0.2.8)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| `NewMetrics(opts)` | func | REMOVE | Users write `prometheus.New(...)`. | `New` already calls it, so there are two constructors for one thing. | collage-docs plugins.md EN line 2184 and TR line 2276; README lines 8 and 115 |
| `OtherRoute` | const | KEEP | | It is a label value users write in PromQL. | |
| `Options` (`Namespace`, `Registry`, `Buckets` Go-only, `path`) | type | KEEP | | | |
| `token` key / `Options.Token` | key | CHANGE | Becomes `Token json:"-"` plus `tokenEnv`. | secret convention: a bearer token is committed with plugins-config.json | README |
| lifecycle + `RenderDuration`, `FragmentDuration`, `CacheEvent`, `HTTPResponse`, `Invalidation` | methods | KEEP | | `collage.Metrics` | |
| route label from `r.Kind` | code | CHANGE | Use the v0.61 `RouteKind*` constants where it compares. The label stays `kind:name`. | use core | |
| route `/metrics` (`path`, `"-"` = none) | route | KEEP | | | |
| manifest | collage.json | CHANGE | Add a `setup` note: hand the same value to `Config.Observability.Metrics`. | After `plugin add`, `/metrics` serves only runtime metrics. | |

plugin verdict: API changes

---

## collage-cdnpurge (v0.2.5)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| lifecycle + `OnCacheInvalidate` | methods | KEEP | | | |
| `Options`, `Cloudflare`, `Webhook` | types | KEEP | | | |
| `ErrNoBaseURL`, `ErrNoProvider` | vars | KEEP | | | |
| `baseURL` | key | ASK | Same choice as meta: REMOVE it in favour of Config.BaseURL or OriginResolver, or keep it as an override. | It is one fact with two keys. | meta, sitemap, indexnow |
| `force` / `Options.Force` | key | RENAME → `inDevelopment` / `InDevelopment` | | errortrack, fail2ban and indexnow all say `inDevelopment` for "act in dev mode too". | README, docs |
| `cloudflare.apiToken` / `APIToken` | key | CHANGE | Becomes `APIToken json:"-"` plus `apiTokenEnv`. | The plugin's own doc says "keep it out of a configuration file under version control" but offers only that file. | README, docs plugins.md around line 1932 |
| `webhook.token` / `Webhook.Token` | key | CHANGE | Becomes `Token json:"-"` plus `tokenEnv`. | secret convention | |
| `Options.Client` | field | RENAME → `HTTPClient` | | errortrack and oauth say `HTTPClient` (indexnow also says `Client`). | |
| `Duration` (+`UnmarshalJSON`) | type | ASK | Same choice as errortrack: core's `collage.Duration` or a per-plugin copy. | | |
| `retries` ("negative tries once") | key | KEEP | | The zero value is the default. | |

plugin verdict: API changes

---

## collage-bundle (v0.1.6)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)` | const/func | KEEP | | | |
| lifecycle | methods | KEEP | | | |
| `Plugin.URL(name)` | method | KEEP | | It is `{{bundle}}` for Go code (preload headers). | |
| `Options` | type | KEEP | | | |
| `minify`, `sourcemap` (`*bool`) | keys | KEEP | | The default depends on dev mode, so a plain bool cannot express it. Nil, the zero value, is the default, so this meets the zero-value rule. | |
| `prefix` (must end in `/`) | key | CHANGE | Accept it with or without the trailing `/` and normalise. | same prefix rule as oauth | |
| mount `/_bundle/`, template func `bundle` | route/func | KEEP | | | |
| manifest | collage.json | CHANGE | Add a `setup` note that `dir` and `entries` are required. | A plain `plugin add` fails at Configure. | |

plugin verdict: patch-level

---

## collage-devtoolbar (v0.3.2)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name` | const | KEEP | | | |
| `New() *Plugin` | func | CHANGE | Becomes `New(opts Options) *Plugin`. | one constructor convention | collage-docs plugins.md EN and TR; README; core plan docs only (no scaffold) |
| `NewWith(opts)` | func | REMOVE | Users write `New(Options{...})`. | It is merged into `New`. | as above |
| `Options` (`allowRemote`) | type | KEEP | | | |
| lifecycle + `OnAfterRender` | methods | KEEP | | | |
| manifest `order: 100` | collage.json | KEEP | | | |

plugin verdict: API changes

---

## Cross-cutting

1. **One constructor: `New(opts Options) *Plugin`. The type is always `Options`, and there is no `NewWith`, `NewTracer` or `NewMetrics`.** Most plugins already do this, `collage plugin add` writes `x.New(x.Options{})`, and the name `Config` collides with `collage.Config`.
   - In my set: devtoolbar, otel, prometheus.
   - Elsewhere, `New()` plus `NewWith(Config)`: consent, deploy, jsonld, live, minimizer, ogimage, opti-image, jobs.
   - Elsewhere, `New()` plus `NewWith(Options)`: health, tenant.
   - Special cases: uploads, websocket and feed (a library like paginate is exempt).
2. **Secrets are configured as `<x>Env`, the name of an environment variable, with the value in a Go-only `json:"-"` field.** errortrack `dsnEnv` and oauth `clientSecretEnv` already work this way. Core does not expand env vars in plugins-config.json.
   - Touches: prometheus `token`; cdnpurge `apiToken` and webhook `token`; flash `key`; oauth `key`/`previousKeys`.
   - Other reviewers: session `key`/`previousKeys`, secure `key`, honeypot `key`.
3. **"Act in development too" is always `inDevelopment`.**
   - cdnpurge `force` changes.
   - offline `inDev` changes (other reviewer).
   - htmlcheck `inProduction` is removed.
4. **The Go-only HTTP client field is always `HTTPClient`.** cdnpurge and indexnow change from `Client`.
5. **One JSON duration type.** ASK whether core adds an additive `collage.Duration` in v0.61.0, which would replace 8 identical copies: errortrack, cdnpurge, fail2ban, health, live, ogimage, opti-image, tenant. The related ASK is whether cookie `maxAge` integer seconds becomes a duration too: flash, session, honeypot, consent.
6. **`baseURL` keys duplicate `Config.BaseURL` and OriginResolver.** One ASK covers meta, cdnpurge, sitemap and indexnow: remove the key or keep it as an override.
7. **Path-filter keys:**
   - `exclude` means page names (`BuiltFile.Name` or the page name); this changes search and indexnow, which use path prefixes today.
   - `skip` means request path prefixes: otel, accesslog, ratelimit, basicauth, honeypot.
   - Prefix keys (`prefix`, `path`) accept a trailing `/` either way: bundle, oauth.
8. **Use core's names, not raw strings, after v0.61:** `BuiltPage` in search and htmlcheck; `RouteKind*` in otel, prometheus and errortrack; `Stage*` in errortrack.
9. **Propose an additive core `collage.IsHTTPS(r)` that honours `TrustedProxies`.** Today the same `r.TLS != nil || X-Forwarded-Proto` check is hand-rolled in flash, secure and session, and errortrack and otel read only `r.TLS`.
10. **`collage plugin add` writes only `x.New(x.Options{})`, so any plugin whose zero Options cannot start, or that needs wiring outside Plugins, needs a manifest `setup` note (and `requires`).**
    - oauth: requires session, and OnLogin is Go-only.
    - prometheus and otel: Observability wiring.
    - bundle: `dir`/`entries`.
    - cdnpurge: a provider.

## Bugs found in passing

1. **oauth's collage.json has no `"requires": ["elagoht/session"]`,** though the package imports collage-session and logs an error on every login without it. `collage plugin add oauth` leaves a broken setup.
2. **search's manifest schema omits the embedded `Text` keys** (`label`, `placeholder`, `results`, `oneResult`, `noResults`, `unavailable`) and sets `additionalProperties:false`. The extension therefore flags the README's own example config. The schema generator does not follow embedded structs.
3. **errortrack `sendIP` sends `r.RemoteAddr`.** Behind `TrustedProxies` that is the proxy's address, not `collage.ClientIP`.
4. **cdnpurge Init has an unreachable case.** Its `switch` has a third `case host.BaseURL() != ""` that the second case (`host.MultiOrigin() || host.BaseURL() != ""`) already covers.
5. **A zero `paginate.Linker` panics on `URL`/`For`** (nil func), and `PathNamed` returns one alongside its error. Callers who ignore the error panic instead of seeing an empty link.
6. **flash's go.mod still requires collage v0.50.0.** Its last commit is "collage v0.50.0, PluginConfig, v0.1.4". It was never re-released for v0.60 (toc too: v0.50.0; uploads: v0.58.0).
7. **htmlcheck's manifest has no description for `descriptionMax`,** because it shares a doc comment with `TitleMax`. Also, errortrack's README says to keep the DSN out of "`collage.json`", which should say plugins-config.json.
8. **flash's two manifest snippets use different class names:** `flash--{{.Kind}}` and `flash-{{.Kind}}`.
