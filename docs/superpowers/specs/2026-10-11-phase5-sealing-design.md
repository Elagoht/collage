# Phase 5: sealing pkg/collage's public API

Date: 2026-10-11
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 5.
Input: [the phase 5 inventory](2026-10-11-phase5-inventory.md) and its three
row-by-row tables under [phase5/](phase5/).

## Motivation

v1.0 freezes `pkg/collage`: after it, every breaking change is a v2. This is the
last pass to remove what should not be public, rename what misleads, merge what
says one thing twice, and fix shapes that would otherwise be frozen. The
inventory walked all 354 exported declarations and `Config`'s fields; 285 stay
as they are. This spec turns the other rows into one breaking release,
v0.60.0, and adds the CI gate that keeps v1 stable.

## Decisions

The user approved these on 2026-10-11 (the inventory's six open questions,
each settled on the recommendation):

1. **Host is not split.** It is already sealed: it embeds `ConfigReader`, whose
   only method is unexported and whose implementation lives in `internal/plugin`,
   so no type outside collage can implement it. Adding a method after v1 breaks
   nobody. Splitting it would buy readability only, and capabilities reached by
   type assertion fail through a wrapper that embeds a Host. So `Origins` folds
   into `Host` and `ConfigHost`, and the two Host methods nobody calls go.
   `collagetest.Host` is not needed for sealing; it stays with the post-v1
   collagetest helpers.
2. **Inspection moves internal.** `App.Inspect` and the `Inspection`/`Inspected*`
   types leave `pkg/collage`. The contract is `collage inspect`'s versioned JSON.
3. **`Build()` returns a copy,** and all four builders have `Build` and `BuildErr`.
4. **An `ActionResult` with more than one body is refused.**
5. **Two routes writing one output file stop a static build,** for documents as
   for pages.
6. **`WithPath` refuses a repeated locale and the empty locale.**

## Authority

The three inventory tables are part of this spec. Every row whose decision is
not KEEP is to be carried out as written there, except where this spec says
otherwise. Where they conflict, this spec wins. Rows marked KEEP are not touched.

Overrides of the tables:

- The `Inspection` and `Inspected*` rows (ASK) become UNEXPORT/REMOVE per
  decision 2.
- The `App` row's forwarded list drops `Inspect` (decision 2).
- The `Host` row's method set is final as below.

## Design

### 1. App becomes a defined type

`type App = core.App` makes every exported `core.App` method v1 API, including
ten plumbing methods only `internal/build` calls, and `go doc` shows none of
App's methods.

- `pkg/collage` defines `type App struct{ c *core.App }`. `New` returns `*App`.
- **It forwards** exactly: Register, RegisterPage, RegisterNotFoundPage,
  RegisterErrorPage, RegisterDocument, RegisterAction, RegisterPlugin,
  RegisterCommand, Commands, Pages, Page, Documents, Actions, Handle, Use, Mount,
  URL, ActionURL, FragmentURL, InvalidateTags, InvalidateTagsN, RenderPath,
  RenderDocumentPath, RenderFragment, Handler, Start, ListenAndServe, Shutdown,
  DevMode, Logger, BaseURL, OriginFor, Locales, PageURLs, ServeStatus, BuildID,
  Check. Each with a doc comment of its own.
- **It hides:** BuildFinished, HasBuildFinishedHook, PluginRedirects,
  CaptureResponses, RenderNotFound, PageRouted, CSRFMarker, Mounts,
  PrefixDefault, DefaultLocale, Inspect.
- The package-level functions that take an app (`Build`, `DispatchCommands`,
  `collagetest`'s helpers) unwrap it inside `pkg/collage`. Code outside
  `pkg/collage` that needs the core value goes through an unexported accessor
  in an internal package; there is no exported way to reach `*core.App`.
- The hand-written method lists in App's doc comment go; godoc lists them.

### 2. Host, ConfigHost and Plugin

- `pkg/collage` declares `Host`, `ConfigHost` and `Plugin` as aliases to
  interface literals spelled with `pkg/collage`'s own names (`*Page`, not
  `*types.Page`). `internal/plugin` declares the same literals. Identical unnamed
  interfaces are the same type, so every plugin's `Init(ctx, collage.Host)` keeps
  compiling, and `go doc` prints the methods with their comments. (Confirmed in
  a scratch module during the inventory.)
