# Plugin extensibility, and six parked defects

**Status:** approved 2026-09-23. **Amended the same day:** the three plugins named
below were built here to drive the design out, then moved to repositories of their
own — a plugin living in the framework's tree would be built against a layout no
third-party author has, and the first one to try would find the difference the hard
way. The capability work described here stayed. Publisher segments in the plugin
names below were placeholders and are now all `elagoht`. Decisions below were taken explicitly; the
rationale travels with them so a later reader can tell a choice from an accident.

## Why

The plugin system is *observable*, not *extensible*. Six hooks let a plugin watch a
request and post-process HTML, and `Host` lets it read pages, invalidate tags, log,
and add a CLI subcommand. It cannot contribute anything the framework then serves:
no page, no document, no mount, no template function.

Three plugins were proposed, and between them they name every missing capability.
That is what this design is built around — not a survey of what a plugin system
could have.

| Plugin | Needs | Today |
|---|---|---|
| opti-image | HTML post-process | `AfterRender` |
| | serve its own rewritten image URLs | **missing** |
| jsonld | inject into `<head>` | `AfterRender` |
| | the data the render produced | **missing** |
| minimizer | HTML | `AfterRender` |
| | document output (JSON, XML) | **missing** |
| | asset output (CSS, JS) | **missing** |

## Decisions

### D1 — Third-party plugins need no new mechanism

Go compiles plugins in. `plugin.Open` is Linux/macOS only, demands an identical
toolchain and identical dependency versions, and is not a basis for an ecosystem.
A third-party plugin is therefore an ordinary Go module:

```go
import optimage "github.com/Elagoht/collage-opti-image"
app.RegisterPlugin(optimage.New())
```

That already works: `collage.Plugin` is public and nothing about the framework's own
plugins is privileged. The three built alongside this design are each their own
module in their own repository, so they are structurally indistinguishable from one
written by anyone else — which is what proves the surface is enough.

### D2 — The application supplies plugin configuration, not the framework

`Config.PluginConfig` is `map[string]json.RawMessage`. The application loads it
however it likes; `collage.LoadPluginConfig(path)` is a convenience for the common
case, not a requirement. `plugins-config.json` is a convention, not a format the
framework imposes — an application wanting YAML or environment variables is not shut
out.

`json.RawMessage` rather than a decoded map is what keeps `any` out of the boundary:
a plugin decodes into its own typed struct.

The key is `Plugin.Name()`. Plugin names are already required to be unique, so a
second identifier would be a second thing to keep in sync. Names should read like
module paths — `elagoht/minimizer`.

A key matching no registered plugin is a startup error. Silently ignoring
`elagoht/minimzer` leaves the plugin running on defaults and the operator certain it
was configured.

The Go constructor stays the primary, type-safe path. JSON is the deployment layer
over it: the plugin decodes over its defaults, so an absent key means defaults.

### D3 — The plugin lifecycle splits in two

`Configure` runs inside `New`, before templates are parsed. `Init` runs at `Start`,
as now.

The split exists because the two phases can offer different things and neither
ordering serves both. Template functions must be registered before parsing —
`html/template` resolves a function name at execution time but can only call one
that existed in the `FuncMap` at parse time. Pages, meanwhile, are registered by the
application *between* `New` and `Start`, so a plugin that wants to read them, or to
register one of its own, must run after that.

`Configure` is an optional interface discovered by type assertion, exactly as the
hooks are, so existing plugins are unaffected:

```go
type Configurer interface {
	Configure(ctx context.Context, host ConfigHost) error
}
```

### D4 — Assets are transformed by wrapping the filesystem, not per request

Mounts serve through `http.ServeContent`, which brings `Range`, `If-Range` and
`206`. Transforming bytes per request shifts every offset, so a range request would
return the wrong slice of a file whose length no longer matches the one advertised.

`ConfigHost.WrapMount(func(fs.FS) fs.FS)` therefore wraps the filesystem at mount
registration. A minifier returns an `fs.FS` whose files are already minified, and
`ServeContent` keeps working on whatever it is handed.

