package asset

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func fingerprintFS() fstest.MapFS {
	return fstest.MapFS{
		"app.css":         &fstest.MapFile{Data: []byte("body{color:red}")},
		"js/app.js":       &fstest.MapFile{Data: []byte("console.log(1)")},
		"logo":            &fstest.MapFile{Data: []byte("no extension")},
		"img/photo.a.png": &fstest.MapFile{Data: []byte("dots in the name")},
	}
}

func newFingerprintMount(t *testing.T) *Mount {
	t.Helper()
	m, err := New("/static/", fingerprintFS())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return m
}

func TestURL_InsertsTheContentHashBeforeTheExtension(t *testing.T) {
	m := newFingerprintMount(t)

	url, err := m.URL("app.css")
	if err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}
	if !strings.HasPrefix(url, "/static/app.") || !strings.HasSuffix(url, ".css") {
		t.Fatalf("URL() = %q, want /static/app.<hash>.css", url)
	}
	// The extension has to survive: it is what decides the Content-Type served,
	// and a stylesheet delivered as something else is not applied by any browser.
	if got := url[len("/static/app.") : len(url)-len(".css")]; len(got) != fingerprintLen {
		t.Errorf("hash = %q (%d chars), want %d", got, len(got), fingerprintLen)
	}
}

func TestURL_IsStableAndFollowsContent(t *testing.T) {
	m := newFingerprintMount(t)
	first, err := m.URL("app.css")
	if err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}
	again, err := m.URL("app.css")
	if err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}
	if first != again {
		t.Errorf("URL() = %q then %q, want the same URL for the same bytes", first, again)
	}

	changed, err := New("/static/", fstest.MapFS{
		"app.css": &fstest.MapFile{Data: []byte("body{color:blue}")},
	})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	other, err := changed.URL("app.css")
	if err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}
	if other == first {
		t.Error("different content produced the same URL; the name is not content-addressed")
	}
}

func TestURL_FileWithNoExtension(t *testing.T) {
	m := newFingerprintMount(t)
	url, err := m.URL("logo")
	if err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}
	if !strings.HasPrefix(url, "/static/logo.") {
		t.Errorf("URL() = %q, want /static/logo.<hash>", url)
	}
}

func TestURL_MissingFile(t *testing.T) {
	m := newFingerprintMount(t)
	if _, err := m.URL("nope.css"); err == nil {
		t.Fatal("URL() = nil error for a file that does not exist, want an error")
	}
}

func TestServe_FingerprintedRequestIsImmutable(t *testing.T) {
	m := newFingerprintMount(t)
	url, err := m.URL("app.css")
	if err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "body{color:red}" {
		t.Errorf("body = %q, want the file's contents", body)
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable: a content-addressed name cannot go stale", cc)
	}
	if !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control = %q, want a year", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
}

func TestServe_PlainRequestKeepsTheMountsOwnCacheControl(t *testing.T) {
	m := newFingerprintMount(t)

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want the mount's own value: an unfingerprinted name can go stale", cc)
	}
}

// The verification is the whole security of serving something immutable. A name
// carrying a hash that is not the file's must not be served for a year — that is a
// year of one visitor's mistake pinned in front of everyone behind a shared cache.
func TestServe_WrongFingerprintIsNotFound(t *testing.T) {
	m := newFingerprintMount(t)

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.0123456789abcdef.css", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a name whose hash is not the file's", rec.Code)
	}
}

// A file whose real name happens to look fingerprinted is still its own file.
func TestServe_LiteralNameThatLooksFingerprinted(t *testing.T) {
	m, err := New("/static/", fstest.MapFS{
		"app.0123456789abcdef.css": &fstest.MapFile{Data: []byte("literal")},
	})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.0123456789abcdef.css", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "literal" {
		t.Errorf("body = %q, want the file itself", body)
	}
	// Served, but not as something that can never change: the name was not minted
	// from these bytes, so nothing guarantees it tracks them.
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want the mount's own value", cc)
	}
}

// A static build writes the fingerprinted copies, and it can only know which names
// to write because the mount remembers every one it handed out.
func TestFingerprinted_RecordsWhatWasHandedOut(t *testing.T) {
	m := newFingerprintMount(t)

	if got := m.Fingerprinted(); len(got) != 0 {
		t.Fatalf("Fingerprinted() = %v before any URL was produced, want empty", got)
	}

	cssURL, err := m.URL("app.css")
	if err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}
	if _, err := m.URL("js/app.js"); err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}
	if _, err := m.URL("app.css"); err != nil {
		t.Fatalf("URL() = %v, want nil", err)
	}

	got := m.Fingerprinted()
	if len(got) != 2 {
		t.Fatalf("Fingerprinted() = %v, want one entry per distinct file", got)
	}
	want := strings.TrimPrefix(cssURL, "/static/")
	if got["app.css"] != want {
		t.Errorf("Fingerprinted()[%q] = %q, want %q", "app.css", got["app.css"], want)
	}
}
