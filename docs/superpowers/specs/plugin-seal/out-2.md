# Plugin sealing inventory, batch 2

Plugins: opti-image, tenant, session, deploy, jobs, i18n, markdown, fail2ban.

The convention proposed here (see Cross-cutting 1): **`New(opts Options) *Plugin` and `type Options`, with no `NewWith`.** 31 of the 44 plugins already use this shape, and `collage plugin add` writes `x.New(x.Options{})` for it. In this batch, opti-image, tenant, deploy and jobs deviate from it.

"docs" below means `~/Desktop/collage-docs/content/plugins.md` and `tr/plugins.md`, plus any other page named.

---

## collage-opti-image (v0.3.7)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| `Name` | const | KEEP | `"elagoht/opti-image"` | | |
| `New() *Plugin` | func | CHANGE | becomes `New(opts Options) *Plugin`. It keeps the clone of AllowedOrigins/Files that NewWith does today | one constructor convention | README, docs, users' plugins.go |
| `NewWith(cfg Config)` | func | MERGE → `New` | | same | README, docs |
| `Config` | type | RENAME → `Options` | | one options type name | README, docs |
| `Origin` (Scheme, Host) | type | REMOVE | `allowedOrigins` becomes `[]string` of `"https://host[:port]"`, each checked with `collage.ParseOrigin` plus a host-required check. Users write `"allowedOrigins": ["https://cms.example.com"]` | duplicates core origin parsing, and ogimage already takes `imageOrigins []string` for the same fact | README, docs (`allowedOrigins` example) |
| `allowedOrigins` | config key | CHANGE | from an array of `{scheme,host}` objects to an array of origin strings (see `Origin`) | one origin spelling across plugins | docs |
| `Duration` (+Marshal/UnmarshalJSON) | type | CHANGE → core `collage.Duration` | see Cross-cutting 2 | the same type exists in 8 plugins | none outside |
| `fetchTimeout`, `originMaxAge` | config key | CHANGE | string-only durations: a bare number is an error, not nanoseconds | `"fetchTimeout": 10` today means 10 ns, so every fetch times out without a clear error | |
| `WebPMode`, `WebPOff`, `WebPOn`, `WebPAuto` (+JSON methods) | type/consts | KEEP | | | |
| `WebPEncoder`, `RegisterWebPEncoder` | iface/func | KEEP | | a deliberate extension point (a cgo libwebp encoder), set at init the way `image.RegisterFormat` is | |
| `Plugin` + Name/Version/Configure/Init/OnAfterRender/Shutdown | type/methods | KEEP | | | |
| `Purge`, `PurgeSource` | method | KEEP | | documented. A mount bypasses tag invalidation, so these are the only way to drop a bad origin image | docs |
| `Files` (`json:"-"`) | field | KEEP | | | |
| `disabled` | config key | KEEP | | matches basicauth/jsonld `disabled` | |
| `noDiskCache` + `cacheDir` | config keys | KEEP | | two separate facts (where vs whether), and the zero value is the default | |
| `cacheBytes`, `maxSourceBytes`, `maxPixels`, `quality`, `prefix`, `alphaThreshold`, `webp` | config keys | KEEP | | | |
| route `/_image/` | route | KEEP | | matches the `/_<name>/` pattern | |

plugin verdict: API changes

## collage-tenant (v0.1.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name` | const | KEEP | | | |
| `New() *Plugin` | func | CHANGE | becomes `New(opts Options) *Plugin` | convention | docs, package doc |
| `NewWith(opts Options)` | func | MERGE → `New` | | convention | docs (`tenant.NewWith(tenant.Options{…})`) |
| `Options` | type | KEEP | | | |
| `Tenant`, `Static`, `Resolver` | types | KEEP | | `Static` = a tenant plus its hosts, while a Resolver answers a `Tenant`. The split is real | docs use `tenant.Static` |
| `Duration` (+UnmarshalJSON) | type | CHANGE → core `collage.Duration` | | duplicated | none |
| `ID(rc)`, `IDFromContext(ctx)` | func | KEEP | | same `X(rc)`/`XFromContext(ctx)` pair as session's `Get`/`FromContext` | docs caching.md |
| `Plugin.Origin` | method | KEEP | implements `collage.OriginResolver` | | |
| `Plugin.Lookup` | method | KEEP | | lets an app read a tenant's origin by ID, e.g. for a mail link | |
| `Plugin.OnBuildFinished`, Init, Name, Version, Shutdown | methods | KEEP | | | |
| `ErrNoTenants`, `ErrInvalidTenant`, `ErrDuplicateHost`, `ErrDuplicateID`, `ErrBypassConflict` | vars | KEEP | | | |
| build warning code `"tenant/no-host"` | finding code | RENAME → `"tenant-no-host"` | | every other plugin uses `<plugin>-<code>` (`deploy-target`, `basicauth-exported`, `redirects-overlap`) | none |
| `ttl` | config key | CHANGE | string-only Duration (see Cross-cutting 2) | | |
| `tenants`, `bypass`, `maxHosts` | config keys | KEEP | | | |
| manifest | collage.json | CHANGE | add `"setup"`: list tenants in `"tenants"`, or pass `Resolve` from Go | `New(Options{})` with no tenants fails Init with ErrNoTenants, and nothing tells the user why | |

