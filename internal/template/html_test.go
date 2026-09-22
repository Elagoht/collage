package template

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"os"
	"path/filepath"
	"sync"
	"testing"
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

func TestHTMLEngine_RootEscape(t *testing.T) {
	// filepath.WalkDir never yields a path outside the root it was given (it does not
	// follow symlinked directories), so this boundary can't be exercised end-to-end
	// through NewHTML/Reload with an ordinary filesystem layout. It is still a real
	// security check Reload applies to every walked path, so it is unit-tested
	// directly against the helper Reload calls.
	root := t.TempDir()
	_, err := templateName(root, filepath.Join(filepath.Dir(root), "outside.html"))
	if !errors.Is(err, ErrTemplateEscapesRoot) {
		t.Fatalf("templateName() error = %v, want ErrTemplateEscapesRoot", err)
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
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			_ = engine.Render(context.Background(), &buf, "pages/home.html", struct{ Heading string }{Heading: "x"})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := engine.Reload(); err != nil {
				t.Errorf("Reload() error = %v", err)
			}
		}()
	}
	wg.Wait()
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}
