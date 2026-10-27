# elagoht/markdown v0.4.0 — render only (design, 2026-10-27)

## Intent

Today `elagoht/markdown` owns a directory of files: it takes an `fs.FS`, reads
`*.md`, and hands out `Handler()`, `IndexHandler()`, `StaticParams()`, `List`,
`Get` and dependency tags. The user finds this impractical: because the plugin
builds the page data itself, SEO and similar per-page concerns (meta, jsonld,
canonical, og cards, custom data) have to be wired through a different path
than every other page of the app.

The user's ruling: **the plugin only turns a Markdown string into HTML.** Where
the string comes from (embed, disk, database, an API) and how the page, its
route, its data handler and its SEO are built is entirely the application's
business. If `elagoht/highlight` is registered, code blocks get coloured; that is
all.

## Goals

1. A template function that renders a Markdown string: `{{markdown .Body}}`.
2. The same rendering from Go, plus a front-matter helper into the
   application's **own struct type**, chosen with `md:"…"` field tags.
3. goldmark extensions selectable from `plugins-config.json`.
4. `==text==` renders as `<mark>text</mark>` (user request).
5. Code blocks keep the `<pre><code class="language-x">` shape, so
   `elagoht/highlight`'s after-render pass colours them with no coupling between
   the two plugins (verified: highlight's README, auto pass on by default).

## Non-goals

- Reading files or directories, locale directories, slugs from file names.
- `collage.Data` handlers, `StaticParams`, `List`/`Get`, sorting by date.
- Dependency tags, `OnCacheInvalidate`, dev-mode re-reading.
- Draft filtering (an app that wants it declares `Draft bool` in its struct).
- Deriving the title from the first `# heading` and removing it from the HTML.
- Several instances (`Options.Name`): one template function name, one plugin.

## Anti-goals

- The plugin must not start doing page/route/data work again by another name.
- Rendering must not become less safe than v0.3.2: raw HTML stays off unless
  `unsafe`, heading `{…}` attributes keep losing `style` and `on…` when not
  `unsafe` (v0.3.2 fix).

## API (Go)

Follow the plugin conventions (`docs/superpowers/specs/plugin-seal/conventions.md`):
one constructor, zero `Options` starts, errors prefixed `markdown:`, no `any`
in code we write except where a framework signature forces it (the
`AddRenderFunc` factory's return).

```go
const Name = "elagoht/markdown"

type Options struct {
	// Extensions lists the goldmark extensions to turn on. Absent: the default
	// set. A JSON list replaces the default whole (PluginConfig semantics), so
	// [] turns every optional extension off.
	Extensions []string `json:"extensions"`
	// Unsafe lets raw HTML through and keeps every heading attribute.
	Unsafe bool `json:"unsafe"`
}

func New(opts Options) *Plugin

// Render renders src (front matter, if any, skipped) for locale.
func (p *Plugin) Render(locale, src string) (template.HTML, error)

// Doc is a parsed document: the front matter in the app's type and the body.
type Doc[T any] struct {
	Meta     T
	HTML     template.HTML
	Text     string     // body as text: prose, lists, tables, inline code; no code blocks, no raw HTML
	Headings []Heading  // every heading, in order
}

type Heading struct {
	ID    string
	Text  string
	Level int
}

// Parse splits src's front matter into a T and renders the body.
func Parse[T any](p *Plugin, locale, src string) (Doc[T], error)

// FrontMatter splits src's front matter into a T and returns the raw body,
// without rendering. Needs no plugin.
func FrontMatter[T any](src string) (T, string, error)
```

Go methods cannot take type parameters, so `Parse` and `FrontMatter` are
package functions; `Parse` takes the plugin for its configured renderer.
`[T any]` is a type-parameter constraint, which is the one place `any` is the
idiom; it is not a value of type `any`.

`Render`/`Parse` before the plugin is configured return an error saying to
register it in `Config.Plugins` (same idea as v0.3.2's `errNotRegistered`).

## Template function

`{{markdown .Body}}` → `template.HTML`.

- Registered with `ConfigHost.AddRenderFunc` (per render), so heading ids use
  the render's locale (`rc.Locale`). This makes the plugin a `Configurer`: it
  must be listed in `Config.Plugins` (README + manifest `setup` note say so).
- A leading front-matter block is skipped, so a template can be given the whole
  file.
- A render error fails the render (template execution error), like any other
  template function error.

## Front matter decoding

- Front matter is YAML between `---` lines at the very start (same splitting as
  v0.3.2's `splitFront`; no front matter → zero T and the whole input is body).
- T must be a struct (or pointer to struct); otherwise an error.
- Field matching: tag `md:"name"`; `md:"-"` skips the field; an untagged
  exported field matches its own name case-insensitively (like encoding/json).
  Unexported fields are never set (Go cannot) — README notes the field must be
  exported even if the tag is lower-case.
- **Keys in the front matter with no field are ignored silently** (user ruling).
- Supported field types: `string`, `bool`, all int/uint/float kinds,
  `time.Time` (formats as v0.3.2's `parseDate`: `2006-01-02`,
  `2006-01-02 15:04`, RFC 3339; no zone = UTC), `[]string` (a YAML list, or one
  comma-separated line as v0.3.2's `splitList`). Pointers to these are allowed
  and left nil when the key is absent.
- A value that does not convert (bad date, `draft: maybe` into bool, a mapping
  into a string) is an error naming the key and the field. Silent zero values
  are what the v0.3.2 README warned against.
- Unsupported field types (maps, nested structs, other slices) are an error at
  decode time naming the field, even if the key is absent, so a wrong struct is
  found on first use.

## Extensions

Names accepted in `extensions` (unknown name → Init/Configure error naming it):

| name | what | default |
|---|---|---|
| `table` | GFM tables | on |
| `strikethrough` | `~~x~~` | on |
| `linkify` | bare URLs become links | on |
| `taskList` | `- [ ]` / `- [x]` | on |
| `footnote` | `[^1]` | on |
| `mark` | `==x==` → `<mark>` (ours) | on |
| `headingIDs` | automatic heading ids, locale-aware slugs (v0.3.2's `slugify`/`ids`, keeps `önemli-başlık`, Turkish/Azerbaijani `I` → `ı`) | on |
| `headingAttributes` | `## Title {#id .class}` | on |
| `definitionList` | PHP-Markdown definition lists | off |
| `typographer` | smart quotes, dashes, ellipses | off |

The default set reproduces v0.3.2's rendering (GFM + footnote + heading ids +
heading attributes) plus `mark`. Rule 2 of the conventions (zero bool = default)
is why this is a list and not `noX` booleans; a list is also what "configure the
extensions" reads like in JSON.

Heading-id uniqueness is per `Render`/`Parse` call (as v0.3.2 was per file).
Two `{{markdown}}` calls on one page can produce the same id; documented, not
solved.

## The `mark` extension

Our own goldmark extension, modelled on goldmark's `extension.Strikethrough`
(delimiter parser with `==`, an inline node kind, an HTML renderer writing
`<mark>`/`</mark>`). CommonMark flanking rules apply, so `a == b` is not marked;
inside code spans and code blocks `==` is literal. No new dependency.

## Kept from v0.3.2

goldmark, `stripUnsafeAttributes`, `plain` (for `Text`), locale-aware heading
ids, `{#id}` heading attributes, `language-x` code block classes.

## Removed (breaking → v0.4.0)

`Options.FS`, `Dir`, `LocaleDirs`, `Drafts`, `Name`; `Handler`, `IndexHandler`,
`StaticParams`, `List`, `Get`, `Tag`, `DirTag`, `OnCacheInvalidate`; the old
`Doc` fields `Slug`, `Locale`, `Title`, `Description`, `Date`, `Updated`,
`Tags`, `Draft`, `Front`. The README gets a "Changed in v0.4.0" section with a
before/after showing the same blog page built by the app itself: an embed FS,
its own data handler calling `markdown.Parse[Post]`, its own `StaticParams`,
and the template calling `{{.HTML}}` or `{{markdown .Body}}`.

## Release surface (for the plan)

- Plugin repo `~/Desktop/collage-markdown`: code, tests (`collagetest.NewSite`),
  README, `collage.json` regenerated (schemagen) with the new `config` schema
  (`extensions` enum list, `unsafe`) and a `setup` note (must be in
  `Config.Plugins`), `Version()` 0.4.0.
- Docs site `~/Desktop/collage-docs/content/plugins.md` and `tr/plugins.md`.
- `~/Desktop/collage/internal/cli/ai/AGENTS.md` mentions the plugin: update.
- VS Code extension: bundled schema/snippets come from the manifests; check
  for markdown snippets that use the removed API.
- Bundles (`@blog` already dropped markdown in v0.75) — no change expected;
  verify.
- `elagoht/feed` patch release (Claude's call): its `collage.json` `setup` note
  says to build items from `md.List(ctx, "")`, which v0.4.0 removes. Rewrite it
  to the app's own loop over `markdown.Parse[Post]` / `FrontMatter[Post]`.
  Order: markdown → feed → extension regeneration → docs site.
- Release only with the user's go-ahead.

## Left to the builder

Internal structure, test layout, exact error wording (prefix `markdown:`),
whether `FrontMatter` shares decoding code with `Parse` (it should).
