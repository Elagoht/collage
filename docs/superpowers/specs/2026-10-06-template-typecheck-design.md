# Typed fragment data and template type checking

Date: 2026-10-06
Status: approved design, pending implementation
Target: collage v0.49.0
Follow-up: typed render keys, SEO removal and typed plugin config are a separate
spec and release (v0.50.0)

## Motivation

A benchmark report on v0.47.0 noted that fragment, slot and template names are
strings, kept honest only by startup validation. The user's own complaint is
wider: nothing about a template is type safe, and the goal is the feeling that
an app which starts will render.

What is checked today, at `RegisterPage` (`internal/core/registry.go`,
`checkTemplates`) and by `collage check`:

- a template that does not exist, a syntax error, a call to an undeclared function;
- a fragment bound into a slot its template never calls; a required slot left
  empty; cycles;
- `pageURL`, `actionURL`, `fragmentURL` and friends with literal names, against
  the routes (`collage check` only).

What is not:

1. **The data contract.** A fragment's data is `any` in the public API:
   `DataHandlerFunc` returns `any`, `WithData` takes `any`, and `collage.Load[T]`
   and `collage.DataHandler[T]` box `T` into a `DataHandlerFunc`, so the type is
   gone by the time the builder sees it. `{{.Titel}}`, `{{.Author.Nmae}}` or a
   field read on a type that has no such field fails only when that template
   renders, and on a page nobody visits, never.
2. **Writing names in Go.** `WithSlotFragment("comments", …)` repeats a name the
   template declares with `{{slot "comments"}}`. A mismatch is caught at startup,
   but the editor offers no completion and no way to jump to the template.

A slot called in a template with nothing bound to it is not a gap: calling a slot
declares it, and an empty slot renders nothing by design (an optional
`{{slot "sidebar"}}` in a layout). It stays legal.

## Decisions

- **`any` leaves the fragment data API.** A fragment's data is a sealed
  `collage.Data`, made only by typed constructors, so every fragment's data type
  is known. This breaks every app and plugin that builds fragments; there are no
  production apps to protect (the one live site moves when its owner chooses), and
  correctness is the goal.
- **html/template stays.** Checking is a static walk of the parse tree against
  the data's Go type, not a move to compiled templates (templ or codegen). No
  template is rewritten, and no build step is added.
- **A definite error fails `RegisterPage`**, like every other registration check.
  A definite error is one that would fail the render when that node is reached,
  so reporting it at startup only reports it earlier.
- **Names stay strings.** Completion and diagnostics for slot names come from the
  VS Code extension, fed by `collage inspect`. No codegen in core.
- **The per-fragment escape hatch is `WithoutTypeCheck()`.** No global switch, no
  template comment directive.
- **`collage inspect` grows a type table; its version stays 1.** The change is
  additive.

## Design

### 1. Fragment data: the `collage.Data` API

`WithData` and `WithDataHandler` become one method taking a sealed interface:

```go
// Data is what a fragment renders with. Only collage's constructors make one.
type Data interface{ data() types.DataSource }

func (b *FragmentBuilder) WithData(d Data) *FragmentBuilder
```

| Constructor | Template's `.` | Replaces |
| --- | --- | --- |
| `collage.Load(fn func(ctx, *RenderContext) (T, error))` | `T` | `Load[T]` |
| `collage.DataHandler(fn func(ctx, *RenderContext) (T, []string, error))` | `T` | `DataHandler[T]`, raw `DataHandlerFunc` |
| `collage.Value(v T)` | `T` | `WithData(v any)` |
| `collage.Effect(fn func(ctx, *RenderContext) error)` | nil | `Effect` |
| no `WithData` | nil | — |

```go
collage.NewFragment("post", "post.html").WithData(collage.Load(loadPost))
collage.NewFragment("home", "home.html").WithData(collage.Value(homeView{Links: links}))
collage.NewFragment("seo", "seo.html").WithData(collage.Effect(setTitle))
collage.NewFragment("layout", "layout.html")
```