- **Host's method set:** DevMode, BaseURL, OriginFor, MultiOrigin, Locales,
  BuildID, Logger, RegisterCommand, RegisterDocument, Mount, Handle, Use, Pages,
  Page, PageURLs, URL, RenderFragment, ServeStatus, InvalidateTags, plus the
  embedded `ConfigReader`.
  - Removed: `RegisterPage` and `FragmentURL` (no plugin calls either; adding
    one back later breaks nobody).
  - Added from `Origins`: `OriginFor(ctx, host string) string` and
    `MultiOrigin() bool` (was `Origins.Dynamic`).
- `ConfigHost` gains `OriginFor` and `MultiOrigin` the same way.
- `Origins` and the type assertion `host.(collage.Origins)` are removed.
- The doc comment groups the methods: environment, contributing routes,
  wrapping requests, reading the site map, acting at request time.

### 3. Methods aliased types must not export

An alias publishes every exported method of the internal type. Each method
below becomes a function in its internal package (`types.Bind(f, …)`, and so
on), so `pkg/collage` users cannot call it:

- `Page`: Root, Guards, Guarded, PathFragments, ContentSlotName. The field
  `LayoutFragment` becomes unexported. Page keeps Locales, PathFor, Validate.
- `Fragment`: Bind, SetDataSource, DataSource, EffectiveTimeout, Slot,
  SlotNames. Fragment keeps Validate.
- `Result`: field `HoistEnds` becomes unexported; method `FragmentReports`
  goes (one fragment-report type, below).

A test pins the exported method sets of `Page`, `Fragment`, `Document`,
`Action`, `Result` and `RenderContext` against an allowlist (by reflection), so
a new exported method on an internal type fails a test until it is added on
purpose.

### 4. Removals, merges, renames, unexports

Carried out row by row from the tables. The groups, for orientation:

- **Hooks with no implementer go:** `PageResolvedHook`/`Event`,
  `CacheWriteHook`/`Event`. Re-adding an optional hook in v1.x breaks nobody.
- **One fact, one name:** `RouteOf` → `RouteInfo`; `FragmentMetadata` →
  `FragmentReport` (`Metadata.Fragments []FragmentReport`);
  `CacheInvalidateEvent.Paths` goes (`Entries` holds the same, with the host);
  `ErrMissingTTL` → `ErrInvalidTTL`; `ErrDuplicateOutputPath` →
  `ErrOutputPathCollision`; `ErrInvalidHandlerPrefix` → `ErrInvalidPrefix`;
  `VariedContext` → `Varied(ctx, header)`; `ErrUnsupportedCache` →
  `ErrInvalidCacheType`; `ErrNilRenderer` → `ErrNilApp`.
- **Renames:** `JSONOf` → `JSON`, today's `JSON` → `JSONBytes`;
  `NoContent` → `Status`; `WithDependency` → `WithDependencyTags` (page and
  document builders); `DataHandler` → `LoadTagged`; `ErrNoMethods` →
  `ErrNoActionMethods`; `ErrInvalidOutDir` → `ErrEmptyOutDir`;
  `ErrInvalidBaseURL` → `ErrInvalidOrigin`, with a message that does not name
  BaseURL.
- **Unexported:** the nil and empty argument sentinels (`ErrNilConfig`,
  `ErrNilPage`, `ErrNilPlugin`, `ErrEmptyCommandName`, `ErrEmptyPluginName`),
  `ErrNoRootFragment`, `ErrEmptyCacheVersion`, `ErrDictKeyNotString`,
  `ErrDictOddArgs`, `ErrNoMountForAsset`, `CheckCommand`, `InspectCommand`,
  `Config.Validate`, `Config.ApplyDefaults`.
