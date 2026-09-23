package asset

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fs.FS {
	return fstest.MapFS{
		"app.css":           {Data: []byte("body{color:red}")},
		"audio/track.mp3":   {Data: []byte("0123456789abcdefghijklmnopqrstuvwxyz")},
		"nested/deep/a.txt": {Data: []byte("deep")},
	}
}

func mustMount(t *testing.T, opts ...Option) *Mount {
	t.Helper()
	m, err := New("/static/", testFS(), opts...)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return m
}

func TestMount_ServesAFile(t *testing.T) {
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "body{color:red}" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", ct)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("ETag is empty — embed.FS has a zero ModTime, so an ETag is the only validator that works")
	}
}

func TestMount_RangeRequestReturns206(t *testing.T) {
	// A ResponseRecorder cannot show this properly; use a real server.
	server := httptest.NewServer(mustMount(t))
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL+"/static/audio/track.mp3", nil)
	req.Header.Set("Range", "bytes=10-19")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 — seeking in audio is a Range request", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "abcdefghij" {
		t.Fatalf("body = %q, want the requested byte range", body)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 10-19/36" {
		t.Fatalf("Content-Range = %q", got)
	}
}

func TestMount_NoDirectoryListing(t *testing.T) {
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/audio/", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a directory listing is an information leak", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "track.mp3") {
		t.Fatalf("body leaks a directory listing: %q", rec.Body.String())
	}
}

func TestMount_RejectsTraversal(t *testing.T) {
	tests := []string{
		"/static/../secret",
		"/static/nested/../../secret",
		"/static//etc/passwd",
		"/static/./../../secret",
	}
	m := mustMount(t)

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
			if rec.Code == http.StatusOK {
				t.Fatalf("status = 200 for %q, want a refusal", target)
			}
		})
	}
}

func TestMount_MethodNotAllowed(t *testing.T) {
	m := mustMount(t)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, httptest.NewRequest(method, "/static/app.css", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rec.Code)
			}
			if rec.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
			}
		})
	}
}

func TestMount_CacheControl(t *testing.T) {
	m := mustMount(t, WithCacheControl("public, max-age=31536000, immutable"))
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))

	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestMount_NotFoundIsPlainText(t *testing.T) {
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/missing.css", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain — consistent with documents, never HTML", ct)
	}
}

func TestNew_RejectsBadPrefixes(t *testing.T) {
	for _, prefix := range []string{"", "/", "static/", "//static/"} {
		t.Run(prefix, func(t *testing.T) {
			if _, err := New(prefix, testFS()); err == nil {
				t.Fatalf("New(%q) = nil error, want ErrInvalidPrefix", prefix)
			}
		})
	}
}

func TestNew_RejectsANilFS(t *testing.T) {
	if _, err := New("/static/", nil); err == nil {
		t.Fatal("New() = nil error, want ErrNilFS")
	}
}

// notReadSeekerFile is a file that is not an io.ReadSeeker.
type notReadSeekerFile struct {
	io.Reader
	info fs.FileInfo
}

func (f *notReadSeekerFile) Stat() (fs.FileInfo, error) {
	return f.info, nil
}

func (f *notReadSeekerFile) Close() error {
	return nil
}

// notReadSeekerFS returns a file that is not an io.ReadSeeker.
type notReadSeekerFS struct {
	inner fs.FS
}

func (nsfs *notReadSeekerFS) Open(name string) (fs.File, error) {
	f, err := nsfs.inner.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &notReadSeekerFile{Reader: f, info: info}, nil
}

func TestMount_FailsLoudlyOnNonReadSeeker(t *testing.T) {
	// A file that is not an io.ReadSeeker must cause a 500, not silently serve nothing.
	inner := fstest.MapFS{"app.css": {Data: []byte("body{color:red}")}}
	m, err := New("/static/", &notReadSeekerFS{inner: inner})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for non-ReadSeeker file", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", rec.Header().Get("Content-Type"))
	}
}

// errorFS returns an error for Open even though the file would exist.
type errorFS struct {
}

func (efs *errorFS) Open(name string) (fs.File, error) {
	return nil, fs.ErrPermission
}

func TestMount_Returns404ForErrorFS(t *testing.T) {
	// A file system that returns an error on Open must return a plain-text 404, not panic.
	m, err := New("/static/", &errorFS{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for errorFS", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", rec.Header().Get("Content-Type"))
	}
}

func TestMount_Handles(t *testing.T) {
	m := mustMount(t)

	tests := []struct {
		path   string
		want   bool
		reason string
	}{
		{"/static/app.css", true, "path under the prefix"},
		{"/api/data", false, "path outside the prefix"},
		{"/staticsibling/app.css", false, "prefix confusion: /static/ must not claim /staticsibling/"},
		{"/static/", true, "bare prefix is owned by the mount (resolve will refuse to serve it)"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := m.Handles(tc.path)
			if got != tc.want {
				t.Fatalf("Handles(%q) = %v, want %v — %s", tc.path, got, tc.want, tc.reason)
			}
		})
	}
}

func TestMount_BarePrefix_ReturnsPlainText404(t *testing.T) {
	// A request to the bare prefix is owned by the mount (Handles returns true)
	// but cannot be served (resolve returns false), producing a plain-text 404
	// from the mount. This ensures Task 7's router does not fall through to the
	// page router and return an HTML error for an asset URL.
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain — bare prefix must be plain-text, not HTML", rec.Header().Get("Content-Type"))
	}
}

// TestMount_PlainTextErrorCarriesTheSameHeadersAsADocumentError pins M6. This
// package writes its own plain-text error rather than importing internal/httpx's,
// which is a deliberate duplication — and the first bill it came due on was a
// missing Content-Length that internal/httpx's writePlainText has always set. The
// two writers are aligned header for header, and this test is what keeps them
// that way from this side.
func TestMount_PlainTextErrorCarriesTheSameHeadersAsADocumentError(t *testing.T) {
	m := mustMount(t)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/static/nope.css", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	body := rec.Body.String()
	for header, want := range map[string]string{
		"Content-Type":           "text/plain; charset=utf-8",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Content-Length":         strconv.Itoa(len(body)),
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}
