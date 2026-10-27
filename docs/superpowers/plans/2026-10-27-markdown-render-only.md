# elagoht/markdown v0.4.0 (render only) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn elagoht/markdown into a Markdown-string renderer: `{{markdown}}`, `Render`, `Parse[T]`, `FrontMatter[T]`, configurable extensions, `==mark==`.

**Architecture:** Plugin becomes a `Configurer` that builds one goldmark instance from `Options.Extensions` and registers a per-render `markdown` func (locale from `rc.Locale`). Front matter is decoded from a yaml.v3 node tree into the caller's struct by `md` tags. `==` is our own goldmark inline extension.

**Tech Stack:** Go 1.26, goldmark v1.8.6, yaml.v3, collage v0.77.0 (`collagetest.NewSite`).

**Spec:** `docs/superpowers/specs/2026-10-27-markdown-render-only-design.md`

## Global Constraints

- Repo `~/Desktop/collage-markdown`, branch `feat/render-only` off main; `Version()` "0.4.0"; go.mod requires collage v0.77.0.
- Errors start with `markdown:`. Never use type `any` except the `AddRenderFunc` factory return (`// any: html/template.FuncMap's own value type`) and `[T any]` constraints.
- `Options` is `{Extensions []string \`json:"extensions"\`; Unsafe bool \`json:"unsafe"\`}`; `New(opts Options) *Plugin`.
- Extension names exactly: table, strikethrough, linkify, taskList, footnote, mark, headingIDs, headingAttributes, definitionList, typographer. Default (nil list) = first eight.
- Keep v0.3.2 helpers verbatim in behaviour: `splitFront`, `stripUnsafeAttributes`, `plain`, `ids`/`slugify`, `parseDate` layouts, `splitList`.
- Release (tag/push) only with the user's go-ahead.

## Review Focus

1. Template given a whole file with front matter → only body rendered (Task 2 test `TestTemplateSkipsFrontMatter`).
2. `unsafe` off: `<script>` and heading `{style=… onclick=…}` dropped; on: kept (Task 2 `TestUnsafe`).
3. `a == b` and `` `==x==` `` stay literal; `==x==` inside emphasis works (Task 1 tests).
4. Front matter that does not close (`---` without end) → error, not silently rendered as body (Task 3 `TestFrontMatterUnclosed`).
5. `Render` called on a plugin that was never configured → `markdown:` error, no nil panic (Task 2 `TestRenderBeforeConfigure`).

---

### Task 1: `==mark==` extension

**Files:** Create `mark.go`, `mark_test.go`

**Interfaces:** Produces `var Mark goldmark.Extender` (unexported is fine: `markExt`), node kind `KindMark`.

- [ ] Test `TestMark` (goldmark with only the extension): `==hi==` → `<p><mark>hi</mark></p>\n`; `a == b` → no `<mark>`; `` `==x==` `` → `<code>==x==</code>`; `**==x==**` → `<strong><mark>x</mark></strong>`; `===x===` not producing nested garbage (assert well-formed: count of `<mark>` == count of `</mark>`).
- [ ] Run, FAIL.
- [ ] Implement modelled on goldmark `extension/strikethrough.go`: delimiter processor char `'='`, `CanOpenCloser` requiring exactly length 2 on both sides; node renderer writes `<mark>`/`</mark>`.
- [ ] Run, PASS. Commit `feat: ==mark== extension`.

### Task 2: Plugin core — Options, extensions, Configure, Render, `{{markdown}}`

**Files:** Rewrite `markdown.go` (remove FS/handlers/List/Get/tags/OnCacheInvalidate/old Doc); rewrite `markdown_test.go`.

**Interfaces:** Produces `func (p *Plugin) Render(locale, src string) (template.HTML, error)`; internal `func (p *Plugin) convert(locale string, body []byte) (html template.HTML, root ast.Node, source []byte, err error)` used by Task 3; `Configure(ctx, collage.ConfigHost) error`.

- [ ] Tests (collagetest.NewSite with a template calling `{{markdown .}}` or via Render):
  - `TestDefaultExtensions`: table, `~~x~~`→`<del>`, bare `https://example.com`→`<a`, task list checkbox, footnote, `==m==`→`<mark>`, `## Önemli Başlık`→`id="önemli-başlık"`, `## X {#install}`→`id="install"`; definition list and typographer NOT applied (`"x"` stays straight quotes).
  - `TestExtensionsConfig`: plugin config `{"extensions":["definitionList","typographer"]}` → `<dl>` appears, smart quotes appear, `<table>` absent, `<mark>` absent.
  - `TestUnknownExtension`: `{"extensions":["tables"]}` → site start fails, error contains `markdown:` and `"tables"`.
  - `TestUnsafe`: off drops `<script>` and heading `style`/`onclick`, keeps `class`; `{"unsafe":true}` keeps both.
  - `TestTemplateLocale`: page in locale tr rendering `## Işık` → `id="ışık"`; en → `id="işık"`... (assert per slugify: en gives `ıșık`? use `## IŞIK` tr→`ışık`, en→`işık`).
  - `TestTemplateSkipsFrontMatter`: `{{markdown .}}` on `"---\ntitle: x\n---\n# Body"` → contains `Body`, not `title:`.
  - `TestCodeBlockClass`: fenced go → `<pre><code class="language-go">`.
  - `TestRenderBeforeConfigure`: `markdown.New(markdown.Options{}).Render("en","x")` → error with `Config.Plugins`.