- **Removed:** `ErrEmptyLocaleDefault`, `ErrEmptyTemplateRoot` (unreachable
  through `New`), `Config.IsDevMode`, `Builder` and `NewBuilder` (replaced by
  `Build`, below).
- **All exported sentinels live in `errors.go`,** grouped by area. Moving a
  declaration between files is not breaking.

### 5. Shape changes

- **`collage.Build(ctx, app *App, opts BuildOptions) (*BuildReport, error)`**
  replaces `NewBuilder(app).Build(ctx, opts)`. A nil app is `ErrNilApp`; an
  empty `OutDir` is `ErrEmptyOutDir`. The scaffold's `main.go` changes with it.
- **Defined, not aliased:** `BuildOptions`, `BuildReport`, `SkipRecord`,
  `WarningRecord`. `SkipRecord.Page` and `WarningRecord.Page` become `Name`
  (they name documents too).
- **`Redirect`:** `StatusCode` becomes `Status` (0 means 302) and `Permanent`
  goes; `WithPermanentRedirect` sets 301.
- **`SlotResolverFunc`** takes `(ctx context.Context, rc *RenderContext)`, like
  every other handler.
- **`FragmentBuilder.WithSlot(name string, rules ...SlotRule)`** with
  `type SlotRule int` and `SlotRequired`, `SlotSingle`; replaces the two bools.
- **`Cache.Invalidate` moves to `TaggedCache`.**
- **Exported function vars become functions:** `WithCacheControl`,
  `WithoutBuildCopy`, and any other `var X = internal.X` holding a function.
  Only sentinel errors stay exported vars.
- **`Config`:**
  - `Template.DevMode` goes (`Config.DevMode` is the one switch);
  - `Locale.DisablePathLocale` goes (one supported locale is the same);
  - `Cache.MaxKeysPerTag` goes; the tag tracker is capped by `MaxEntries`;
  - `Security.NoSniff *bool` becomes `DisableNoSniff bool`;
  - `Cache.Type` becomes `CacheType` with `CacheMemory` and `CacheDisk`
    (untyped string literals still compile);
  - `Observability.Metrics`/`Tracer` are spelled with the `pkg/collage` names.
- **Additive, in the same pass:** `AfterRenderEvent.Path`.

### 6. The six behaviour decisions

1. Host: section 2.
2. **Inspection:** `App.Inspect`, `Inspection` and the `Inspected*` types move
   to `internal/core`. `DispatchCommands` still answers `collage inspect` with
   the same versioned JSON. The CLI and the VS Code extension read only that
   JSON, so neither changes.
3. **Builders:**
   - `Build()` returns a new value: the struct copied, and every slice and map
     a builder method can append to or write cloned, so a `WithX` after `Build`
     never reaches a built or registered value.
   - `ActionBuilder` gains `BuildErr` and records the mistakes the other
     builders record eagerly (a nil handler, a negative timeout).
   - Calling `Build` twice returns two equal values and does not record an
     error twice.
4. **`ActionResult`:** setting more than one of `Location`, `Fragment`, `Page`
   and `Body` is refused when the result is written: the response is a 500 and
   the error, `ErrAmbiguousActionResult`, names the fields that were set and
   reaches the ErrorHook. A result with none set keeps its current meaning.
5. **Output paths:** a document whose output file another route already writes
   stops the build with `ErrOutputPathCollision`, as a page does today.
6. **`WithPath`:** on all three builders, a second `WithPath` for the same locale
   records `ErrDuplicateLocalePath` in `BuildErr`. The empty locale is recorded
   as `ErrInvalidLocale` (or the existing sentinel for an unsupported locale, if
   one fits); a document at the root is spelled `AtRoot`.

### 7. Documentation hygiene

