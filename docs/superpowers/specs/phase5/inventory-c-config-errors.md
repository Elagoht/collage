# Inventory C — errors, config, build, command, http, mount, url, data, once and small files

Source: collage main @ 30f5cfa (v0.59.0). 121 rows, input order. `plugins broken` counts non-test .go code in collage-* repos only (comments excluded).

| name | kind | decision | detail (new name / how) | why (one line) | plugins broken |
|---|---|---|---|---|---|
| BuildOptions | alias | DEFINE | define `type BuildOptions struct{OutDir, Locales, Clean, Concurrency, AllowDegraded}` in pkg/collage, convert in Build | every scaffolded main.go fills its fields; godoc through the alias shows none | 0 |
| BuildReport | alias | DEFINE | define in pkg/collage (Written, Skipped, Warnings, Findings, Errors, Duration) | callers read its fields; the alias hides them on pkg.go.dev | 0 |
| Builder | alias | REMOVE | use `collage.Build(ctx, app, opts)` (see NewBuilder) | a type whose only method is Build, held between two calls that always run back to back | 0 |
| ErrBuildFindings | var | KEEP |  | build error a CI caller branches on (fail on findings) | 0 |
| ErrBuildPanic | var | KEEP |  | recorded per page in BuildReport.Errors; distinct cause | 0 |
| ErrDangerousOutDir | var | KEEP |  | safety refusal a caller may report specially | 0 |
| ErrDegradedRender | var | KEEP |  | tells the caller AllowDegraded would have written it | 0 |
| ErrDuplicateOutputPath | var | MERGE | → ErrOutputPathCollision | same condition (two routes, one file) under two names; documents skip, pages fail — see passing #2 | 0 |
| ErrDuplicateRedirect | var | KEEP |  | redirects plugin checks it | 0 |
| ErrDynamicPathUnresolved | var | KEEP |  | SkipRecord.Err value; actionable (add WithStaticParams) | 0 |
| ErrEmptyDocumentBody | var | KEEP |  | served as 500 live and refused in build; part of the document handler contract | 0 |
| ErrEmptyRender | var | KEEP |  | refused regardless of AllowDegraded; distinct from ErrDegradedRender | 0 |
| ErrGuarded | var | KEEP |  | SkipRecord.Err value | 0 |
| ErrInvalidOutDir | var | RENAME | → ErrEmptyOutDir | only ever returned for an empty OutDir; matches ErrEmptyCacheDir, and "invalid" collides in meaning with ErrDangerousOutDir | 0 |
| ErrNilRenderer | var | MERGE | → ErrNilApp | returned for a nil *App; "Renderer" is internal/build's interface name | 0 |
| ErrNotStatic | var | KEEP |  | SkipRecord.Err value | 0 |
| ErrOutputPathCollision | var | KEEP |  | survivor of the merge; also used for documents | 0 |
| ErrPathEscapesOutDir | var | KEEP |  | security refusal | 0 |
| ErrRedirectShadowsFile | var | KEEP |  | build error, distinct cause | 0 |
| ErrUnresolvedToken | var | KEEP |  | explains a form page left out of a static build | 0 |
| NewBuilder | func | CHANGE | replace with `func Build(ctx context.Context, app *App, opts BuildOptions) (*BuildReport, error)`; nil app → ErrNilApp, empty OutDir → ErrEmptyOutDir | New-then-Build is one step; one exported type fewer; scaffold main.go and docs change, no plugin | 0 |
| SkipRecord | alias | CHANGE | DEFINE in pkg/collage and rename field Page → Name (pages and documents) | the field's own godoc apologises for "Page" naming a document | 0 |
| WarningRecord | alias | CHANGE | DEFINE in pkg/collage and rename field Page → Name | same as SkipRecord | 0 |
| CheckCommand | const | UNEXPORT |  | wire name between the `collage` binary and DispatchCommands; no user types it, the CLI hard-codes the string anyway | 0 |
| DispatchCommands | func | KEEP |  | scaffold main.go entry for plugin commands | 0 |
| ErrNilApp | var | KEEP |  | survivor for nil *App (DispatchCommands, Build) | 0 |
| ErrUnknownCommand | var | KEEP |  | the documented fall-through signal callers errors.Is | 0 |
| InspectCommand | const | UNEXPORT |  | same as CheckCommand | 0 |
| InspectedAction | alias | ASK | follows Inspection | see Inspection | 0 |
| InspectedDocument | alias | ASK | follows Inspection | see Inspection | 0 |
| InspectedFragment | alias | ASK | follows Inspection | see Inspection | 0 |
| InspectedFragmentPath | alias | ASK | follows Inspection | see Inspection | 0 |
| InspectedMount | alias | ASK | follows Inspection | see Inspection | 0 |
| InspectedPage | alias | ASK | follows Inspection | see Inspection | 0 |
| InspectedPlugin | alias | ASK | follows Inspection | see Inspection | 0 |
| Inspection | alias | ASK | (a) keep App.Inspect + 9 Inspected* types public and DEFINE them (add missing InspectedType), frozen under v1; or (b) move Inspect to an internal func, unexport all 9; the versioned collage-inspect JSON is the only contract | a JSON wire format with its own `version` field vs a Go API frozen until v2; no plugin or collagetest uses it | 0 |
| RuleNoPathInLocale | const | KEEP |  | Finding.Rule value a caller filters App.Check output by | 0 |
| RuleRouteParams | const | KEEP |  | same | 0 |
| RuleUnknownRoute | const | KEEP |  | same | 0 |
| RuleUnreachableLocale | const | KEEP |  | same | 0 |
| CacheConfig | type | KEEP |  | fields: see Config fields | 0 |
| Config | type | KEEP |  | fields: see Config fields | 0 |
| Config.ApplyDefaults | method | UNEXPORT |  | New applies it; public it is a second half-step entry point | 0 |
| Config.IsDevMode | method | REMOVE | read Config.DevMode | only exists to OR in Template.DevMode, which goes (Config fields) | 0 |
| Config.Validate | method | UNEXPORT |  | New reports the same errors; public, `Config{}.Validate()` fails ErrInvalidPort because defaults were not applied (passing #3) | 0 |
| ErrEmptyCacheDir | var | KEEP |  | reachable from New (disk cache without Dir) | 0 |
| ErrEmptyCacheVersion | var | UNEXPORT |  | its own godoc: New never returns it; the disk cache still uses it internally | 0 |
| ErrEmptyLocaleDefault | var | REMOVE |  | unreachable through New: ApplyDefaults sets "en" first | 0 |
| ErrEmptyTemplateRoot | var | REMOVE |  | unreachable through New: ApplyDefaults sets "./templates" when FS is nil | 0 |
| ErrInvalidBaseURL | var | RENAME | → ErrInvalidOrigin | also what ParseOrigin and OriginResolver results report (tenant hosts); change the message too; the internal sentinel is already ErrInvalidOrigin | 0 |
| ErrInvalidCacheType | var | KEEP |  | reachable from New | 0 |
| ErrInvalidPort | var | KEEP |  | reachable from New (negative or >65535) | 0 |
| ErrLocaleDefaultNotSupported | var | KEEP |  | reachable from New | 0 |
| ErrNegativeDuration | var | KEEP |  | one sentinel for all duration fields, field named in message; drop the godoc reference to internal types.ErrInvalidTimeout | 0 |
| LocaleConfig | type | KEEP |  | fields: see Config fields | 0 |
| ObservabilityConfig | type | KEEP |  | fields: see Config fields | 0 |
| SecurityConfig | type | KEEP |  | fields: see Config fields; godoc says "request-forgery protection" but it also holds FrameOptions/NoSniff — fix the sentence | 0 |
| ServerConfig | type | KEEP |  | fields fine | 0 |
| TemplateConfig | type | KEEP |  | fields: see Config fields | 0 |
| Data | type | KEEP |  | sealed interface, the only way to give a fragment data | 0 |
| DataHandler | func | RENAME | → LoadTagged | siblings are Load, Value, Effect (what it does); DataHandler is the old concept noun and reads like a type | 1 |
| Effect | func | KEEP |  | sibling of Load | 0 |
| Load | func | KEEP |  | most common data shape | 0 |
| Value | func | KEEP |  | fixed data | 0 |
| ErrCSRFCrossOrigin | var | KEEP |  | reported to error hooks; distinct cause an operator acts on | 0 |
| ErrCSRFDisabled | var | KEEP |  | returned by {{csrfToken}} with DisableCSRF; explains a render error | 0 |
| ErrCSRFHeaderRequired | var | KEEP |  | wraps ErrCSRFMissing but names a distinct, fixable cause (streaming action) | 0 |
| ErrCSRFInvalid | var | KEEP |  | reported to error hooks | 0 |
| ErrCSRFMismatch | var | KEEP |  | reported to error hooks | 0 |
| ErrCSRFMissing | var | KEEP |  | reported to error hooks | 0 |
| ErrDictKeyNotString | var | UNEXPORT |  | a template author's typo; nothing branches on it, the message says it all | 0 |
| ErrDictOddArgs | var | UNEXPORT |  | same as ErrDictKeyNotString | 0 |
| ErrDuplicateAction | var | KEEP |  | registration error (rule in Cross-cutting #1) | 0 |
| ErrEmptyActionName | var | KEEP |  | registration error | 0 |
| ErrInvalidActionMethod | var | KEEP |  | registration error; security-relevant | 0 |
| ErrMethodNotAllowed | var | KEEP |  | reported to error hooks per 405 | 0 |
| ErrNegativeBodyTimeout | var | KEEP |  | registration error | 0 |
| ErrNilAction | var | KEEP |  | registration error | 0 |
| ErrNilFragmentPath | var | KEEP |  | registration error | 0 |
| ErrNilRegistrable | var | KEEP |  | registration error from Register | 0 |
| ErrNoActionHandler | var | KEEP |  | registration error | 0 |
| ErrNoActionPaths | var | KEEP |  | registration error | 0 |
| ErrNoMethods | var | RENAME | → ErrNoActionMethods | its siblings are ErrNoActionPaths / ErrNoActionHandler; bare "NoMethods" says nothing about actions | 0 |
| ErrNoMountForAsset | var | UNEXPORT |  | always wrapped in ErrUnknownAsset (types/head.go:33); callers check the outer sentinel, the inner cause is in the message | 0 |
| ErrSourceConflict | var | KEEP |  | pairs with Fragment.Source; registration error | 0 |
| ErrStreamingBody | var | KEEP |  | honeypot checks it | 0 |
| ErrStreamingBodyMethod | var | KEEP |  | registration error | 0 |
| ErrTemplateEscapesRoot | var | KEEP |  | security refusal from New | 0 |
| FindPlugin | func | KEEP |  | typed lookup, websocket uses it | 0 |
| BaseURL | func | KEEP |  | 6 plugins; needs both ctx and Host, so a func on rc is right | 0 |
| ErrHandlerFailed | var | KEEP |  | reported to error hooks | 0 |
| ErrInvalidHandlerPrefix | var | MERGE | → ErrInvalidPrefix | App.Handle and Mount both refuse a malformed prefix; the call site already tells which, the message says why | 0 |
| ErrNilHandler | var | KEEP |  | registration error from Handle/Use | 0 |
| ErrVaryOutsideRequest | var | KEEP |  | consent checks it | 0 |
| ErrVaryTooLate | var | KEEP |  | returned by Vary and SkipCache; documented misuse to test for | 0 |
| IsCapture | func | KEEP |  | 9 plugins; the one way to tell a build capture from a visitor | 0 |
| SkipCache | func | KEEP |  | preview primitive | 0 |
| Varied | func | CHANGE | `Varied(ctx context.Context, header string) (string, bool)` — takes the ctx every data handler already has | rc form only reads rc.Request.Context(); one function covers handlers, StaticParams, documents | 2 |
| VariedContext | func | MERGE | → Varied (ctx form) | same lookup on a context; one name | 1 |
| Vary | func | KEEP |  | cache-dimension primitive | 0 |
| ErrInvalidPrefix | var | KEEP |  | survivor; covers Handle and Mount prefixes | 0 |
| ErrMountConflict | var | KEEP |  | registration error | 0 |
| ErrMountShadowsRoute | var | KEEP |  | registration error | 0 |
| ErrNilFS | var | KEEP |  | registration error | 0 |
| Mount | alias | KEEP |  | element type of App.Mounts; read-only use, alias fine | 0 |
| MountOption | alias | KEEP |  | ogimage names it | 0 |
| WithCacheControl | var | CHANGE | declare as `func WithCacheControl(v string) MountOption { return asset.WithCacheControl(v) }`, not `var` | an exported var can be reassigned by any importer; call sites unchanged | 0 |
| WithoutBuildCopy | var | CHANGE | declare as func, not var | same as WithCacheControl | 0 |
| Cached | func | KEEP |  | cross-page data cache, the user's real ask | 0 |
| ErrCachedFetchPanicked | var | KEEP |  | what a waiter gets when another's fetch panicked; distinct from fetch errors | 0 |
| Once | func | KEEP |  | per-render single flight | 0 |
| LoadPluginConfig | func | KEEP |  | scaffold uses it; returns exactly Config.PluginConfig's type | 0 |
| SafeRedirect | func | KEEP |  | open-redirect guard, oauth uses it | 0 |
| PrintBuildReport | func | KEEP |  | scaffold uses it; keeps skip/fail presentation consistent | 0 |
| ErrAmbiguousFragmentPath | var | KEEP |  | returned by App.FragmentURL | 0 |
| ErrLocaleUnreachable | var | KEEP |  | returned by App.URL and registration | 0 |
| ErrNoPathInLocale | var | KEEP |  | returned by App.URL; Check rule | 0 |
| ErrRouteParams | var | KEEP |  | returned by App.URL; Check rule | 0 |
| ErrUnknownAsset | var | KEEP |  | returned by rc.Asset / {{asset}} | 0 |
| ErrUnknownFragmentPath | var | KEEP |  | live and websocket check it | 0 |
| ErrUnknownRoute | var | KEEP |  | returned by App.URL; Check rule | 0 |

## Config fields

I walked Config, ServerConfig, SecurityConfig, TemplateConfig, CacheConfig, LocaleConfig and ObservabilityConfig (all in pkg/collage/config.go) field by field. Every field not listed here is KEEP.

| name | kind | decision | detail (new name / how) | why (one line) | plugins broken |
|---|---|---|---|---|---|
| TemplateConfig.DevMode | field | REMOVE | set Config.DevMode | `core` only ever uses `cfg.DevMode \|\| cfg.Template.DevMode` (app.go:495, host.go:200), so it is a second spelling of one switch. Removing it also removes IsDevMode. No plugin or scaffold sets it. | 0 |
| SecurityConfig.NoSniff | field | CHANGE | `DisableNoSniff bool` | It is the only tri-state `*bool` in Config, and its sibling is `DisableCSRF bool`. The zero value already means "send it". secure has its own Options, so it is untouched. | 0 |
| LocaleConfig.DisablePathLocale | field | REMOVE | set `Supported` to one locale | Once it is set, every non-default locale is unreachable (url.go:190, localePath), and rc.Locale is always Default, which is exactly the single-locale config. | 0 |
| CacheConfig.MaxKeysPerTag | field | REMOVE | the tracker caps each tag at MaxEntries | It is an internal tracker knob that needs a 25-line godoc. With a built-in cache the tracker is already held to MaxEntries in total. Both default to 10000. | 0 |
| CacheConfig.Type | field | CHANGE | `Type CacheType` with `const CacheMemory CacheType = "memory"; CacheDisk = "disk"` | It is stringly typed, and Validate exists partly to catch typos. String literals still compile; only a `string` variable needs a conversion. | 0 |
| ObservabilityConfig.Metrics / .Tracer | field | CHANGE | spell the field types `Metrics` / `Tracer` (the pkg aliases) and drop `observability.MetricsOrNoop` from the godoc | It is the same type, but the public godoc currently names an internal package. This is not breaking. | 0 |
| TemplateConfig.Timeout | field | KEEP | (see passing #6) | The field itself is fine. Documents having no per-route timeout is the shape issue. | 0 |

## Cross-cutting

1. **Sentinel rule.** A sentinel is exported if a public function returns it, if it reaches an ErrorHook, or if it is a SkipRecord.Err value. Registration-time sentinels (ErrNilAction, ErrNoActionPaths, ErrMountConflict, …) count, because they are what an app's tests assert. A sentinel is not exported if it is only ever wrapped inside another exported one (ErrNoMountForAsset), if it is unreachable through New (ErrEmptyLocaleDefault, ErrEmptyTemplateRoot, ErrEmptyCacheVersion), or if it is a template author's typo (ErrDict*). The ErrCSRF* family, the build Err* family and the url.go family all stay.
2. **Name after the concept, not the internal package.** Use ErrNilRenderer → ErrNilApp and ErrInvalidBaseURL → ErrInvalidOrigin. No public godoc should name `internal/...` identifiers: ErrNegativeDuration cites types.ErrInvalidTimeout, ObservabilityConfig cites observability.MetricsOrNoop, and NewBuilder's godoc explains build.Renderer boxing. Add a `go doc`-grep CI check for `internal/` and `types\.` in pkg/collage comments.
3. **One name per condition.** Make these merges: ErrDuplicateOutputPath → ErrOutputPathCollision, ErrInvalidHandlerPrefix → ErrInvalidPrefix, VariedContext → Varied. Rule: if two sentinels or functions differ only in which call site produced them, keep one.
4. **Families use one prefix shape.** Action errors are `ErrNoAction*` / `Err*Action*` (so ErrNoMethods → ErrNoActionMethods). Empty-required-field errors are `ErrEmpty<Field>` (ErrInvalidOutDir → ErrEmptyOutDir, matching ErrEmptyCacheDir). Opt-out booleans are `Disable<X> bool` (DisableCSRF, DisableNoSniff). There are no `*bool` and no "-" magic in new fields.
5. **No exported `var` holding a function.** WithCacheControl and WithoutBuildCopy become `func`. Apply the same rule to any `var X = internal.X` that is a function in the other inventories (A/B). Sentinel error vars are the only exported vars allowed.
6. **DEFINE only what users construct or read field by field.** That means BuildOptions, BuildReport, SkipRecord, WarningRecord, and the Inspection family if it stays public. Read-only handles (Mount) and option funcs (MountOption) stay aliases.
7. **One entry point per job.** New is the only way to apply defaults and validate (ApplyDefaults and Validate are unexported). `collage.Build(ctx, app, opts)` replaces NewBuilder + Builder.Build. A data helper takes the `ctx` a handler already has, not `rc`, when it needs only the context (Varied). It takes `rc` only when it needs the request too (BaseURL).
8. **Data constructor names are verbs/nouns of what they give.** These are Load, LoadTagged, Value and Effect. "Handler" stays out of public names that are not handler types.

## Breaking-change candidates found in passing

1. **InspectedType is unnameable.** `Inspection.Types map[string]InspectedType` (internal/core/inspect.go:38) has no alias in pkg/collage, and neither has `InspectionVersion`. Users outside the module can read the field but cannot declare its type. Either add both or take option (b) of the Inspection ASK.
2. **Output-path collisions behave two ways.** Pages fail the whole build before rendering (builder.go:977, ErrOutputPathCollision). Documents skip the second one and carry on (document.go:150, ErrDuplicateOutputPath, recorded only as a skip). It is the same mistake, so it should get one sentinel and one behaviour. Fail for both, since a silently missing sitemap is worse than a stopped build.
3. **`Config.Validate` is a trap on its own.** It does not apply defaults, so `(&collage.Config{}).Validate()` returns ErrInvalidPort, even though New would accept that exact config. The docs site uses Validate ten times. Unexport it, or make it validate a defaulted copy.
4. **Exported function vars are mutable.** `collage.WithCacheControl = nil` compiles in any importer and would break every mount option in the process. Make them funcs (non-breaking for callers).
5. **ErrInvalidBaseURL comes from more than BaseURL.** ParseOrigin is used by tenant (host.go:36), and OriginResolver results also report this sentinel (internal/plugin/registry.go:220). The message itself says "collage: BaseURL must be a bare origin", so a bad tenant origin blames BaseURL. Rename it before freezing.
6. **TemplateConfig.Timeout is the only bound on a document handler.** Document has no per-route Timeout, so raising the timeout for one slow fragment raises it for every sitemap and feed (the godoc admits this). Add `DocumentBuilder.WithTimeout` before v1 if documents are meant to be bounded independently. That is additive, but the coupling becomes a frozen semantic once v1 ships.