- [ ] Run, FAIL.
- [ ] Implement: `Configure` decodes `PluginConfig`, validates names, builds goldmark (`parser.WithAutoHeadingID` only if headingIDs, `parser.WithAttribute`… keep v0.3.2's `WithHeadingAttribute` only if headingAttributes), registers `AddRenderFunc("markdown", …)` returning `func(src string) (template.HTML, error)` using `rc.Locale`. `Init` does nothing but return nil. `convert` = splitFront, parse with `parser.WithIDs(newIDs(locale))` context, strip unsafe heading attributes when !Unsafe (as v0.3.2), render. Var-assert `collage.Configurer`.
- [ ] Run, PASS; `go vet`. Commit `feat!: render Markdown strings only`.

### Task 3: `FrontMatter[T]` and `Parse[T]`

**Files:** Create `front.go`, `front_test.go`.

**Interfaces:** Consumes `convert`, `splitFront`, `parseDate`, `splitList`, `plain`. Produces `type Doc[T any] struct{Meta T; HTML template.HTML; Text string; Headings []Heading}`, `type Heading struct{ID, Text string; Level int}`, `func FrontMatter[T any](src string) (T, string, error)`, `func Parse[T any](p *Plugin, locale, src string) (Doc[T], error)`.

- [ ] Tests with `type post struct{Title string \`md:"title"\`; Date time.Time \`md:"date"\`; Tags []string \`md:"tags"\`; Draft bool; Views int \`md:"views"\`; Score *float64 \`md:"score"\`; Skip string \`md:"-"\`; hidden string}`:
  - `TestFrontMatterDecodes`: title, `date: 2026-09-20` → UTC date, `tags: [a, b]` and `tags: a, b` both → `[a b]`, `draft: true` (untagged, case-insensitive match of `Draft`), `views: 3`, `score` absent → nil, `Skip`/unknown key `author` ignored; body returned raw.
  - `TestFrontMatterErrors`: `date: someday` → error naming `date`; `draft: maybe` → error; `title: {a: 1}` → error; `T=int` → error; struct with `M map[string]string` field → error naming `M` even when key absent.
  - `TestFrontMatterNone`: no front matter → zero T, body == src.
  - `TestFrontMatterUnclosed`: `"---\ntitle: x\n"` → error.
  - `TestParse`: Meta filled, HTML has body, `Headings` = [{önemli-başlık, Önemli Başlık, 2}], `Text` excludes code block and raw HTML; pointer-to-struct T works.
- [ ] Run, FAIL.
- [ ] Implement: decode yaml into `yaml.Node`; build field index via reflect (tag, `-`, case-insensitive name; unexported skipped); validate field types up front; convert scalar/sequence nodes per type; headings via `ast.Walk` over `*ast.Heading` (ID from attribute `id`, text `plain`).
- [ ] Run, PASS. Commit `feat: FrontMatter and Parse into the application's own type`.

### Task 4: Manifest, README, module

**Files:** `go.mod` (collage v0.77.0, module comment), `collage.json` (regenerate config with schemagen: `extensions` array enum, `unsafe`; `setup`: must be listed in `Config.Plugins`; description), `README.md` (rewrite; "Changed in v0.4.0" with before/after blog page).

- [ ] `GOWORK=off go mod tidy && go test -count=1 ./... && go vet ./...` PASS; staticcheck@v0.7.0 clean.
- [ ] `collage plugin check` (built CLI from collage checkout) passes on the repo.
- [ ] Commit `docs: v0.4.0 README and manifest`.

### Task 5: feed setup note

**Files:** `~/Desktop/collage-feed/collage.json` setup text (and README if it mentions `md.List`), branch `docs/markdown-v040`, Version patch bump.

- [ ] Replace the `md.List(ctx, "")` advice with the app's own loop over files using `markdown.FrontMatter[Post]`; tests pass; commit.

### Release (only on the user's go-ahead)

markdown tag v0.4.0 → feed patch → extension regeneration → docs site (plugins.md EN+TR) → AGENTS.md in collage.
