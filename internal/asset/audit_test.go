package asset

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

func auditFS() fstest.MapFS {
	return fstest.MapFS{
		"a b#c?.css":   &fstest.MapFile{Data: []byte("a{}")},
		"100%.css":     &fstest.MapFile{Data: []byte("b{}")},
		"sub dir/x.js": &fstest.MapFile{Data: []byte("1")},
		".env":         &fstest.MapFile{Data: []byte("SECRET=1")},
		"sub/.git/c":   &fstest.MapFile{Data: []byte("x")},
		"app.css":      &fstest.MapFile{Data: []byte("body{}")},
		"up.html":      &fstest.MapFile{Data: []byte("<script>alert(1)</script>")},
		"up.svg":       &fstest.MapFile{Data: []byte("<svg onload=alert(1)/>")},
		"up.xml":       &fstest.MapFile{Data: []byte("<x/>")},
		"up.xhtml":     &fstest.MapFile{Data: []byte("<html/>")},
		"up.png":       &fstest.MapFile{Data: []byte("\x89PNG")},
	}
}

func auditMount(t *testing.T, opts ...Option) *Mount {
	t.Helper()
	m, err := New("/static/", auditFS(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// URL pasted the file name into the link as it was: "a b#c?.css" ended its path
// at the "#", and "100%.css" was not a URL a client could parse. Each segment is
// escaped, and the link the mount is asked for resolves back to the file.
func TestURL_EscapesEachSegmentAndResolvesBack(t *testing.T) {
	m := auditMount(t)
	for _, name := range []string{"a b#c?.css", "100%.css", "sub dir/x.js"} {
		link, err := m.URL(name)
		if err != nil {
			t.Fatalf("URL(%q) = %v", name, err)
		}
		parsed, err := url.Parse(link)
		if err != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
			t.Fatalf("URL(%q) = %q, which does not parse as one path: %v", name, link, err)
		}
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, link, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s (for %q) = %d, want 200", link, name, rec.Code)
		}
	}
	// The slash between segments is structure, not text to escape.
	if link, _ := m.URL("sub dir/x.js"); !strings.HasPrefix(link, "/static/sub%20dir/x.") {
		t.Errorf("URL = %q, want the segments escaped and the slash kept", link)
	}
}

// A link to a file the mount will not serve is a link that 404s: URL refuses to
// mint one, as it does for a file that is not there.
func TestURL_RefusesWhatTheMountWillNotServe(t *testing.T) {
	m := auditMount(t)
	for _, name := range []string{".env", "sub/.git/c", "../x", "/app.css", "", "."} {
		if link, err := m.URL(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("URL(%q) = %q, %v; want fs.ErrNotExist", name, link, err)
		}
	}
	if _, err := m.URL("app.css"); err != nil {
		t.Errorf("URL(app.css) = %v", err)
	}
}

// A mount of uploads serves what readers stored, under the site's own origin: an
// .html or .svg there runs its script as the site. WithAttachments turns those
// types into downloads, which a browser does not render.
func TestMount_WithAttachmentsForcesScriptCapableTypesToDownload(t *testing.T) {
	get := func(m *Mount, name string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/"+name, nil))
		return rec
	}
	plain := auditMount(t)
	if got := get(plain, "up.html").Header().Get("Content-Disposition"); got != "" {
		t.Errorf("without the option Content-Disposition = %q, want none: it is opt-in", got)
	}
	guarded := auditMount(t, WithAttachments())
	for _, name := range []string{"up.html", "up.svg", "up.xml", "up.xhtml"} {
		rec := get(guarded, name)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Errorf("%s: %d, Content-Disposition %q, want attachment", name, rec.Code, rec.Header().Get("Content-Disposition"))
		}
	}
	for _, name := range []string{"up.png", "app.css"} {
		if got := get(guarded, name).Header().Get("Content-Disposition"); got != "" {
			t.Errorf("%s: Content-Disposition %q, want none for a type that cannot run script", name, got)
		}
	}
}

// A file is served at its own URL, not also at that URL with a slash after it.
func TestMount_AFileIsNotServedWithATrailingSlash(t *testing.T) {
	m := auditMount(t)
	for path, want := range map[string]int{
		"/static/app.css":  http.StatusOK,
		"/static/app.css/": http.StatusNotFound,
		"/static/sub dir/": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.ReplaceAll(path, " ", "%20"), nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}