## Interfaces

```go
// ConfigHost is what Configure receives. It is narrower than Host: at this point
// the application has registered nothing, so there are no pages to read.
type ConfigHost interface {
	DevMode() bool
	Logger() *slog.Logger
	// Config decodes this plugin's section of Config.PluginConfig into v. It
	// leaves v untouched when the plugin has no section, so v carries defaults in.
	Config(v any) error // any: the JSON decoder's own parameter type
	// AddTemplateFunc registers a template function. It must be called from
	// Configure: templates are parsed when New returns.
	AddTemplateFunc(name string, fn any) error // any: html/template.FuncMap's own value type
	// WrapMount wraps every mounted filesystem, in registration order.
	WrapMount(wrap func(fs.FS) fs.FS)
}

// Host gains, alongside what it already has:
type Host interface {
	// ... DevMode, Pages, Page, InvalidateTags, Logger, RegisterCommand
	Config(v any) error // any: the JSON decoder's own parameter type
	RegisterPage(p *types.Page) error
	RegisterDocument(d *types.Document) error
	Mount(prefix string, fsys fs.FS, opts ...asset.Option) error
}

// DocumentRenderedHook lets a plugin transform a document's bytes, which is what
// makes a minifier work for JSON and XML as well as HTML.
type DocumentRenderedHook interface {
	OnDocumentRendered(ctx context.Context, ev *DocumentRenderedEvent) error
}

type DocumentRenderedEvent struct {
	Document    *types.Document
	ContentType string
	Locale      string
	Path        string
	// Body is the document's output. A plugin MAY replace it; the replacement is
	// what the caller serves and, unless a CacheWriteHook suppresses it, caches.
	Body []byte
}
```

`AfterRenderEvent` gains `Data map[string]any` — the render's `SharedData`. That is
how a JSON-LD plugin reaches the article a page was built from, rather than parsing
it back out of the HTML it is about to annotate. It is the live map, not a copy, for
the same reason `Page` is: copying it on every render would cost the hot path.

## The six parked defects

1. **Cache keys have no query allowlist.** The raw query string is a cache-key
   dimension, so `?utm_source=x` mints an entry per variant and a crawler can evict
   the real archive. `PageBuilder.WithCacheParams(names ...string)` restricts the key
   to the named parameters; the default stays "everything", since narrowing by
   default would silently merge representations.
2. **A layout cannot see its content's data.** Deferred, and documented instead:
   `examples/magazine` shows the head-slot pattern, which is the answer a two-pass
   render would only complicate. Revisit if a second application hits it.
3. **`ClaimedPaths` misses `/tr/`.** `localePrefixed` maps the root pattern to
   `/tr`, but `/tr/` resolves too, so a mount at `/tr/` shadows the Turkish home page
   instead of being refused at startup.
4. **A panicking `Tracer.StartSpan` drops the connection.** It sits outside the
   `safeCall` containment the rest of the pipeline has.
5. **The static builder can write two pages to one path.** Nothing detects it; the
   second silently overwrites the first.
6. **Plugins cannot contribute** — the subject of this document.

## Plugins

Each was built as its own module and now lives in its own repository.

- **`elagoht/minimizer`** — HTML via `AfterRender`, documents via
  `OnDocumentRendered`, assets via `WrapMount`. Each of `html`, `json`, `css`, `js`
  is independently switchable. Whitespace and comment removal only: a correct
  minifier for these formats is a parser, and a regex one silently corrupts input.
- **`elagoht/jsonld`** — emits `<script type="application/ld+json">` into `<head>`
  from `Page.SEO` and the render's `SharedData`.
- **`elagoht/opti-image`** — rewrites `<img>` elements that declare `width` and
  `height`, fetching from an allowlist of hosts, resizing, and serving the result
  from a mount it registers itself. WebP is behind a build tag, because encoding it
  requires cgo and a plugin that cannot be built without a C toolchain is a plugin
  most people cannot use.
