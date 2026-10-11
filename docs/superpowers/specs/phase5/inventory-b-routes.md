# Phase 5 inventory — group B: pages, fragments, documents, actions (types.go, page.go, fragment.go, document.go, action.go)

Totals: 117 KEEP, 5 RENAME, 5 CHANGE, 1 MERGE, 0 REMOVE/UNEXPORT/DEFINE/ASK (128 rows).
"plugins broken" counts repos whose code (tests included, marked) must change; no plugin's non-test code breaks from any row here.

| name | kind | decision | detail (new name / how) | why (one line) | plugins broken |
|---|---|---|---|---|---|
| Action | alias | KEEP | — | Return/param type of NewAction, WithActionFor, Register; plugins read nothing else. | 0 |
| ActionBuilder | type | KEEP | Add BuildErr later (additive) for parity with the other three builders | Only builder that records nothing; registration still catches every mistake. | 0 |
| ActionBuilder.Build | method | KEEP | — | Matches Page/Fragment/Document Build. | 0 |
| ActionBuilder.WithBodyTimeout | method | KEEP | — | Real need (long uploads); validated at registration. | 0 |
| ActionBuilder.WithHandler | method | KEEP | — | Required core setter, same name as DocumentBuilder.WithHandler. | 0 |
| ActionBuilder.WithMaxBodyBytes | method | KEEP | — | Mirrors Server.MaxBodyBytes, same negative=unbounded convention. | 0 |
| ActionBuilder.WithMethods | method | KEEP | — | Variadic, append semantics are clear. | 0 |
| ActionBuilder.WithPath | method | KEEP | — | Same signature as Page/Document WithPath. | 0 |
| ActionBuilder.WithStreamingBody | method | KEEP | — | Needed for uploads; refused on non-body methods. | 0 |
| ActionBuilder.WithoutCSRF | method | KEEP | — | Honest name for a dangerous switch; webhooks need it. | 0 |
| ActionHandlerFunc | alias | KEEP | — | Signature every action handler is written against. | 0 |
| ActionResult | alias | KEEP | — | Plugins (honeypot) build it directly; fields are the documented contract. | 0 |
| ErrUnregisteredPage | var | KEEP | — | Sentinel for a real runtime mistake (RenderPage of an unregistered value). | 0 |
| JSON | func | RENAME → JSONBytes | Frees the plain name for the common case | Pre-encoded bytes is the rare case but owns the short name. | 0 |
| JSONOf | func | RENAME → JSON | `func JSON[T any](status int, v T) (*ActionResult, error)` | The value form is what every JSON action wants; "Of" is noise once JSON(bytes) moves. | 2 (tests: session, validate) |
| NewAction | func | KEEP | — | Constructor, matches NewPage/NewDocument/NewFragment. | 0 |
| NoContent | func | RENAME → Status | `collage.Status(http.StatusUnauthorized)` | Used with 401/400 in plugins; "NoContent(401)" reads as a 204. | 2 (tests: consent, i18n) |
| RenderFragment | func | KEEP | — | Reads well as `return collage.RenderFragment(f), nil`; see cross-cutting on Host.RenderFragment clash. | 0 |
| RenderPage | func | KEEP | — | Documented validation-failure shape; validate uses it. | 0 |
| SeeOther | func | KEEP | — | Post-redirect-get answer, used by 4 plugins. | 0 |
| Document | alias | KEEP | — | Return type of NewDocument/Documents(); fields read by plugins. | 0 |
| DocumentBuilder | type | KEEP | — | Builder type returned by NewDocument. | 0 |
| DocumentBuilder.AtRoot | method | KEEP | — | The one way to escape locale prefixes (robots.txt). | 0 |
| DocumentBuilder.Build | method | KEEP | — | Same contract as the other builders. | 0 |
| DocumentBuilder.BuildErr | method | KEEP | — | Same contract as the other builders. | 0 |
| DocumentBuilder.Dynamic | method | KEEP | — | Strategy setter shared with PageBuilder; 7 plugins. | 0 |
| DocumentBuilder.Incremental | method | KEEP | — | Strategy setter shared with PageBuilder. | 0 |
| DocumentBuilder.Static | method | KEEP | — | Strategy setter shared with PageBuilder; 7 plugins. | 0 |
| DocumentBuilder.WithBody | method | KEEP | — | Fixed bodies (robots.txt) without a handler. | 0 |
| DocumentBuilder.WithCacheParams | method | KEEP | — | Unused but it is the cache-key DoS mitigation; mirrors PageBuilder. | 0 |
| DocumentBuilder.WithDependency | method | RENAME → WithDependencyTags | Variadic tags, same as field DependencyTags | Singular "Dependency" for a ...string of tags; field and InvalidateTags say "Tags". | 0 |
| DocumentBuilder.WithHandler | method | KEEP | — | Core setter. | 0 |
| DocumentBuilder.WithPath | method | KEEP | — | Shared with Page/Action. | 0 |
| DocumentBuilder.WithPermanentRedirect | method | KEEP | Sets StatusCode/Status 301 once Redirect.Permanent goes | Convenience for the common case, like SeeOther. | 0 |
| DocumentBuilder.WithRedirect | method | KEEP | — | General form; validated at registration. | 0 |
| DocumentBuilder.WithStaticParams | method | KEEP | — | Static build of parametrised documents; markdown uses it. | 0 |
| DocumentHandlerFunc | alias | KEEP | — | Handler contract, already (ctx, rc). | 0 |
| DocumentResult | alias | KEEP | — | Return type of App.RenderDocumentPath; must be nameable. | 0 |
| ErrDocumentNotFound | var | KEEP | — | RenderDocumentPath's documented miss. | 0 |
| ErrDuplicateDocument | var | KEEP | — | Sibling of ErrDuplicatePage/Action. | 0 |
| ErrEmptyContentType | var | KEEP | — | Registration refusal, errors.Is-able. | 0 |
| ErrNilDocument | var | KEEP | — | Registration refusal. | 0 |
| ErrNoDocumentHandler | var | KEEP | — | Registration/Build refusal. | 0 |
| NewDocument | func | KEEP | — | Constructor; 7 plugins. | 0 |
| ErrDuplicateSlot | var | KEEP | Move the var into internal/types like its siblings (non-breaking) | Only sentinel of this family defined in pkg/collage itself; behaviour is right. | 0 |
| FragmentBuilder | type | KEEP | — | Builder type. | 0 |
| FragmentBuilder.Build | method | KEEP | — | Shared contract. | 0 |
| FragmentBuilder.BuildErr | method | KEEP | — | Shared contract. | 0 |
| FragmentBuilder.Required | method | KEEP | — | Used by consent, validate. | 0 |
| FragmentBuilder.Shared | method | KEEP | — | Distinct promise from Static; live depends on it. | 0 |
| FragmentBuilder.Static | method | KEEP | — | Fragment half of page Static; 7 plugins. | 0 |
| FragmentBuilder.WithData | method | KEEP | — | The typed data entry point. | 0 |
| FragmentBuilder.WithFallback | method | KEEP | — | Error-isolation feature. | 0 |
| FragmentBuilder.WithGuard | method | KEEP | — | How a layout carries access policy. | 0 |
| FragmentBuilder.WithSlot | method | CHANGE | `WithSlot(name string, rules ...SlotRule)` with `SlotRequired`, `SlotSingle` (new `type SlotRule int`) | Two bare bools: `WithSlot("x", true, false)` is unreadable at the call site. | 4 (tests: consent, jsonld, meta, minimizer) |
| FragmentBuilder.WithSlotFragment | method | KEEP | — | Static slot binding. | 0 |
| FragmentBuilder.WithSlotResolver | method | KEEP | — | Per-render slot fill (CMS blocks). | 0 |
| FragmentBuilder.WithTimeout | method | KEEP | — | 10 plugins. | 0 |
| FragmentBuilder.WithTitle | method | KEEP | — | Lets static layouts set <title>. | 0 |
| FragmentBuilder.WithoutTypeCheck | method | KEEP | — | Escape hatch for a wrong type check. | 0 |
| InlineHTML | alias | KEEP | — | Editor-highlighting marker; extension and scaffold rely on it. | 0 |
| NewFragment | func | KEEP | — | Constructor. | 0 |
| NewInlineFragment | func | KEEP | — | Constructor. | 0 |
| NewPage | func | KEEP | — | Constructor. | 0 |
| PageBuilder | type | KEEP | — | Builder type. | 0 |
| PageBuilder.Build | method | KEEP | — | Shared contract. | 0 |
| PageBuilder.BuildErr | method | KEEP | — | Shared contract. | 0 |
| PageBuilder.Dynamic | method | KEEP | — | Strategy setter. | 0 |
| PageBuilder.Incremental | method | KEEP | — | Strategy setter. | 0 |
| PageBuilder.Static | method | KEEP | — | Strategy setter. | 0 |
| PageBuilder.WithAction | method | KEEP | — | The form-post shorthand the docs lead with. | 0 |
| PageBuilder.WithActionFor | method | KEEP | — | Full-option form; CLI `collage add` prints it, docs use it. | 0 |
| PageBuilder.WithCacheParams | method | KEEP | — | Unused but it is the cache-key DoS mitigation. | 0 |
| PageBuilder.WithContent | method | KEEP | — | Required core setter. | 0 |
| PageBuilder.WithDependency | method | RENAME → WithDependencyTags | Same as DocumentBuilder | Singular name for variadic tags; field is DependencyTags. | 4 (tests: devtoolbar, indexnow, prometheus, tenant) |
| PageBuilder.WithErrorPage | method | KEEP | — | Per-page 500 page; documented, honoured in httpx/errorpage.go. | 0 |
| PageBuilder.WithFragmentPath | method | KEEP | — | Explicit opt-in fragment URLs. | 0 |
| PageBuilder.WithLayouts | method | KEEP | — | Layout chain. | 0 |
| PageBuilder.WithNotFoundPage | method | KEEP | — | Per-page 404 that ErrNotFound's doc promises. | 0 |
| PageBuilder.WithPath | method | KEEP | — | Shared with Document/Action. | 0 |
| PageBuilder.WithPermanentRedirect | method | KEEP | Sets 301 once Redirect.Permanent goes | Convenience for the common case. | 0 |
| PageBuilder.WithRedirect | method | KEEP | — | General form. | 0 |
| PageBuilder.WithStaticParams | method | KEEP | — | Static build of parametrised pages. | 0 |
| DefaultContentSlot | const | KEEP | — | Name layouts write in {{slot "content"}}. | 0 |
| ErrConflictingData | var | KEEP | — | Builder/registration refusal. | 0 |
| ErrConflictingLayout | var | KEEP | — | Builder refusal. | 0 |
| ErrConflictingTemplate | var | KEEP | — | Reachable through hand-built Fragment (fields are exported). | 0 |
| ErrEmptyName | var | KEEP | — | Registration refusal for all kinds. | 0 |
| ErrEmptyTemplatePath | var | KEEP | — | Also returned by NewInlineFragment(""). | 0 |
| ErrFragmentCycle | var | KEEP | — | Registration refusal. | 0 |
| ErrGuardRefused | var | KEEP | — | Plugins streaming fragments must errors.Is it. | 0 |
| ErrInvalidGuardDecision | var | KEEP | — | Guard contract violation. | 0 |
| ErrInvalidPath | var | KEEP | — | Path/redirect validation. | 0 |
| ErrInvalidRedirect | var | KEEP | — | Header-injection guard; also used by RedirectSource plugins. | 0 |
| ErrInvalidRedirectStatus | var | KEEP | — | Distinct cause from ErrInvalidRedirect. | 0 |
| ErrInvalidSlotDefinition | var | KEEP | — | Registration refusal. | 0 |
| ErrInvalidTTL | var | KEEP | Doc: "negative TTL, or Incremental without a positive one" after the merge | Absorbs ErrMissingTTL. | 0 |
| ErrInvalidTimeout | var | KEEP | — | Builder refusal. | 0 |
| ErrMissingContent | var | KEEP | — | Build/registration refusal. | 0 |
| ErrMissingLayout | var | KEEP | — | Builder refusal. | 0 |
| ErrMissingTTL | var | MERGE → ErrInvalidTTL | Incremental(0) and Incremental(-1) both become ErrInvalidTTL | Two sentinels for "the TTL you passed to Incremental is unusable"; nobody can act differently on them. | 0 |
| ErrNilFragment | var | KEEP | — | Builder/Bind refusal. | 0 |
| ErrNotFound | var | KEEP | — | The one sentinel apps return; 3 plugins. | 0 |
| ErrRequiredSlotUnfilled | var | KEEP | — | Registration refusal. | 0 |
| ErrSelfErrorPage | var | KEEP | — | Registration refusal. | 0 |
| ErrSlotOccupied | var | KEEP | — | Builder refusal. | 0 |
| ErrSlotResolved | var | KEEP | — | Builder refusal. | 0 |
| ErrTemplateType | var | KEEP | — | errors.Is target for every TemplateTypeError. | 0 |
| ErrUnknownSlot | var | KEEP | — | Registration refusal (typo on a slot name). | 0 |
| Fragment | alias | CHANGE | Take Bind, SetDataSource, DataSource, EffectiveTimeout, Slot, SlotNames off the method set (turn into internal/types funcs); keep Validate | Alias makes every internal method public: SetDataSource/DataSource leak internal types.DataSource with an `any` field and bypass the typed WithData constructors; Bind mutates a possibly registered fragment. | 0 |
| GuardDecision | alias | KEEP | — | session plugin builds it. | 0 |
| GuardFunc | alias | KEEP | — | session plugin implements it. | 0 |
| Key | alias | KEEP | — | Typed shared-value key; generic alias reads fine. | 0 |
| NewKey | func | KEEP | — | 8 plugins. | 0 |
| Page | alias | CHANGE | Unexport LayoutFragment (registration's folded root; LayoutChain is what users declare); move Root, Guards, Guarded, PathFragments, ContentSlotName off the method set; keep Locales, PathFor, Validate | Two exported fields describe one layout chain with a "must equal LayoutChain[0]" rule, and engine helpers ride along as public methods. | 0 |
| Redirect | alias | CHANGE | Drop Permanent; rename StatusCode → Status (0 = 302); WithPermanentRedirect sets 301 | Permanent duplicates StatusCode; every other status field (ActionResult, GuardDecision, BuiltRedirect) is called Status. | 0 |
| Registrable | alias | KEEP | — | Sealed interface App.Register takes. | 0 |
| RenderContext | alias | KEEP | — | 19 plugins; DEFINE would move the core of internal/types for no user gain. | 0 |
| RenderStrategy | alias | KEEP | — | Type of Page.Strategy, which devtoolbar reads. | 0 |
| RenderValues | alias | KEEP | — | Type of AfterRenderEvent.Values. | 0 |
| SlotDefinition | alias | KEEP | — | Type of the exported Fragment.Slots field. | 0 |
| SlotResolverFunc | alias | CHANGE | `func(ctx context.Context, rc *RenderContext) ([]*Fragment, error)` | Every other handler type takes (ctx, rc); this one alone lacks ctx, so a resolver that queries cannot honour the render deadline. | 0 |
| StaticParamsFunc | alias | KEEP | — | markdown, paginate implement it. | 0 |
| StrategyAuto | const | KEEP | — | Zero value of RenderStrategy, readable on unregistered pages. | 0 |
| StrategyDynamic | const | KEEP | — | Value of an exported field. | 0 |
| StrategyIncremental | const | KEEP | — | Value of an exported field. | 0 |
| StrategyStatic | const | KEEP | — | Value of an exported field. | 0 |
| TemplateTypeError | alias | KEEP | — | errors.As target; documented. | 0 |

## Cross-cutting

- **Aliases publish every method of the internal type.** `type Page = types.Page` makes `Root`, `Guards`, `Guarded`, `PathFragments`, `ContentSlotName`, and on `Fragment` `Bind`, `DataSource`, `SetDataSource`, `EffectiveTimeout`, `Slot`, `SlotNames`, frozen public API, even though nothing outside collage calls them (grep of all 45 plugins: only `PathFor`/`Locales`/fields are used). Rule: before v1, an exported method on an aliased internal type must be either meant for users (documented as such) or turned into a package-level func in the internal package. No DEFINE needed.
- **Handler signatures: always `(ctx context.Context, rc *RenderContext)`.** DataHandlerFunc, ActionHandlerFunc, DocumentHandlerFunc follow it; SlotResolverFunc (`rc` only) breaks it, and GuardFunc takes `(ctx, *http.Request)`, where ctx just repeats `r.Context()`. Rule: every user callback takes ctx first. Fix SlotResolverFunc (0 users). Leave GuardFunc as it is (it runs before an rc exists, and session implements it), but say so in its doc.
- **Status fields are called `Status`.** ActionResult.Status, GuardDecision.Status, plugin.BuiltRedirect.Status, but Redirect.StatusCode plus a redundant Permanent bool. Rule: one field, `Status`, zero meaning a documented default.
- **Tag naming.** `WithDependency(tags ...string)` sets `DependencyTags`. The rest of the API is `InvalidateTags`, `ActionResult.InvalidateTags`, `DependencyTags`. Rule: anything taking cache tags says "Tags" in its name, so `WithDependencyTags`.
- **Builder parity.** Page/Fragment/Document builders record errors and have BuildErr. ActionBuilder records nothing and has no BuildErr; the other three surface mistakes at Build, actions only at registration. Rule: all four builders have `Build` + `BuildErr`, and anything one builder checks eagerly the others check too (nil handler, negative timeout). Adding BuildErr later is non-breaking, but changing when errors appear is not, so do it before v1.
- **No bare bools in builder methods.** `WithSlot(name, required, allowMultiple bool)` is the only one. Rule: a builder method takes named options or separate methods, never positional bools.
- **Sentinel errors: one home, one cause each.** This group's sentinels live in types.go, document.go, action.go and fragment.go (ErrDuplicateSlot is the only one defined in pkg/collage itself). Siblings sit in errors.go, url.go and build.go. Rule: all exported Err* vars in pkg/collage/errors.go, grouped by area (non-breaking move). Merge pairs a caller cannot act on differently (ErrMissingTTL → ErrInvalidTTL). Keep pairs with different fixes (ErrInvalidRedirect vs ErrInvalidRedirectStatus).
- **"Render*" names mean two things.** `collage.RenderFragment(f)`/`RenderPage(p)` build an ActionResult. `Host.RenderFragment(r, req)`, `App.RenderPath` and `App.RenderDocumentPath` actually render. Kept for readability in `return collage.RenderPage(rc.Page), nil`, but the docs for ActionResult helpers should state they render later, when the result is written.

## Breaking-change candidates found in passing

1. **`Fragment.SetDataSource` is public** (internal/types/datasource.go:65). Any caller can do `f.SetDataSource(other.DataSource())` on a built, even registered, fragment. That skips ErrConflictingData and pairs data with whatever `Type` the source carries, which defeats the "only collage's constructors set it" promise in the field comment, and it exposes `types.DataSource` (an `any` field) through the public method set. Make both functions in internal/types.
2. **`Page.LayoutFragment` and `Page.LayoutChain` are both exported with an invariant between them** (`LayoutFragment` must equal `LayoutChain[0]` before registration, and is replaced by a per-page folded copy after). A plugin that reads `App.Pages()` sees the folded copy in one field and the originals in the other. Unexport LayoutFragment before the freeze.
3. **Builders hand out their live value.** `Build()` returns the builder's own pointer, so any WithX after Build mutates the already built (or registered) Page/Fragment/Document. `PageBuilder.Build()` called twice also appends ErrMissingContent twice. Either have Build return a copy or have later WithX calls record an error. Decide before v1, because users may come to rely on "keep chaining after Build".
4. **`ActionResult` silently drops fields.** "Exactly one of Location, Fragment, Page and Body" is enforced only by precedence order. A result with both Page and Body renders the page and ignores Body, with no error. It is a plugin-facing struct (honeypot builds it), so if it is ever going to be refused (a 500 plus an error naming both fields), that has to happen before v1.
5. **`Redirect` with both `StatusCode` and `Permanent`**: `WithRedirect(from, to, 302)` combined with a hand-set `Permanent: true` is ambiguous, and `EffectiveStatus` quietly prefers StatusCode. Collapse it to one `Status` field (see the Redirect row).
6. **`WithPath` (all three builders) overwrites silently** when called twice with the same locale. `DocumentBuilder.WithPath("", p)` is identical to `AtRoot(p)`, because RootLocale is `""`. For a page, the same `""` key is (as far as I can tell) refused only indirectly, as an unreachable locale. Record an error for a repeated locale, and refuse `""` in WithPath (AtRoot is the spelling for documents).
