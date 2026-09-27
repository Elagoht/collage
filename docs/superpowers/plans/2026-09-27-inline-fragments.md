# Inline Fragments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `collage.NewInlineFragment(name, html)` — a fragment whose template is a string, behaving exactly as a file template does.

**Architecture:** `Fragment.Source` carries the text; `types.TemplateName(f)` gives every fragment one engine name (its path, or `inline:<name>#<hash>`). The HTML engine gains `AddSource`, which adds a template to a *clone* of the shared set and swaps it in (renders only ever execute clones, so the shared set must never be mutated in place), and `Reload` re-parses kept sources. Registration adds sources in `checkTemplates`' existing visit; the render engine adds a resolver-returned source on first sight.

**Tech Stack:** Go stdlib (`html/template`, `crypto/sha256`); tests with `testing/fstest`, `net/http/httptest`.

**Spec:** `docs/superpowers/specs/2026-09-27-inline-fragments-design.md`

## Global Constraints

- Non-breaking: `NewFragment` and every file-template behaviour unchanged.
- No new `any` in code (project rule).
- A fragment has exactly one of `TemplatePath` and `Source`: both empty → `ErrEmptyTemplatePath`, both set → `ErrConflictingTemplate`.
- Inline template name: `inline:<fragment name>#<hash>`, hash = first 8 bytes of sha256(source), hex.
- `Names()` lists file templates only.
- Verify with `go test -count=1 ./...` (cached results hide the scaffold tests).
- Commits end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

1. **An inline source that `{{define}}`s a name** silently replaces a file partial of that name in the shared set — every page using the partial changes. Expected: refused. (Spec is silent; ruled here: `AddSource` rejects a source that defines any template besides its own. Task 2 pins it.)
2. **Concurrent first render of a resolver-returned inline fragment** mutating the set while other renders clone it — a data race. Task 3 pins it under `-race`.
3. **Dev mode reload dropping inline sources** — every inline fragment 500s in `collage dev`. Task 2 (engine) and Task 3 (HTTP) pin it.
4. **Same fragment name, different sources** — one page renders the other's markup. Task 2 and Task 3 pin it.
5. **Parse error message** naming a synthetic `inline:...#...` string instead of the fragment — Task 3 pins that the registration error names page and fragment.

---

### Task 1: Fragment.Source, TemplateName, NewInlineFragment

**Files:**
- Modify: `internal/types/fragment.go` (field `Source` after `TemplatePath`; `TemplateName`; `Validate` template check)
- Modify: `internal/types/errors.go` (append `ErrConflictingTemplate`)
- Modify: `pkg/collage/fragment.go` (`NewInlineFragment` after `NewFragment`)
- Modify: `pkg/collage/types.go` (re-export `ErrConflictingTemplate`)
- Test: `internal/types/fragment_test.go`, `pkg/collage/fragment_test.go`

**Interfaces:**
- Produces: `Fragment.Source string`; `types.TemplateName(f *Fragment) string`; `types.IsInline(f *Fragment) bool`; `types.ErrConflictingTemplate`; `collage.NewInlineFragment(name, html string) *FragmentBuilder`; `collage.ErrConflictingTemplate`.

- [ ] **Step 1: Write the failing tests**

`internal/types/fragment_test.go` (append; create the file with `package types` + imports `errors`, `strings`, `testing` if absent):

```go
func TestTemplateName(t *testing.T) {
	file := &Fragment{Name: "home", TemplatePath: "pages/home.html"}
	if got := TemplateName(file); got != "pages/home.html" {
		t.Fatalf("TemplateName(file) = %q", got)
	}
	a := &Fragment{Name: "row", Source: "<tr>a</tr>"}
	b := &Fragment{Name: "row", Source: "<tr>b</tr>"}
	same := &Fragment{Name: "row", Source: "<tr>a</tr>"}
	if !strings.HasPrefix(TemplateName(a), "inline:row#") {
		t.Fatalf("TemplateName(inline) = %q, want inline:row#<hash>", TemplateName(a))
	}
	if TemplateName(a) == TemplateName(b) {
		t.Fatal("two sources under one fragment name share a template name")
	}
	if TemplateName(a) != TemplateName(same) {
		t.Fatal("one source under one name got two template names")
	}
}

func TestValidateTemplateSource(t *testing.T) {
	if err := (&Fragment{Name: "x"}).Validate(); !errors.Is(err, ErrEmptyTemplatePath) {
		t.Fatalf("no path, no source: %v, want ErrEmptyTemplatePath", err)
	}
	if err := (&Fragment{Name: "x", TemplatePath: "a.html", Source: "<p/>"}).Validate(); !errors.Is(err, ErrConflictingTemplate) {
		t.Fatalf("path and source: %v, want ErrConflictingTemplate", err)
	}
	if err := (&Fragment{Name: "x", Source: "<p/>"}).Validate(); err != nil {
		t.Fatalf("source only: %v, want nil", err)
	}
}
```

