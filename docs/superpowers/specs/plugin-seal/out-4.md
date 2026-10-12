# Plugin sealing inventory, part 4

Plugins: indexnow, honeypot, health, feed, consent, analytics, redirects, minimizer, highlight, favicon, toc, sitemap, secure, robots, offline, compress, basicauth, accesslog.

All 18 are a single root package with no subpackages. None registers a CLI command or compares route-kind or stage strings, so the core v0.61.0 `RouteKind*`, `Stage*` and `Built*` constants change nothing here. `collagetest.NewSite` can replace the test boilerplate in all of them, but that is not API.

**Note on renames.** `collage.PluginConfig` decodes with a plain `json.Unmarshal`, so:
- A key that changes only its case still decodes. It breaks only the schema and the docs.
- A renamed or removed key is silently ignored. See Cross-cutting #4.

The common plugin interface methods (`Name`, `Version`, `Init`, `Shutdown`, `Configure` and `On*` hooks, and `Redirects`) and the `Plugin` type are KEEP throughout. Each table gives them one row.

---

## elagoht/indexnow (v0.2.2)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown, OnCacheInvalidate) | const/func/type/methods | KEEP | — | Matches the convention. | — |
| DefaultEndpoint, MaxBatch | const | KEEP | — | Documented defaults and limits, and `batchSize`'s doc names them. | — |
| ErrInvalidKey | var | KEEP | — | Returned by Init and asserted by tests (core's sentinel rule). | — |
| ErrNoBaseURL | var | CHANGE | Return it only when no origin is known. For a malformed `baseURL`, return core's `collage.ErrInvalidOrigin`, from `collage.ParseOrigin`. | Today a malformed value is reported as "BaseURL is required". The hand-written url.Parse check duplicates core's ParseOrigin. | — |
| Options.BaseURL / `baseURL` | field/key | ASK | See Cross-cutting #8. | It is a second key for Config.BaseURL. | docs plugins.md EN+TR |
| Options.Window + Options.WindowString / `window` | field/key | MERGE → Window | One field `Window Duration` with json `"window"`. Delete WindowString. | Two fields hold one fact. | — (indexnow only) |
| Options.Backoff | field | CHANGE | Give it the duration type with json `"backoff"`. | It is the only Go-only duration. Configuration cannot set it, although `retries` can be set. | — |
| Options.Exclude / `exclude` | field/key | RENAME → Skip / `skip` | The values are path prefixes. | Elsewhere `exclude` means page names (analytics, sitemap) and `skip` means path prefixes (accesslog, basicauth, honeypot). See Cross-cutting #7. | docs EN+TR, manifest |
| Options.InDevelopment / `inDevelopment` | field/key | KEEP | — | This is the majority spelling (errortrack, fail2ban). offline follows it. | — |
| Options.Retries / `retries` | key | KEEP | Zero means 3 and negative means one try. Document "-1 = no retry". | An int needs a sentinel to say "no retries". | — |

plugin verdict: API changes

## elagoht/honeypot (v0.4.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Configure, Init, Name, Version, Shutdown, OnBeforeAction, OnBeforeRender, OnPersonalise) | — | KEEP | — | Matches the convention. | — |
| TimeField (`"_hpt"`) | const | KEEP | — | It is the wire name of the signed field, and an app's test that posts a form by hand needs it. | — |
| Options.Key []byte + Options.KeyHex / `key` | field/key | MERGE → `Key string` (hex, json `"key"`) | Delete the []byte field. Go users write `hex.EncodeToString(raw)`. | Two fields hold one fact, with a "KeyHex wins" rule. The same pair exists in secure, session, flash and oauth (Cross-cutting #6). | docs EN+TR (honeypot section) |
| Options.MinDelay: legacy `-1` alias | key | REMOVE | Users write `0` to turn the check off. Make -1 an error like other negatives. | It is a pre-v1 compatibility shim. | — |
| Options.MinDelay (float seconds) / MaxAge (int seconds) | key | KEEP | — | The `{{honeypot N}}` argument is seconds. Switching to a Duration type would read old numbers as nanoseconds without any error. | — |
| template func `honeypot` | func | KEEP | — | — | — |

plugin verdict: API changes

## elagoht/health (v0.1.2)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, Plugin, interface methods (Init, Name, Version, Shutdown, OnDrain) | — | KEEP | — | — | — |
| Plugin.Check(name, fn) | method | KEEP | — | This is the plugin's whole extension API. | — |
| New() + NewWith(opts Options) | func | MERGE → `New(opts Options)` | Delete NewWith. `collage plugin add` writes `health.New(health.Options{})`. | Cross-cutting #1. | docs EN+TR (`health.New` ×2) |
| Duration (+UnmarshalJSON) | type | ASK → core `collage.Duration` or keep a per-plugin copy | Either way, stop accepting a bare JSON number as nanoseconds. | The same type is copied into cdnpurge, errortrack, fail2ban, live, ogimage, opti-image and tenant. With bare numbers, `"checkTimeout": 2` means 2 ns and every check fails (see Bugs). | snippets-highlighter schemagen keys `"elagoht/health.Duration"`; the 7 plugins above |

plugin verdict: API changes

## elagoht/feed (v0.2.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, Plugin, interface methods (Init, Name, Version, Shutdown, OnBeforeRender) | — | KEEP | — | — | — |
| New(feeds ...Feed) | func | KEEP (documented exception to #1) | — | Feeds hold Go funcs and the plugin has no config section. ReadConstructor already writes `feed.New()`. | — |
| Feed, Item | type | KEEP | — | — | — |
| Feed.BaseURL | field | ASK (Cross-cutting #8). If kept: CHANGE to validate with `collage.ParseOrigin`. | — | Today any `scheme://host/anything` passes (see Bugs). | — |
| Feed.RSS/Atom `"-"` sentinel | field | KEEP | — | This is a Go-only struct, and `"-"` matches highlight `dark` and secure's headers. | — |
| ErrNoBaseURL, ErrUnsafeLink, ErrNoItems | var | KEEP | ErrNoBaseURL should get the same split as indexnow: missing vs `ErrInvalidOrigin`. | Init sentinels. | — |
| hoist keys `feed:<name>:rss/atom` | hoist | KEEP | — | Documented as replaceable by a page. | — |

plugin verdict: patch-level (API changes if #8 resolves to remove)

## elagoht/consent (v0.1.3)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, Plugin, interface methods (Init, Name, Version, Shutdown, OnBeforeRender) | — | KEEP | — | — | — |
| New() + NewWith(cfg Config) | func | MERGE → `New(opts Options)` | — | Cross-cutting #1. | docs EN+TR (`consent.New` ×4) |
| Config | type | RENAME → Options | — | Cross-cutting #1. | — |
| Category | type | KEEP | — | — | — |
| Text + Config.Text `map[locale]Text` / `text` | type/key | CHANGE | `text` becomes the default locale's wording (`Text`) and `locales: {tr: Text}` holds the overrides, the same shape as toc and search (Cross-cutting #9). | consent alone keys `text` by locale. Text cannot be embedded because its `categories` would collide with the config's `categories`. | docs EN+TR consent section, manifest schema |
| Granted(rc, category) | func | KEEP (see Bugs #4) | — | The documented server read, used in docs. | — |
| `maxAgeDays`, `policyURL`, `serverPaths`, `version` | key | KEEP | — | Days are the natural unit for a consent cookie. | — |
| cookie `collage_consent`, header `X-Collage-Consent`, mount `/_collage/consent/` | wire | KEEP | — | This is already the layout proposed in #10. | — |

plugin verdict: API changes

## elagoht/analytics (v0.2.2)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown, OnBeforeRender) | — | KEEP | — | — | — |
| Plausible, Umami, GoatCounter, GoogleAnalytics | type | KEEP | — | — | — |
| `respectDnt` | key | RENAME → `respectDNT` | — | Initialism casing (#3). The old key still decodes. | docs EN+TR (2), manifest |
| `goatcounter` | key | RENAME → `goatCounter` | — | Matches `googleAnalytics` and the Go name. | manifest, docs |
| Umami `websiteId`, GoogleAnalytics `measurementId` | key | RENAME → `websiteID`, `measurementID` | — | #3. | manifest, docs |
| `requireConsent` + `consentCategory` | key | KEEP | — | They are different mechanisms (the plugin's own JS hook vs elagoht/consent), already mutually exclusive with a clear error. | — |
| `loaderPath` default `/collage-analytics.js` | route | ASK (#10) | `/_collage/analytics/loader.js` or keep | The asset-path convention is split. | docs |
| `exclude` (page names) | key | KEEP | — | This is the "page names" meaning #7 keeps. | — |
| localStorage key, `window.collageAnalyticsConsent` | wire | KEEP | — | — | — |

plugin verdict: patch-level (casing only, plus #10)

## elagoht/redirects (v0.2.1)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown, OnBuildFinished, Redirects [core RedirectSource]) | — | KEEP | — | — | — |
| Rule | type | KEEP | — | Imported by collage-deploy's e2e test. | collage-deploy |
| ErrInvalidRule | var | KEEP | — | An Init sentinel that tests assert. | — |
| Options.File doc "Default redirects.txt" | doc | CHANGE (doc) | Say that the default applies only when FS is set. | The default is never read from disk unless File is set. | — |

plugin verdict: unchanged

## elagoht/minimizer (v0.1.11)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, Plugin, interface methods (Configure, Init, Name, Version, Shutdown, OnAfterRender, OnDocumentRendered) | — | KEEP | — | — | — |
| New() + NewWith(cfg Config) | func | MERGE → `New(opts Options)` | — | Cross-cutting #1. Today NewWith bypasses the defaults, so `NewWith(Config{JS:true})` silently turns HTML, CSS and JSON off. | docs EN+TR (`minimizer.New`, `minimizer.Config` ×2) |
| Config | type | RENAME → Options | — | #1. | — |
| `html`, `json`, `css` (default true) | key | CHANGE → `noHTML`, `noJSON`, `noCSS`; `js` stays | The zero value of Options must mean the defaults (#2). | Today they are true-by-default booleans that only New() presets. | core `LoadPluginConfig` godoc example (`"elagoht/minimizer": {"html": true, …}`), docs EN+TR, manifest |

plugin verdict: API changes

## elagoht/highlight (v0.2.6)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Configure, Init, Name, Version, Shutdown, OnAfterRender, OnBeforeRender) | — | KEEP | — | — | — |
| Options.Auto *bool / `auto` | field/key | CHANGE → `NoAuto bool` / `noAuto` | — | #2: no *bool. The old `"auto": false` would be silently ignored (#4). | docs EN+TR, manifest |
| StylePath `/_highlight/style.css` | const/route | ASK (#10) | Move to `/_collage/highlight/style.css`, or keep | It is the only plugin with its own top-level `/_<name>/` prefix. | docs writing-plugins.md EN+TR, collage `docs/plugins.md` example |
| `light`, `dark` (`"-"`), `noBackground` | key | KEEP | — | — | — |
| template func `highlight` | func | KEEP | — | — | — |

plugin verdict: API changes

## elagoht/favicon (v0.1.7)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown, OnBeforeRender) | — | KEEP | — | — | — |
| ErrNoSource, ErrNotSquare, ErrBadSVG | var | KEEP | — | Init sentinels. | — |
| `startUrl` | key | RENAME → `startURL` | — | #3. It still decodes. | manifest |
| Options.Image / SVGImage | field | RENAME → SourceBytes / SVGBytes | — | "Image" beside "Source" does not say it holds bytes. The pair is Source/SVG → SourceBytes/SVGBytes. Neither is used in docs or other plugins. | — |
| served `/favicon.ico`, `/apple-touch-icon.png`, `/icon-192.png`, `/icon-512.png`, `/icon.svg`, `/site.webmanifest` | route | KEEP | — | Well-known paths. | — |

plugin verdict: API changes

## elagoht/toc (v0.1.5)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Configure, Init, Name, Version, Shutdown, OnAfterRender) | — | KEEP | — | — | — |
| Options.IDs *bool / `ids` | field/key | CHANGE → `NoIDs bool` / `noIDs` | — | #2. | manifest |
| Text embedded in Options (`label`, `readingTime` at top level) | type/key | CHANGE → `Text Text \`json:"text"\`` (not embedded) + `locales` | — | One localized-text shape with consent and search (#9). | docs EN+TR, manifest; collage-search makes the same change |
| template funcs `toc`, `readingTime` | func | KEEP | — | — | — |
| go.mod pinned to collage v0.50.0 | release | CHANGE | Bump and re-tag with the rest. | It is the only one of the 18 not re-released for v0.60.x. It builds and passes with a replace to v0.60.1. | — |

plugin verdict: API changes

## elagoht/sitemap (v0.2.3)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown) | — | KEEP | — | — | — |
| Tag (`"sitemap"`), MaxURLs | const | KEEP | — | Tag is documented (docs EN+TR) as what apps invalidate. | — |
| ErrNoBaseURL | var | CHANGE | Split it into missing and `collage.ErrInvalidOrigin` (as in indexnow). | — | — |
| Options.BaseURL / `baseURL` | field/key | ASK (#8). If kept: CHANGE to validate with `collage.ParseOrigin`. | — | Today `ftp://x` or `https://x.com/blog?q` is accepted and joined to paths (see Bugs). | manifest `setup` text names `"baseURL"` |
| `exclude` (page names), `path`, `maxURLs` | key | KEEP | — | — | — |

plugin verdict: patch-level (API changes if #8 resolves to remove)

## elagoht/secure (v0.2.5)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Configure, Init, Name, Version, Shutdown, OnAfterRender, OnPersonalise) | — | KEEP | — | — | — |
| Options.NoSniff *bool / `noSniff` | field/key | CHANGE → `ContentTypeOptions string` / `contentTypeOptions`, default `"nosniff"`, `"-"` omits | — | Today `noSniff: false` turns nosniff off, and it is the one *bool. Every other header already uses the string-with-`"-"` rule. | docs EN+TR (4 mentions), manifest |
| Options.Key + KeyHex / `key` | field/key | MERGE → `Key string` (hex) | — | #6. | docs |
| `hsts` (int seconds, negative omits) | key | KEEP | — | It mirrors the header's max-age. | — |
| `cspReportOnly`, `hstsSubdomains`, `hstsPreload`, others | key | KEEP | — | — | — |
| template func `cspNonce` | func | KEEP | — | — | — |

plugin verdict: API changes

## elagoht/robots (v0.2.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Rule, Plugin, interface methods (Init, Name, Version, Shutdown) | — | KEEP | — | — | — |
| Plugin.Render() | method | UNEXPORT | Tests use `collagetest` with `site.Get("/robots.txt")`. | It was exported for convenience and nothing outside the package calls it. Before Init it ignores plugins-config, and with path sitemaps it emits a relative `Sitemap: /x` (see Bugs). | — |
| ErrNoBaseURL | var | KEEP | — | — | — |

plugin verdict: API changes

## elagoht/offline (v0.1.6)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Configure, Init, Name, Version, Shutdown) | — | KEEP | — | — | — |
| Path (`/sw.js`) | const | KEEP | — | The worker scope requires the root path, and the constant is documented. | — |
| Plugin.CacheVersion() | method | UNEXPORT | — | Only offline_test uses it (an external test package), so the test moves in. | — |
| Options.Version / `version` | field/key | RENAME → BuildID / `buildID` | — | It overrides `Host.BuildID`, and "Version" reads as the plugin's own `Version()`. The old key would be silently ignored (#4). | manifest, README |
| Options.InDev / `inDev` | field/key | RENAME → InDevelopment / `inDevelopment` | — | Matches indexnow, errortrack and fail2ban. | manifest |
| template func `offlineScript` | func | KEEP | — | — | — |

plugin verdict: API changes

## elagoht/compress (v0.1.10)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown, OnBuildFinished) | — | KEEP | — | — | — |
| Brotli, Gzip | const | UNEXPORT | — | No option or function takes them and nothing outside the package uses them. They were exported for convenience. | — |
| Options.SkipPrivate / `skipPrivate` (default false) | key | ASK | Keep compressing private/no-store responses by default (today), or flip to safe by default with `compressPrivate: true` to opt in. | BREACH: the security default is the real trade-off, and this is the last chance to flip it without a /v2. | docs EN+TR |
| levels, `cacheBytes`, `types`, `noPrecompress`, `minSize` | key | KEEP | — | — | — |

plugin verdict: patch-level (API changes if SkipPrivate flips)

## elagoht/basicauth (v0.1.8)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown, OnBuildFinished) | — | KEEP | — | — | — |
| EnvUsers, EnvDisabled | const | KEEP | — | A documented deployment contract (COLLAGE_BASICAUTH_*). | — |
| Options.Skip default `["/healthz", "/_collage/"]` | key | CHANGE | Add `/readyz` (health's default readiness path). | It should match accesslog's default (#7). | docs |
| `users`, `realm`, `protect`, `disabled` | key | KEEP | — | — | — |

plugin verdict: patch-level

## elagoht/accesslog (v0.1.13)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| Name, New(opts Options), Options, Plugin, interface methods (Init, Name, Version, Shutdown) | — | KEEP | — | — | — |
| RequestID(ctx) | func | KEEP | — | Documented in docs EN+TR. | — |
| Options.TrustProxy + TrustedProxies / `trustProxy`, `trustedProxies` | field/key | REMOVE → use `collage.ClientIP(r)` and `ServerConfig.TrustedProxies` | Users move the list to Config.Server.TrustedProxies. | Core already owns this fact. Differences to note in the changelog: today an empty list trusts loopback and private ranges where core trusts nothing, and accesslog reads X-Real-IP where core does not. | docs EN+TR (2); collage-ratelimit has the identical pair |
| `requestIdHeader` | key | RENAME → `requestIDHeader` | — | #3. It still decodes. | docs EN+TR, manifest |
| `skip`, `sample`, `fullReferer`, Logger (Go-only) | key | KEEP | — | — | — |

plugin verdict: API changes

---

## Cross-cutting

1. **Constructor and options: `func New(opts Options) *Plugin`, type `Options`, no `NewWith`.** 30 of 44 plugins already do this, and `plugincontract.ReadConstructor` writes `x.New(x.Options{})`.
   - Deviating here: health and minimizer (`New()` + `NewWith`), consent (`New()` + `NewWith(Config)`), minimizer (`Config`).
   - feed's `New(...Feed)` is a documented exception, because it has no config section.
   - Deviating elsewhere: deploy, jobs, jsonld, live, ogimage, opti-image and uploads use `Config`/`NewWith`; devtoolbar, tenant and websocket use `NewWith`.
2. **A Boolean's zero value is its default.** That means no `*bool` and no true-by-default bool, with `no<Thing>` used for an opt-out.
   - highlight `auto` → `noAuto`, toc `ids` → `noIDs`, minimizer `html/json/css` → `noHTML/noJSON/noCSS`.
   - secure `noSniff` → `contentTypeOptions` string, following its own `"-"` rule.
3. **JSON key casing: initialisms are uppercase, as in `baseURL`, `policyURL` and `maxURLs`.** Keys to change:
   - analytics: `respectDnt`, `websiteId`, `measurementId`, `goatcounter` → `goatCounter`
   - favicon: `startUrl`
   - accesslog: `requestIdHeader`

   json decoding is case-insensitive, so only schemas and docs break.
4. **Renames are silent today.** `PluginConfig` uses a plain `json.Unmarshal`, so an old key is simply ignored. Every rename or removal above (indexnow `exclude`, offline `version`/`inDev`, highlight `auto`, toc `ids`, minimizer `html…`, secure `noSniff`/`key`, accesslog `trustProxy`) should fail Init when it meets the old key ("X was renamed Y in v1"), at least through the rc. Better still, `collage check` should warn about unknown plugin-config keys using the manifest schemas.
5. **Durations: Go duration strings, and no bare numbers.** ASK: add an additive `collage.Duration` to core in v0.61, or keep identical per-plugin copies.
   - Touches health and indexnow, plus cdnpurge, errortrack, fail2ban, live, ogimage, opti-image and tenant.
   - Keep seconds where the protocol defines them (secure `hsts`, robots `crawlDelay`) and where a type change would silently reinterpret existing numbers (honeypot `maxAge`/`minDelay`, consent `maxAgeDays`).
6. **One field per secret: `Key string` (hex), json `"key"`.** Drop the `Key []byte` + `KeyHex` pair from honeypot and secure, and from session, flash and oauth.
7. **Path-list vocabulary:**
   - `skip` means path prefixes never handled.
   - `protect` means path prefixes always handled.
   - `exclude` means page names.

   Changes:
   - indexnow `exclude` → `skip` (search too).
   - Default probe skips are `/healthz` and `/readyz` everywhere: basicauth gains `/readyz`, accesslog already has it.
8. **Use core for origins and client addresses.** ASK whether the plugin-level `baseURL` stays. It is a second key for Config.BaseURL, which with OriginResolver now covers every case.
   - Plugins with the key: sitemap, indexnow, feed `Feed.BaseURL`, plus meta and cdnpurge.
   - Recommendation: REMOVE it. Users set Config.BaseURL (BASE_URL), and Init still fails loudly with no origin.
   - Whatever the answer, validate with `collage.ParseOrigin` and return `ErrInvalidOrigin` for malformed values.
   - accesslog (and ratelimit) drop `trustProxy`/`trustedProxies` for `collage.ClientIP`.
   - The 4-line `canResolve` helper is copied in feed, sitemap, robots and indexnow. It is fine as a private helper, or core could add a one-line Host method later (additive).
9. **One localized-text shape: `text` (the default locale's `Text`) + `locales: {loc: Text}`, not embedded.**
   - toc and search un-embed `Text`.
   - consent changes from `text: {locale: Text}` to the same shape.
10. **Plugin-owned asset URLs.** ASK: everything under `/_collage/<name>/` (consent already does this) vs well-known root paths only.
    - Moves: highlight `/_highlight/style.css` and analytics `/collage-analytics.js`.
    - Pro: one namespace, already skipped by basicauth, honeypot and accesslog.
    - Con: offline's worker never caches `/_collage/`, so the stylesheet would not work offline. Docs quote the highlight path in three places.

## Bugs found in passing

1. **health `Duration` reads a bare JSON number as nanoseconds.** `"checkTimeout": 2` means 2 ns, so every check times out and `/readyz` reports 503. `"cacheFor": 1` turns the cache off. The same is true of the 7 copies.
2. **sitemap and feed accept any `scheme://host…` as BaseURL** (url.Parse with a non-empty scheme and host). `ftp://x` or `https://x.com/blog?a=1` passes and is concatenated with page paths. indexnow alone checks for a real origin. Fix with `collage.ParseOrigin`.
3. **indexnow returns `ErrNoBaseURL` ("BaseURL is required") for a malformed but present `baseURL`.** The message misleads the operator.
4. **consent `Granted` reads process-global state (`active`)** that the last App's Init sets. Two Apps in one process, such as parallel tests with `collagetest.NewSite`, read each other's categories.
5. **minimizer's `Config` doc says a decoded absent section "leaves the plugin inert"**, which contradicts the code: config is decoded over New's presets. `NewWith(Config{JS: true})` also silently disables HTML, CSS and JSON.
6. **robots `Render()`** returns the Go-only options before Init has decoded plugins-config. With path sitemaps it writes `Sitemap: /sitemap.xml` without an origin, which is invalid by the protocol.
7. **toc still requires collage v0.50.0**, and its last tag predates the v0.60 seal. It passes against v0.60.1 with a replace, so it only needs a re-release.
8. **secure sends HSTS whenever `X-Forwarded-Proto: https` is present, from any client**, ignoring `ServerConfig.TrustedProxies`. Browsers ignore STS over http, so the impact is low, but it should use the same trust as `ClientIP`.
