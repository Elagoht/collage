# Deploy Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A static export carries the server's response headers and every redirect to Netlify, Cloudflare Pages, Vercel or GitHub Pages, through a new `elagoht/deploy` plugin fed by the core.

**Architecture:** The core captures each written file's response headers by serving its path twice through the application's real handler in-process, keeps what is stable, and hands it with every redirect (pages, documents, `RedirectSource` plugins) to `BuildFinishedHook`. `elagoht/deploy` compacts the headers and writes the chosen host's files. collage-redirects stops writing `_redirects` and becomes a `RedirectSource`.

**Tech Stack:** Go 1.26, `net/http/httptest`, `encoding/json`.

**Spec:** `docs/superpowers/specs/2026-10-07-deploy-adapter-design.md`

## Global Constraints

- Never write the type `any` in new code; an unavoidable one carries a trailing `// any: <why>` comment.
- Request-specific headers always dropped: `Date`, `ETag`, `Last-Modified`, `Content-Length`, `Set-Cookie`, `Vary`, `Content-Encoding`, `Transfer-Encoding`, `Connection`, `Age`.
- Each path is requested twice; a header differing between the two is dropped with a warning (once per header name, with a count of paths).
- A redirect's `From`/`To` with `\r`, `\n` or any other control character is refused (`ErrInvalidRedirect`), at the builder and again before any file is written.
- Targets: `netlify`, `cloudflare`, `vercel`, `github-pages`; one per build; empty → write nothing, warn once.
- Output is deterministic: the same site writes byte-identical files.
- `go test -count=1`; staticcheck (`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`) clean.
- Commit trailer `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`; read `git status` before `git add`; add by name; never add `.superpowers/` files.
- Core work in a worktree: `git worktree add -b feat/deploy ../collage-deploy-core main`. The plugin lives in a new repo `~/Desktop/collage-deploy` (module `github.com/Elagoht/collage-deploy`), developed with `replace github.com/Elagoht/collage => ../collage-deploy-core` until collage v0.52.0 is tagged. Plugin conventions: `const Name = "elagoht/deploy"`, `New()`/`NewWith(cfg)`, `Version()`, README titled `# elagoht/deploy`, `.gitignore` of test output, comment above `module` describing the plugin.

## Review Focus

1. **A file the handler answers differently from what the build wrote** — the build's own 404 pages (`/404.html`, `/tr/404.html`) and the root meta-refresh page under `PrefixDefault` — must not be captured (no headers, no warning). Pinned in Task 3.
2. **A redirect whose `From` is a pattern** (`/old/{slug}`, `/docs/{rest...}`) must translate per host and never be written as a literal `{slug}`; GitHub Pages skips it with a warning. Pinned in Task 6.
3. **Compaction never changes which headers a path gets**: expanding the written rules back over every file reproduces each file's captured headers exactly (property test). Pinned in Task 5.
4. **A host limit exceeded** (Cloudflare 100 header rules, 2000+100 redirects) warns naming what was left out and still writes a valid, truncated file in rule order. Pinned in Task 6.
5. **A user's own `vercel.json` / `_headers` / `_redirects` already in the output** (from a mount or a document) is never overwritten: Vercel fails the build; Netlify/Cloudflare fail too when the file exists and wasn't written by the plugin. Pinned in Task 6.

---

### Task 1: Redirects refuse control characters

**Files:**
- Modify: `internal/types/errors.go` (new `ErrInvalidRedirect`), `internal/types/page.go` (helper), `pkg/collage/page.go:98-108`, `pkg/collage/document.go:124-135`, `pkg/collage/types.go` (alias)
- Test: `internal/types/redirect_test.go`, `pkg/collage/redirect_text_test.go`

**Interfaces:**
- Produces: `types.ErrInvalidRedirect`; `func types.RedirectTextError(from, to string) error` (nil when both are clean; otherwise names the field); `collage.ErrInvalidRedirect`.

- [ ] **Step 1: Write the failing tests**

`internal/types/redirect_test.go`:

