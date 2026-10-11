# Reviewer A: collage.go (App, hooks, events, Host/plugin surface)

Source read at 30f5cfa: pkg/collage/collage.go, internal/plugin/{plugin,hooks,configure,config,origin,redirects}.go, internal/core/{host,registry,register}.go, internal/cache/cache.go, internal/render/engine.go, internal/types/finding.go.
Hook implementer counts come from a grep for `func (...) OnX(` across the 45 plugin repos (non-test code). They replace the TSV's alias-name counts, which undercount: a plugin implements a hook without naming its interface.

| name | kind | decision | detail (new name / how) | why (one line) | plugins broken |
|---|---|---|---|---|---|
| AfterRenderEvent | alias | KEEP | | 9 implementers. Hoist, Warn and Error carry it. (It has no Path field while BeforeRenderEvent does; adding one later is additive.) | 0 |
| AfterRenderHook | alias | KEEP | | 9 implementers | 0 |
| App | alias | DEFINE | Make it `type App struct{ c *core.App }` in pkg/collage, with internal/build reaching the core value through an unexported accessor. **Forwards these:** Register, RegisterPage, RegisterNotFoundPage, RegisterErrorPage, RegisterDocument, RegisterAction, RegisterPlugin, RegisterCommand, Commands, Pages, Page, Documents, Actions (no caller, kept so the Pages/Documents/Actions listings stay symmetric), Handle, Use, Mount, URL, ActionURL, FragmentURL, InvalidateTags, InvalidateTagsN, RenderPath, RenderDocumentPath (docs teach it), RenderFragment, Handler, Start, ListenAndServe, Shutdown, DevMode, Logger, BaseURL, OriginFor, Locales, PageURLs, ServeStatus, BuildID, Inspect (cli.md), Check. **Hides these:** BuildFinished, HasBuildFinishedHook, PluginRedirects, CaptureResponses, RenderNotFound, PageRouted, CSRFMarker, Mounts, PrefixDefault, DefaultLocale (Locales covers it). | `go doc` shows no methods through the alias, and the doc comment's method list is stale. Every exported core.App method becomes v1 API, including 10 plumbing methods only internal/build calls (see breaking #1). | 2 (tests only: secure calls RenderNotFound, bundle calls Mounts) |
| BeforeActionEvent | alias | KEEP | | honeypot uses it. Form() and ErrStreamingBody are a sound shape. | 0 |
| BeforeActionHook | alias | KEEP | | 1 implementer (honeypot); the only pre-handler gate | 0 |
| BeforeRenderEvent | alias | KEEP | | 10 implementers | 0 |
| BeforeRenderHook | alias | KEEP | | 10 implementers | 0 |
| BuildFinishedEvent | alias | KEEP | | 9 implementers | 0 |
| BuildFinishedHook | alias | KEEP | | 9 implementers | 0 |
| BuiltFile | alias | KEEP | | 4 repos. Kind is a magic string; add constants instead of retyping the field (see cross-cutting). | 0 |
| BuiltRedirect | alias | KEEP | | deploy and redirects use it. Its `Status` differs from `Redirect.StatusCode` (see cross-cutting). | 0 |
| Cache | alias | CHANGE | Move `Invalidate(ctx, tags)` from Cache to TaggedCache. | The framework calls Invalidate only on a TaggedCache (core/app.go:1351). A plain Cache never receives tags, so it can only implement it as a no-op. | 0 |
| CacheCoalesced | const | KEEP | | A Metrics implementation receives it | 0 |
| CacheEvent | alias | KEEP | | Required to implement Metrics | 0 |
| CacheEvict | const | KEEP | | Same as CacheEvent | 0 |
| CacheHit | const | KEEP | | Same as CacheEvent | 0 |
| CacheInvalidate | const | KEEP | | Same as CacheEvent | 0 |
| CacheInvalidateEvent | alias | CHANGE | Drop `Paths`. `Entries` carries the same drops plus the host. | No plugin reads Paths: cdnpurge and indexnow read Entries, live and markdown read Tags. Two fields for one fact. | 0 |
| CacheInvalidateHook | alias | KEEP | | 4 implementers | 0 |
| CacheMiss | const | KEEP | | Same as CacheEvent | 0 |
| CacheSet | const | KEEP | | Same as CacheEvent | 0 |
| CacheWriteEvent | alias | REMOVE | Users set Page CacheTTL/Strategy and data-handler tags instead. | 0 implementers anywhere. An optional hook can be re-added in v1.x without breaking anyone; once frozen, its odd semantics are permanent (Page is nil for a document). | 0 |
| CacheWriteHook | alias | REMOVE | Same as CacheWriteEvent. | Same as CacheWriteEvent | 0 |
| ClientIP | func | KEEP | | devtoolbar and fail2ban use it; docs 12 | 0 |
| Command | alias | KEEP | | ogimage; the RegisterCommand payload | 0 |
| ConfigHost | alias | KEEP | | 18 Configure implementers. Under the Host split, it shares one base (DevMode, BaseURL, Logger, origins) with Host. | 0 |
| ConfigReader | alias | KEEP | | PluginConfig's parameter type. Its unexported method is what seals Host and ConfigHost. | 0 |
| Configurer | alias | KEEP | | 18 implementers | 0 |
| DocumentRenderedEvent | alias | KEEP | | minimizer | 0 |
| DocumentRenderedHook | alias | KEEP | | 1 implementer (minimizer); the TSV's 0 is the alias-count artefact | 0 |
| DrainHook | alias | KEEP | | health implements it | 0 |
| ETag | func | KEEP | | Cache.Set must return an ETag. This is the only way a custom store matches the framework's own ETags. | 0 |
| ErrAmbiguousParameterName | var | KEEP | | A distinct routing mistake, worth errors.Is in a test | 0 |
| ErrAppStarted | var | KEEP | | A lifecycle misuse users hit; docs table | 0 |
| ErrAssetFailed | var | KEEP | | ErrorHook stage "asset" | 0 |
| ErrConfigurerRegisteredLate | var | KEEP | | Tells a plugin author the plugin must go in Config.Plugins | 0 |
| ErrDuplicateCommand | var | KEEP | | Duplicate-name family (see cross-cutting) | 0 |
| ErrDuplicatePage | var | KEEP | | Duplicate-name family | 0 |
| ErrDuplicatePlugin | var | KEEP | | Duplicate-name family | 0 |
| ErrDuplicateRoute | var | KEEP | | Duplicate-name family; docs 6 | 0 |
| ErrDuplicateTemplateFunc | var | KEEP | | Duplicate-name family; two plugins colliding | 0 |
| ErrEmptyCommandName | var | UNEXPORT | The error stays unexported inside core. | A programming mistake nobody branches on; the message already says what went wrong. | 0 |
| ErrEmptyErrorPage | var | KEEP | | ErrorHook stage "error_page" | 0 |
| ErrEmptyPluginName | var | UNEXPORT | The error stays unexported inside plugin. | Same as ErrEmptyCommandName | 0 |
| ErrInvalidPattern | var | KEEP | | A route-pattern mistake | 0 |
| ErrMaxDepthExceeded | var | KEEP | | A render failure an ErrorHook can see; a slot cycle can be dynamic | 0 |
| ErrNilConfig | var | UNEXPORT | New(nil) still fails with an unexported error. | Nil-argument family: nobody errors.Is a nil argument | 0 |
| ErrNilPage | var | UNEXPORT | Same as ErrNilConfig. | Nil-argument family | 0 |
| ErrNilPlugin | var | UNEXPORT | Same as ErrNilConfig. | Nil-argument family | 0 |
| ErrNoRootFragment | var | UNEXPORT | Keep it internal as a defensive check. | Unreachable: all three Register*Page go through prepare, which calls Page.Validate (ErrMissingContent), and Host.RegisterPage forwards to app.RegisterPage. | 0 |
| ErrNoRoute | var | KEEP | | ErrorHook: tells an unmatched URL from a missing record | 0 |
| ErrOverlappingPattern | var | KEEP | | Affix-pattern rule (v0.41) | 0 |
| ErrPageNotFound | var | KEEP | | RenderPath stays public (see App) | 0 |
| ErrPanic | var | KEEP | | errortrack uses it | 0 |
| ErrRedirectShadowsPage | var | KEEP | | A routing mistake | 0 |
| ErrRequiredSlotEmpty | var | KEEP | | A per-render failure, distinct from the validation-time one | 0 |
| ErrTemplateNotFound | var | KEEP | | Registration and render time | 0 |
| ErrTemplateRootMissing | var | KEEP | | The most common startup failure. The doc comment argues for telling it apart. | 0 |
| ErrUnknownPluginConfig | var | KEEP | | The config typo case | 0 |
| ErrUnregisteredErrorPage | var | KEEP | | Startup check | 0 |
| ErrUnsafeRedirectTarget | var | KEEP | | ErrorHook stage "route" | 0 |
| ErrUnsubstitutedPlaceholder | var | KEEP | | A redirect-pattern mistake | 0 |
| ErrUnsupportedCache | var | MERGE → ErrInvalidCacheType | | Unreachable through New: Config.Validate already refuses the same input with ErrInvalidCacheType, and New always runs Validate before core.New. | 0 |
| ErrorEvent | alias | KEEP | | errortrack. Stage is a magic string; add constants instead of retyping (errortrack passes it as a string). | 0 |
| ErrorHook | alias | KEEP | | errortrack implements it | 0 |
| FetchHeader | const | KEEP | | The wire contract with the client script ("Collage-Fetch") | 0 |
| Finding | alias | KEEP | | The check/report type | 0 |
| FindingError | const | KEEP | | | 0 |
| FindingLevel | alias | KEEP | | | 0 |
| FindingWarning | const | KEEP | | | 0 |
| FragmentMetadata | alias | MERGE → FragmentReport | Metadata.Fragments becomes []FragmentReport; delete Result.FragmentReports(). | The two structs have identical fields (Name, Duration, Failed, UsedFallback, Err). Result.FragmentReports() exists only to copy one into the other. | 0 |
| FragmentRender | alias | KEEP | | live | 0 |
| FragmentReport | alias | KEEP | | devtoolbar; the surviving name | 0 |
| FragmentRequest | alias | KEEP | | live | 0 |
| HoistItem | alias | KEEP | | FragmentRender.Head's element type | 0 |
| Host | alias | DEFINE | In pkg/collage, write `type Host = interface{ …methods…; plugin.ConfigReader }` using collage's own alias names (`*Page`, not `*types.Page`), with internal/plugin using the same literal. A scratch module confirmed both claims: identical unnamed interfaces (embedding the same ConfigReader) keep Plugin.Init type-compatible, and `go doc` prints the methods with their comments. Method set: the 19 minus RegisterPage and FragmentURL (0 callers), plus OriginFor and MultiOrigin from Origins, so 19 in all. | `go doc` shows no methods. The collage.go comment lists 9 of the 19 ("…and Mount, and nothing else"). All 44 repos use it. | 0 |
| InvalidatedEntry | alias | KEEP | | cdnpurge and indexnow read Entries | 0 |
| LocationHeader | const | KEEP | | Wire contract ("Collage-Location") | 0 |
| Metadata | alias | CHANGE | `Fragments []FragmentReport` (follows the FragmentMetadata merge). | One fragment-report type | 0 |
| Metrics | alias | KEEP | | prometheus implements it | 0 |
| New | func | KEEP | | Every app | 0 |
| OriginResolver | alias | KEEP | | tenant implements it | 0 |
| Origins | alias | MERGE → Host | OriginFor(ctx, host) and Dynamic() (better named `MultiOrigin()`) become plain Host/ConfigHost methods. | Host is sealed and hostView always implements Origins, so the assertion can only fail through a wrapper. fail2ban's test `useSpy{collage.Host}` shows the pattern: a plugin asserting Origins on such a wrapper would silently get false. The 8 repos asserting it are the 8 that call BaseURL. | 8 |
| PageResolvedEvent | alias | REMOVE | For fresh renders, use BeforeRenderHook. For every request, cache hits included, read RouteInfo(ctx) after routing: in a RequestHook's status callback, or after `next` in a Host.Use middleware. | 0 implementers, 0 docs; re-addable in v1.x without breaking anyone | 0 |
| PageResolvedHook | alias | REMOVE | Same as PageResolvedEvent. | Same as PageResolvedEvent | 0 |
| PageURL | alias | KEEP | | sitemap | 0 |
| PanicError | alias | KEEP | | errortrack uses errors.As | 0 |
| ParseOrigin | func | KEEP | | tenant; validates the OriginResolver contract | 0 |
| PathTag | func | KEEP | | docs 8; the invalidation idiom | 0 |
| PersonaliseEvent | alias | KEEP | | honeypot and secure. The British spelling is the only one in the API but is honest, and renaming a hook method would break 2 repos. | 0 |
| PersonaliseHook | alias | KEEP | | 2 implementers, both with `var _` assertions | 0 |
| Plugin | alias | DEFINE | Same literal-alias treatment as Host (its Init names Host). | Every plugin implements it, and godoc through the alias shows no methods | 0 |
| PluginConfig | func | KEEP | | 42 repos | 0 |
| RedirectSource | alias | KEEP | | redirects | 0 |
| RequestHook | alias | KEEP | | fail2ban and otel | 0 |
| Result | alias | CHANGE | Unexport `HoistEnds` (AfterRender plumbing); drop `FragmentReports()`. Keep HTML, DependencyTags, Metadata, NotFound, Findings and Degraded(). | An exported plumbing field on a type RenderPath returns | 0 |
| Route | alias | KEEP | | errortrack and otel. Kind is a magic string; add constants (see cross-cutting). | 0 |
| RouteInfo | func | KEEP | | The surviving accessor; returns the whole Route | 0 |
| RouteOf | func | MERGE → RouteInfo | `kind, name := RouteOf(ctx)` becomes `r := RouteInfo(ctx); r.Kind, r.Name` | Two accessors for one value; RouteInfo is a superset | 2 |
| ServeHook | alias | KEEP | | jobs implements it | 0 |
| Span | alias | KEEP | | Required to implement Tracer | 0 |
| StreamCloser | alias | KEEP | | live implements it (the TSV's 0 is an artefact). The name matches its CloseStreams method. | 0 |
| TaggedCache | alias | CHANGE | Gains `Invalidate(ctx, tags)`, moved from Cache. | Tags are only ever invalidated on a tagged store | 0 |
| Timing | alias | KEEP | | Metadata.Timing's type | 0 |
| Tracer | alias | KEEP | | otel implements it | 0 |

## Cross-cutting

- **Optional interfaces cost nothing to add later, so drop the unused ones now.** Hook interfaces are discovered by type assertion, and Host cannot be implemented outside collage. Adding either after v1.0 breaks nobody; removing either later is a v2. Rule: any hook with 0 implementers, and any Host method with 0 callers, goes before the freeze unless the docs teach it. That removes PageResolvedHook/Event and CacheWriteHook/Event. On Host it removes RegisterPage and FragmentURL (decided: drop both; re-adding one later is free). InvalidateTags is also unused but stays, because the docs teach `host.InvalidateTags`.
- **Sentinel errors, three tiers.**
  - (a) ErrorHook and runtime sentinels (ErrNoRoute, ErrPanic, ErrAssetFailed, ErrEmptyErrorPage, ErrUnsafeRedirectTarget, render failures, ErrPageNotFound): KEEP.
  - (b) Configuration and registration mistakes a test or `collage check` could distinguish (duplicates, pattern errors, ErrAppStarted, ErrTemplateRootMissing, ErrConfigurerRegisteredLate, ErrUnknownPluginConfig): KEEP.
  - (c) Nil- or empty-argument errors (ErrNil*, ErrEmpty*Name): UNEXPORT, keeping them internal and out of pkg/collage. Apply the same rule to the other files' rows (ErrNilRegistrable, ErrNilRenderer, …).
  - Also take out any sentinel that is unreachable through the public path: ErrUnsupportedCache (MERGE) and ErrNoRootFragment (UNEXPORT).
- **Aliases hide godoc. DEFINE only what users implement or call methods on.** App, Host and Plugin show no methods in `go doc` today; data structs and events show their fields fine. For interfaces, use an alias to an identical interface literal in both packages: type identity holds, nothing breaks, and the methods appear.
- **Magic-string enums.** Route.Kind, ErrorEvent.Stage and BuiltFile.Kind are compared against literals in plugins (htmlcheck, search, otel, errortrack, deploy). Retyping them would break 4–5 repos for little gain. Rule: keep them as `string` and export named constants (RouteKindPage, StagePanic, BuiltKindPage, …). That is additive and can even land after v1.0. Write down in each field's doc comment that the set may grow.
- **Status-field naming.** `Redirect.StatusCode` versus `BuiltRedirect.Status` and `BuiltFile.Status`. Rule: use `Status` everywhere in event and report structs, and decide on Redirect.StatusCode in the types.go review.
- **One fact, one field or function.** The rule merges RouteOf into RouteInfo and FragmentMetadata into FragmentReport, drops CacheInvalidateEvent.Paths in favour of Entries, and drops Result.FragmentReports(). Apply the same check to sibling files (for example, BaseURL() against OriginFor).
- **Additive, but do it in this pass: AfterRenderEvent.Path.** BeforeRenderEvent and DocumentRenderedEvent carry Path; AfterRenderEvent doesn't, so a plugin can't tell which URL of a pattern page it is post-processing.
- **Stale enumerations in doc comments.** The App and Host comments list their methods by hand and have drifted. Once these are DEFINEd, delete the hand-written lists and let godoc show the methods.

## Breaking-change candidates found in passing

1. **core.App's plumbing methods are public API through the alias.** Only internal/build calls these 10: BuildFinished, HasBuildFinishedHook, PluginRedirects, CaptureResponses, RenderNotFound, PageRouted, CSRFMarker, Mounts, PrefixDefault and DefaultLocale. No plugin's non-test code uses any of them; two plugin tests do (secure calls RenderNotFound, bundle calls Mounts). Freezing them would lock the static-build internals. Fix: the App DEFINE (wrapper), or move them behind a package-level accessor in internal/core.
2. **Cache.Invalidate sits on the wrong interface** (see the Cache row). A custom store must implement a method that is never called unless it is also a TaggedCache.
3. **ErrUnsupportedCache duplicates ErrInvalidCacheType** and cannot be reached through New.
4. **Origins as an asserted capability would fail through Host wrappers.** `type spy struct{ collage.Host }` (the pattern in fail2ban's test) promotes only Host's methods, so `host.(collage.Origins)` would return false and the plugin would act as if no origin were known. Fold it into Host before v1.
5. **Result exports AfterRender plumbing** (HoistEnds). Metadata and Result also carry a second fragment-report type.

## Host methods

**Host is sealed.** It embeds ConfigReader, whose only method is unexported, so no type outside collage can implement it. The only way is to embed a real Host, as fail2ban's test `useSpy{collage.Host}` does. Consequences for the split:
- Adding a Host method after v1 breaks nobody; removing or retyping one does.
- So splitting Host into capability interfaces buys godoc readability only, not room to evolve.
- Capabilities reached by type assertion are actively harmful: they fail through wrappers (see Origins).

Recommendation: keep one Host with grouped doc sections, and drop the asserted Origins capability.

Counts are plugin repos (of 45) calling `host.M(` or `p.host.M(` in non-test code. Every plugin names the Host value `host`. Unused methods were re-checked with any receiver. For DevMode, BaseURL and Logger, the ConfigHost counts (from Configure bodies) are listed separately.

| # | method | repos | repos (names) |
|---|---|---|---|
| 1 | DevMode | 10 (+6 ConfigHost) | analytics, cdnpurge, devtoolbar, htmlcheck, indexnow, markdown, oauth, ogimage, opti-image, session |
| 2 | BaseURL | 8 (+0 ConfigHost) | cdnpurge, feed, indexnow, meta, oauth, ogimage, robots, sitemap (exactly the 8 that also assert Origins) |
| 3 | Pages | 4 | health, offline, redirects, sitemap |
| 4 | Page | 1 | analytics |
| 5 | InvalidateTags | 0 | none (taught in collage-docs writing-plugins.md) |
| 6 | Logger | 26 (+6 ConfigHost) | most |
| 7 | RegisterCommand | 1 | ogimage |
| 8 | RegisterPage | 0 | none |
| 9 | RegisterDocument | 7 | analytics, favicon, feed, indexnow, offline, robots, sitemap |
| 10 | Mount | 7 | bundle, consent, highlight, live, ogimage, opti-image, search |
| 11 | Handle | 5 | live, oauth, ogimage, prometheus, websocket |
| 12 | RenderFragment | 1 | live |
| 13 | Use | 18 | accesslog, basicauth, compress, consent, devtoolbar, fail2ban, flash, health, i18n, offline, opti-image, ratelimit, redirects, robots, secure, session, tenant, validate |
| 14 | URL | 2 | meta, offline |
| 15 | FragmentURL | 0 | none |
| 16 | Locales | 5 | consent, i18n, markdown, meta, sitemap |
| 17 | PageURLs | 2 | redirects, sitemap |
| 18 | BuildID | 2 | errortrack, offline |
| 19 | ServeStatus | 3 | oauth, redirects, tenant |

Asserted capability, outside the 19: `host.(collage.Origins)` appears in 8 repos (the same 8 as BaseURL). The ConfigHost-only methods are AddRenderFunc 7, AddTemplateFunc 5 and WrapMount 1.

Natural groups:
- **Environment (base, shared with ConfigHost):** DevMode, Logger, BaseURL, plus OriginFor and MultiOrigin folded in from Origins, Locales and BuildID. These are read-only facts every phase can offer.
- **Contribute routes:** RegisterDocument, Mount, Handle, and RegisterCommand. RegisterPage has 0 callers and is dropped. These are startup-only registrations.
- **Wrap requests:** Use. It has 18 repos, the widest after Logger, and could arguably sit in the contribute group.
- **Read the site map:** Pages, Page, PageURLs and URL. FragmentURL has 0 callers and is dropped. These are introspection used by sitemap, redirects, offline and meta.
- **Act at request time:** RenderFragment, ServeStatus and InvalidateTags. These are called from handlers and goroutines after Init, not during it.
