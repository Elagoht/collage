package collage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A page at /blogs/{slug} and its Markdown at /blogs/{slug}.md: served side by
// side, linked by name, and both written by an export.
func TestAffixedDocumentBesideAPage(t *testing.T) {
	app := buildTestApp(t)
	slugs := func(context.Context, string) ([]map[string]string, error) {
		return []map[string]string{{"slug": "hello"}, {"slug": "a.b"}}, nil
	}
	page := NewPage("post").
		WithLayouts(NewFragment("layout", "layouts/default.html").WithSlot("content", true, false).Build()).
		WithContent(NewFragment("post-content", "pages/home.html").Build()).
		WithPath("en", "/blogs/{slug}").
		WithStaticParams(slugs).
		Static().
		Build()
	doc := NewDocument("post-md", "text/markdown; charset=utf-8").
		WithPath("en", "/blogs/{slug}.md").
		WithHandler(func(_ context.Context, rc *RenderContext) ([]byte, []string, error) {
			return []byte("# " + rc.Param("slug") + "\n"), nil, nil
		}).
		WithStaticParams(slugs).
		Static().
		Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterPage(page); err != nil {
		t.Fatal(err)
	}

	get := func(path string) (int, string, string) {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body, _ := io.ReadAll(rec.Body)
		return rec.Code, rec.Header().Get("Content-Type"), string(body)
	}
	if code, ct, body := get("/blogs/a.b.md"); code != http.StatusOK || !strings.HasPrefix(ct, "text/markdown") || body != "# a.b\n" {
		t.Errorf("GET /blogs/a.b.md = %d %q %q", code, ct, body)
	}
	if code, ct, _ := get("/blogs/hello"); code != http.StatusOK || !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /blogs/hello = %d %q", code, ct)
	}

	if u, err := app.URL("post-md", "en", map[string]string{"slug": "çay"}); err != nil || u != "/blogs/%C3%A7ay.md" {
		t.Errorf(`URL("post-md", çay) = %q, %v`, u, err)
	}

	out := t.TempDir()
	builder, err := NewBuilder(app, BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for file, want := range map[string]string{
		"blogs/hello.md":         "# hello\n",
		"blogs/a.b.md":           "# a.b\n",
		"blogs/hello/index.html": "",
		"blogs/a.b/index.html":   "",
	} {
		body, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(file)))
		if err != nil {
			t.Errorf("the export has no %s: %v", file, err)
			continue
		}
		if want != "" && string(body) != want {
			t.Errorf("%s = %q, want %q", file, body, want)
		}
	}
}

// Crossing patterns are refused through the public API, with the sentinel.
func TestOverlappingPatternIsExported(t *testing.T) {
	app := buildTestApp(t)
	md := func(name, pattern string) *Document {
		return NewDocument(name, "text/plain").WithPath("en", pattern).WithBody([]byte("x")).Build()
	}
	if err := app.RegisterDocument(md("one", "/f/a{x}")); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterDocument(md("two", "/f/{x}b")); !errors.Is(err, ErrOverlappingPattern) {
		t.Errorf("RegisterDocument = %v, want ErrOverlappingPattern", err)
	}
}