```go
package types

import (
	"errors"
	"testing"
)

func TestRedirectTextError(t *testing.T) {
	tests := []struct {
		from, to string
		bad      bool
	}{
		{"/old", "/new", false},
		{"/old/{slug}", "https://example.com/x", false},
		{"/old\r\nX-Evil: 1", "/new", true},
		{"/old", "/new\n/evil 301", true},
		{"/old\t", "/new", true},
		{"/old", "/new\x7f", true},
	}
	for _, test := range tests {
		err := RedirectTextError(test.from, test.to)
		if (err != nil) != test.bad {
			t.Errorf("RedirectTextError(%q, %q) = %v, want bad=%v", test.from, test.to, err, test.bad)
		}
		if err != nil && !errors.Is(err, ErrInvalidRedirect) {
			t.Errorf("error %v does not wrap ErrInvalidRedirect", err)
		}
	}
}
```

`pkg/collage/redirect_text_test.go`:

```go
package collage_test

import (
	"errors"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestBuilders_RefuseControlCharactersInRedirects(t *testing.T) {
	page := collage.NewPage("p").WithRedirect("/a\r\nX: y", "/b", 301)
	if err := page.BuildErr(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("page WithRedirect: %v, want ErrInvalidRedirect", err)
	}
	permanent := collage.NewPage("p").WithPermanentRedirect("/a", "/b\n")
	if err := permanent.BuildErr(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("page WithPermanentRedirect: %v, want ErrInvalidRedirect", err)
	}
	doc := collage.NewDocument("d", "text/plain").WithRedirect("/a", "/b\r", 302)
	if err := doc.BuildErr(); !errors.Is(err, collage.ErrInvalidRedirect) {
		t.Errorf("document WithRedirect: %v, want ErrInvalidRedirect", err)
	}
	if err := collage.NewPage("p").WithRedirect("/a", "/b", 301).BuildErr(); err != nil {
		t.Errorf("a clean redirect: %v", err)
	}
}
```

Check the document builder's redirect method names (`grep -n 'func (b \*DocumentBuilder) With' pkg/collage/document.go`) and adapt.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -run 'RedirectTextError|RefuseControlCharacters' ./internal/types/ ./pkg/collage/`
Expected: FAIL to compile (`undefined: RedirectTextError`, `collage.ErrInvalidRedirect`).

- [ ] **Step 3: Implement**

`internal/types/errors.go`, beside `ErrInvalidRedirectStatus`:

```go
// ErrInvalidRedirect is returned for a redirect whose source or destination
// holds a control character: a carriage return or a line feed in it would add
// lines to a host's redirect file, a header to a response.
var ErrInvalidRedirect = errors.New("collage: invalid redirect")
```

`internal/types/page.go`, below `Redirect`:

```go
// RedirectTextError reports a control character in a redirect's from or to,
// naming the field, wrapped in ErrInvalidRedirect; nil when both are clean.
func RedirectTextError(from, to string) error {
	for _, field := range []struct{ name, value string }{{"from", from}, {"to", to}} {
		for _, r := range field.value {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("%w: %s %q holds a control character", ErrInvalidRedirect, field.name, field.value)
			}
		}
	}
	return nil
}
```

