package template

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
)

func TestNewHTML_LoadsAndNamesTemplates(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	want := []string{"layouts/default.html", "pages/home.html", "partial.html", "slot.html"}
	got := engine.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], name)
		}
	}

	for _, name := range want {
		if !engine.Lookup(name) {
			t.Errorf("Lookup(%q) = false, want true", name)
		}
	}
	if engine.Lookup("does/not-exist.html") {
		t.Error("Lookup(\"does/not-exist.html\") = true, want false")
	}
}

func TestNewHTML_RootMissing(t *testing.T) {
	_, err := NewHTML(HTMLConfig{Root: "testdata/does-not-exist", Extension: ".html"})
	if !errors.Is(err, ErrTemplateRootMissing) {
		t.Fatalf("NewHTML() error = %v, want ErrTemplateRootMissing", err)
	}
}

func TestNewHTML_ParseErrorAtConstruction(t *testing.T) {
	_, err := NewHTML(HTMLConfig{Root: "testdata/syntaxerror", Extension: ".html"})
	if err == nil {
		t.Fatal("NewHTML() error = nil, want a parse error")
	}
}

func TestHTMLEngine_Render_WithData(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	var buf bytes.Buffer
	data := struct{ Heading string }{Heading: "Hello"}
	if err := engine.Render(context.Background(), &buf, "pages/home.html", data); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := "<h1>Hello</h1>\n"
	if buf.String() != want {
		t.Errorf("Render() output = %q, want %q", buf.String(), want)
	}
}

func TestHTMLEngine_Render_UnknownTemplate(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	var buf bytes.Buffer
	err = engine.Render(context.Background(), &buf, "does/not-exist.html", nil)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("Render() error = %v, want ErrTemplateNotFound", err)
	}
}

func TestHTMLEngine_Render_ContextAlreadyCancelled(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	err = engine.Render(ctx, &buf, "pages/home.html", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Render() error = %v, want context.Canceled", err)
	}
	if buf.Len() != 0 {
		t.Errorf("buffer = %q, want empty", buf.String())
	}
}

func TestHTMLEngine_Render_PartialOutputNotWritten(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	type partialData struct {
		Name string
	}

	var buf bytes.Buffer
	err = engine.Render(context.Background(), &buf, "partial.html", partialData{Name: "x"})
	if err == nil {
		t.Fatal("Render() error = nil, want an execution error from the missing field")
	}
	if buf.Len() != 0 {
		t.Errorf("buffer = %q, want empty: a failed render must not emit partial output", buf.String())
	}
}

func TestHTMLEngine_DevMode_ReloadsBeforeEachRender(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "page.html"), "v1")

	engine, err := NewHTML(HTMLConfig{Root: root, Extension: ".html", DevMode: true})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	var buf1 bytes.Buffer
	if err := engine.Render(context.Background(), &buf1, "page.html", nil); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if buf1.String() != "v1" {
		t.Fatalf("Render() output = %q, want %q", buf1.String(), "v1")
	}

	writeFixture(t, filepath.Join(root, "page.html"), "v2")

	var buf2 bytes.Buffer
	if err := engine.Render(context.Background(), &buf2, "page.html", nil); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if buf2.String() != "v2" {
		t.Fatalf("DevMode Render() output = %q, want %q (rewritten file should be picked up)", buf2.String(), "v2")
	}
}

func TestHTMLEngine_NonDevMode_DoesNotReloadUntilExplicit(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "page.html"), "v1")

	engine, err := NewHTML(HTMLConfig{Root: root, Extension: ".html", DevMode: false})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	writeFixture(t, filepath.Join(root, "page.html"), "v2")

	var buf1 bytes.Buffer
	if err := engine.Render(context.Background(), &buf1, "page.html", nil); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if buf1.String() != "v1" {
		t.Fatalf("non-DevMode Render() output = %q, want %q (must not pick up the rewrite yet)", buf1.String(), "v1")
	}

	if err := engine.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	var buf2 bytes.Buffer
	if err := engine.Render(context.Background(), &buf2, "page.html", nil); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if buf2.String() != "v2" {
		t.Fatalf("Render() after explicit Reload() output = %q, want %q", buf2.String(), "v2")
	}
}