- No public doc comment in `pkg/collage` names an internal package or
  identifier. A test greps `pkg/collage`'s non-test sources for `internal/` and
  for `types.`, `core.`, `plugin.` inside comments, and fails on a hit outside an
  allowlist.
- Hand-written method lists in doc comments go wherever godoc now shows the
  methods.

### 8. The apidiff gate

A CI job, `api`, runs on every push and pull request:

- It compares `pkg/collage` (and `pkg/collagetest`) against the latest tag with
  `golang.org/x/exp/cmd/apidiff`, pinned to a version.
- It writes the report to the job summary.
- Before a `v1.*` tag exists, it reports and never fails. Once one does, it
  compares against the latest `v1.*` tag and fails on an incompatible change.
- A breaking change after v1 is a v2 module path, not an override flag.

## Plugins and other dependents

Plugin code that must change (from the tables):

| Change | Repos |
|---|---|
| `host.(collage.Origins)` → `host.OriginFor` / `host.MultiOrigin` | cdnpurge, feed, indexnow, meta, oauth, ogimage, robots, sitemap |
| `RouteOf` → `RouteInfo` | fail2ban, prometheus |
| `Varied(rc, h)` / `VariedContext` → `Varied(ctx, h)` | consent, tenant |
| `DataHandler` → `LoadTagged` | markdown |

Tests that must change: `JSONOf` (session, validate), `NoContent` (consent,
i18n), `WithDependency` (devtoolbar, indexnow, prometheus, tenant), `WithSlot`
(consent, jsonld, meta, minimizer), `RenderNotFound`/`Mounts` (secure, bundle).

The tag CI builds and tests every plugin's latest release against the new
collage, so a plugin whose code or tests use a changed name goes red until it is
re-released. Before tagging, the implementation greps all 45 repos for every
renamed or removed identifier and lists the repos to update; the table above is
the inventory's count, not a substitute for that grep.

Also updated: the scaffold templates, `collagetest`, the docs site (EN and TR;
`Config.Validate` appears ten times, `NewBuilder` in the build pages), the VS
Code extension's snippets if any name a changed identifier, and the framework's
own `docs/`.

## Testing

- The existing suite, `go test -race ./...`, with every test that names a
  changed identifier updated, not deleted.
- New tests:
  - the method-set allowlist test (section 3);
  - the doc-comment hygiene test (section 7);
  - `Build()` copy semantics: a `WithX` after `Build` leaves the built value
    unchanged, for all four builders; `Build` twice records one error;
  - `ActionBuilder.BuildErr` reports a nil handler;
  - `ActionResult` with two bodies: 500, `ErrAmbiguousActionResult` reaches the
    ErrorHook, and the response carries neither body;
  - a static build with a document and a page writing one path fails with
    `ErrOutputPathCollision`; so do two documents;
  - `WithPath` twice for one locale and `WithPath("")` record their errors;
  - `Host.OriginFor`/`MultiOrigin` through a wrapper that embeds a Host (the
    case the assertion got wrong);
  - `collage inspect` JSON unchanged (golden output before and after the move).
- The scaffold compiles and its tests pass (`go test -count=1`, since the test
  cache hides scaffold breakage).
- Every plugin passes against the release in the tag CI matrix before the phase
  is called done.

## Release

1. collage v0.60.0, breaking, with a CHANGELOG section that lists every rename
   and removal with its replacement.
2. The plugins named by the grep, re-released against v0.60.0 (patch or minor
   by the plugin's own rule), then a dispatch of the CI matrix until all are
   green.
3. Docs site EN and TR, extension if touched, memory.
4. The `api` gate lands with or right after v0.60.0, in report mode.

## Out of scope

- Splitting Host (decided against, above).
- `collagetest.Host` and plugin-author test helpers (post-v1).
- Named constants for `Route.Kind`, `ErrorEvent.Stage`, `BuiltFile.Kind`, and
  `DocumentBuilder.WithTimeout`: additive, can land after v1.
- Phase 6 (release candidate, v1.0.0).
