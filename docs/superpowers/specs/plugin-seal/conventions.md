# Plugin conventions for v1 (decided 2026-10-12)

Every plugin repo (github.com/Elagoht/collage-<name>) is brought to these before the joint v1.0.0-rc.1. The user decided rules 1–4 and the core additions; the controller ruled the rest from the four inventories (out-1..out-4.md in this directory).

## Core (collage v0.61.0) gives plugins
- `collage.Duration`: JSON string only ("10s"); a number is an error; `null` leaves the default.
- `collage.IsHTTPS(r)`: TLS or a TRUSTED proxy's X-Forwarded-Proto. Never read X-Forwarded-Proto yourself.
- `collage.ClientIP(r)` + `Server.TrustedProxies`: never parse proxy headers yourself.
- `collage.BaseURL(rc)`, `host.BaseURL()`, `host.OriginFor`, `host.MultiOrigin()`, `collage.ParseOrigin` (now refuses a hostless origin).
- `collage.RouteKind*`, `collage.Built*`, `collage.Stage*` constants: never compare with literal strings.
- Strict plugin configuration: an unknown key in plugins-config.json stops the app at Init. So a renamed or removed key fails loudly by itself; no per-plugin "was renamed" shims are needed.
- `collagetest.NewSite(t, opts...)`: a real app for tests (WithPlugins, WithPluginConfig, WithTemplates, WithRegister, WithoutHome, WithConfig; site.Logs.Has(level, parts...)).

## Rules (user-approved)
1. **One constructor:** `type Options struct{…}` and `func New(opts Options) *Plugin`. No `NewWith`, `NewTracer`, `NewMetrics`, no `Config` type name (collides with collage.Config). The zero `Options` must start (or fail at Init with a clear message and a manifest `setup` note). Exceptions that keep real arguments: feed `New(...Feed)` (documented), paginate (a library).
2. **A boolean's zero value is its default:** no `*bool`, no true-by-default bools. Opt-outs are `noX` (e.g. highlight `noAuto`, toc `noIDs`, minimizer `noHTML`/`noJSON`/`noCSS`); secure's nosniff becomes a `contentTypeOptions` string following its own `"-"` = omit rule. "Also in development" is always `inDevelopment` (cdnpurge `force`, offline `inDev` change; htmlcheck `inProduction` goes).
3. **Secrets never in plugins-config.json:** a `<x>Env` key names the environment variable that holds the value (hex for keys; previous keys: comma-separated hex in `previousKeysEnv`), and a Go-only `json:"-"` field takes the value from code (`Key []byte`, `PreviousKeys [][]byte`). Applies to session, flash, honeypot, secure, oauth `key`/`previousKeys`, prometheus `token`, cdnpurge `apiToken` and webhook `token`. errortrack `dsnEnv` and oauth `clientSecretEnv` already follow it.
4. **No plugin-level `baseURL`:** remove it from meta, cdnpurge, sitemap, indexnow, feed (`Feed.BaseURL`) and jsonld (`siteURL`); use core's origin (`collage.BaseURL(rc)`, `host.OriginFor`). Init keeps failing loudly when no origin exists.

## Rules (controller rulings from the inventories)
5. **Durations are `collage.Duration`** (delete the 8 copies: live, ogimage, errortrack, cdnpurge, fail2ban, health, opti-image, tenant; health and indexnow too). Integers stay only where the key name carries a protocol unit (secure `hsts` max-age seconds, robots `crawlDelay`, consent `maxAgeDays`); cookie lifetimes in session, flash and honeypot become `collage.Duration` (old integer configs then fail loudly through strict decoding).
6. **Numbers:** 0 means the default, a negative value is an error. No "negative means unlimited" (ogimage `maxEntries`/`maxBytes` change; document a large explicit limit instead).
7. **JSON keys:** lowerCamel with initialisms upper-case (`baseURL`, `requestIDHeader`, `websiteID`, `measurementID`, `respectDNT`, `goatCounter`, `startURL`). Every exported Options field has a json tag or `json:"-"`.
8. **Path vocabulary:** `skip` = request path prefixes the plugin never handles; `protect` = prefixes it always handles; `exclude` = page names. Default probe skips are `/healthz` and `/readyz`. Prefix keys accept a trailing `/` either way.
9. **Localized text:** `text` (the default locale's Text) + `locales: {locale: Text}`; no embedded Text (toc, search un-embed; consent moves to this shape).
10. **Names in code:** the HTTP client field is `HTTPClient`; exported `Default*` constants that only restate a documented default are unexported; exported helpers used only by the plugin's own external tests are unexported (with an `export_test.go` bridge).
11. **Messages:** errors start with the plugin's short name and a colon (`live:`, `deploy:`); build/render finding rule codes are `<short-name>-<code>` (tenant, i18n change).
12. **Manifest:** every plugin whose zero `New(Options{})` cannot start or cannot be used gets a `setup` note, and `requires` lists every plugin it imports or needs at runtime (oauth requires elagoht/session; i18n, jobs, tenant, prometheus, otel, bundle, cdnpurge get setup notes). The manifest's `config` schema must cover every key (search's embedded Text keys were missing) — regenerate it from the Options type.
13. **Kept as is (rulings):** plugin asset paths stay where they are (user); `siteName` stays a separate key in meta, jsonld and ogimage (no core field — core stays minimal); push settings (`keepAlive`, `maxStreamAge`, `prefix`) live in live, and websocket reads them through live instead of repeating them.
14. **Tests:** new and rewritten tests use `collagetest.NewSite` where they build an app; convert a plugin's main site helper to NewSite when it is a straight replacement. Use the core constants and `collage.Duration` in tests too.

## Release of each plugin
API changes → minor bump (v0.x.0 → v0.(x+1).0); patch otherwise. README and docs reflect the new keys; the README gets a short "Changed in vX" section listing renamed and removed keys with replacements (strict decoding names the offending key, the README tells what to write instead).