`pkg/collage/fragment_test.go` (append):

```go
func TestNewInlineFragment(t *testing.T) {
	b := NewInlineFragment("row", "<tr>{{.}}</tr>")
	f := b.Build()
	if err := b.BuildErr(); err != nil {
		t.Fatalf("BuildErr = %v", err)
	}
	if f.Source != "<tr>{{.}}</tr>" || f.TemplatePath != "" {
		t.Fatalf("fragment = %+v, want the source and no path", f)
	}
	empty := NewInlineFragment("row", "")
	empty.Build()
	if err := empty.BuildErr(); !errors.Is(err, ErrEmptyTemplatePath) {
		t.Fatalf("empty source: %v, want ErrEmptyTemplatePath", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/types/ ./pkg/collage/ -run 'TestTemplateName|TestValidateTemplateSource|TestNewInlineFragment'`
Expected: build failure — `Source`, `TemplateName`, `ErrConflictingTemplate`, `NewInlineFragment` undefined.

- [ ] **Step 3: Implement**

`internal/types/fragment.go`, after `TemplatePath`:

```go
	// Source, when set, is the fragment's template itself rather than the path
	// to a file holding it: an inline fragment. A fragment has exactly one of
	// TemplatePath and Source. See TemplateName for how the engine addresses it.
	Source string
```

and, near `Slot`:

```go
// IsInline reports whether f carries its template as Source rather than naming a
// file. It is nil-safe.
func IsInline(f *Fragment) bool { return f != nil && f.Source != "" }

// TemplateName is the name the template engine knows f's template by: its
// TemplatePath, or for an inline fragment "inline:<name>#<hash>". The hash is of
// the source, so two fragments that share a name but not a template never collide,
// and one template used by two fragment values is parsed once. It is nil-safe.
func TemplateName(f *Fragment) string {
	if f == nil {
		return ""
	}
	if f.Source == "" {
		return f.TemplatePath
	}
	sum := sha256.Sum256([]byte(f.Source))
	return "inline:" + f.Name + "#" + hex.EncodeToString(sum[:8])
}
```

(imports: `crypto/sha256`, `encoding/hex`).

In `validate`, replace

```go
	if f.TemplatePath == "" {
		return ErrEmptyTemplatePath
	}
```

with

```go
	switch {
	case f.TemplatePath == "" && f.Source == "":
		return ErrEmptyTemplatePath
	case f.TemplatePath != "" && f.Source != "":
		return fmt.Errorf("%w: fragment %q", ErrConflictingTemplate, f.Name)
	}
```

`internal/types/errors.go`, after `ErrEmptyTemplatePath`:

```go
// ErrConflictingTemplate is returned when a fragment names both a template file
// and an inline template source. Which one renders is not something a reader of
// the fragment could tell, so neither is chosen.
var ErrConflictingTemplate = errors.New("collage: fragment has both a template path and an inline template")
```

`pkg/collage/fragment.go`, after `NewFragment`:

```go
// NewInlineFragment starts a FragmentBuilder for a fragment named name whose
// template is html itself rather than a file — for the small parts of a page, a
// table row or a button, whose markup reads best next to the handler that feeds
// it:
//
//	row := collage.NewInlineFragment("post-row", `
//	  <tr><td>{{.Title}}</td><td>{{.Date}}</td></tr>`).
//		WithDataHandler(loadRow).
//		Build()
//
// It renders exactly as a file template does: slots, hoist, every template
// function, and {{template}} calls into the template directory's partials.
// Registration parses it and checks it like one. A Go raw string cannot hold a
// backtick, so a template with a JavaScript template literal stays in a file.
// An empty html records ErrEmptyTemplatePath, retrievable via BuildErr.
func NewInlineFragment(name, html string) *FragmentBuilder {
	b := &FragmentBuilder{
		fragment: &Fragment{
			Name:   name,
			Source: html,
			Slots:  make(map[string]*SlotDefinition),
		},
	}
	if html == "" {
		b.errs = append(b.errs, fmt.Errorf("%w: inline fragment %q has no template", ErrEmptyTemplatePath, name))
	}
	return b
}
```

`pkg/collage/types.go`, after `ErrEmptyTemplatePath`:

```go
// ErrConflictingTemplate is returned when a fragment names both a template file
// and an inline template.
var ErrConflictingTemplate = types.ErrConflictingTemplate
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 ./internal/types/ ./pkg/collage/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types pkg/collage/fragment.go pkg/collage/fragment_test.go pkg/collage/types.go
git commit -m "feat: Fragment.Source, TemplateName and NewInlineFragment"
```

---

### Task 2: Engine.AddSource

**Files:**
- Modify: `internal/template/engine.go` (interface method + `ErrSourceConflict`)
- Modify: `internal/template/html.go` (`sources` field, `AddSource`, `Reload` re-parse)
- Test: `internal/template/html_test.go`

**Interfaces:**
- Produces: `Engine.AddSource(name, src string) error`; `template.ErrSourceConflict`.

- [ ] **Step 1: Write the failing tests** (append to `html_test.go`)

```go
func inlineEngine(t *testing.T, dev bool) *HTMLEngine {
	t.Helper()
	engine, err := NewHTML(HTMLConfig{
		FS:        fstest.MapFS{"partials/name.html": {Data: []byte(`<b>{{.}}</b>`)}},
		Extension: ".html",
		DevMode:   dev,
	})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	return engine
}

func renderString(t *testing.T, e *HTMLEngine, name string, data string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := e.Render(context.Background(), &buf, name, data); err != nil {
		t.Fatalf("Render(%q): %v", name, err)
	}
	return buf.String()
}

func TestAddSource_RendersAndCallsAPartial(t *testing.T) {
	e := inlineEngine(t, false)
	if err := e.AddSource("inline:row#1", `<tr>{{template "partials/name.html" .}}</tr>`); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	if got := renderString(t, e, "inline:row#1", "Ada"); got != "<tr><b>Ada</b></tr>" {
		t.Fatalf("render = %q", got)
	}
	for _, n := range e.Names() {
		if n == "inline:row#1" {
			t.Fatal("Names() lists an inline source; it describes the template directory")
		}
	}
}

func TestAddSource_SurvivesReload(t *testing.T) {
	e := inlineEngine(t, true) // dev mode reloads before every render
	if err := e.AddSource("inline:row#1", `<tr>{{.}}</tr>`); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	for i := 0; i < 2; i++ {
		if got := renderString(t, e, "inline:row#1", "x"); got != "<tr>x</tr>" {
			t.Fatalf("render %d after reload = %q", i, got)
		}
	}
}

func TestAddSource_SameNameSameSourceIsANoOp(t *testing.T) {
	e := inlineEngine(t, false)
	for i := 0; i < 2; i++ {
		if err := e.AddSource("inline:row#1", `<tr/>`); err != nil {
			t.Fatalf("AddSource %d: %v", i, err)
		}
	}
	if err := e.AddSource("inline:row#1", `<td/>`); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("different source under one name: %v, want ErrSourceConflict", err)
	}
}

func TestAddSource_ParseError(t *testing.T) {
	e := inlineEngine(t, false)
	if err := e.AddSource("inline:row#1", `{{.Broken`); err == nil {
		t.Fatal("a malformed source was accepted")
	}
	if e.Lookup("inline:row#1") {
		t.Fatal("a source that failed to parse is in the set")
	}
}

