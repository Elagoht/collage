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
parent's has returned, so a parent putting something in `SharedData` for its children
to read still works, and so does a child reading a path parameter its parent
resolved.

**Siblings run at the same time**, and that is the one thing that is genuinely
different. Three consequences follow, in order of how likely they are to matter.

*Read and write `SharedData` through `Get` and `Set`, not as a bare map.* Two
siblings writing a map directly is a data race. `Get` and `Set` hold the render's
lock. `collage.Get[T](rc, key)` reads a value as the type it was stored as, so a
read needs no type assertion: `post, ok := collage.Get[Post](rc, "post")`.

*Prefer `Once` to a read-then-write.* This shape has a hole in it:

```go
if v, ok := rc.Get(key); ok { return v.(Article), nil }
article, err := api.Article(ctx, slug)   // both siblings are here at once
rc.Set(key, article)
```

Both siblings miss the `Get`, both fetch, and the page quietly asks the upstream
twice. `Once` closes it — the first caller fetches, the rest wait for it:

```go
article, err := collage.Once(rc, "article:"+slug, func(ctx context.Context) (Article, error) {
	return api.Article(ctx, slug)
})
```

`Once` lives as long as one render. When the same data appears on many pages — an
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
	WithDataHandler(handler).
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

## Slots

A slot is a named position inside a fragment's template, written `{{slot "name"}}`.
Calling it in the template is all the declaring it needs:

```html
<!-- templates/layouts/default.html -->
<!doctype html>
<html lang="en">
<head><title>{{.Site.Name}}</title></head>
<body>
  <main>{{slot "content"}}</main>
</body>
</html>
```

```go
layout := collage.NewFragment("layout", "layouts/default.html").Build()
```

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
page := collage.NewFragment("sections", "pages/sections.html").
	WithDataHandler(loadSections). // rc.Set("sections", blocks)
	WithSlotResolver("sections", func(rc *collage.RenderContext) ([]*collage.Fragment, error) {
		value, _ := rc.Get("sections")
		blocks, _ := value.([]block)
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
  not exist fails that render rather than registration.

The nesting limit is 32 levels, which exists to catch a fragment bound, directly
or indirectly, into one of its own slots.

## Pages, layouts, and the content slot

A page's content fragment is bound into its layout's `"content"` slot by
registration — you do not bind it yourself:

```go
page := collage.NewPage("home").
	WithLayout(layout).
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

## Data handlers

```go
type DataHandlerFunc func(ctx context.Context, rc *RenderContext) (data any, tags []string, err error)
```

A handler is a function of that shape, handed straight to the fragment:

```go
collage.NewFragment("blog-post", "pages/blog-post.html").
	WithDataHandler(func(ctx context.Context, rc *collage.RenderContext) (any, []string, error) {
		post, err := store.Post(ctx, rc.Param("slug"))
		if err != nil {
			return nil, nil, err
		}
		return post, []string{"post:" + post.Slug}, nil
	}).
	Build()
```

The data is `any` because its one reader, the template, is untyped anyway: a
`{{.Titel}}` fails when the page renders whatever the handler's return type says,
so a concrete type would check nothing the template does not.

Where a concrete type does pay is outside the page — a loader called from a test,
or from a sitemap's handler, is easier to use when it returns a `*Post` rather
than an `any` to assert. Write that loader against its own type and adapt it
with `collage.DataHandler`, which is generic over the return type:

```go
func loadPost(ctx context.Context, rc *collage.RenderContext) (*Post, []string, error) {
	// ...
}

collage.NewFragment("blog-post", "pages/blog-post.html").
	WithDataHandler(collage.DataHandler(loadPost)).
	Build()
```

A Go method cannot take a type parameter, so the adapter is a function of its
own rather than a form of `WithDataHandler`. Two shorter ones cover the loaders
that report no tags: `collage.Load(func(ctx, rc) (T, error))`, for a page that is
not cached or data that does not change — a cached page whose data does change
wants its tags, so that they invalidate it — and `collage.Effect(func(ctx, rc)
error)`, for a fragment that only declares things for the page, a title or
structured data, and renders nothing.

A handler whose output depends only on the URL — the path's parameters and the
locale — can say so with `Static()` on the fragment, which keeps it from making a
page that declares no strategy dynamic; see
[caching](caching.md#a-page-that-declares-none). One whose output is the same for
every reader but changes over time — a measurement — says `Shared()` instead, which
lets its render be shared without making the page static.

Data fixed when the program starts — a list of links, a heading — needs no
handler at all. `WithData(v)` hands the template the same value on every render:

```go
collage.NewFragment("home-content", "pages/home.html").
	WithData(homeView{Links: links}).
	Build()
```

Unlike a handler, it leaves a page that declares no strategy static; see
[caching](caching.md#a-page-that-declares-none). Setting both `WithData` and
`WithDataHandler` is `collage.ErrConflictingData` at registration.

The handler returns three things:

- **data** — whatever the fragment's template renders with. Each fragment gets its
  own; a child does not inherit its parent's.
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
func loadPost(ctx context.Context, rc *collage.RenderContext) (any, []string, error) {
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
| `SharedData`, `Get`, `Set` | Values exchanged between fragments in one render |
| `Context()` | The underlying `context.Context` |

`SharedData` works because a parent's data handler returns before its children's
start: a fragment can read what an ancestor's handler stored. Siblings' handlers
run concurrently, so read and write it through `Get` and `Set` (or `collage.Get`
and `collage.Once`), never as a bare map — see
[how a page's fragments run](#how-a-pages-fragments-run).

> **`rc.Page` is not yours to write to.** Every other member of the render context
> is request-scoped; `Page` is a pointer to the one `*collage.Page` the framework
> registered, shared by every request that reaches it and by every goroutine
> serving them. A data handler that writes `rc.Page.SEO["title"] = ...`, appends to
> `rc.Page.DependencyTags`, or edits `rc.Page.Paths` is not customising one
> response — it is mutating live framework state while other requests read it,
> which is a data race in the precise sense: `go test -race` will report it, and
> without the race detector it corrupts a map sooner or later. Read from it freely;
> put anything you want to vary per request in `SharedData` or in the data your
> handler returns.

## Failure policy

```go
postContent := collage.NewFragment("blog-post", "pages/blog-post.html").
	WithDataHandler(loadPost).
	Required().
	Build()

sidebar := collage.NewFragment("sidebar", "partials/sidebar.html").
	WithDataHandler(loadSidebar).
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
like a section nobody wrote.

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
- **Values are strings.** Pass a number through `printf`:
  `{{pageURL "user" "id" (printf "%d" .ID)}}`.

A language switcher is `localeURL`, which keeps the page's own path parameters
and is empty for a language the page has not been translated into:

```html
{{with localeURL "en"}}<a hreflang="en" href="{{.}}">English</a>{{end}}
{{with localeURL "tr"}}<a hreflang="tr" href="{{.}}">Türkçe</a>{{end}}
```

From Go — an action redirecting to a named page — it is `app.URL`:

```go
target, err := app.URL("blog-post", "tr", map[string]string{"slug": post.Slug})
return collage.SeeOther(target), err
```

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
served ahead of middleware. A hidden tab closes its stream and reconnects when it is
seen again — a browser allows six connections to one origin across all its tabs —
and the page reloads then if anything changed while it was hidden. The answer to a POST never carries it: reloading one
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