plugin verdict: API changes

## collage-session (v0.2.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)`, `Options`, `Plugin` + methods | | KEEP | already follows the convention | | |
| `Options.Key []byte` + `Options.KeyHex string` (`json:"key"`) | fields | MERGE → `Key Secret \`json:"key"\`` | one field whose type decodes hex from JSON and is a `[]byte` in Go (see Cross-cutting 3). `KeyHex` goes away | two fields for one fact, and KeyHex silently overrides Key | docs; oauth has the same pattern |
| `Options.PreviousKeys` + `PreviousKeysHex` | fields | MERGE → `PreviousKeys []Secret \`json:"previousKeys"\`` | | same | docs |
| `maxAge`, `idleTimeout` (int seconds) | config keys | CHANGE | to `collage.Duration` strings: `"168h"`, `"30m"`. A bare number fails loudly because of the string-only rule | every other plugin in this batch writes durations as strings. The loud failure stops old configs from silently becoming nanoseconds | docs configuration.md, deployment.md, plugins.md (`idleTimeout`) |
| `MaxCookieSize` | const | UNEXPORT | | used by nothing outside the package. It is a browser fact, not a knob | none |
| `ErrTooLarge`, `ErrNoSession`, `ErrCommitted` | vars | KEEP | | oauth uses ErrTooLarge | oauth |
| `Session` + `ID/Get/Set/Delete/Clear/Regenerate` | type/methods | KEEP | | | oauth uses `Session` |
| `Get(rc)`, `FromContext(ctx)` | func | KEEP | | oauth uses FromContext | oauth |
| `Require`, `RequireUser`, `UserKey` | func/const | KEEP | | oauth uses UserKey | oauth |
| `cookie`, `path`, `domain`, `secure`, `sameSite`, `encrypt` | config keys | KEEP | | `secure` false = auto, and that is the zero value | |
| cookie `collage_session` | cookie | KEEP | | matches `collage_flash` and `collage_consent` | |

plugin verdict: API changes

## collage-deploy (v0.1.2)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name` | const | KEEP | | | |
| `New() *Plugin` | func | CHANGE | becomes `New(opts Options) *Plugin` | convention | README, package doc, docs |
| `NewWith(cfg Config)` | func | MERGE → `New` | | convention | README, docs |
| `Config` | type | RENAME → `Options` | | convention | README, docs |
| `Target`, `TargetNone`, `TargetNetlify`, `TargetCloudflare`, `TargetVercel`, `TargetGitHubPages` | type/consts | KEEP | | | |
| `Targets` | var ([]Target) | UNEXPORT | | a mutable exported slice that only Init's check reads. Appending to it would let an unknown target through | none |
| `ManifestFile` | const | KEEP | | names a file users see in their output (and may .gitignore) | |
| `Compact`, `Expand`, `HeaderRule` | func/type | UNEXPORT | the tests move to `export_test.go` | exported for the external test package only. Nothing else uses them | none (only compact_test.go) |
| `ServedAt`, `ServedPath`, `ImpliedByExtension` | func | UNEXPORT | | the same: internal helpers, no outside users | none |
| `Plugin` + Init/OnBuildFinished/Name/Version/Shutdown | | KEEP | | | |
| `output.go` `builtSource` uses raw `f.Kind` strings | internal | CHANGE → core | compare against `collage.BuiltPage/BuiltDocument/BuiltAsset` once v0.61 lands | duplicates core kind strings | none |
| `target` | config key | KEEP | | | |
| finding codes `deploy-*` | codes | KEEP | | | |

plugin verdict: API changes