// A source defining a template of its own would replace a file partial of that
// name for every page in the application.
func TestAddSource_RefusesDefine(t *testing.T) {
	e := inlineEngine(t, false)
	err := e.AddSource("inline:row#1", `{{define "partials/name.html"}}hijacked{{end}}<tr/>`)
	if !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("define in an inline source: %v, want ErrSourceConflict", err)
	}
	if got := renderString(t, e, "partials/name.html", "Ada"); got != "<b>Ada</b>" {
		t.Fatalf("partial after refused define = %q, want it untouched", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/template/ -run TestAddSource`
Expected: build failure — `AddSource`, `ErrSourceConflict` undefined.

- [ ] **Step 3: Implement**

`engine.go` — add to `Engine`:

```go
	// AddSource adds a template given as text rather than as a file, under name,
	// to the set every render draws from, and keeps it so Reload does not drop
	// it. The same name and source again is a no-op; the same name with another
	// source, or a source that defines a template besides its own, is
	// ErrSourceConflict.
	AddSource(name, src string) error
```

and the sentinel:

```go
// ErrSourceConflict is returned by AddSource for a name already holding another
// source, and for a source that {{define}}s or {{block}}s a template of its own —
// which would replace a file template of that name for every page.
var ErrSourceConflict = errors.New("collage: inline template conflicts with another template")
```

`html.go` — field on `HTMLEngine`, after `names`:

```go
	// sources are the templates added as text with AddSource, by name, re-parsed
	// by every Reload so a dev-mode reload does not lose them.
	sources map[string]string
```

Method:

```go
// AddSource parses src into a clone of the current set under name and swaps the
// clone in. It never touches the shared set in place: every render executes a
// clone of it, and adding to the original while a render clones it is a data race.
func (e *HTMLEngine) AddSource(name, src string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.sources[name]; ok {
		if existing == src {
			return nil
		}
		return fmt.Errorf("%w: %s already holds another template", ErrSourceConflict, name)
	}
	next, err := e.tmpl.Clone()
	if err != nil {
		return fmt.Errorf("collage: clone template set: %w", err)
	}
	if err := addSource(next, name, src); err != nil {
		return err
	}
	if e.sources == nil {
		e.sources = make(map[string]string)
	}
	e.sources[name] = src
	e.tmpl = next
	return nil
}

// addSource parses src into set under name, refusing a source that defines any
// template besides its own. Comparing tree pointers, not counting templates,
// catches a {{define}} that replaces an existing template as well as one that
// adds a new name. Callers pass a clone, so a refused source leaves the live set
// untouched.
func addSource(set *template.Template, name, src string) error {
	trees := make(map[string]*parse.Tree)
	for _, t := range set.Templates() {
		trees[t.Name()] = t.Tree
	}
	if _, err := set.New(name).Parse(src); err != nil {
		return fmt.Errorf("collage: parse inline template %s: %w", name, err)
	}
	for _, t := range set.Templates() {
		if t.Name() == name {
			continue
		}
		if old, ok := trees[t.Name()]; !ok || old != t.Tree {
			return fmt.Errorf("%w: %s defines template %q; move it to a file", ErrSourceConflict, name, t.Name())
		}
	}
	return nil
}
```

(import `text/template/parse`, already used in `slots.go`.)

In `Reload`, after the walk and before the lock/swap:

```go
	e.mu.RLock()
	sources := maps.Clone(e.sources)
	e.mu.RUnlock()
	for name, src := range sources {
		if err := addSource(set, name, src); err != nil {
			return err
		}
	}
```

(`names` stays file-only.)

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 -race ./internal/template/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/template
git commit -m "feat: template engine AddSource, kept across reloads"
```

---

### Task 3: Registration, render and inspect

**Files:**
- Modify: `internal/core/registry.go` (`checkTemplates` visit)
- Modify: `internal/render/fragment.go:304-308` (engine name, first-sight add, message)
- Modify: `internal/core/inspect.go` (`InspectedFragment.Inline`, `Template`)
- Test: `pkg/collage/inline_test.go` (create)

**Interfaces:**
- Consumes: `types.TemplateName`, `types.IsInline`, `Engine.AddSource`.
- Produces: `InspectedFragment.Inline bool` (`json:"inline,omitempty"`).

- [ ] **Step 1: Write the failing tests** — `pkg/collage/inline_test.go`:

```go
package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

func inlineApp(t *testing.T, dev bool) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:  collage.ServerConfig{Host: "localhost", Port: 0},
		DevMode: dev,
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"templates/layouts/default.html": {Data: []byte(`<html><head>{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`)},
				"templates/partials/name.html":   {Data: []byte(`<b>{{.}}</b>`)},
			},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func body(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestInlineFragment_RendersInAFileLayout(t *testing.T) {
	app := inlineApp(t, false)
	row := collage.NewInlineFragment("row", `{{stylesheet "/static/row.css"}}<p>{{template "partials/name.html" .}}</p>`).
		WithData("Ada").Build()
	layout := collage.NewFragment("layout", "layouts/default.html").Build()
	if err := app.RegisterPage(collage.NewPage("home").WithLayouts(layout).WithContent(row).WithPath("en", "/").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	got := body(t, app.Handler(), "/")
	head, _, _ := strings.Cut(got, "</head>")
	if !strings.Contains(got, "<p><b>Ada</b></p>") || !strings.Contains(head, "/static/row.css") {
		t.Fatalf("body = %q, want the partial rendered and the stylesheet hoisted into the head", got)
	}
}
```

(`{{stylesheet}}` hoists a `<link>` into the layout's `{{hoist "head"}}` marker; if it resolves the URL through a mount, register one or use the documented fixed-URL form from `docs/fragments.md` "Hoisting".) Then add:

```go
func TestInlineFragment_WithASlot(t *testing.T) {
	app := inlineApp(t, false)
	child := collage.NewInlineFragment("child", `<i>c</i>`).Build()
	parent := collage.NewInlineFragment("parent", `<div>{{slot "items"}}</div>`).
		WithSlotFragment("items", child).Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(parent).WithPath("en", "/p").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if got := body(t, app.Handler(), "/p"); got != "<div><i>c</i></div>" {
		t.Fatalf("body = %q", got)
	}
}

func TestInlineFragment_SameNameDifferentSources(t *testing.T) {
	app := inlineApp(t, false)
	for _, page := range []string{"a", "b"} {
		frag := collage.NewInlineFragment("card", "<p>"+page+"</p>").Build()
		if err := app.RegisterPage(collage.NewPage(page).WithContent(frag).WithPath("en", "/"+page).Build()); err != nil {
			t.Fatalf("RegisterPage(%s): %v", page, err)
		}
	}
	if a, b := body(t, app.Handler(), "/a"), body(t, app.Handler(), "/b"); a != "<p>a</p>" || b != "<p>b</p>" {
		t.Fatalf("a = %q, b = %q", a, b)
	}
}

func TestInlineFragment_DevModeReloads(t *testing.T) {
	app := inlineApp(t, true)
	frag := collage.NewInlineFragment("x", `<p>x</p>`).Build()
	if err := app.RegisterPage(collage.NewPage("x").WithContent(frag).WithPath("en", "/x").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	for i := 0; i < 2; i++ {
		if got := body(t, app.Handler(), "/x"); !strings.Contains(got, "<p>x</p>") {
			t.Fatalf("dev render %d = %q", i, got)
		}
	}
}

func TestInlineFragment_ParseErrorNamesPageAndFragment(t *testing.T) {
	app := inlineApp(t, false)
	frag := collage.NewInlineFragment("broken-row", `{{.Nope`).Build()
	err := app.RegisterPage(collage.NewPage("list").WithContent(frag).WithPath("en", "/l").Build())
	if err == nil || !strings.Contains(err.Error(), `page "list"`) || !strings.Contains(err.Error(), `fragment "broken-row"`) {
		t.Fatalf("RegisterPage = %v, want an error naming page and fragment", err)
	}
}

func TestInlineFragment_UnknownSlot(t *testing.T) {
	app := inlineApp(t, false)
	frag := collage.NewInlineFragment("p", `<p/>`).
		WithSlotFragment("never", collage.NewInlineFragment("c", `c`).Build()).Build()
	err := app.RegisterPage(collage.NewPage("p").WithContent(frag).WithPath("en", "/p").Build())
	if !errors.Is(err, collage.ErrUnknownSlot) {
		t.Fatalf("RegisterPage = %v, want ErrUnknownSlot", err)
	}
}

func TestInlineFragment_ConflictingTemplate(t *testing.T) {
	app := inlineApp(t, false)
	frag := &collage.Fragment{Name: "both", TemplatePath: "partials/name.html", Source: "<p/>"}
	err := app.RegisterPage(collage.NewPage("p").WithContent(frag).WithPath("en", "/p").Build())
	if !errors.Is(err, collage.ErrConflictingTemplate) {
		t.Fatalf("RegisterPage = %v, want ErrConflictingTemplate", err)
	}
}

// A resolver's fragments are not known at registration; an inline one is added
// on first sight. Rendered from many goroutines at once, under -race.
func TestInlineFragment_FromAResolver(t *testing.T) {
	app := inlineApp(t, false)
	item := collage.NewInlineFragment("item", `<li>i</li>`).Build()
	list := collage.NewInlineFragment("list", `<ul>{{slot "items"}}</ul>`).
		WithSlotResolver("items", func(*collage.RenderContext) ([]*collage.Fragment, error) {
			return []*collage.Fragment{item}, nil
		}).Build()
	if err := app.RegisterPage(collage.NewPage("l").WithContent(list).WithPath("en", "/l").Dynamic().Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	h := app.Handler()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/l", nil))
			if w.Body.String() != "<ul><li>i</li></ul>" {
				t.Errorf("body = %q", w.Body.String())
			}
		}()
	}
	wg.Wait()
}

func TestInlineFragment_StaticBuildAndFragmentPath(t *testing.T) {
	app := inlineApp(t, false)
	results := collage.NewInlineFragment("results", `<ol>r</ol>`).Build()
	page := collage.NewPage("search").
		WithContent(collage.NewInlineFragment("search", `<form></form>`).Build()).
		WithPath("en", "/search").
		WithFragmentPath("en", "/search/results", results).
		Static().Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if got := body(t, app.Handler(), "/search/results"); got != "<ol>r</ol>" {
		t.Fatalf("fragment path body = %q", got)
	}
	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(out, "search", "index.html"))
	if err != nil || !strings.Contains(string(written), "<form></form>") {
		t.Fatalf("built page = %q, %v", written, err)
	}
}

func TestInspect_InlineFragment(t *testing.T) {
	app := inlineApp(t, false)
	if err := app.RegisterPage(collage.NewPage("p").WithContent(collage.NewInlineFragment("card", `<p/>`).Build()).
		WithPath("en", "/p").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	for _, f := range app.Inspect().Fragments {
		if f.Name == "card" {
			if !f.Inline || f.Template != "" {
				t.Fatalf("inspected = %+v, want inline and no template path", f)
			}
			return
		}
	}
	t.Fatal("inline fragment not inspected")
}
```

`collage.Config.DevMode` is the verified field name. `ErrSourceConflict` is internal to `internal/template`; re-export it in this task from `pkg/collage/errors.go`, beside `ErrTemplateEscapesRoot`:

```go
// ErrSourceConflict is returned at registration for an inline template that
// defines a template of its own, which would replace a file template of that name.
var ErrSourceConflict = template.ErrSourceConflict
```

and add to `inline_test.go`:

```go
func TestInlineFragment_DefineRefused(t *testing.T) {
	app := inlineApp(t, false)
	frag := collage.NewInlineFragment("sneaky", `{{define "partials/name.html"}}x{{end}}<p/>`).Build()
	err := app.RegisterPage(collage.NewPage("p").WithContent(frag).WithPath("en", "/p").Build())
	if !errors.Is(err, collage.ErrSourceConflict) {
		t.Fatalf("RegisterPage = %v, want ErrSourceConflict", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./pkg/collage/ -run 'TestInlineFragment|TestInspect_InlineFragment'`
Expected: FAIL — registration reports `ErrTemplateNotFound` for every inline fragment (the engine was never given the source); inspect lacks `Inline`.

- [ ] **Step 3: Implement**

`internal/core/registry.go`, `checkTemplates`' `visit`, at its start:

```go
		name := types.TemplateName(f)
		if types.IsInline(f) {
			if err := a.tmpl.AddSource(name, f.Source); err != nil {
				return fmt.Errorf("collage: page %q: inline template of fragment %q: %w", p.Name, f.Name, err)
			}
		}
		if !a.tmpl.Lookup(name) {
```

and replace the remaining `f.TemplatePath` uses in `visit` with `name` (Lookup message, `SlotCalls` argument, `ErrUnknownSlot` message). Where a message prints the template, print `describeTemplate(f)`:

```go
// describeTemplate names f's template for a person: its path, or for an inline
// fragment what it is, since the engine's synthetic name means nothing to anyone.
func describeTemplate(f *types.Fragment) string {
	if types.IsInline(f) {
		return fmt.Sprintf("the inline template of fragment %q", f.Name)
	}
	return fmt.Sprintf("%q", f.TemplatePath)
}
```

(adjust the existing `%q` verbs that took `f.TemplatePath` to `%s` with `describeTemplate(f)`).

A fragment's `Validate` runs in `p.Validate()` *before* `checkTemplates` in `prepare`, so `ErrConflictingTemplate` is reported before `AddSource` would see a two-template fragment. Confirm by reading `prepare`; keep that order.

`internal/render/fragment.go` around line 304:

```go
	name := types.TemplateName(f)
	if types.IsInline(f) && !e.tmpl.Lookup(name) {
		// A fragment a slot resolver returned is not seen at registration; its
		// source reaches the engine here, the first time it renders.
		if err := e.tmpl.AddSource(name, f.Source); err != nil {
			return nil, wrapFragment("inline template", f.Name, err)
		}
	}
	err = Execute(rc.Context(), 0, func(ctx context.Context) error {
		return e.tmpl.RenderWithFuncs(ctx, &buf, name, data, e.slotFuncs(rc, f, state, started, fills))
	})
	...
	if err != nil {
		label := "template " + f.TemplatePath
		if types.IsInline(f) {
			label = "inline template"
		}
		return nil, wrapFragment(label, f.Name, err)
	}
```

(Read the surrounding function first; place the add before the `renderStarted` timer so parse time is not billed as template time, and keep the variable names the function already uses.)

`internal/core/inspect.go`: `InspectedFragment` gets

```go
	// Inline reports a template given as text with NewInlineFragment; Template
	// is then empty.
	Inline bool `json:"inline,omitempty"`
```

and the constructor sets `Inline: types.IsInline(f)` (Template stays `f.TemplatePath`, empty for inline).

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 -race ./...`
Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
git add internal/core internal/render pkg/collage/inline_test.go
git commit -m "feat: inline fragments register, render and inspect"
```

---

### Task 4: Docs and changelog

**Files:**
- Modify: `docs/fragments.md` (new "Inline templates" section after "Building a fragment")
- Modify: `CHANGELOG.md` (new `## Unreleased` at top)

- [ ] **Step 1: fragments.md**

```markdown
### Inline templates

A small fragment can carry its template itself instead of naming a file:

```go
row := collage.NewInlineFragment("post-row", `
  <tr>
    <td>{{.Title}}</td>
    <td>{{template "partials/date.html" .Date}}</td>
  </tr>`).
	WithDataHandler(loadRow).
	Build()
```

It renders as a file template does — slots, `hoist`, every template function,
`{{template}}` calls into the template directory — and registration parses and
checks it like one: a parse error or a slot it never calls stops startup, naming
the page and the fragment.

Use it for the parts of a page that are a few lines of markup next to the handler
that feeds them. Layouts and whole pages read better as files. Two limits come
from it being a Go string: a raw string cannot hold a backtick, so a template with
a JavaScript template literal stays in a file; and an inline template cannot
`{{define}}` templates of its own (`ErrSourceConflict` at registration), because a
definition would replace a file template of that name for every page. A file
template cannot `{{template}}` an inline one either — its name in the template set
is internal.
```

- [ ] **Step 2: CHANGELOG**

```markdown
## Unreleased

### Added

- **`NewInlineFragment(name, html)`**: a fragment whose template is a string
  rather than a file, rendered and checked at registration exactly like a file
  template — slots, hoist, template functions, `{{template}}` calls into partials.
  An inline template cannot `{{define}}` templates of its own. `collage inspect`
  reports `inline: true` for one. `ErrConflictingTemplate` refuses a fragment
  naming both a file and an inline template.
```

- [ ] **Step 3: Verify and commit**

Run: `go test -count=1 ./... && go vet ./...`

```bash
git add docs/fragments.md CHANGELOG.md
git commit -m "docs: inline templates"
```
