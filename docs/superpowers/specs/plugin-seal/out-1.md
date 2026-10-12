# Plugin sealing inventory, reviewer 1

Plugins: validate, ogimage, jsonld, live, uploads, websocket, ratelimit.

Usage was checked by grepping the other plugin repos, collage-docs/content and the core repo. In the core repo, every hit for these seven plugins is in docs only: docs/http.md, docs/actions.md, docs/plugins.md, and superpowers plans/specs. No scaffold `.go.tmpl` names them. collage-health references ratelimit only in its README.

The convention applied below is the one in Cross-cutting #1: `type Options`, `func New(opts Options) *Plugin`, no `NewWith`, and the zero Options works.

## validate (v0.1.7)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| Name | const | KEEP | | | |
| New(opts Options) | func | KEEP | | already the convention | |
| Options | type | KEEP | | | |
| Options.NoRefill / `noRefill` | config | CHANGE | make it additive: "password" is always excluded, and the list adds more parts | Today `"noRefill": ["pin"]` silently starts echoing passwords back into the HTML. A security default should not disappear because a key was set. | README, collage.json schema description |
| Options.Messages / `messages`, LocaleMessages / `localeMessages` | config | KEEP | | | |
| Form(rc) | func | KEEP | | | docs ×8 |
| (new) Values(rc, url.Values) *Validator | func | CHANGE (add before v1) | a validator over values already read, for `Refuse` and refill | After `uploads.Receive` has streamed the body, `validate.Form` finds an empty form (collage-uploads README:93). The non-file fields in `Received.Values` cannot be checked or refused with the 422 flow today. | uploads README |
| Refuse(rc, v, page) | func | KEEP | | consistent with collage.RenderPage | docs ×4 |
| Validator, .Field, .Value, .Valid, .Errors, .Fail | type/methods | KEEP | | | |
| Field and its 12 methods (Required, MinLen, MaxLen, Email, URL, Int, Range, OneOf, Matches, Equal, Custom, Message) | type/methods | KEEP | | | |
| RuleRequired … RuleEqual (10) | const | KEEP | | they are the documented keys of `messages` | |
| Plugin + Name/Version/Configure/Init/Shutdown | type/methods | KEEP | | | |
| template funcs `fieldError`, `fieldValue`, `hasErrors` | template func | KEEP | | | |

plugin verdict: API changes (behaviour of `noRefill`, plus one addition)