func TestHTMLEngine_SymlinkInsideRootIsLoaded(t *testing.T) {
	// The containment check must refuse only what leaves the root. A symlink whose
	// target is another file *inside* the root is legitimate — a shared partial
	// linked into two trees, a checked-out theme — and must still load. This is the
	// regression guard for a containment fix that over-corrects into rejecting every
	// symlink rather than every escaping one.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "shared"), 0o755); err != nil {
		t.Fatalf("Mkdir(shared): %v", err)
	}
	writeFixture(t, filepath.Join(root, "shared", "banner.html"), "INSIDE")

	link := filepath.Join(root, "linked.html")
	if err := os.Symlink(filepath.Join("shared", "banner.html"), link); err != nil {
		t.Skipf("symlink creation not supported on this platform: %v", err)
	}

	engine, err := NewHTML(HTMLConfig{Root: root, Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v, want success (linked.html resolves inside root)", err)
	}

	var buf bytes.Buffer
	if err := engine.Render(context.Background(), &buf, "linked.html", nil); err != nil {
		t.Fatalf("Render(linked.html) error = %v", err)
	}
	if buf.String() != "INSIDE" {
		t.Errorf("Render(linked.html) = %q, want %q", buf.String(), "INSIDE")
	}
}

func TestHTMLEngine_RootEscape_SymlinkedFile(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir(root): %v", err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatalf("Mkdir(outside): %v", err)
	}
	writeFixture(t, filepath.Join(outside, "secret.html"), "TOP SECRET")

	link := filepath.Join(root, "leak.html")
	if err := os.Symlink(filepath.Join(outside, "secret.html"), link); err != nil {
		t.Skipf("symlink creation not supported on this platform: %v", err)
	}

	_, err := NewHTML(HTMLConfig{Root: root, Extension: ".html"})
	if !errors.Is(err, ErrTemplateEscapesRoot) {
		t.Fatalf("NewHTML() error = %v, want ErrTemplateEscapesRoot (leak.html is lexically inside root but resolves outside it)", err)
	}
}

func TestHTMLEngine_ConfigFuncsOverrideDefaults(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "shout.html"), `{{upper .Word}}`)

	engine, err := NewHTML(HTMLConfig{
		Root:      root,
		Extension: ".html",
		Funcs: template.FuncMap{
			"upper": func(string) string { return "OVERRIDDEN" },
		},
	})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	var buf bytes.Buffer
	if err := engine.Render(context.Background(), &buf, "shout.html", struct{ Word string }{Word: "hi"}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if buf.String() != "OVERRIDDEN" {
		t.Errorf("Render() output = %q, want %q (HTMLConfig.Funcs must override DefaultFuncs)", buf.String(), "OVERRIDDEN")
	}
}

func TestHTMLEngine_ConcurrentRenders(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			var buf bytes.Buffer
			_ = engine.Render(context.Background(), &buf, "pages/home.html", struct{ Heading string }{Heading: "x"})
		})
		wg.Go(func() {
			if err := engine.Reload(); err != nil {
				t.Errorf("Reload() error = %v", err)
			}
		})
	}
	wg.Wait()
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

// embeddedTemplates is a real embed.FS rather than an fstest.MapFS because embed is
// the mode that exists to be supported: a binary that carries its templates and runs
// from any working directory. embed.FS also has quirks a map cannot reproduce — zero
// ModTime, no symlinks, its own directory synthesis — so testing against it is what
// proves the loader works for the case it was added for.
//
//go:embed testdata/valid
var embeddedTemplates embed.FS

func TestNewHTML_EmbeddedFS_RootIsSubPath(t *testing.T) {
	engine, err := NewHTML(HTMLConfig{FS: embeddedTemplates, Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	want := []string{"layouts/default.html", "pages/home.html", "partial.html", "slot.html"}
	got := engine.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("Names()[%d] = %q, want %q (Root must be stripped from template names)", i, got[i], name)
		}
	}
}