(add `"fmt"` to page.go's imports if missing).

`pkg/collage/page.go` `WithRedirect` and `WithPermanentRedirect`, before appending:

```go
	if err := types.RedirectTextError(from, to); err != nil {
		b.errs = append(b.errs, fmt.Errorf("page %q: %w", b.page.Name, err))
	}
```

Same in `pkg/collage/document.go` (`document %q`). Append the redirect anyway (the build error refuses registration).

`pkg/collage/types.go`, beside `ErrInvalidRedirectStatus`:

```go
// ErrInvalidRedirect is returned for a redirect whose source or destination
// holds a control character.
var ErrInvalidRedirect = types.ErrInvalidRedirect
```

- [ ] **Step 4: Run, then everything**

Run: `go test -count=1 -run 'RedirectTextError|RefuseControlCharacters' ./internal/types/ ./pkg/collage/ && go vet ./... && go test -count=1 ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add internal/types/errors.go internal/types/page.go internal/types/redirect_test.go pkg/collage/page.go pkg/collage/document.go pkg/collage/types.go pkg/collage/redirect_text_test.go
git commit -m "feat!: a redirect with a control character is refused"
```

---

### Task 2: Plugin types for headers and redirects

**Files:**
- Modify: `internal/plugin/hooks.go:257-299` (event, BuiltFile), create `internal/plugin/redirects.go`, modify `internal/plugin/registry.go` (collect), `internal/core/app.go` (App method), `pkg/collage/collage.go` (aliases)
- Test: `internal/plugin/redirects_test.go`

**Interfaces:**
- Consumes: `types.RedirectTextError` (Task 1).
- Produces:
  ```go
  // package plugin
  type BuiltRedirect struct { From, To string; Status int; Source string }
  type RedirectSource interface { Redirects() []BuiltRedirect }
  // BuiltFile gains: Status int; Headers http.Header
  // BuildFinishedEvent gains: Redirects []BuiltRedirect
  func (r *Registry) PluginRedirects() ([]BuiltRedirect, error) // Source = plugin name; validated
  // package core
  func (a *App) PluginRedirects() ([]plugin.BuiltRedirect, error)
  // package collage: type BuiltRedirect = plugin.BuiltRedirect; type RedirectSource = plugin.RedirectSource
  ```

- [ ] **Step 1: Write the failing test**

`internal/plugin/redirects_test.go` (use the package's existing test plugin helpers; `grep -n 'type .*Plugin struct' internal/plugin/*_test.go`):

```go
package plugin

import (
	"errors"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

type redirectsPlugin struct {
	name  string
	rules []BuiltRedirect
}

func (p redirectsPlugin) Name() string               { return p.name }
func (p redirectsPlugin) Version() string            { return "0" }
func (p redirectsPlugin) Redirects() []BuiltRedirect { return p.rules }

func TestRegistry_PluginRedirects(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, redirectsPlugin{name: "a", rules: []BuiltRedirect{{From: "/x", To: "/y", Status: 301}}})
	mustRegister(t, r, redirectsPlugin{name: "b", rules: []BuiltRedirect{{From: "/gone", Status: 410}}})
	got, err := r.PluginRedirects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Source != "a" || got[1].Source != "b" || got[1].Status != 410 {
		t.Errorf("PluginRedirects = %+v", got)
	}

	bad := NewRegistry()
	mustRegister(t, bad, redirectsPlugin{name: "evil", rules: []BuiltRedirect{{From: "/x\r\n", To: "/y", Status: 301}}})
	if _, err := bad.PluginRedirects(); !errors.Is(err, types.ErrInvalidRedirect) {
		t.Errorf("err = %v, want ErrInvalidRedirect naming the plugin", err)
	}
}
```

Adapt `NewRegistry` / `mustRegister` to the package's actual constructors (read `registry.go` and existing tests); the plugin must satisfy whatever `Plugin` requires.

- [ ] **Step 2: Run to verify it fails**

Run: `go test -count=1 -run PluginRedirects ./internal/plugin/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/plugin/redirects.go`:

```go
package plugin

import (
	"fmt"

	"github.com/Elagoht/collage/internal/types"
)

// BuiltRedirect is one redirect a static export carries to its host.
type BuiltRedirect struct {
	// From is the path pattern, as registered: "/old/{slug}".
	From string
	// To is the destination, as registered: "/new/{slug}" or an absolute URL.
	To string
	// Status is 301, 302, 307 or 308, or 410 for a path that is gone.
	Status int
	// Source names where the rule came from: "page:<name>",
	// "document:<name>" or a plugin's name.
	Source string
}

// RedirectSource is a plugin whose redirects a static export carries. The build
// asks it once, after every file is written, and hands its rules to
// BuildFinishedHook in BuildFinishedEvent.Redirects.
type RedirectSource interface {
	Redirects() []BuiltRedirect
}

// PluginRedirects collects the rules of every RedirectSource, in registration
// order, each stamped with its plugin's name and checked for control
// characters.
func (r *Registry) PluginRedirects() ([]BuiltRedirect, error) {
	var out []BuiltRedirect
	for _, p := range r.snapshot() {
		source, ok := p.(RedirectSource)
		if !ok {
			continue
		}
		var rules []BuiltRedirect
		if err := runHook(p.Name(), "Redirects", func() error {
			rules = source.Redirects()
			return nil
		}); err != nil {
			return nil, err
		}
		for _, rule := range rules {
			if err := types.RedirectTextError(rule.From, rule.To); err != nil {
				return nil, fmt.Errorf("collage: plugin %q redirects: %w", p.Name(), err)
			}
			rule.Source = p.Name()
			out = append(out, rule)
		}
	}
	return out, nil
}
```

`internal/plugin/hooks.go`: add to `BuildFinishedEvent`

```go
	// Redirects are every redirect the site declares — its pages', its
	// documents' and each RedirectSource plugin's — for a deploy adapter to
	// write in its host's form.
	Redirects []BuiltRedirect
```

and to `BuiltFile`

```go
	// Status is the status the application answered the file's path with
	// when the build asked for it, or 0 when it was not asked.
	Status int
	// Headers are the response headers the application sends for the file's
	// path that a static host can carry: request-specific ones and any that
	// differ from one response to the next are left out.
	Headers http.Header
```

(import `net/http`).

`internal/core/app.go`, near `BuildFinished`:

```go
// PluginRedirects returns every RedirectSource plugin's redirects, for the
// static build.
func (a *App) PluginRedirects() ([]plugin.BuiltRedirect, error) {
	return a.plugins.PluginRedirects()
}
```

`pkg/collage/collage.go`, beside `BuiltFile`:

```go
// BuiltRedirect is one redirect a static export carries; see RedirectSource.
type BuiltRedirect = plugin.BuiltRedirect

// RedirectSource is a plugin whose redirects a static export carries.
type RedirectSource = plugin.RedirectSource
```

- [ ] **Step 4: Run, then everything**

Run: `go test -count=1 ./internal/plugin/ && go vet ./... && go test -count=1 ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git status --short
git add internal/plugin/redirects.go internal/plugin/redirects_test.go internal/plugin/hooks.go internal/core/app.go pkg/collage/collage.go
git commit -m "feat: BuiltRedirect, RedirectSource and header fields on BuiltFile"
```

---

### Task 3: The build captures each file's headers

**Files:**
- Create: `internal/core/capture.go`
- Modify: `internal/build/builder.go` (call capture before BuildFinished; skip synthesized files)
- Test: `internal/core/capture_test.go`, `internal/build/capture_test.go` (or the builder's existing test file)

**Interfaces:**
- Consumes: `BuiltFile.Status/Headers` (Task 2).
- Produces:
  ```go
  // package core
  type CapturedResponse struct { Status int; Headers http.Header; Unstable []string }
  func (a *App) CaptureResponses(ctx context.Context, paths []string) (map[string]CapturedResponse, error)
  // package build: optional interface asserted on b.app
  type ResponseCapturer interface {
  	CaptureResponses(ctx context.Context, paths []string) (map[string]core.CapturedResponse, error)
  }
  ```
  (If importing `internal/core` from `internal/build` would cycle — check `go list -deps` — move `CapturedResponse` to `internal/plugin` or `internal/types` and alias it.)

- [ ] **Step 1: Write the failing core test**

`internal/core/capture_test.go` (build an App the way the package's other tests do; register a page at `/a`, a document at `/feed.xml` with `application/rss+xml`, and a test middleware via `app.Use` that sets `X-Stable: 1` and `X-Nonce: <random per request>` and `Set-Cookie: s=1`):

```go
func TestCaptureResponses(t *testing.T) {
	app := newCaptureApp(t) // helper in this file: the page, the document, the middleware above
	got, err := app.CaptureResponses(context.Background(), []string{"/a", "/feed.xml"})
	if err != nil {
		t.Fatal(err)
	}
	a := got["/a"]
	if a.Status != 200 || a.Headers.Get("X-Stable") != "1" || a.Headers.Get("Cache-Control") == "" {
		t.Errorf("/a = %+v", a)
	}
	for _, dropped := range []string{"Date", "Etag", "Set-Cookie", "Vary", "Content-Length", "X-Nonce"} {
		if a.Headers.Get(dropped) != "" {
			t.Errorf("/a kept %s", dropped)
		}
	}
	if !slices.Contains(a.Unstable, "X-Nonce") {
		t.Errorf("/a Unstable = %v, want X-Nonce", a.Unstable)
	}
	if got["/feed.xml"].Headers.Get("Content-Type") != "application/rss+xml" {
		t.Errorf("/feed.xml Content-Type = %q", got["/feed.xml"].Headers.Get("Content-Type"))
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -count=1 -run CaptureResponses ./internal/core/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement capture**

`internal/core/capture.go`:

```go
package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
)

// CapturedResponse is what the application answers a static file's path with,
// as a static host can carry it.
type CapturedResponse struct {
	Status int
	// Headers are the stable, host-carriable response headers.
	Headers http.Header
	// Unstable names the headers that differed between two requests — a nonce,
	// a timestamp — and were left out.
	Unstable []string
}

// requestSpecific are headers that describe one response, never the file.
var requestSpecific = []string{
	"Date", "Etag", "Last-Modified", "Content-Length", "Set-Cookie", "Vary",
	"Content-Encoding", "Transfer-Encoding", "Connection", "Age",
}

// CaptureResponses asks the application's handler for each path twice, as a
// static export would be asked for it — GET, no cookies, no Accept-Encoding,
// the host of Config.BaseURL — and keeps the headers both responses agree on.
func (a *App) CaptureResponses(ctx context.Context, paths []string) (map[string]CapturedResponse, error) {
	handler := a.Handler()
	host := "localhost"
	if u, err := url.Parse(a.cfg.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	out := make(map[string]CapturedResponse, len(paths))
	for _, p := range paths {
		first := a.captureOnce(ctx, handler, host, p)
		second := a.captureOnce(ctx, handler, host, p)
		kept := http.Header{}
		var unstable []string
		for name, values := range first.Header() {
			if slices.Contains(requestSpecific, name) {
				continue
			}
			if !slices.Equal(values, second.Header()[name]) {
				unstable = append(unstable, name)
				continue
			}
			kept[name] = slices.Clone(values)
		}
		slices.Sort(unstable)
		out[p] = CapturedResponse{Status: first.Code, Headers: kept, Unstable: unstable}
	}
	return out, nil
}

func (a *App) captureOnce(ctx context.Context, handler http.Handler, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
```

Header names from `ResponseRecorder.Header()` are canonical (`Etag`, not `ETag`); `requestSpecific` uses canonical forms. A header present in the second response but not the first is unstable too: after the loop, add every name in `second.Header()` missing from `first` (and not request-specific) to `unstable`.

- [ ] **Step 4: Wire into the build**

In `internal/build/builder.go`, before the `BuildFinisher` block (`~:497`):
- Build `files := b.builtFiles(...)` first.
- If `b.app` implements `ResponseCapturer`, collect the URL `Path` of every file **except** files the build synthesized: the 404 pages and the root meta-refresh page (identify them where they are written — `notfound.go`, `writeRootRedirect` — and keep a set of their output files; do not infer from names).
- Call `CaptureResponses(ctx, paths)`; set each file's `Status` and `Headers`. For every distinct unstable header name, add one warning to the event's findings after it is created: `ev.Warn("", "unstable-header", fmt.Sprintf("%s differs between two responses on %d path(s) (e.g. %s); a static host cannot carry it", name, count, firstPath))`. A file whose captured status is not 2xx: `ev.Warn(path, "capture-status", fmt.Sprintf("the application answered %s with %d; its headers are what that answer carried", path, status))`.
- Assign `event.Files = files`.

Add a builder test (beside the builder's existing tests, using their fake Renderer) asserting: a fake implementing `ResponseCapturer` receives every page/document/asset path and no 404/root-redirect path; the returned headers land on the matching `BuiltFile`; an unstable header yields exactly one warning.

- [ ] **Step 5: Run everything and commit**

Run: `go vet ./... && go test -count=1 ./... && go test -race -count=1 ./internal/core/ ./internal/build/`
Expected: PASS.

```bash
git status --short
git add internal/core/capture.go internal/core/capture_test.go internal/build/builder.go <build test file>
git commit -m "feat: a static build records each file's response headers"
```

---

### Task 4: The build hands every redirect to the hook

**Files:**
- Modify: `internal/build/builder.go` (collect, check, set `event.Redirects`), `internal/build/` errors (new sentinels if needed), `CHANGELOG.md` (`## Unreleased`), `docs/deployment.md`, the static-export doc in `docs/` (`grep -ln 'collage export' docs/*.md`)
- Test: builder test file

**Interfaces:**
- Consumes: `plugin.BuiltRedirect`, `(*core.App).PluginRedirects` (Task 2), `Renderer.Pages()`/`Documents()` (existing).
- Produces: `event.Redirects` filled; build errors `ErrDuplicateRedirect` (two rules with one `From`) and `ErrRedirectShadowsFile` (a `From` equal to a written file's URL path) in package build, aliased in `pkg/collage/build.go` beside `ErrBuildFindings`.

- [ ] **Step 1: Write the failing tests**

In the builder's test file, with its fake Renderer: pages with `Redirects` (one `WithRedirect("/old", "/new", 301)`, one permanent), a document with one, and a fake `PluginRedirects()` returning a 410 — assert `event.Redirects` holds all four in order (pages in registration order, then documents, then plugins) with `Status` from `EffectiveStatus()` and `Source` `"page:<name>"` / `"document:<name>"` / plugin name. A second test: a plugin rule duplicating a page's `From` fails the build with `ErrDuplicateRedirect`; a third: a `From` equal to a written page's path (`/a/`) fails with `ErrRedirectShadowsFile`. A fourth: a `PluginRedirects` error (control character) fails the build.

- [ ] **Step 2: Run to verify they fail, implement, run**

Implement in `builder.go` right after Task 3's capture: gather page redirects (`for _, p := range b.app.Pages() { for _, r := range p.Redirects { … } }`), document redirects, then `PluginRedirects()` when `b.app` implements `interface{ PluginRedirects() ([]plugin.BuiltRedirect, error) }`. Check duplicates by `From` and shadowing against the set of written URL paths (both `"/a"` and `"/a/"` forms for directories). Errors join `errs` like the other build failures.

Run: `go vet ./... && go test -count=1 ./...`

- [ ] **Step 3: Docs and changelog**

`CHANGELOG.md` `## Unreleased`: **Breaking** — a redirect with a control character fails registration (`ErrInvalidRedirect`); **Added** — `BuiltFile.Status/Headers`, `BuildFinishedEvent.Redirects`, `BuiltRedirect`, `RedirectSource`, the build's response capture (two requests per file, unstable headers left out with a warning), `ErrDuplicateRedirect`, `ErrRedirectShadowsFile`. `docs/deployment.md` and the static-export doc: a "Static hosts" section — what is captured and left out, redirects now reach the build hook, and that `elagoht/deploy` writes the host's files.

- [ ] **Step 4: Commit**

```bash
git status --short
git add internal/build/ pkg/collage/build.go CHANGELOG.md docs/<files by name>
git commit -m "feat: the static build hands every redirect to BuildFinishedHook"
```

---

### Task 5: `elagoht/deploy`: repo, configuration, compaction

**Files (new repo `~/Desktop/collage-deploy`):**
- Create: `go.mod` (comment above `module`; `go 1.26`; `require github.com/Elagoht/collage` + `replace github.com/Elagoht/collage => ../collage-deploy-core`), `deploy.go` (Plugin, config, target dispatch), `compact.go`, `compact_test.go`, `deploy_test.go`, `README.md`, `.gitignore`
- `git init`; do not create the GitHub repo yet.

**Interfaces:**
- Consumes: `collage.BuildFinishedEvent`, `collage.BuiltFile` (`Path`, `Status`, `Headers`), `collage.BuiltRedirect`, `collage.PluginConfig`.
- Produces:
  ```go
  const Name = "elagoht/deploy"
  type Config struct { Target string `json:"target"` }
  func New() *Plugin; func NewWith(cfg Config) *Plugin
  func (p *Plugin) Version() string // "0.1.0"
  // compaction
  type HeaderRule struct { Path string; Headers http.Header }  // Path: "/*", "/static/*", "/a/"
  func Compact(files []collage.BuiltFile) []HeaderRule          // deterministic
  func Expand(rules []HeaderRule, path string) http.Header      // what a host applies to path (all matching rules, in order, later overriding)
  ```

- [ ] **Step 1: Write the failing compaction tests**

`compact_test.go`: fixtures of BuiltFiles (headers via `http.Header{...}`):
- all files share `X-Content-Type-Options: nosniff` → one `/*` rule holds it, no file rule repeats it;
- `/static/app.3f9a.css`, `/static/logo.1b2c.png` share `Cache-Control: public, max-age=31536000, immutable` and nothing else under `/static/` differs → one `/static/*` rule;
- `/feed.xml` with its own `Content-Type` → its own rule;
- a directory where one file differs → no wildcard for that directory, per-path rules;
- determinism: `Compact` on the files in any order gives the same rules (shuffle 20 times);
- **property (Review Focus 3):** for every file, `Expand(rules, f.Path)` equals `f.Headers` exactly (canonical names, value order).
Files with `Status` 0 (not captured) or nil `Headers` contribute no rules.

- [ ] **Step 2: Run to verify they fail; implement `compact.go`; run**

Algorithm (keep it simple and exact):
1. Intersection of (name, values) over all captured files → `/*` rule.
2. Remove those from each file's set. Group files by their parent directory chain; for each directory, from deepest to shallowest, if every captured file under it shares a non-empty identical remainder set, emit `<dir>/*` with it and clear those headers from the files under it. Only emit when the directory holds ≥ 2 captured files.
3. Every file with a remaining non-empty set gets a rule at its `Path`.
4. Order: `/*`, then wildcard rules by path, then file rules by path.
`Expand` applies, in that order, every rule whose pattern matches (`/*` all, `/dir/*` prefix `/dir/`, exact path), later rules setting (replacing) a header's values. The property test proves step 2 never assigns a header to a file that didn't have it: when a directory's files disagree, no wildcard.

Run: `go test -count=1 ./...` (with the replace in go.mod).

- [ ] **Step 3: Plugin skeleton, configuration and dispatch**

`deploy.go`: `Plugin` with `Init(host collage.Host) error` reading `collage.PluginConfig(host, p.cfg)`; validating `Target` in {"", "netlify", "cloudflare", "vercel", "github-pages"} (else `fmt.Errorf("elagoht/deploy: unknown target %q; want netlify, cloudflare, vercel or github-pages", t)`); `OnBuildFinished(ctx, ev)`: empty target → `ev.Warn("", "deploy-target", "no target set: nothing is written for a static host")` and return; otherwise check every redirect and header value for control characters once more (`ev.Error(...)` naming `Source` / path) and call the target's writer (Task 6; for now a stub returning nil). Tests in `deploy_test.go`: unknown target fails Init; empty target warns once and writes nothing; a control character in a redirect reaching the hook (construct the event by hand) is an error naming its source.

- [ ] **Step 4: README, .gitignore, commit**

README `# elagoht/deploy`: what it does, the four targets, the table of what each cannot carry (from the spec), configuration, "requires collage v0.52.0". Commit in the new repo (trailer as always; add by name).

---

### Task 6: `elagoht/deploy`: the four writers

**Files (in `~/Desktop/collage-deploy`):**
- Create: `netlify.go` (Netlify and Cloudflare share the format; Cloudflare adds limits and drops 410), `vercel.go`, `ghpages.go`, `writers_test.go`, `testdata/golden/<target>/…`
- Modify: `deploy.go` (dispatch)

**Interfaces:**
- Consumes: `Compact`, `HeaderRule` (Task 5); `BuildFinishedEvent.OutDir/Files/Redirects`.
- Produces: `func writeNetlify(ev, rules, limits)`, `func writeVercel(ev, rules)`, `func writeGitHubPages(ev)` (names free; dispatch from `OnBuildFinished`).

- [ ] **Step 1: Re-verify the host formats**

Before writing, check each host's current documentation (WebFetch): Netlify `_headers`/`_redirects` (status codes, splat/placeholder syntax), Cloudflare Pages `_headers` (rule limit, line length) and `_redirects` (static/dynamic limits, statuses), Vercel `vercel.json` `headers`/`redirects` (`source` syntax `:param`, `:path*`, `permanent` vs `statusCode`), GitHub Pages (no headers; `.nojekyll`). Record the URLs and the facts used in the README's "Host formats" section. Where the docs contradict the spec's table, follow the docs and note it in the report.

- [ ] **Step 2: Write the failing golden tests**

One fixture event (built by hand in `writers_test.go`, OutDir = `t.TempDir()`): files with shared headers, a `/static/*` group, `/feed.xml`, one exception; redirects: literal `/old`→`/new` 301, patterned `/blog/{slug}`→`/posts/{slug}` 308, catch-all `/docs/{rest...}`→`/manual/{rest...}` 301, `/gone` 410. For each target, compare the written files byte for byte with `testdata/golden/<target>/` (support `-update` to regenerate). Plus:
- Cloudflare: 101 distinct header rules → warning naming the rules beyond 100, file holds the first 100 in order (Review Focus 4); 410 → warning, line absent.
- Vercel: an existing `vercel.json` in OutDir → `ev.Error` and the file untouched; 410 → warning.
- Netlify/Cloudflare: a `_headers` or `_redirects` already in OutDir — whether the build wrote it (a document or a mounted file, so it is in `ev.Files`) or it was there before — → `ev.Error` naming the file, which is left untouched (Review Focus 5). The plugin never merges into a user's file.
- GitHub Pages: `.nojekyll` written; `old/index.html` meta-refresh to `/new` with `<link rel="canonical" href="/new">` and `<meta name="robots" content="noindex">`; patterned rules and 410 → one warning each kind; headers → exactly one summary warning `"<n> header names on <m> paths cannot be set on GitHub Pages"`; no `_headers` file.
- Pattern translation (Review Focus 2): no written file contains `{` from a redirect.

- [ ] **Step 3: Implement the writers; run**

- `{name}` → `:name`; `{name...}` → `*` and its `:splat` (Netlify/Cloudflare) / `:name*` (Vercel), in both From and To.
- Netlify/Cloudflare `_headers`: `path\n  Name: value\n` blocks, a blank line between; multiple values → one line each. `_redirects`: `from to status\n`; 410 on Netlify → `from /404.html 410`? — use what Step 1's docs say for "gone" (if Netlify has no 410 form, warn like Cloudflare).
- Vercel `vercel.json`: `{"headers":[{"source":..., "headers":[{"key":..,"value":..}]}], "redirects":[{"source":..,"destination":..,"statusCode":..}]}` with deterministic key order and 2-space indent, `"/*"` → `"/(.*)"` per Vercel's source syntax (verify in Step 1).
- GitHub Pages: write `<from>/index.html` (or `<from>.html` when `from` has an extension) unless a file exists there (then `ev.Error`).

Run: `go vet ./... && go test -count=1 ./...` and staticcheck.

- [ ] **Step 4: Commit**

```bash
git status --short
git add <files by name>
git commit -m "feat: netlify, cloudflare, vercel and github-pages writers"
```

---

### Task 7: collage-redirects becomes a RedirectSource; end to end

**Files:**
- Modify (`~/Desktop/collage-redirects`): `redirects.go` (`newRule` control characters; stop registering `/_redirects`; implement `Redirects() []collage.BuiltRedirect`; `Version()` 0.2.0), `redirects_test.go`, `README.md`, `go.mod` (`replace github.com/Elagoht/collage => ../collage-deploy-core` for development)
- Create (`~/Desktop/collage-deploy`): `e2e_test.go`

- [ ] **Step 1: Failing tests in collage-redirects**

- `newRule` refuses `\r`, `\n`, other control characters in From and To (from Go rules and JSON) — `ErrInvalidRule`.
- After `Init`, no document is registered at `/_redirects` (use the host's document listing or a test app: `app.Documents()`).
- `Redirects()` returns every rule (410 included) with From/To/Status as configured.

- [ ] **Step 2: Implement; run**

Remove `NoRedirectsFile` handling for the document (keep the option field, documented as having no effect, or remove it — removal is fine in a breaking minor; update README). `Redirects()` maps internal rules to `collage.BuiltRedirect` (`To` "" for 410; trailing `/*` kept as is — note: collage's `{rest...}` and this plugin's `/*` differ; translate `/*` to `{rest...}`/`:splat` consistently with the deploy plugin: emit From `"/x/{rest...}"` and To with `:splat` → `{rest...}` so the deploy plugin's translation applies uniformly).

Run: `GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...`

- [ ] **Step 3: End to end in collage-deploy**

`e2e_test.go`: a small collage app (inline fragments, one document, a static mount with a fingerprinted file) with collage-secure (`go get` its latest tag) and collage-redirects (`replace => ../collage-redirects`), a page with `WithRedirect`, built with `collage.NewBuilder` into `t.TempDir()` for each target via `NewWith(Config{Target: t})`; assert the files exist and contain: secure's `Referrer-Policy`, the document's `Content-Type`, the page redirect and the redirects-plugin rule, and that a nonce-bearing CSP (configure secure with `{nonce}`) produced the unstable-header warning and is absent.

- [ ] **Step 4: Commit both repos**

Commit collage-redirects (`feat!: rules reach the static build through RedirectSource; no _redirects document; control characters refused`) and collage-deploy (`test: end to end with collage-secure and collage-redirects`). Do not push or tag.

---

## After this plan

Release, in order (the user authorizes each push/tag at that time):
1. Review the core branch; merge; collage CHANGELOG `## v0.52.0`; push main; main CI green; tag v0.52.0; the tag CI must be green with collage-redirects' **current** tag (it still compiles: it only stops being needed).
2. collage-redirects: drop the `replace`, `go get collage@v0.52.0`, test, commit, push, tag v0.2.0.
3. collage-deploy: drop both `replace`s, `go get` collage v0.52.0 and redirects v0.2.0, test, `gh repo create Elagoht/collage-deploy --public`, push, tag v0.1.0; then add `collage-deploy` to collage's CI matrix (after `collage-cdnpurge` alphabetically) in its own commit and rerun the tag CI.
4. Docs site EN+TR (static-export, plugins page with the new plugin and redirects v0.2.0), the VS Code extension's schema for `elagoht/deploy` (`npm run schema`, release), and the memory/plugin catalog.
