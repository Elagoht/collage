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
	WithSlot("widgets", false, true)

sidebar := builder.Build()
if err := builder.BuildErr(); err != nil {
	return fmt.Errorf("sidebar: %w", err)
}
```

Ignoring `BuildErr` does not lose a mistake silently: `RegisterPage` validates the
page and refuses a malformed one by name before it serves a request. Check it when
the builder's inputs are not known to be well-formed ahead of time.

## Slots

A slot is a named position inside a fragment's template, written `{{slot "name"}}`
and declared with `WithSlot(name, required, allowMultiple)`:

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
layout := collage.NewFragment("layout", "layouts/default.html").
	WithSlot("content", true, false). // required, single fill
	Build()
```

- `required` — the render fails if nothing is bound to the slot. This is enforced
  before the template runs, so a required slot is caught even if the template
  never asks for it.
- `allowMultiple` — more than one fragment may be bound; they render in binding
  order, concatenated.

Binding a child explicitly:

```go
sidebar := collage.NewFragment("sidebar", "partials/sidebar.html").
	WithSlot("widgets", false, true).
	WithSlotFragment("widgets", recentPosts).
	WithSlotFragment("widgets", tagCloud).
	Build()
```

Referring to a slot the fragment never declared is an error, not empty output: a
typo in a template would otherwise become a section that is simply missing.

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

The `any` is the framework's: `html/template` renders arbitrary data and the
framework cannot know an application's shape. Your own code does not have to
spread it around — write handlers against a concrete type and adapt once:

```go
// pageData is everything this application's templates render with.
type pageData struct {
	Site  siteInfo
	Posts []Post
	Post  *Post
}

// bind adapts a typed data function to collage.DataHandlerFunc.
func bind(fn func(context.Context, *collage.RenderContext) (*pageData, []string, error)) collage.DataHandlerFunc {
	return func(ctx context.Context, rc *collage.RenderContext) (any, []string, error) { // any: restates DataHandlerFunc's own declaration
		data, tags, err := fn(ctx, rc)
		if err != nil {
			return nil, tags, err
		}
		return data, tags, nil
	}
}
```

That is the pattern `examples/blog` uses, and why the whole example contains
exactly one `any`.

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
func loadPost(ctx context.Context, rc *collage.RenderContext) (*pageData, []string, error) {
	post, err := store.Post(ctx, rc.Param("slug"))
	if err != nil {
		return nil, nil, err
	}
	return &pageData{Post: post}, []string{"post:" + post.Slug}, nil
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
| `Page` | The page being rendered |
| `SharedData`, `Get`, `Set` | Values exchanged between fragments in one render |
| `Context()` | The underlying `context.Context` |

`SharedData` works because the render is sequential: a fragment can read what an
ancestor rendered earlier in the same walk.

## Failure policy

```go
postContent := collage.NewFragment("blog-post", "pages/blog-post.html").
	WithDataHandler(bind(loadPost)).
	Required().
	Build()

sidebar := collage.NewFragment("sidebar", "partials/sidebar.html").
	WithDataHandler(bind(loadSidebar)).
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
never consulted. Overriding `"slot"` is possible but pointless: the render engine
rebinds it per render.

Anything that needs request state belongs in the data handler rather than in a
function: that is where the data comes from anyway.

## Development mode

`Config.DevMode` (or `Config.Template.DevMode`) reloads every template from disk
before each render, turns on the failed-fragment comments, adds an
`X-Collage-Render-Time` header, and lets the built-in error page show the failing
fragment and the full error chain. **It must be off in production**: those
diagnostics routinely carry hostnames, filesystem paths, and credentials from an
error message.

It does not hot-reload Go code. A change to your `.go` files still needs a
restart.
