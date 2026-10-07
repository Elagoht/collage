# Fragments and pages

A **fragment** is a template, an optional data handler, and the slots it exposes
to child fragments. A **page** is a render configuration: which fragments to
compose, which paths reach it, and how its output is cached.

## Templates

Every file under `Config.Template.Root` whose extension matches
`Config.Template.Extension` is parsed into one template set at startup. A
fragment names its template by the path relative to the root, extension included:

```
templates/
  layouts/default.html      -> "layouts/default.html"
  pages/home.html           -> "pages/home.html"
  errors/404.html           -> "errors/404.html"
```

A fragment that names a template the engine did not load is rejected at
`RegisterPage`, not at the first request.

### Where the templates come from

`Root` on its own is a directory on disk, resolved against the process's working
directory. That is what you want in development, where `DevMode` reparses from
disk on every request and a markup change shows up without a rebuild.

Set `FS` and `Root` becomes a directory *within* that filesystem instead, which
is how a binary carries its own templates and runs from anywhere:

```go
//go:embed templates
var templates embed.FS

Template: collage.TemplateConfig{
	FS:        templates,
	Root:      "templates",
	Extension: ".html",
}
```

`Root` is still stripped from every template name, which is exactly what makes
it the right knob here: `embed.FS` names each file by its path in the source
tree, so without it every template would be called
`templates/pages/home.html`. An empty `Root` alongside an `FS` means the root of
that filesystem.

**In development, an embedded template set is ignored when the directory is there
on disk.** Embedding is what a production binary needs and exactly what a developer
does not: the embedded copy was fixed when the binary was built, so editing a
template would change nothing until the next rebuild — and reloading an embedded
file only reparses the same bytes. So with `DevMode` on, if `Root` also exists as a
directory relative to the working directory, that is what is rendered, and the
application logs that it chose it. The embed directive's path is a source-relative
path, which is the same path a `go run .` in the package directory sees, so the two
normally coincide; when they do not, the directory is not there and the embedded
copy is used.

The other half of that loop is the page cache, which **is not read in development**
— see [caching](caching.md). Reloading a template is no use if the page it renders
was cached before the edit.

With `Root` alone, templates are read through `os.OpenRoot`, so a symlink whose
target leaves the root is refused by the kernel during path resolution. A
symlink resolving *inside* the root loads normally — containment refuses what
leaves the root, not symlinks as such.

## How a page's fragments run

Rendering is depth-first and in order, and always was. What changed is when the data
handlers run.

**A fragment's declared children start their data handlers before it renders its own
template.** A home page made of a navigation bar, an article list and a popular
sidebar asks the upstream for all three at once, and the reader waits for the longest
of them rather than the sum. Nothing about the markup changes: the templates still
execute one at a time, in tree order, so `{{hoist}}` and everything else that depends
on ordering behaves exactly as before.

**Parent before child is preserved.** A child's handler starts only after its
parent's has returned, so a parent storing a value under a key for its children
to read still works, and so does a child reading a path parameter its parent
resolved.

**Siblings run at the same time**, and that is the one thing that is genuinely
different. Three consequences follow, in order of how likely they are to matter.

*Share values through a `collage.Key`.* A key is a name and a type, declared once at
package level; `Set` stores a value under it for this render and `Get` reads it back
as that type, so a read needs no type assertion and a write of the wrong type does
not compile. Both hold the render's lock, so siblings can use them at once:

```go
var postKey = collage.NewKey[Post]("post")

postKey.Set(rc, post)          // in a parent's data handler
post, ok := postKey.Get(rc)    // in a child's; ok is false if nothing was set
```

A key is its name *and* its type: `NewKey[Post]("post")` and `NewKey[Draft]("post")`
are two keys holding two values, and two `NewKey[Post]("post")` made in different
files are one. `With` derives a key per value from a declared one —
`articleKey.With(slug)` is named `article:<slug>` and holds the same type — and chains,
`boardKey.With(id).With("filter")`. `NewKey` panics on an empty name, at the line
that declared it, and a zero `Key` — a struct field nothing set, never made with
`NewKey` — panics where it is used rather than share one nameless value.

*Prefer `Once` to a read-then-write.* This shape has a hole in it:

```go
key := articleKey.With(slug)
if article, ok := key.Get(rc); ok { return article, nil }
article, err := api.Article(ctx, slug)   // both siblings are here at once
key.Set(rc, article)
```

Both siblings miss the `Get`, both fetch, and the page quietly asks the upstream
twice. `Once` closes it — the first caller fetches, the rest wait for it:

```go
var articleKey = collage.NewKey[Article]("article")

article, err := collage.Once(rc, articleKey.With(slug), func(ctx context.Context) (Article, error) {
	return api.Article(ctx, slug)
})
```