## collage-jobs (v0.1.1)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name` | const | KEEP | | | |
| `New() *Plugin` | func | CHANGE | becomes `New(opts Options) *Plugin` | convention | docs, README |
| `NewWith(cfg Config)` | func | MERGE → `New` | | convention | README (`jobs.NewWith(jobs.Config{Disabled: off})`), docs |
| `Config` | type | RENAME → `Options` | | convention | README, docs |
| `ErrDraining`, `ErrQueueFull` | vars | KEEP | | | |
| `Job`, `Job.RunOnStart`, `Job.Timeout` | type/methods | KEEP | | | docs |
| `Plugin.Every`, `Plugin.Cron` | methods | KEEP | | | |
| `NewQueue[T]`, `Queue[T]`, `Queue.Enqueue`, `QueueOptions[T]` | | KEEP | the `T any` type parameter is a constraint, not an `any` value | | |
| `Plugin.OnServe` | method | KEEP | `collage.ServeHook` | | |
| `Plugin.Start(ctx)` | method | KEEP | | core has no public way for an app that runs its own server over `app.Handler` to fire ServeHooks. If core ever adds one, this becomes a REMOVE | docs deployment.md, writing-plugins.md |
| Init/Name/Version/Shutdown | | KEEP | | | |
| `disabled` ([]string of job names) | config key | KEEP | | other plugins use `disabled` as a bool that turns the plugin off. Here `"disabled": true` fails to decode loudly, and inside `elagoht/jobs` the list reads clearly | |
| `timezone` | config key | KEEP | | | |
| manifest | collage.json | CHANGE | add `"setup"`: keep the plugin in a variable and define jobs with `Every`/`Cron` before `Start` | `collage plugin add` writes `jobs.New(...)` inline in the plugins list, which leaves nothing to call `Every` on | |

plugin verdict: API changes

## collage-i18n (v0.2.5)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)`, `Options`, `Plugin` + hook methods | | KEEP | already follows the convention | | |
| `T(rc, key string, args ...any) string` | func | CHANGE | `args ...string` (name, value pairs). Counts go through `TN`'s `n int` | no `any` where a type is possible. The Go caller always has strings, and template funcs keep their own variadic signature | validate (doc comment only), docs |
| `Translator.T`, `Translator.TH`, `Translator.TN` | methods | CHANGE | `args ...string`, as `T` | same | docs |
| `Translator`, `Plugin.In`, `Translator.Locale`, `Translator.Funcs` | | KEEP | | | docs, writing-plugins.md |
| `Options.FS`, `Options.Plural` (`json:"-"`) | fields | KEEP | | | |
| `dir`, `strict` | config keys | KEEP | | | |
| template funcs `t`, `th`, `tn` | funcs | KEEP | | short, documented, and in the VS Code catalog | |
| finding codes `"missing-translation"` (render) and `"untranslated"` (build) | codes | RENAME → `"i18n-missing"`, `"i18n-untranslated"` | | the only codes in this batch without a plugin prefix | none |
| manifest | collage.json | CHANGE | add `"setup"` as markdown has: "Catalogs are read from an `fs.FS`: `i18n.New(i18n.Options{FS: locales})`" | without FS, Configure fails with "Options.FS is required", so `collage plugin add` writes code that cannot start | |

plugin verdict: API changes

## collage-markdown (v0.2.4)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)`, `Options`, `Plugin` | | KEEP | | | |
| `Doc` (12 fields), `Heading` | types | KEEP | | | |
| `Plugin.List(ctx)` + `Plugin.ListIn(ctx, locale)` | methods | MERGE → `List(ctx, locale string)` | `""` means the default locale | two methods for one query. Neither is used outside the repo | none (docs do not mention them) |
| `Plugin.Get`, `Tag`, `DirTag`, `StaticParams` | methods | KEEP | | | docs (DirTag) |
| `Plugin.Handler`, `Plugin.IndexHandler` | methods | KEEP | | "data handler" is the docs' term for `collage.Data` | docs |
| `Plugin.OnCacheInvalidate`, Init, Name, Version, Shutdown | | KEEP | | | |
| `Options.Name` (`json:"-"`, instance suffix) | field | KEEP | | | |
| `dir`, `localeDirs`, `drafts`, `unsafe` | config keys | KEEP | | `unsafe`/`drafts` are off by default, which is the zero value | |
| dependency tags `markdown:<dir>/…` | tag scheme | KEEP | | | |

plugin verdict: API changes (the List merge only; everything else unchanged)

## collage-fail2ban (v0.1.8)

