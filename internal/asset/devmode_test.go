package asset

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// diskMount serves dir through os.OpenRoot, the way an application serving files
// it is editing would.
func diskMount(t *testing.T, dir string, opts ...Option) *Mount {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	m, err := New("/static/", root.FS(), opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// Editing a file and reloading has to show the edit. It is the whole point of
// development mode, and a content-addressed URL breaks it in a way that looks like
// nothing happened: the name is computed once, so the page keeps linking the old
// URL, and the browser was told that URL can never change.
func TestDevMode_AnEditedFileGetsANewURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.css")
	writeFile(t, path, "body{color:red}")

	m := diskMount(t, dir, WithDevMode())

	before, err := m.URL("app.css")
	if err != nil {
		t.Fatalf("URL: %v", err)
	}

	writeFile(t, path, "body{color:blue}")

	after, err := m.URL("app.css")
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if after == before {
		t.Fatalf("URL = %q both before and after the edit; the page would keep linking the old one", after)
	}

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, after, nil))
	if body := rec.Body.String(); body != "body{color:blue}" {
		t.Errorf("body = %q, want the edited file", body)
	}
}

// And nothing it serves in development may be cached, because the next request is
// meant to show the next edit.
func TestDevMode_NothingIsCached(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.css"), "body{color:red}")

	m := diskMount(t, dir, WithDevMode())
	url, _ := m.URL("app.css")

	for _, target := range []string{url, "/static/app.css"} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-store") {
			t.Errorf("GET %s Cache-Control = %q, want no-store in development", target, cc)
		}
		if strings.Contains(cc, "immutable") {
			t.Errorf("GET %s Cache-Control = %q, want no promise the next edit would break", target, cc)
		}
	}
}

// Outside development the hash is computed once and kept, which is what makes it
// cheap: a name derived from bytes that do not change need not be derived twice.
func TestDevMode_OffTheHashIsMemoised(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.css")
	writeFile(t, path, "body{color:red}")

	m := diskMount(t, dir)

	before, _ := m.URL("app.css")
	writeFile(t, path, "body{color:blue}")
	after, _ := m.URL("app.css")

	if after != before {
		t.Errorf("URL = %q then %q: a production mount re-hashed a file it had already named", before, after)
	}
}