- `T` comes from the function's signature, so the handler and the type cannot
  disagree. The user writes no `any`.
- `DataHandlerFunc` and `WithDataHandler` leave the public API. `Fragment`'s
  exported `DataHandler` and `Data` fields become one unexported data source;
  internally the handler still returns `any` to the template engine, which is
  html/template's own parameter type.
- `Value` and the handlers record `reflect.TypeFor[T]()`. When `T` is an
  interface (`collage.Load[any]` written on purpose), the type is *unknown* and
  the template is not walked.
- The existing rules carry over unchanged: a fragment has one data source;
  `Static()`/`Shared()`, `Timeout`, prefetching and the typed nil dropped on error
  behave as today.

`WithoutTypeCheck()` sets a flag the walker honours; the fragment otherwise
registers as before.

**A fragment with no data** (no `WithData`, or `Effect`) is walked with an
unknown `.`. Reading a field of nil data is not an error in html/template: it
renders nothing (probed on Go 1.26: `{{.X}}`, `{{.X.Y}}` and `{{template "p"}}`
with nil data all execute cleanly). So nothing about `.` can be reported there;
function calls, argument counts and `$`-free expressions still are.

### 2. The walker: `internal/template/typecheck`

A pure function over a parse tree:

```go
func Check(set *template.Template, name string, dot reflect.Type, funcs map[string]reflect.Type) []Finding
```

It knows nothing about pages, render or the app, and is tested on its own. It
tracks the type of `.` through the tree, mirroring how `text/template` resolves
at runtime; a value whose type is not known is *unknown*, and nothing below an
unknown value is reported.