`Once` keeps its values apart from `Set`'s: a value `Once` fetched is not readable
with `Get`. It lives as long as one render. When the same data appears on many pages — an
author card on every post — use `collage.Cached`, which keeps it across pages and
requests; see [caching](caching.md#caching-data-not-only-pages).

*Two siblings hoisting one key is settled by declaration order.* Not by which handler
finished first — that would make a page's `<head>` depend on the weather. Innermost
still wins over depth, exactly as before; at equal depth the later-declared fragment
wins.

**A slot the template does not render still costs its handler.** Children are started
one level ahead, before the template has decided what it will ask for, so a fragment
behind a conditional slot may fetch for nothing. Its context is cancelled as soon as
the template is done, and its failure is discarded — a fragment that was never
rendered cannot fail a page, whether or not it is `Required`.

## Building a fragment

```go
content := collage.NewFragment("home-content", "pages/home.html").
	WithData(collage.Load(loadHome)).
	WithTimeout(2 * time.Second).
	Build()
```

The builders accumulate errors instead of returning `(*T, error)` from every
`WithX` call, so a chain stays a chain. Call `BuildErr()` to read whatever was
accumulated:

```go
builder := collage.NewFragment("sidebar", "partials/sidebar.html").
	WithSlot("widgets", true, false).
	WithSlotFragment("widgets", recentPosts)

sidebar := builder.Build()
if err := builder.BuildErr(); err != nil {
	return fmt.Errorf("sidebar: %w", err)
}
```

Ignoring `BuildErr` does not lose a mistake silently. `Build` keeps what the builder
recorded on the value it returns, and `RegisterPage` refuses a page whose builder —
or the builder of any fragment in its tree, through its slots and fallbacks —
recorded one, naming the page, before it serves a request; `RegisterDocument` does
the same for a document. Check it anyway when the builder's inputs are not known to
be well-formed ahead of time, to report the mistake where it was made.

### Inline templates

A small fragment can carry its template itself instead of naming a file:

```go
row := collage.NewInlineFragment("post-row", `
  <tr>
    <td>{{.Title}}</td>
    <td>{{template "partials/date.html" .Date}}</td>
  </tr>`).
	WithData(collage.Load(loadRow)).
	Build()
```

It renders as a file template does — slots, `hoist`, every template function,
`{{template}}` calls into the template directory — and registration parses and
checks it like one: a parse error or a slot it never calls stops startup, naming
the page and the fragment. Two fragments may share a name and still carry
different templates; each renders its own.

A longer template can be a constant of its own, declared as `collage.InlineHTML`
— a name for `string` that editor tooling colours as HTML:

```go
const loginForm collage.InlineHTML = `
  <form method="post">
    <input type="email" name="email" required>
  </form>`

content := collage.NewInlineFragment("login", loginForm).Build()
```

Use it for the parts of a page that are a few lines of markup next to the handler
that feeds them. Layouts and whole pages read better as files.

The template is code, so it must be a constant. Never build it from data —
`NewInlineFragment("row", "<p>"+post.Title+"</p>")` runs whatever `{{…}}` the
title holds, and each distinct string becomes a template the program keeps until
it exits. This matters most in a slot resolver, which builds fragments per
request: its fragments can be inline, but their templates are fixed, and the data
reaches them through `WithData`, as a value or from a handler.

Three limits come with it:

- A Go raw string cannot hold a backtick, so a template with a JavaScript template
  literal stays in a file.
- An inline template cannot `{{define}}` or `{{block}}` templates of its own
  (`collage.ErrSourceConflict` at registration): a definition would replace a file
  template of that name for every page.
- A file template cannot `{{template}}` an inline one; its name in the template set
  is internal.

A fragment names a file or carries a template, never both
(`collage.ErrConflictingTemplate`), and `NewInlineFragment` with an empty template
records `collage.ErrEmptyTemplatePath`. `collage inspect` marks an inline fragment
with `"inline": true`.

## Slots

A slot is a named position inside a fragment's template, written `{{slot "name"}}`.
Calling it in the template is all the declaring it needs:

```html
<!-- templates/layouts/default.html -->
<!doctype html>
<html lang="en">
<head><title>{{.Name}}</title></head>
<body>
  <main>{{slot "content"}}</main>
</body>
</html>
```

```go
type site struct{ Name string }

layout := collage.NewFragment("layout", "layouts/default.html").
	WithData(collage.Value(site{Name: "My site"})).
	Build()
```

`{{.Name}}` is checked against `site` when the page registers (see
[How templates are checked](#how-templates-are-checked)).

A page's content goes into the layout's `"content"` slot at registration. Binding a
child yourself:

```go
sidebar := collage.NewFragment("sidebar", "partials/sidebar.html").
	WithSlotFragment("widgets", recentPosts).
	WithSlotFragment("widgets", tagCloud).
	Build()
```

A slot holds any number of fragments, rendered in binding order and
concatenated, and a slot nothing is bound to renders nothing.
`WithSlot(name, required, allowMultiple)` is for when that is not what you want,
and may come before or after the bindings it constrains:

- `required` — the render fails if nothing is bound to the slot. This is enforced
  before the template runs, so a required slot is caught even if the template
  never asks for it.
- `allowMultiple` set to false — one fragment at most; binding a second is
  `ErrSlotOccupied`.

A typo on either side of a binding — `WithSlotFragment("sidbar", ...)` against
`{{slot "sidebar"}}` — fails registration with `ErrUnknownSlot`: a fragment bound
into a slot its template never calls could never render. A template that names a
slot by anything but a literal, `{{slot .Which}}`, may call any of them, so its
fragment's bindings are not checked.

### Slots filled per render

When the sections of a page come from content — a CMS's block list, in the order an
editor chose — bind a resolver instead of fragments:

```go
var sectionsKey = collage.NewKey[[]block]("sections")

page := collage.NewFragment("sections", "pages/sections.html").
	WithData(collage.DataHandler(loadSections)). // sectionsKey.Set(rc, blocks)
	WithSlotResolver("sections", func(rc *collage.RenderContext) ([]*collage.Fragment, error) {
		blocks, _ := sectionsKey.Get(rc)
		fragments := make([]*collage.Fragment, 0, len(blocks))
		for _, b := range blocks {
			fragments = append(fragments, sectionFragments[b.Kind])
		}
		return fragments, nil
	}).
	Build()
```

Adding, removing or reordering a section in the content then needs no restart, and
no guard against the code's order disagreeing with the data's.

- The resolver runs **after its own fragment's data handler**, so it reads what that
  handler fetched, and **before the fragments it returns start theirs**, which still
  run concurrently.
- What it returns is held to the slot's rules when the page renders: one fragment
  unless the slot allows multiple, at least one if it is required. An error from it
  fails its fragment, and the fragment's failure policy applies.
- A slot is filled by a resolver or by `WithSlotFragment`, never both —
  `ErrSlotResolved`.
- Templates are all parsed at startup, so a returned fragment whose template does
  not exist fails that render rather than registration. For the same reason a
  returned fragment's template is not [checked against its data](#how-templates-are-checked):
  registration never sees it.

The nesting limit is 32 levels, which exists to catch a fragment bound, directly
or indirectly, into one of its own slots.

## Pages, layouts, and the content slot

A page's content fragment is bound into its layout's `"content"` slot by
registration — you do not bind it yourself:

```go
page := collage.NewPage("home").
	WithLayouts(layout).
	WithContent(content).
	WithPath("en", "/").
	Incremental(time.Minute).
	Build()
```

Registration gives each page a **private copy of the layout's slot table** before
binding, so one layout value can be shared by every page in the application —
including its error pages. Everything else about the layout stays shared: the
template path, the data handler, the fallback, and any child fragments already
bound into its other slots. Registration therefore snapshots the layout's
bindings; a fragment bound into a shared layout *after* a page was registered does
not appear on that page.

A page with no layout renders its content fragment as the root.

### Layout chains

A page wraps its content in as many layouts as it has, outermost first.
Registration fills every `content` slot in the chain — the content fragment into
the innermost layout, each layout into the one outside it — so a layout with a
hole is a finished fragment, not a builder a page has to finish:

```go
func Master() *collage.Fragment {
	return collage.NewFragment("layout", "layouts/default.html").WithTitle("My site").Build()
}

func Auth() *collage.Fragment {
	return collage.NewFragment("auth-layout", "layouts/auth.html").Build()
}

page := collage.NewPage("login").
	WithLayouts(layouts.Master(), layouts.Auth()).
	WithContent(login).
	WithPath("en", "/login").
	Build()
```

Every layout in the chain renders the one inside it with `{{slot "content"}}`.
Every page still gets its own copy of every layout's slot table, so one layout
value serves the whole application. A layout in a chain must not arrive with its
`content` slot filled — `collage.ErrSlotOccupied` at registration — except the
innermost holding exactly the page's content fragment, the hand-bound escape
hatch a single layout has always had.

`WithLayouts` records `collage.ErrMissingLayout` when called with no layouts,
`collage.ErrNilFragment` for a nil one, `collage.ErrFragmentCycle` for the same
layout twice, and `collage.ErrConflictingLayout` when called a second time.

A layout can also say who may see the pages it wraps; see
[Guards](routing.md#guards).

## Data handlers

A fragment's data is set with `WithData`, which takes a `collage.Data`. Only four
constructors make one, and each says where the data comes from:

| Constructor | What it runs | The template's `.` |
| --- | --- | --- |
| `collage.DataHandler(fn)`, `fn` returning `(T, []string, error)` | `fn`, on every render, reporting its dependency tags | `T` |
| `collage.Load(fn)`, `fn` returning `(T, error)` | `fn`, on every render, reporting no tags | `T` |
| `collage.Value(v)` | nothing: `v` is handed over on every render | `v`'s type |
| `collage.Effect(fn)`, `fn` returning `error` | `fn`, on every render, for what it declares | nothing |

A fragment with no `WithData` renders with no data, like `Effect`.

A handler is an ordinary function returning its own type:

```go
func loadPost(ctx context.Context, rc *collage.RenderContext) (*Post, []string, error) {
	post, err := store.Post(ctx, rc.Param("slug"))
	if err != nil {
		return nil, nil, err
	}
	return post, []string{"post:" + post.Slug}, nil
}

collage.NewFragment("blog-post", "pages/blog-post.html").
	WithData(collage.DataHandler(loadPost)).
	Build()
```

`T` comes from the function's signature, so the handler and the type its template
is checked against cannot disagree — see
[how templates are checked](#how-templates-are-checked). The same loader is just as
easy to call from a test or a sitemap's handler, which get a `*Post` rather than a
value to assert.

A Go method cannot take a type parameter, so the constructors are functions of
their own rather than forms of `WithData`. `collage.Load(func(ctx, rc) (T,
error))` is for a page that is not cached or data that does not change — a cached
page whose data does change wants its tags, so that they invalidate it — and
`collage.Effect(func(ctx, rc) error)` is for a fragment that only declares things
for the page, a title or structured data, and renders nothing.

A handler whose output depends only on the URL — the path's parameters and the
locale — can say so with `Static()` on the fragment, which keeps it from making a
page that declares no strategy dynamic; see
[caching](caching.md#a-page-that-declares-none). One whose output is the same for
every reader but changes over time — a measurement — says `Shared()` instead, which
lets its render be shared without making the page static.

Data fixed when the program starts — a list of links, a heading — needs no
handler at all. `collage.Value(v)` hands the template the same value on every
render:

```go
collage.NewFragment("home-content", "pages/home.html").
	WithData(collage.Value(homeView{Links: links})).
	Build()
```

Unlike a handler, it leaves a page that declares no strategy static; see
[caching](caching.md#a-page-that-declares-none). A fragment has one source of data:
calling `WithData` twice with data is `collage.ErrConflictingData` at
registration. A nil `Data` — `WithData(nil)`, or a constructor handed a nil
function — is no data, and conflicts with nothing.

A handler made with `DataHandler` returns three things:

- **data** — whatever the fragment's template renders with. Each fragment gets its
  own; a child does not inherit its parent's. On an error the data is dropped,
  so a nil `*Post` returned beside an error never reaches the template as a
  value that looks present.
- **tags** — the dependency tags this data was derived from, e.g.
  `[]string{"post:" + slug}`. They are unioned with the page's own
  `WithDependency` tags and stored with the cache entry. See
  [caching](caching.md).
- **error** — see below.

Tags are collected even when the handler then fails: a handler that resolved what
it depends on and *then* errored has still said what would invalidate this page.

### Timeouts

`WithTimeout(d)` bounds a fragment's data handler; `Config.Template.Timeout`
(default 5s) is the fallback for fragments that set none. The timeout bounds the
context the handler is handed, not the handler itself — see
[architecture](architecture.md) for why — so honour `ctx`:

```go
func loadPost(ctx context.Context, rc *collage.RenderContext) (*Post, []string, error) {
	post, err := store.Post(ctx, rc.Param("slug"))
	if err != nil {
		return nil, nil, err
	}
	return post, []string{"post:" + post.Slug}, nil
}
```

### The render context

`*collage.RenderContext` is request-scoped and must not be retained past the
render that created it.

| Member | What it gives you |
| --- | --- |
| `Request` | The inbound `*http.Request` |
| `Locale` | The resolved locale |
| `PathParams`, `Param(name)` | Route parameters captured from the path |
| `Page` | The page being rendered — **read-only, see below** |
| `Key.Get(rc)`, `Key.Set(rc, v)` | Values exchanged between fragments in one render |
| `Context()` | The underlying `context.Context` |

Keyed values work because a parent's data handler returns before its children's
start: a fragment can read what an ancestor's handler stored. Siblings' handlers
run concurrently; `Get`, `Set` and `collage.Once` hold the render's lock — see
[how a page's fragments run](#how-a-pages-fragments-run).

> **`rc.Page` is not yours to write to.** Every other member of the render context
> is request-scoped; `Page` is a pointer to the one `*collage.Page` the framework
> registered, shared by every request that reaches it and by every goroutine
> serving them. A data handler that writes `rc.Page.Paths["en"] = ...`, appends to
> `rc.Page.DependencyTags`, or edits `rc.Page.Redirects` is not customising one
> response — it is mutating live framework state while other requests read it,
> which is a data race in the precise sense: `go test -race` will report it, and
> without the race detector it corrupts a map sooner or later. Read from it freely;
> put anything you want to vary per request under a `collage.Key` or in the data your
> handler returns.

## How templates are checked

A template reads its data by name — `{{.Title}}`, `{{.Author.Name}}` — and
`html/template` resolves those names only when the template runs. A `{{.Titel}}`
would fail every render of that page, and on a page nobody visits, never. Because
every fragment's data comes from a typed constructor, registration knows the Go type
each template will run with, and walks the template against it. `RegisterPage`
refuses a page whose templates do not fit their data, naming the page, the
fragment, the file, the line and the column:

```
collage: page "post": fragment "post-body" (post.html:1:6): {{.Titel}}: type blog.Post has no field or method Titel (did you mean Title?)
```

It reports only what is certain to fail: each finding is an expression
`text/template` would fail on when the render reached it. So an application that
now fails at startup had a template that would already have failed when rendered;
the check reports it earlier, and adds no rule of its own.

### What is walked

Every fragment a page reaches: its layouts, its content, everything bound into
their slots, fallbacks, inline fragments, fragments opened with
`WithFragmentPath`, and the pages named as `NotFoundPage` and `ErrorPage`. A
partial included with `{{template "partials/author.html" .Author}}` is walked
with the type it is handed, and once for each type it is handed across the
application. A fragment a [slot resolver](#slots-filled-per-render) returns is
built while the page renders, so registration never sees it and its template is
not checked.

`.` starts as the fragment's data type and follows the template: inside
`{{range}}` it is the element, inside `{{with}}` the value, and `$x := …`
variables keep the type they were given for their scope.

### What is reported

- **A field or method the type does not have**, or a field that is unexported.
  Pointers are followed, and fields promoted from embedded structs count. The
  closest exported name is suggested when one is near.
- **A method with a pointer receiver, reached through a value that is not
  addressable.** `text/template` calls such a method through the value's
  address, and data handed to a template by value has none — nor have its
  fields, nor a map's elements. What a pointer points to and a slice's elements
  are addressable. If `URL` is declared on `*Post`, `{{.URL}}` fails on a `Post`
  and works on a `*Post`; the fix is to hand the template the pointer.
- **A map keyed by a named string type** (`map[Slug]Post`): `.key` looks a key
  up as a plain `string`, which such a map does not accept. A map keyed by
  `string` is fine. A key it does not hold is not an error either: it gives an
  invalid value that silently ends the chain, so `{{.Meta.absent.Name}}` renders
  nothing. The check, knowing only the map's element type, still reports a name
  that element type lacks.
- **Calls of the wrong shape**: a method, a template function or a built-in
  given the wrong number of arguments; a field or map key given arguments; a
  method or function returning more than a value and an error; `call` on
  something that is not a function.
- **`range`, `len`, `index` and `slice` on the wrong kind**: ranging over a
  string or a struct, or over an integer with two variables; `len` of a struct;
  `index` into a struct; `slice` of anything but a string, a slice or an array,
  of a string by three indexes, or of an array `text/template` cannot take the
  address of (`{{slice .Days 0}}` on data passed by value: pass the pointer).
  A slice, array or string is indexed and sliced by integers, and a map is
  indexed by a key of its own key type, or an integer converted to it:
  `{{index .ByID "7"}}` on a `map[int]Post` is reported.
- **An argument its parameter cannot take**, as `text/template` judges it when
  it calls a template function or a method (from v0.51.0):
  - a constant is judged by the parameter's kind. An integer parameter takes a
    number written as an integer (`1`, `1.0`, `'a'`) and converts it without a
    range check, so `{{i8 300}}` renders 44; an unsigned one a non-negative
    integer; a float parameter any number but a complex one; a string or
    bool parameter a string or a bool. So `{{upper 1}}`, `{{printf 1}}`,
    `{{uint -1}}` and `{{i8 1.5}}` are reported. A parameter of a named string
    type takes a string constant, `{{slugFor "x"}}`; an interface with methods
    (`fmt.Stringer`), a pointer, a slice or a map takes no constant but `nil`,
    and `nil` goes only where a nil can;
  - any other value must be assignable to the parameter, or be a pointer to
    something that is, or be addressable where the parameter is its pointer.
    So `{{upper .Count}}` with an `int` field, `{{slugFor .Title}}` with a
    `string` one, and `{{edit .Owner}}` for a `func(*User)` on data passed by
    value are reported, while `{{show .Author}}` for a `func(User)` with a
    `*User` field is fine. A number in parentheses, or in a variable, is a
    value of type `int`, not a constant: `{{i8 (1)}}` is reported. A variadic
    parameter takes its element type, and the value piped in,
    `{{.Count | upper}}`, is judged like any other argument;
  - `call` checks what it passes as `text/template`'s `call` does: a value
    assignable to the parameter, or an integer converted to another integer
    type, and nothing else — it follows no pointer and takes no address.
- **Number constants**, typed as `text/template` types them where nothing else
  does: `1`, `'a'` and `0x10` are `int`, `1.5`, `1e3` and `0x1p4` are
  `float64`, `1i` is `complex128`. So `{{$n := 1}}{{$n.Name}}`, `{{len 1}}` and
  `{{range $i, $v := 3}}` are reported, as is a number too large for an `int`,
  and `nil` used as a command rather than an argument. A number passed to a
  function's `int8` or `float64` parameter is converted to it, and judged as
  below.

What the render can never reach is not reported. An `{{if}}` or `{{with}}`
whose condition is a literal — `true`, `false`, `0`, `1`, `""`, `"x"`, or `not`
of one — never runs the side the literal rules out. `{{and}}` and `{{or}}` stop
at an operand only when it is a literal that decides them, `false`, `0`, `""` or
`nil` for `and`; `true`, a non-zero number or a non-empty string for `or`: `{{and 0 .Nope}}` reports nothing, while
`{{and 1 .Nope}}` and `{{and .Title .Nope}}` report `.Nope`, since the render
may reach it. `text/template`'s truth decides: `false`, zero, the empty string
and `nil` are false.

### What is not

- **What cannot be known before the render.** An interface — `any`, a
  `map[string]any`'s entries, an `error` — could hold anything, so nothing read
  from one is reported. Nor is anything read from a `reflect.Value` a method or
  function returns, which `text/template` unwraps into whatever it holds. A
  function `call` invokes is checked like any other: its argument count, and
  what it returns.
- **A fragment with no data.** Reading a field of nil data is not an error in
  `html/template`: `{{.Title}}` renders nothing. A fragment without `WithData`,
  or with `collage.Effect`, is still walked, but only its function calls are
  checked: their argument counts, and the arguments not read from `.`.
- **Nil pointers inside the data.** `{{.Author.Name}}` with a nil `Author` fails
  when it renders, and whether it is nil is the data's business, not the type's.
- **An argument whose fit the render decides.** A value of an interface type,
  a `reflect.Value`, a function parameter of type `any` or `reflect.Value`, and
  a map entry handed to a parameter that can be nil — a key the map does not
  hold arrives as nil, which that parameter takes — are not judged; nor is an
  index out of range. A plugin's render functions are parsed with a stand-in
  that takes anything, so their arguments are left to the plugin.

### Reading the error

`RegisterPage` returns every finding in the page's own fragments at once,
joined with `errors.Join` in a fixed order — by file, line and column — so ten
mistakes are one restart rather than ten. The page's not-found page is checked
once the page itself passes, and its error page once both have: their findings
come wrapped in a `collage: page "post" not-found page: …` (or `error page: …`)
error, on the start after the page's own are fixed. Each finding is a
`*collage.TemplateTypeError`, and each matches `collage.ErrTemplateType`. Since
a finding may sit under a wrapping error as well as a join, walk the whole tree
to list them:

```go
// typeErrors collects every *collage.TemplateTypeError in err's tree.
func typeErrors(err error) []*collage.TemplateTypeError {
	switch e := err.(type) {
	case nil:
		return nil
	case *collage.TemplateTypeError:
		return []*collage.TemplateTypeError{e}
	case interface{ Unwrap() []error }:
		var found []*collage.TemplateTypeError
		for _, inner := range e.Unwrap() {
			found = append(found, typeErrors(inner)...)
		}
		return found
	case interface{ Unwrap() error }:
		return typeErrors(e.Unwrap())
	}
	return nil
}
```

and, where the page is registered:

```go
if err := app.RegisterPage(page); errors.Is(err, collage.ErrTemplateType) {
	for _, typeErr := range typeErrors(err) {
		fmt.Printf("%s:%d:%d %s\n", typeErr.Template, typeErr.Line, typeErr.Col, typeErr.Reason)
	}
}
```

`errors.As` on the returned error itself finds the first finding. The fields are
`Page`, `Fragment`, `Template` (the file the expression is in — the fragment's
own or a partial it includes — or `inline template of fragment "x"`), `Line`,
`Col`, `Expr` (the expression as written, `{{.Titel}}`), `Reason` and
`Suggestion` (the closest name the type has, or empty). `Line` and `Col` are
where `text/template` itself would name the error, which may lie inside `Expr`
rather than at its start — at the last argument of a call, say.

The check runs where the other registration checks run: at startup, and so in
`collage check`, which starts the application, and in a test that registers its
pages. Under `collage dev` a template edited while the program runs is reparsed
but not checked again until the next restart, which is the next change to its Go
code.

### Turning it off

`WithoutTypeCheck()` leaves one fragment's template out of the check, for the
rare finding that is wrong, while it is fixed; the fragment renders as before,
and `collage inspect` marks it `"typeCheck": false`:

```go
collage.NewFragment("post-body", "pages/post.html").
	WithData(collage.DataHandler(loadPost)).
	WithoutTypeCheck().
	Build()
```

A handler that declares an interface as its type —
`collage.Load(func(ctx context.Context, rc *collage.RenderContext) (any, error) {…})`
— leaves the type unknown, and its template is not walked: that is the way to
say on purpose that a fragment's data has no fixed shape. `collage.Value` is the
exception: its value is in hand, so a value held in an interface is checked
against the type it holds.

## Failure policy

```go
postContent := collage.NewFragment("blog-post", "pages/blog-post.html").
	WithData(collage.DataHandler(loadPost)).
	Required().
	Build()

sidebar := collage.NewFragment("sidebar", "partials/sidebar.html").
	WithData(collage.Load(loadSidebar)).
	WithFallback(collage.NewFragment("sidebar-empty", "partials/sidebar-empty.html").Build()).
	Build()
```

- `Required()` — this fragment's failure fails the page. Use it for the content
  the page exists to show.
- `WithFallback(f)` — on failure, `f` renders instead. If `f` fails too, the
  fragment emits nothing and the page still succeeds: a fallback exists to contain
  a failure, so its own failure is contained rather than escalated.
- Neither — the fragment emits nothing and the page still succeeds.

In development mode a failed fragment emits an HTML comment naming the fragment
and its error instead of nothing, so a failure looks like a failure rather than
like a section nobody wrote. In every mode it is logged, since v0.36.0, as a
warning through `Config.Logger`: `collage: fragment failed; the page is served
without it`, with the page, the fragment, the locale, whether a fallback covered
for it, and the error. The page still answers 200, so the log is where a missing
form shows in production.

Any of these makes the render *degraded*, and a degraded render is served but
never cached.

### "Missing" versus "broken"

A required fragment whose handler returns an error wrapping `collage.ErrNotFound`
produces a **404** served by the page's `NotFoundPage`. Any other error produces a
**500** served by the page's `ErrorPage`.

```go
post, err := store.Post(ctx, slug)
if errors.Is(err, sql.ErrNoRows) {
	return nil, nil, fmt.Errorf("blog: no post %q: %w", slug, collage.ErrNotFound)
}
if err != nil {
	return nil, nil, fmt.Errorf("blog: load post %q: %w", slug, err)
}
```

The wrap must survive to the framework, so use `%w` all the way up. The sentinel
only takes effect on a `Required()` fragment: an optional fragment's
`ErrNotFound` follows the ordinary optional-failure policy, because the page
itself is still renderable without it.

## Template functions

Available in every template:

| Function | Purpose |
| --- | --- |
| `slot "name"` | Render the fragments bound to this fragment's slot |
| `safeHTML s` | Mark a string as trusted HTML (escape hatch — validate first) |
| `safeURL s` | Mark a string as a trusted URL (escape hatch — validate first) |
| `dict "k" v ...` | Build an ad-hoc map for a sub-template |
| `default fallback v` | `v`, or `fallback` when `v` is empty |
| `upper`, `lower`, `title` | Case conversion |
| `join sep items` | `strings.Join` |
| `formatTime t layout` | `t.Format(layout)` |
| `pageURL "name" "param" value ...` | The URL of a page or document, in this render's locale — see below |
| `pageURLIn "tr" "name" ...` | The same, in exactly the locale given |
| `localeURL "tr"` | The page being rendered, in another locale; empty when it has no path there |
| `actionURL "name" "param" value ...` | The URL of an action, in this render's locale — see [Linking one](actions.md#linking-an-action) |
| `fragmentURL "page" "fragment" "param" value ...` | The path a page opened for one of its fragments with `WithFragmentPath`, in this render's locale — see [Fragment paths](actions.md#a-fragment-at-its-own-url) |
| `fragmentURLIn "tr" "page" "fragment" ...` | The same, in exactly the locale given |
| `asset "/static/app.css"` | A mounted file's content-addressed URL |
| `stylesheet "/static/app.css"` | Hoist a `<link rel="stylesheet">` for a mounted file into the head, by its content-addressed URL — see [Hoisting](#hoisting) |
| `csrfToken` | The hidden input a form's forgery token travels in |
| `hoist "area"` | Where hoisted content lands — see [Hoisting](#hoisting) |

### Links by name

A link written as a path breaks silently when the path changes, and cannot know
which locale it is in. Link by the name the page was registered under instead:

```html
<a href="{{pageURL "about"}}">About</a>
<a href="{{pageURL "blog-post" "slug" .Slug}}">{{.Title}}</a>
<link rel="alternate" type="application/rss+xml" href="{{pageURL "feed"}}">
```

- **Locale-aware.** On `/tr/hakkinda`, `pageURL "blog-post"` is `/tr/yazi/...`. A
  page with no path in the current locale links its default-locale one instead,
  so a Turkish page linking a page that exists only in English still renders.
  `pageURLIn` asks for one locale and does not fall back.
- **Strict.** An unknown name, a missing or empty parameter, or a parameter the
  pattern has no placeholder for fails the render: a link that cannot be built is
  a bug to find in development, not a 404 for a reader. Values are escaped, and a
  value of `.` or `..` — which a browser would resolve as a path step — is refused.
- **Values are strings, integers or `fmt.Stringer`s.** `{{pageURL "story" "id" .ID}}`
  works for an `int64` `.ID`. A float, a bool or nil fails with
  `collage.ErrRouteParams` naming its type, since none has one obvious spelling in
  a URL. Before v0.36.0 only strings were taken.

A language switcher is `localeURL`, which keeps the page's own path parameters
and is empty for a language the page has not been translated into:

```html
{{with localeURL "en"}}<a hreflang="en" href="{{.}}">English</a>{{end}}
{{with localeURL "tr"}}<a hreflang="tr" href="{{.}}">Türkçe</a>{{end}}
```

From Go — a data handler, or an action redirecting to a named page — it is
`rc.URL`, and `rc.ActionURL` for an action. Both build in the render's own
locale and fall back to the default one as `pageURL` does, so nothing has to
hand the application to the code that needs a link:

```go
target, err := rc.URL("blog-post", map[string]string{"slug": post.Slug})
if err != nil {
	return nil, err
}
return collage.SeeOther(target), nil
```

A value, even one straight from the request, is one escaped path segment: one
holding `/`, or being `.` or `..`, is `ErrRouteParams`, so it cannot turn the
path into another site's URL. On a `RenderContext` collage did not make — one a
test builds by hand — both return `ErrUnknownRoute`. For a locale other than the
render's own, `app.URL(name, locale, params)` takes one explicitly.

### Adding your own

`Config.Template.Funcs` is merged over the built-ins at construction, so an entry
under a built-in name replaces it:

```go
app, err := collage.New(&collage.Config{
	Template: collage.TemplateConfig{
		Root: "templates",
		Funcs: template.FuncMap{
			"money": func(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) },
			"title": strings.ToTitle, // replaces the built-in
		},
	},
})
```

It must be set before `New`. `html/template` resolves a function name at execution
time but can only call a name that was already in the map when the template was
parsed, and `New` is where parsing happens — so a template calling a name nobody
registered fails in `New`, not at the first request, and a name added afterwards is
never consulted. Overriding a per-render function — `slot`, `hoist`, `asset`,
`stylesheet`, `csrfToken`, `pageURL`, `pageURLIn`, `localeURL`, `fragmentURL`,
`fragmentURLIn` — is possible but
pointless: each needs the render it runs in, so the render engine rebinds them all
on every render and yours never runs.

Anything that needs request state belongs in the data handler rather than in a
function: that is where the data comes from anyway.

## Development mode

`Config.DevMode` (or `Config.Template.DevMode`) reloads every template from disk
before each render, turns on the failed-fragment comments, adds an
`X-Collage-Render-Time` header, and lets the built-in error page show the failing
fragment and the full error chain. **It must be off in production**: those
diagnostics routinely carry hostnames, filesystem paths, and credentials from an
error message.

**A page with a broken part says so.** In development a fragment that failed —
even one whose fallback covered for it — puts a panel over the page, fixed to the
bottom of the viewport, naming the fragment where the failure started and the error,
and for a template the file and line. It can be dismissed. Your own error page gets
the same panel, with the failure it is standing in for. Neither ever
reaches the cache, and neither exists outside development.

Content your application reads from disk in development — Markdown pages, JSON data
— is neither a template nor a mount, so name its directory in `Config.DevWatch` to
have a page reload when it changes too.

Every page it serves in answer to a GET carries a small script that reloads it
when a template or a mounted file changes, and when the program restarts — so under
`collage dev`, which rebuilds on a Go change, saving any file is enough. The script
listens on `/_collage/reload`, a stream that exists only in development and is
served ahead of middleware. A browser allows six connections to one origin across
all its tabs and windows, so every development tab listens through one shared worker
(`/_collage/reload-worker.js`) holding one stream for all of them; six pages side by
side no longer use up the connections their own fragments need. Where there is no
shared worker, each tab holds its own stream and closes it while hidden, reloading
on return if anything changed meanwhile. The answer to a POST never carries it: reloading one
would submit the form again. A strict `Content-Security-Policy` of your own may
block the inline script in development; production pages never carry it.

A stream that never closes is a page that never finishes loading, which is what a
screenshot tool or an end-to-end test waits for. A browser driven by Playwright,
Puppeteer or Selenium sets `navigator.webdriver`, and the script does not connect
there. For the tools that do not — headless Chrome's `--screenshot` and
`--dump-dom` — add `?collage-reload=0` to the URL and the page is served without
the script.

## Hoisting

A fragment often needs something that belongs to the page rather than to itself: the
stylesheet it uses, the title it is the subject of, a preload hint for its own
image. It cannot write those where they go, because it does not know where that is —
and by the time it renders, the layout has already written its `<head>`.

So it declares, and the layout decides where declarations land. A title known when
the program starts — the site's name, on its layout — is `WithTitle`, which takes
no handler and so leaves the page static:

```go
collage.NewFragment("layout", "layouts/default.html").
	WithTitle("My site").
	Build()
```

Everything else is declared from a data handler:

```go
// in a fragment's data handler
rc.HoistTitle(post.Title)
rc.HoistMeta("description", post.Summary)
rc.HoistProperty("og:image", post.CoverURL)
rc.HoistLink("canonical", canonicalURL)
rc.HoistAlternate("tr", trURL)             // <link rel="alternate" hreflang="tr">
rc.HoistStylesheet("/static/gallery.css") // its content-addressed URL
```

```html
<!-- or from the fragment's own template -->
{{stylesheet "/static/gallery.css"}}
```

```html
<!-- in the layout -->
<head>
  {{hoist "head"}}
</head>
```

The helpers write to the `"head"` area, escape what they are given, and choose the
key — `title`, `meta:description`, `link:canonical`, `alternate:tr`,
`stylesheet:/static/gallery.css` — so a more specific fragment's declaration replaces a less specific one's, and a
stylesheet several fragments ask for appears once. `rc.Asset(path)` is the
content-addressed URL `{{asset}}` renders, for when Go needs it.

Underneath them is `rc.Hoist(area, key, html)`, which inserts `html` exactly as
written — the right tool for anything the helpers do not cover, and the one where
escaping is yours:

```go
rc.Hoist("head", "preload:hero", template.HTML(
	`<link rel="preload" as="image" href="`+html.EscapeString(heroURL)+`">`))
```

`{{hoist}}` writes a marker rather than content, because nothing below it has
rendered yet. The engine replaces the marker once the whole tree is finished, so a
declaration made anywhere below still reaches it. One pass; the layout keeps
deciding the position.

### The key decides what counts as the same thing

Distinct keys all appear, in the order they were first declared. The same key
declared twice keeps **the innermost declaration**, because that is what specificity
looks like in a fragment tree:

```go
// layout: a default
rc.Hoist("head", "title", `<title>The Wire</title>`)

// article content, nested inside it: more specific, and wins
rc.Hoist("head", "title", `<title>Seawalls Buy Time — The Wire</title>`)
```

That one rule covers both jobs. Stylesheets get a key per href, so they accumulate
and deduplicate. A title gets one key, so it overrides. Nothing has to be declared
as "a set" or "a single value".

Two details worth knowing rather than discovering:

- **Position comes from the first declaration, not the winning one.** Otherwise a
  page's `<head>` would reorder itself depending on whether something nested
  happened to override a title.
- **At equal depth, the later declaration wins.** Two siblings writing one key is a
  real conflict with no specificity to settle it, so the rule is arbitrary — stated
  here rather than found out.

### What it does not escape

`Hoist` inserts what you give it, exactly as written. That is the point of the
mechanism and the responsibility that comes with it: build the markup from values
you control, or escape them yourself.

Call it from a data handler, before the handler returns. The collector takes a
lock, so sibling handlers hoisting at the same moment are safe; what is not is a
goroutine of the handler's own that hoists after the handler returned — by then the
page may already have been assembled, and the declaration lands nowhere.