| item | kind | decision | detail | why | breaks |
|---|---|---|---|---|---|
| `Name`, `New(opts Options)`, `Options`, `Plugin` | | KEEP | | | |
| `Duration` (+UnmarshalJSON) | type | CHANGE → core `collage.Duration` | string-only | duplicated. `"banTime": 3600` today bans for 3.6 µs with no error | none |
| `Jail` | type | KEEP | | | docs |
| `Jail.Off` / jail key `off` | field/key | RENAME → `Disabled` / `"disabled"` | | `disabled` is the bool word other plugins use (opti-image, basicauth, jsonld) | docs |
| `Ban` (Prefix, Jail, Until, Count) | type | KEEP | | | |
| `Options.OnBan` (`json:"-"`) | field | KEEP | | | |
| `Plugin.Report`, `Forgive`, `Ban`, `Unban`, `Bans` | methods | KEEP | | the app-facing jail API, documented | docs |
| `Plugin.OnRequest`, Configure, Init, Name, Version, Shutdown | | KEEP | | | |
| internal `servesPath` / `kind != "mount"` raw route-kind strings | internal | CHANGE → core | `collage.RouteKindPage/Document/Action/Mount` (v0.61) | duplicates core strings, and a core rename would silently break the ban logic | none |
| `inDevelopment` | config key | KEEP | | matches errortrack and indexnow (offline's `inDev` is the outlier) | |
| `allow`, `probePaths`, `maxBanTime`, `maxTracked`, `maxBans`, `jails` | config keys | KEEP | | | |
| jail names `probe`, `notfound`, `manual` | names | KEEP | | | |
| manifest `order: -25` | collage.json | KEEP | | | |

plugin verdict: API changes

---

## Cross-cutting

1. **One constructor shape: `New(opts Options) *Plugin`, `type Options`, no `NewWith`.** It is already the majority (31 of 44), and `collage plugin add` writes `x.New(x.Options{})`. This batch's deviators: opti-image (`New()`/`NewWith`/`Config`), tenant (`New()`/`NewWith`), deploy (`New()`/`NewWith`/`Config`), jobs (`New()`/`NewWith`/`Config`). Deviators elsewhere: consent, jsonld, live, minimizer, ogimage, devtoolbar, health, uploads. feed, paginate and websocket take real arguments, and that is fine.
2. **Add `collage.Duration` to core (v0.61) and delete the per-plugin copies. String only: `"10s"`, and a JSON number is an error naming the key.** Today a number means nanoseconds, so `"banTime": 3600` or `"fetchTimeout": 10` is silently near-zero. The string-only rule also lets session (and flash, honeypot) move from int seconds to Duration, because an old config then fails loudly. This batch: opti-image, tenant, fail2ban (copies), session (int seconds). Elsewhere: cdnpurge, errortrack, health, live, ogimage (copies), flash, honeypot, consent (`maxAgeDays`).
3. **Secret keys as one hex-decoding type (`collage.Secret []byte`, or one per plugin), not `Key []byte` + `KeyHex string`.** This batch: session. Elsewhere: oauth, flash, honeypot, secure.
4. **Origin lists are `[]string` of `scheme://host[:port]`, checked with `collage.ParseOrigin`.** opti-image is the one to change (its `{scheme,host}` objects). ogimage's `imageOrigins` already does this.
5. **Build/render finding codes are `<plugin>-<code>`.** tenant (`tenant/no-host`) and i18n (`untranslated`, `missing-translation`) deviate.
6. **Use core's v0.61 RouteKind*/Built* constants instead of literal strings.** This batch: fail2ban (route kinds), deploy (BuiltFile.Kind).
7. **Every plugin that cannot start, or cannot be used, from `collage plugin add`'s zero-value `New(...)` call has a `"setup"` in collage.json.** This batch: i18n (FS required), jobs (needs a variable to call `Every`/`Cron` on), tenant (needs `tenants` or `Resolve`). markdown already has one.
8. **Bool config keys: `disabled` turns a whole plugin or part off, and `inDevelopment` opts in to dev mode.** fail2ban's jail `off` is renamed. offline's `inDev` (not in this batch) should be renamed too.
9. **Exported only for an external `_test` package means UNEXPORT, with an `export_test.go` bridge.** deploy (6 identifiers).
10. **Error-string prefixes are mixed: `opti-image:`, `tenant:`, `elagoht/deploy:`, `elagoht/jobs:`.** Not API, but make them consistent with one choice (the bare plugin short name) while the plugins are open anyway. This batch: deploy, jobs.

## Bugs found in passing

1. **i18n and jobs `collage plugin add` output does not work.** i18n's manifest has no `setup`, and `i18n.New(i18n.Options{})` fails Configure because FS is required. jobs' inline `jobs.New()` leaves no variable to define jobs on.
2. **Duration footgun (opti-image, tenant, fail2ban).** A bare JSON number is read as nanoseconds. `fetchTimeout: 10` makes every origin fetch time out. `jails.*.banTime: 3600` bans for 3.6 µs. Neither gives an error. (opti-image's `originMaxAge` alone catches it, through its ≥1s check.)
3. **session: `KeyHex` silently wins over a Go-set `Key`.** `configure()` also appends the hex PreviousKeys onto `o.PreviousKeys` on every Init, so a Plugin value started twice (a test reusing it) carries duplicate keys. Harmless but wasteful, and it goes away with the Cross-cutting 3 merge.
4. **Core `collage.ParseOrigin` accepts a hostless origin (`https://:8080`), according to tenant's own comment in host.go.** tenant wraps it with a host check. Any other caller of ParseOrigin (opti-image after the change, ogimage) would need the same wrapper, so the check belongs in core.
5. **fail2ban compares `collage.RouteInfo(ctx).Kind` with literal `"page"/"document"/"action"/"mount"`.** If the core strings ever change, probe detection silently treats every probe as unserved.
