package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exported writes a directory shaped like a static export and returns its path.
func exported(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func serving(t *testing.T, files map[string]string) http.Handler {
	t.Helper()
	handler, count, err := staticHandler(exported(t, files))
	if err != nil {
		t.Fatalf("staticHandler: %v", err)
	}
	if count != len(files) {
		t.Errorf("counted %d files, want %d", count, len(files))
	}
	return handler
}

func fetch(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// The shape "collage export" writes: a page is a directory holding an index.html,
// and a static host resolves the path without one.
func TestServe_CleanURLsResolveToIndexHTML(t *testing.T) {
	h := serving(t, map[string]string{
		"index.html":       "<h1>home</h1>",
		"about/index.html": "<h1>about</h1>",
	})

	for target, want := range map[string]string{
		"/":       "home",
		"/about":  "about",
		"/about/": "about",
	} {
		rec := fetch(t, h, target)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %q, want it to contain %q", target, rec.Body.String(), want)
		}
	}
}

// A directory with no index.html is not a listing. No static host shows one, and
// showing one here would put the shape of an export in front of anyone who asks.
func TestServe_NoDirectoryListings(t *testing.T) {
	h := serving(t, map[string]string{
		"index.html":       "<h1>home</h1>",
		"assets/thing.css": "body{}",
	})

	rec := fetch(t, h, "/assets/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /assets/ = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "thing.css") {
		t.Errorf("the response listed the directory: %q", rec.Body.String())
	}
}

// An export's own 404.html is what a static host serves, so it is what should be
// seen here — with the status, not just the page.
func TestServe_UsesTheExports404Page(t *testing.T) {
	h := serving(t, map[string]string{
		"index.html": "<h1>home</h1>",
		"404.html":   "<h1>nothing here</h1>",
	})

	rec := fetch(t, h, "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nothing here") {
		t.Errorf("body = %q, want the export's own 404 page", rec.Body.String())
	}
}

func TestServe_WithoutA404Page(t *testing.T) {
	h := serving(t, map[string]string{"index.html": "<h1>home</h1>"})

	if rec := fetch(t, h, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// Nothing is cached: the point of the command is to look at what was just
// exported, and a browser holding the previous one is what stops that.
func TestServe_SendsNoCaching(t *testing.T) {
	h := serving(t, map[string]string{"index.html": "<h1>home</h1>", "app.css": "body{}"})

	for _, target := range []string{"/", "/app.css"} {
		rec := fetch(t, h, target)
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("GET %s Cache-Control = %q, want no-store", target, cc)
		}
	}
}

func TestServe_ContentTypes(t *testing.T) {
	h := serving(t, map[string]string{"index.html": "<h1>home</h1>", "app.css": "body{}"})

	for target, want := range map[string]string{"/": "text/html", "/app.css": "text/css"} {
		if got := fetch(t, h, target).Header().Get("Content-Type"); !strings.HasPrefix(got, want) {
			t.Errorf("GET %s Content-Type = %q, want %q", target, got, want)
		}
	}
}

// Running it before exporting is the usual mistake, and saying so beats serving an
// empty site that looks like a broken one.
func TestServe_RefusesWhatIsNotAnExport(t *testing.T) {
	if _, _, err := staticHandler(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("staticHandler accepted a directory that does not exist")
	} else if !strings.Contains(err.Error(), "collage export") {
		t.Errorf("error = %v, want it to say what to run", err)
	}

	if _, _, err := staticHandler(t.TempDir()); err == nil {
		t.Error("staticHandler accepted an empty directory")
	}
}

// Containment: os.OpenRoot is what serves this, so a path climbing out of the
// export is refused by the kernel rather than by string matching.
func TestServe_CannotEscapeTheExport(t *testing.T) {
	h := serving(t, map[string]string{"index.html": "<h1>home</h1>"})

	for _, target := range []string{"/../secret", "/..%2fsecret", "/a/../../secret"} {
		rec := fetch(t, h, target)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200, want a refusal", target)
		}
	}
}
