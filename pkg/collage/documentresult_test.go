package collage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// mountTemplateRoot writes the minimal template set an App needs to start, and
// returns its directory. It is this package's own copy of collage_test.go's (the
// internal test package's) templateRoot: an external test package cannot reach
// an internal test helper of the package under test.
func mountTemplateRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, "pages", "home.html")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`<h1>Welcome Home</h1>`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

// TestDocumentResult_IsNameableFromOutsideThePackage is the reachability test for
// App.RenderDocumentPath's return type, and it lives in the external test package
// because the import list is the proof. render.DocumentResult lives in internal/render,
// unreachable from outside this module, so without the collage.DocumentResult
// alias none of the declarations below would compile.
//
// This was the sixth instance of one defect. App was then a bare alias of
// core.App, so its methods were public surface that never appeared in package
// collage's own source, and a return type reached only that way was easy to miss: a caller can
// always write `result := app.RenderDocumentPath(...)`, and the gap only shows up
// when it tries to store the value in a field, pass it to a helper, or declare a
// variable of that type. RenderPath's Result alias exists to avoid exactly this;
// RenderDocumentPath simply never got its sibling.
func TestDocumentResult_IsNameableFromOutsideThePackage(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{Root: mountTemplateRoot(t)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	doc := collage.NewDocument("sitemap", "application/xml").
		WithPath("en", "/sitemap.xml").
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		}).
		Static().
		Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}

	// The declaration is the assertion: a var of the aliased type, assigned from
	// the method that returns it.
	var result *collage.DocumentResult
	result, err = app.RenderDocumentPath(context.Background(), "/sitemap.xml", "en", nil)
	if err != nil {
		t.Fatalf("RenderDocumentPath: %v", err)
	}

	// A struct field and a function parameter of the same type: the two shapes a
	// caller that only ever wrote `result :=` could not express.
	type cachedDocument struct {
		latest *collage.DocumentResult
	}
	contentTypeOf := func(r *collage.DocumentResult) string { return r.ContentType }

	held := cachedDocument{latest: result}
	if got := contentTypeOf(held.latest); got != "application/xml" {
		t.Fatalf("ContentType = %q, want %q", got, "application/xml")
	}
	if string(held.latest.Body) != "<urlset/>" {
		t.Fatalf("Body = %q, want %q", held.latest.Body, "<urlset/>")
	}
	if len(held.latest.Tags) != 1 || held.latest.Tags[0] != "blog:posts" {
		t.Fatalf("Tags = %v, want [blog:posts]", held.latest.Tags)
	}
	if held.latest.NotFound {
		t.Fatal("NotFound = true, want false")
	}
	if held.latest.Timing.Total < 0 {
		t.Fatalf("Timing.Total = %v, want a non-negative duration", held.latest.Timing.Total)
	}
}