| Construct | Rule |
| --- | --- |
| `.Field`, `.A.B.C` | Pointers are followed. On a struct: a field first, promoted fields of embedded structs included, then a method. Neither found, or the field is unexported: **definite error**. |
| `.Method` | A method of `T`. A method of `*T` is callable only when the value is addressable, as `text/template` reaches it through `Value.Addr`: data handed to the template, a field of a non-addressable struct, a map element and a function's result are not addressable; what a pointer points to, a slice element (by `range` or `index`), and fields of an addressable struct are. `{{.In.PM}}` with `PM` on `*Inner` and the data passed by value is a **definite error**. The result type is the method's first result; the argument count is checked. |
| `.key` on a map | `text/template` looks `.key` up as a plain `string`, which must be assignable to the map's key type: `map[string]V`, or a map keyed by an interface type `string` implements (`map[any]V`), is valid, type `V`. A map keyed by a named string type (`map[Slug]V`), or by an interface `string` does not implement (`map[fmt.Stringer]V`), is a **definite error**. A key the map does not hold is not an error at runtime: it gives an invalid value that silently ends the chain. |
| interface, `any` | Unknown. |
| `{{range x}}` | Slice, array, map, channel, integer, `iter.Seq`/`iter.Seq2`. Inside, `.` is the element; `$i, $v :=` is typed. Ranging over a string, a struct or any other kind, or over an integer with two variables: **definite error**. `else` keeps the outer `.`. |
| `{{with x}}` | Inside, `.` is `x`'s type; `else` keeps the outer `.`. |
| `{{if}}`, `{{else if}}` | `.` unchanged; every branch is walked, except the side a literal condition (`false`, `0`, `""`, `true`, or `not` of a literal) never runs. |
| `len x`, `index x …` | `len` of a kind other than array, chan, map, slice, string, and `index` into a kind other than array, slice, map, string: **definite error**. |
| `$x := …`, `$x = …`, `$` | Variables are typed for their scope; `$` is the root type. |
| Function calls | Signatures from the merged FuncMap (built-ins, collage's own, plugin render funcs, `TemplateConfig.Funcs`) by `reflect`. Argument count checked, result type propagated. Special rules for `index`, `slice`, `len`, `not`, `eq`/`ne`/`lt`/`le`/`gt`/`ge`, `print*`, `html`/`js`/`urlquery`. `call` on a function type is typed through that function's signature like any other call; `and`/`or` are unknown when their operands differ in type. A call returning a `reflect.Value` is unknown, since `text/template` unwraps it and goes on with what it holds. |
| `{{template "x" pipeline}}` | `x` is walked with the pipeline's type. A partial included with several types is walked once per type; `(template, type)` pairs are memoised. |
| `{{block}}`, `{{define}}` | As `template`. |
| `_html_template_*` identifiers | Ignored: they are the escaper's, inserted after parsing. |

Argument *types* were not checked up to v0.50.x, only their count:
`text/template` converts some arguments itself, and mirroring that exactly is
where false alarms would come from.

**From v0.51.0 they are**, now that every rule is pinned against `text/template`
itself by the differential test. The checker mirrors `evalArg`:

- a constant argument (number, string, bool, nil) against the parameter's kind:
  `int` kinds take an integer constant that fits, `uint` kinds an unsigned one,
  float and complex kinds theirs, `string` a string, `bool` a bool, an interface
  the constant's own type (`idealConstant`: overflow is an error); anything else
  is a definite error ("expected string; found 1");
- a value of known type against the parameter type by `validateType`'s rules:
  assignable, or reached through an interface, a pointer deref or an address
  taken of an addressable value; otherwise a definite error ("wrong type for
  value"). An unknown value, a `reflect.Value` parameter, and anything the rules
  leave in doubt are not judged;
- `slice` of a kind that is not a string, slice or array is a definite error; an
  array whose addressability is in doubt is not judged.

Every rule lands with differential cases both ways (a call that fails and a
neighbouring one that renders) before it reports anything.


Not in scope: nil-pointer chains inside data, "probably wrong" warnings, anything
that is not certain to fail.

### 3. Wiring and errors

In `checkTemplates`, where slot calls are checked today, every fragment of the
page (inline fragments and fallbacks included) is walked unless its data type is
unknown or it was built `WithoutTypeCheck()`. Every finding of the page's own fragments is returned at once, joined
with `errors.Join`, ordered by file, line and column, so ten mistakes are not ten
restarts.

A finding is exposed as `*collage.TemplateTypeError`, readable with `errors.As`:

```go
type TemplateTypeError struct {
	Page, Fragment string
	Template       string // file path, or the inline template's name
	Line, Col      int
	Expr           string // "{{.Titel}}"
	Reason         string // "type blog.Post has no field or method Titel"
	Suggestion     string // "Title", or empty
}
```

Its message follows the registration errors:

```
collage: page "post": fragment "post-body" (templates/post.html:12:9):
  {{.Titel}}: type blog.Post has no field or method Titel (did you mean Title?)
```

The suggestion is the closest exported field or method name by edit distance,
offered only under a small threshold.

Since `collage check` starts the app, it reports these too. `collage dev`
restarts the process on every save, so the error shows in the terminal on the next
build with nothing added.

### 4. `collage inspect`: the type table

Additive; `version` stays `1`.

At the root, `types` maps a type's name to its shape, so recursive types are
named rather than expanded:

```json
"types": {
  "blog.Post": {
    "kind": "struct",
    "fields":  [{"name": "Title", "type": "string"},
                {"name": "Author", "type": "*blog.User"},
                {"name": "Comments", "type": "[]blog.Comment"}],
    "methods": [{"name": "URL", "args": 0, "returns": "string"}]
  }
}
```

- Exported fields and methods only; a template sees nothing else.
- Only types reachable from some fragment's data type are listed. A named type
  is listed once; unnamed composites (`[]X`, `map[string]X`, `*X`) are written
  inline in the type string.
- Basic types, `time.Time`, `template.HTML` and similar are opaque: named, not
  expanded.

Each fragment entry gains `"dataType": "blog.Post"` (`"nil"` for a fragment with
no data, `null` when unknown) and
`"typeCheck": false`, written only when `WithoutTypeCheck` was used.

What the extension is expected to do with it (the extension itself is separate
work, with its own release):

- complete `{{.` from the type of the fragment(s) the template file belongs to,
  narrowing inside `range` and `with`;
- when one template serves fragments of different types, offer the union and flag
  only a name that none of them has;
- complete `WithSlotFragment("` from the `{{slot "…"}}` names of the parent's
  template, and flag an unknown one.

The core stays the authority: the extension's squiggles are early feedback, the
startup check is the guarantee.

## Testing

1. **Walker tables** (`internal/template/typecheck`): template, Go type, expected
   findings, with a passing and a failing case for every row of the table in
   section 2 — embedded fields, pointer-receiver methods, `range`/`with`/`else`,
   variable scope, partials with several types, plugin funcs, recursive types.
2. **Differential against `text/template`.** For each case the walker calls an
   error, rendering a value of that type through that node must fail; for each it
   calls clean, rendering a populated value must not fail on a field or method.
   The reference is `text/template` itself, not this spec's reading of it.
3. **Registration** (`pkg/collage`): `RegisterPage` fails with findings readable
   through `errors.As`; `WithoutTypeCheck` silences a fragment; `Load[any]` is
   skipped; a data-less fragment reports nothing about `.`; inline fragments and
   fallbacks are walked.
4. **Data constructors**: each of `Load`, `DataHandler`, `Value`, `Effect` records
   the right type and keeps today's behaviour (tags, errors dropping the data,
   timeouts, prefetch).
5. **False-alarm sweep before the tag.** The checker runs over the scaffold's
   demo templates, `collage-docs`, furkanbaytekin.dev, Sen de Yaz and kanban.
   The bar is zero false alarms. Real bugs it finds in those apps are reported to
   the user, not fixed in their repositories.

## Migration

Every fragment that has data changes one call:

| Before | After |
| --- | --- |
| `WithDataHandler(collage.Load(fn))` | `WithData(collage.Load(fn))` |
| `WithDataHandler(collage.DataHandler(fn))` | `WithData(collage.DataHandler(fn))` |
| `WithDataHandler(collage.Effect(fn))` | `WithData(collage.Effect(fn))` |
| `WithDataHandler(fn)`, `fn` returning `(any, []string, error)` | `WithData(collage.DataHandler(fn))`, with `fn` returning its real type |
| `WithData(v)` | `WithData(collage.Value(v))` |
| a factory returning `collage.DataHandlerFunc` | returns `collage.Data` |

Updated in this release: the framework's tests, examples, scaffold templates
(`.go.tmpl` included) and `docs/`; the docs site (EN, TR), whose handlers are
rewritten to return their real types; every elagoht plugin that builds
fragments or handlers, each with its own release; the VS Code extension's
snippets. The test apps (Sen de Yaz, kanban, pusula) are the user's to move;
furkanbaytekin.dev moves when its owner decides.

## Release

- collage **v0.49.0**, a breaking release. The changelog leads with the
  migration table, then says plainly that an app may now fail at startup over a
  template that would already fail when rendered, how to read the error, and that
  `WithoutTypeCheck()` silences one fragment while it is fixed.
- Order as in every release: tag collage, then each plugin drops its `replace`,
  `go get`s the tag, tests, tags. The tag's CI run builds each plugin's latest
  *tag* against collage, so it fails until the plugins that build fragments are
  re-released; rerun it (`gh workflow run ci.yml --ref v0.49.0`) once they are,
  and the release is done when that run is green.
- Framework `docs/` (templates, fragments) and the docs site, EN and TR, gain a
  section on how templates are checked.
- The extension follows separately, against the inspect contract above.

## Out of scope

Codegen of name constants; leaving html/template; argument type checks;
nil-pointer and "suspicious" warnings inside data; the extension's
implementation; the other `any`s of the public API (`RenderContext.Get/Set`,
`Page.SEO`, `Host.Config`), which are the v0.50.0 spec. Template functions
(`template.FuncMap`) and `PanicError.Value` keep `any`: html/template and
`recover` define those types.