func TestNewHTML_EmbeddedFS_RendersWithoutWorkingDirectory(t *testing.T) {
	// The whole point of the FS mode: no relative path is resolved against the
	// process working directory, so a chdir cannot break template loading.
	t.Chdir(t.TempDir())

	engine, err := NewHTML(HTMLConfig{FS: embeddedTemplates, Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	var buf bytes.Buffer
	data := struct{ Heading string }{Heading: "Hello"}
	if err := engine.Render(context.Background(), &buf, "pages/home.html", data); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if want := "<h1>Hello</h1>\n"; buf.String() != want {
		t.Errorf("Render() output = %q, want %q", buf.String(), want)
	}
}

func TestNewHTML_FS_EmptyRootMeansFSRoot(t *testing.T) {
	fsys := fstest.MapFS{
		"pages/home.html": &fstest.MapFile{Data: []byte("<h1>{{.Heading}}</h1>\n")},
		"notes.md":        &fstest.MapFile{Data: []byte("ignored")},
	}

	engine, err := NewHTML(HTMLConfig{FS: fsys, Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}

	got := engine.Names()
	if len(got) != 1 || got[0] != "pages/home.html" {
		t.Fatalf("Names() = %v, want [pages/home.html] (empty Root means the FS root; .md is not the extension)", got)
	}
}

func TestNewHTML_FS_MissingRootSubPath(t *testing.T) {
	fsys := fstest.MapFS{
		"pages/home.html": &fstest.MapFile{Data: []byte("<h1>hi</h1>")},
	}

	_, err := NewHTML(HTMLConfig{FS: fsys, Root: "nonexistent", Extension: ".html"})
	if !errors.Is(err, ErrTemplateRootMissing) {
		t.Fatalf("NewHTML() error = %v, want ErrTemplateRootMissing", err)
	}
}

func TestNewHTML_FS_TakesPrecedenceOverDiskPath(t *testing.T) {
	// Root is a path *within* FS when FS is set, never a disk path. A Root that
	// happens to name a real directory on disk must not be read from disk.
	fsys := fstest.MapFS{
		"testdata/valid/only.html": &fstest.MapFile{Data: []byte("FROM FS")},
	}

	engine, err := NewHTML(HTMLConfig{FS: fsys, Root: "testdata/valid", Extension: ".html"})
	if err != nil {
		t.Fatalf("NewHTML() error = %v", err)
	}
	got := engine.Names()
	if len(got) != 1 || got[0] != "only.html" {
		t.Fatalf("Names() = %v, want [only.html] (disk testdata/valid must not be consulted)", got)
	}
}

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

// A Reload that snapshotted the sources before an AddSource and swapped its set
// in after it must not drop the new source: dev mode reloads on every render, and
// a resolver adds inline sources while other requests render.
func TestAddSource_NotLostToAConcurrentReload(t *testing.T) {
	e := inlineEngine(t, false)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if err := e.Reload(); err != nil {
						t.Errorf("Reload: %v", err)
						return
					}
				}
			}
		}()
	}
	lost := 0
	for i := 0; i < 300; i++ {
		name := fmt.Sprintf("inline:row#%d", i)
		if err := e.AddSource(name, `<tr/>`); err != nil {
			t.Fatalf("AddSource: %v", err)
		}
		if !e.Lookup(name) {
			lost++
		}
	}
	close(stop)
	wg.Wait()
	if lost > 0 {
		t.Fatalf("%d of 300 sources were missing right after AddSource", lost)
	}
}

// Adding a source whose name is kept but absent from the live set re-adds it.
func TestAddSource_ReAddsWhenTheLiveSetLacksIt(t *testing.T) {
	e := inlineEngine(t, false)
	if err := e.AddSource("inline:row#1", `<tr/>`); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	// Simulate a stale swap: a set without the source goes live.
	e.mu.Lock()
	stale, err := NewHTML(HTMLConfig{FS: fstest.MapFS{"partials/name.html": {Data: []byte(`<b>{{.}}</b>`)}}, Extension: ".html"})
	if err != nil {
		e.mu.Unlock()
		t.Fatalf("NewHTML: %v", err)
	}
	e.tmpl = stale.tmpl
	e.mu.Unlock()
	if err := e.AddSource("inline:row#1", `<tr/>`); err != nil {
		t.Fatalf("AddSource again: %v", err)
	}
	if !e.Lookup("inline:row#1") {
		t.Fatal("AddSource of a kept source missing from the live set left it missing")
	}
}