## ogimage (v0.2.4)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| New() + NewWith(cfg Config) | func | MERGE → `New(opts Options)` | one constructor; `plugin add` writes `ogimage.New(ogimage.Options{})`. The setup note becomes "fill Options.Templates". | convention | README, DESIGN.md, collage.json `setup`, docs ×2 (NewWith/Config) |
| Config | type | RENAME → Options | | convention | same as above; ErrNoTemplates message names "Config.Templates" |
| Card, Font | type | KEEP | | | |
| Set(rc, tmpl, card) | func | KEEP | | | docs |
| RecordedCard, Plugin.Recorded | type/method | KEEP | Only the bookkeeping behind it should change: record in dev mode, or cap it (see Bugs #1). | documented test hook | |
| Draw(cfg Config, html) | func | CHANGE | `Draw(ctx context.Context, opts Options, html string)`. It should validate opts first; today a nil FontFiles with Fonts set panics (Bugs #2). | It runs on context.Background, so RenderTimeout never applies. It is the documented golden-test entry point. | README:328, DESIGN.md:565 |
| SiteInfo, PageInfo | type | UNEXPORT | Templates still read `.Site.*` and `.Page.*`, and the README table documents them. | They appear in no exported signature, so they were exported only for convenience. | DESIGN.md:148 |
| DefaultRoot, DefaultCardDir, DefaultPrefix, DefaultMaxEntries, DefaultMaxBytes, DefaultRenderTimeout | const | UNEXPORT | The defaults stay in field docs and the README. | Exporting them makes each default value API, and nothing outside imports them (Cross-cutting #6). | Config field docs mention DefaultMaxEntries/DefaultMaxBytes/DefaultRenderTimeout |
| Duration (+ MarshalJSON/UnmarshalJSON) | type | CHANGE → use a shared core type | see Cross-cutting #3 | duplicated in 8 plugins with different behaviour | collage-snippets-highlighter (schema generator) |
| ErrNoTemplates, ErrInvalidConfig, ErrNoBaseURL, ErrUnknownTemplate, ErrUnsupported, TemplateError | var/type | KEEP | Reword the ErrNoTemplates text to say "Options.Templates". | | |
| `imageOrigins` validation | config | CHANGE → use core | validate each entry with `collage.ParseOrigin` | It repeats core's origin parsing by hand (config.go validate). | |
| BaseURL / dev-origin handling (Init, origin(), canResolve) | internal | CHANGE → use core | lean on `collage.BaseURL(rc)` / `host.OriginFor`; drop the plugin's own `url.Parse` of BaseURL | duplicate of core | |
| `siteName` | config | ASK | see Cross-cutting #7 (meta, jsonld and ogimage each have `siteName`) | | |
| `default` | config | KEEP | | | |
| `root` + `cardDir` | config | KEEP | | Names are relative to `root`, as collage's own template names are. | |
| route `/_og/`, dev route `/_og-preview/`, command `ogimage` | route/cmd | KEEP | | | |
| Plugin + Name/Version/Configure/Init/Shutdown/OnBeforeRender/OnAfterRender/OnBuildFinished | methods | KEEP | | hooks | |

plugin verdict: API changes

## jsonld (v0.2.10)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| New() + NewWith(cfg Config) | func | MERGE → `New(opts Options)` | | convention | docs ×8 for `jsonld.New()` become `jsonld.New(jsonld.Options{})` |
| Config | type | RENAME → Options | | convention | README |
| `siteURL` (Config.SiteURL) | config | REMOVE | Users do nothing: the WebSite node's url becomes `collage.BaseURL(rc)`, the app's `baseURL` per render, which is multi-origin correct. | It is a second key for the app's BaseURL, and it is the only `siteURL` among 44 plugins (cdnpurge, indexnow and sitemap say `baseURL`). | README, collage.json |
| `siteName`, `searchURL`, `disabled` | config | KEEP (`siteName`: see Cross-cutting #7) | | | |
| HoistArea | const | UNEXPORT | The layout writes `{{hoist "head"}}` as for every head declaration. | No user anywhere; exported because it was convenient. | |
| Emit | func | KEEP | | | docs ×6, core docs |
| Node, Raw, Article, BlogPosting, Blog, Person, WebSite, Breadcrumb, BreadcrumbList (+ Type/MarshalJSON) | types | KEEP | | | docs |
| Plugin + Name/Version/Init/Shutdown/OnBeforeRender | methods | KEEP | | | |

plugin verdict: API changes

## live (v0.4.3)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| New() + NewWith(cfg Config) | func | MERGE → `New(opts Options)` | | convention | collage-websocket (tests, README), core docs, docs ×6 |
| Config | type | RENAME → Options | | convention | collage-websocket README/doc comment ("live.Config.MaxStreamAge") |
| Duration (+ UnmarshalJSON) | type | CHANGE → use a shared core type | Today it has no MarshalJSON, rejects `null` (Bugs #4), and rejects nanoseconds, all unlike ogimage.Duration. | duplicate | collage-snippets-highlighter |
| DefaultPrefix | const | UNEXPORT | | Nothing imports it, and the default is documented on `prefix`. | |
| (new) Plugin.KeepAlive() time.Duration | method | CHANGE (add) | the accessor websocket reads instead of its own `Ping` | one fact, one key (see websocket) | |
| Subscribe, Subscription (Close/Cookie/Initial/Wait), Message, HeadItem, Watch, ParseWatches, ErrClosed, ErrNoFragments, ErrTooManyFragments, Transport, UseTransport, MaxStreamAge | API | KEEP | | This is the transport contract. | removing any of them breaks collage-websocket |
| ClientPath, StreamPath | method | KEEP | | for layouts without `{{liveClient}}` (RegisterPlugin) and for custom clients | |
| CloseStreams, OnCacheInvalidate, Configure/Init/Shutdown/Name/Version | methods | KEEP | | core StreamCloser and hooks | |
| `prefix`, `noStream`, `keepAlive`, `maxFragments`, `maxStreamAge` | config | KEEP | | | |
| template func `liveClient`, attributes `data-collage-*`, routes `<prefix>client/…`, `<prefix>stream/` | | KEEP | | | |

plugin verdict: API changes

## uploads (v0.1.1, still on collage v0.58.0)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| New(store Store) + NewWith(cfg Config) | func | MERGE → `New(opts Options)` | `Options{Store Store \`json:"-"\`; Dir string \`json:"dir"\`; CacheControl string \`json:"cacheControl"\`}`. A nil Store builds Dir(dir). | The convention; today two constructors split one choice. | core docs (docs/actions.md, docs/http.md), docs ×2 |
| Config | type | RENAME → Options | | convention | |
| HandlerOptions, Handler(opts ...HandlerOptions) | type/method | MERGE → Options.CacheControl; becomes `Handler()` | | A variadic options struct holding one field is a second config channel the JSON cannot reach. | README |
| Receive (method) | method | ASK | Keep the method, which needs the *Plugin in a variable, or add a package func `uploads.Receive(rc, …)` that finds the plugin through the request context, like validate.Form, ogimage.Set and jsonld.Emit. | explicit dependency vs. one access pattern across plugins | docs |
| Field, FieldSpec, Rules, File, Received | types/func | KEEP | | | docs, core docs |
| Store, Dir, Plugin.Store | interface/func/method | KEEP | | | docs |
| Error, ErrTooLarge, ErrType, ErrTooMany, ErrUnexpectedField, ErrAlreadyRead, ErrExists, ErrBadKey | types/vars | KEEP | | | |
| error text prefix `elagoht/uploads:` | text | CHANGE | `uploads:`, as every other plugin prefixes (Cross-cutting #9) | consistency | |
| go.mod collage v0.58.0 | release | CHANGE (patch) | Bump it to the v0.60 line with the others. `go vet` against v0.60.0 passes, so this is release order only. | | README "Requires collage v0.58.0" |

plugin verdict: API changes

## websocket (v0.3.1)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| New(lv *live.Plugin) + NewWith(lv, opts) | func | CHANGE → `New(opts Options)` | Always find live with `collage.FindPlugin` in Configure, which New(nil) already does, and call `UseTransport` there, after config is decoded. | `plugin add` already writes `New(nil)`, so the parameter is vestigial. NewWith hands live the path before configuration could change it. | collage-snippets-highlighter snippet, docs ×4/×2 |
| Options | type | CHANGE | Add json tags (`path`, `originPatterns`), decode with `collage.PluginConfig` in Configure, and add a config schema to collage.json. | No json tags, and Configure never reads PluginConfig, so an `"elagoht/websocket"` section in plugins-config.json is silently ignored. | |
| Options.Ping | field | MERGE → live's `keepAlive` (via the new live.Plugin.KeepAlive()) | | It is the same fact as live's keepAlive (idle-connection interval, also 25s): two keys for one fact. websocket already reads MaxStreamAge from live this way. | README |
| Options.Path default / DefaultPath | field/const | CHANGE + UNEXPORT | default to live's prefix + `ws/` | `/_live/ws/` is hard-coded, so moving live's `prefix` leaves the socket under the old one. | |
| Options.OriginPatterns | field | KEEP (json `originPatterns`) | | | |
| Plugin + Name/Version/Configure/Init/Shutdown | methods | KEEP | | | |
| collage.json requires live, order -1 | manifest | KEEP | | | |

plugin verdict: API changes

## ratelimit (v0.1.8)

| item | kind | decision | detail | why | breaks (other plugins / docs) |
|---|---|---|---|---|---|
| New(opts Options), Options, Rule | func/types | KEEP | | already the convention | collage-health README, core docs |
| `trustProxy`, `trustedProxies` (+ clientIP/isTrusted) | config | REMOVE → use core | Users set `Server.TrustedProxies` once. The plugin keys on `collage.ClientIP(r)`, keeps the IPv6 /64 grouping, and falls back to RemoteAddr's host when ClientIP is the zero Addr. | Core's ClientIP doc says a rate limit "should use it". Two notes: ratelimit's default trusts loopback and private ranges (core's trusts nothing), and it also reads X-Real-IP (core doesn't). Release-note both. accesslog has the same two keys. | README, collage.json schema, docs |
| `rules`, `skip`, `maxKeys`, Options.KeyFunc (`json:"-"`) | config | KEEP | | | |
| Rule.Methods/PathPrefix/Rate/Burst | fields | KEEP | | | |
| Plugin + Name/Version/Init/Shutdown | methods | KEEP | | | |

plugin verdict: API changes

## Cross-cutting

1. **One constructor:** `type Options` plus `func New(opts Options) *Plugin`. Remove NewWith, make the zero Options work, and mark Go-only fields `json:"-"`. That is what `collage plugin add` writes (`x.New(x.Options{})`) and what about 30 of 44 plugins already do. Deviators: jsonld, live, ogimage (New()+NewWith(Config)); uploads (New(Store)+NewWith(Config)); websocket (New(lv)+NewWith(lv, Options)); and from the TSVs, tenant, health, devtoolbar (NewWith(Options)), opti-image, minimizer, jobs, deploy, consent (Config+NewWith), feed (New(feeds ...Feed)).
2. **Read configuration in Configure, never only in Init, and every Options field gets a json tag.** websocket reads none today. ratelimit, jsonld and uploads read it in Init, which is fine but should be uniform.
3. **One Duration:** ship `collage.Duration` in core v0.61.0, accepting "5s" or nanoseconds, marshalling as a string, with `null` as a no-op. Then delete the 8 copies: live, ogimage, errortrack, cdnpurge, fail2ban, health, opti-image, tenant. collage-snippets-highlighter's schema generator must learn the core type.
4. **Client identity from core:** replace each plugin's own trusted-proxy parsing with `collage.ClientIP` and `Server.TrustedProxies`. Touches ratelimit and accesslog (`trustProxy`/`trustedProxies`); fail2ban should be checked too.
5. **Origins from core:** use `collage.BaseURL(rc)` / `OriginFor` for "the site's URL" and `collage.ParseOrigin` for origin lists, with the key spelled `baseURL` wherever one survives. Touches jsonld (`siteURL`, remove) and ogimage (`imageOrigins` validation, BaseURL parsing).
6. **Default\* constants:** unexport any that only restate a documented default. Touches ogimage (6), live (DefaultPrefix) and websocket (DefaultPath).
7. **ASK, site name:** meta, jsonld and ogimage each have `siteName` for the same fact. The options are a core `Config.SiteName` that all three default from, or keep three keys with ogimage/jsonld defaulting to meta's.
8. **Numeric zero/negative semantics.** Make "0 = default, negative = error" the rule. "Unlimited" should be an explicit large number, or say -1 in one documented way. Today ogimage `maxEntries`/`maxBytes` treat negative as unlimited, ogimage `workers` and ratelimit `maxKeys` treat it as an error, and live `maxFragments` treats ≤0 as the default.
9. **Error-string prefix:** use `<short name>:` ("live:", "ogimage:"). Touches uploads, which uses "elagoht/uploads:".
10. **Push-connection settings live in live.** A transport plugin reads keepAlive, maxStreamAge and prefix through live's accessors and never repeats them. Touches websocket (Ping, Path).

## Bugs found in passing

1. **ogimage, unbounded memory in production.** `p.recorded[path]` gets an entry for every path whose render carries a card, and it is never evicted. Error pages run BeforeRender/AfterRender (collage internal/httpx/errorpage.go:274-313), so with `default` set, every distinct 404 URL a crawler or attacker invents adds an entry. Fix: record only in dev mode or tests, or cap it. Found by reading the code, not tested.
2. **ogimage.Draw panics.** It calls `withDefaults` but not `validate`, so `Fonts` set with a nil `FontFiles` dereferences nil (`fs.ReadFile(nil, …)`) instead of returning ErrInvalidConfig. Reproduced in a /tmp test.
3. **ogimage.Draw and the `ogimage` command** run on context.Background (no RenderTimeout) and hard-code locale "en", so a Turkish card's "i"→"İ" is never drawn the way the site draws it.
4. **live.Duration rejects `null`** (`"keepAlive": null` → `time: invalid duration "null"`, reproduced) and has no MarshalJSON. ogimage.Duration handles both differently.
5. **websocket ignores its JSON config entirely.** There are no json tags and no PluginConfig call, and the hard-coded `/_live/ws/` does not follow live's `prefix`.
6. **websocket enforces `maxStreamAge` late.** Expiry is checked only between `Wait` timeouts, so a connection can outlive `maxStreamAge` by up to `Ping` (25s).
7. **live + websocket silently override `noStream: true`.** `clientTag` checks `transport != nil` before `NoStream`, so the client pushes over the WebSocket although the operator turned push off. Make it a startup error or have one rule win, and document it.
8. **jsonld drops a malformed node with no trace.** An invalid `Raw` (or a MarshalJSON error) disappears from the page and nothing is logged. `p.log` is assigned in Init and never used.
