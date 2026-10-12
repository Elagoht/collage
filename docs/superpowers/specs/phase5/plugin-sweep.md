# Phase 5 plugin sweep

Plugin repos (`~/Desktop/collage-*`, excluding `collage-docs` and `collage-snippets-highlighter`: 45 repos) whose Go code names an identifier that v0.60.0 changed, found by grepping the working trees on 2026-10-11. 42 of the 45 use one: 13 in non-test code (each needs a code change and a release), 29 only in tests or comments (`collage-jsonld`: a doc comment; the rest: test fixes, released only if the repo wants its tests green against v0.60.0). The other 3 repos — `collage-flash`, `collage-toc` and `collage-uploads` — use no changed identifier.

Replacements: see the v0.60.0 section of `CHANGELOG.md`. `Origins` means `host.(collage.Origins)` with `OriginFor` and `Dynamic` (now `host.OriginFor` and `host.MultiOrigin()`); `NewBuilder` means `collage.NewBuilder(app, opts)` plus `.Build(ctx)` (now `collage.Build(ctx, app, opts)`; `SkipRecord.Page` is `.Name`). Counts are occurrences. "Latest tag" is `git tag --sort=-v:refname | head -1`; "collage" is the version the repo requires today.

| Repo | Latest tag | collage | Identifiers in non-test code | Identifiers in tests | Needs | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| collage-accesslog | v0.1.12 | v0.58.0 | - | NewBuilder(1) | test fix only |  |
| collage-analytics | v0.2.1 | v0.58.0 | - | NewBuilder(1) | test fix only |  |
| collage-basicauth | v0.1.7 | v0.53.0 | - | NewBuilder(1) | test fix only |  |
| collage-bundle | v0.1.5 | v0.50.0 | - | .Mounts()(1) NewBuilder(1) | test fix only | test calls `App.Mounts()`, gone from `*App` |
| collage-cdnpurge | v0.2.4 | v0.50.0 | Origins(3) | DataHandler(2) | code change |  |
| collage-compress | v0.1.9 | v0.58.0 | - | NewBuilder(2) | test fix only |  |
| collage-consent | v0.1.2 | v0.57.0 | Varied(1) | NewBuilder(2) NoContent(1) VariedContext(1) WithSlot(4) | code change | `collage.Varied(rc, h)` now takes a context; `WithSlot` with bools in tests |
| collage-deploy | v0.1.1 | v0.52.0 | - | NewBuilder(1) | test fix only |  |
| collage-devtoolbar | v0.3.1 | v0.58.0 | - | DataHandler(1) NewBuilder(1) WithDependency(1) WithSlot(2) | test fix only | `WithSlot` with bools (tests) |
| collage-errortrack | v0.1.6 | v0.52.0 | - | NewBuilder(1) | test fix only |  |
| collage-fail2ban | v0.1.7 | v0.58.0 | RouteOf(1) | NewBuilder(1) | code change | test embeds `collage.Host` in `useSpy` (Host-shaped fake; still compiles) |
| collage-favicon | v0.1.6 | v0.50.0 | - | NewBuilder(1) | test fix only |  |
| collage-feed | v0.2.3 | v0.58.0 | Origins(1) | - | code change |  |
| collage-health | v0.1.1 | v0.58.0 | - | NewBuilder(1) | test fix only |  |
| collage-highlight | v0.2.5 | v0.50.0 | - | NewBuilder(1) | test fix only |  |
| collage-honeypot | v0.4.3 | v0.57.0 | - | NewBuilder(1) | test fix only |  |
| collage-htmlcheck | v0.1.5 | v0.50.0 | - | NewBuilder(1) | test fix only |  |
| collage-i18n | v0.2.4 | v0.50.0 | - | NewBuilder(1) NoContent(1) | test fix only |  |
| collage-indexnow | v0.2.1 | v0.50.0 | Origins(4) | NewBuilder(1) WithDependency(4) | code change |  |
| collage-jobs | v0.1.0 | v0.55.0 | - | NewBuilder(1) | test fix only |  |
| collage-jsonld | v0.2.9 | v0.50.0 | DataHandler(1) | WithSlot(2) | comment only | the non-test hit is a doc comment only (`jsonld.go:23`); `WithSlot` with bools in tests |
| collage-live | v0.4.2 | v0.50.0 | - | DataHandler(2) | test fix only |  |
| collage-markdown | v0.2.3 | v0.50.0 | DataHandler(2) | NewBuilder(1) | code change |  |
| collage-meta | v0.2.3 | v0.50.0 | Origins(1) | WithSlot(1) | code change | `WithSlot` with bools (tests) |
| collage-minimizer | v0.1.10 | v0.55.0 | - | WithSlot(1) | test fix only | `WithSlot` with bools (tests) |
| collage-oauth | v0.1.2 | v0.58.0 | Origins(1) | - | code change |  |
| collage-offline | v0.1.5 | v0.50.0 | - | NewBuilder(1) | test fix only |  |
| collage-ogimage | v0.2.3 | v0.50.0 | Origins(5) | NewBuilder(1) Origins(1) | code change |  |
| collage-opti-image | v0.3.6 | v0.58.0 | - | NewBuilder(2) | test fix only |  |
| collage-otel | v0.2.9 | v0.52.0 | - | NewBuilder(1) | test fix only |  |
| collage-paginate | v0.1.0 | v0.57.0 | - | DataHandler(3) NewBuilder(1) | test fix only |  |
| collage-prometheus | v0.2.7 | v0.52.0 | RouteOf(2) | NewBuilder(1) WithDependency(1) | code change |  |
| collage-ratelimit | v0.1.7 | v0.52.0 | - | NewBuilder(1) | test fix only |  |
| collage-redirects | v0.2.0 | v0.52.0 | - | NewBuilder(5) | test fix only |  |
| collage-robots | v0.2.3 | v0.50.0 | Origins(1) | - | code change |  |
| collage-search | v0.1.5 | v0.50.0 | - | NewBuilder(1) | test fix only |  |
| collage-secure | v0.2.4 | v0.58.0 | - | .RenderNotFound(1) | test fix only | test calls `App.RenderNotFound`, gone from `*App` |
| collage-session | v0.2.3 | v0.50.0 | - | JSONOf(1) | test fix only |  |
| collage-sitemap | v0.2.2 | v0.50.0 | Origins(1) | NewBuilder(1) | code change |  |
| collage-tenant | v0.1.3 | v0.53.0 | Varied(1) VariedContext(1) | DataHandler(2) NewBuilder(1) WithDependency(1) | code change | `Varied(rc, h)` and `VariedContext(ctx, h)` both become `Varied(ctx, h)` |
| collage-validate | v0.1.6 | v0.50.0 | - | JSONOf(2) | test fix only |  |
| collage-websocket | v0.3.0 | v0.58.0 | - | DataHandler(1) | test fix only |  |

## Flags

- **Host-shaped fakes or embedding `collage.Host`:** only `collage-fail2ban` (`options_test.go`, `useSpy` embeds `collage.Host`; the embedded value forwards `OriginFor` and `MultiOrigin`, so it keeps compiling). No repo declares its own type implementing the whole of `Host`. `collage-errortrack`, `collage-oauth` and `collage-honeypot` only hold or receive a `collage.Host`, which is unchanged.
- **Code that calls `Host.RegisterPage` or `Host.FragmentURL`:** none. Every `.RegisterPage(` found is on an `*App`, in a test.
- **Code reading `Redirect.StatusCode` or `Redirect.Permanent`:** none. `collage-deploy`'s `StatusCode:` is its own Vercel struct, fed from `BuiltRedirect.Status`.
- **`WithSlot` with bools:** `collage-consent`, `collage-devtoolbar`, `collage-jsonld`, `collage-meta`, `collage-minimizer`, all in tests.
- **`SlotResolverFunc` / `WithSlotResolver`:** none.
- **Plugins implementing `PageResolvedHook` or `CacheWriteHook`:** none.
- **`CacheInvalidateEvent.Paths`:** none; `collage-cdnpurge` and `collage-indexnow` already read `Entries`.
- **Sentinels removed from the package** (`ErrNilConfig` and the like): no repo references one.
- **`collage-secure`'s own `NoSniff *bool` option** is the plugin's config, not `collage.SecurityConfig`, and is unaffected.
- **The search is textual** (`collage.<Name>` and the method names above): a use through an unusual variable name could be missed. Compile each repo against v0.60.0 to be sure.
