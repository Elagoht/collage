package asset

// Regression tests harvested from Next.js's file-serving suite, asked of a Mount on
// its own: nothing in front of it cleans the path first, so what it refuses here it
// refuses whoever serves it.

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// traversalSteps are the "go up one directory" spellings of PayloadsAllTheThings'
// traversals-8-deep-exotic-encoding list, which Next.js's file-serving test sends
// in full: "..", "/" and "\" plain, escaped, double-escaped, as overlong UTF-8, as
// %u, and padded with extra dots and slashes.
var traversalSteps = []string{
	"../", `..\`, "..%2f", "..%5c", "%2e%2e/", `%2e%2e\`, "%2e%2e%2f", "%2e%2e%5c",
	"..%252f", "..%255c", "%252e%252e/", "%252e%252e%252f", "%252e%252e%255c",
	"%c0%ae%c0%ae/", "%c0%2e%c0%2e/", "%c0%ae%c0%ae%c0%af", "..%c0%af", "..%c1%9c", "..%c0%2f",
	"%25c0%25ae%25c0%25ae/", "..%25c0%25af", "%uff0e%uff0e/", "..%u2215", "..%u2216",
	"%%32%65%%32%65/", "..%%32%66", "..%%35%63", ".../", "..../", "....//", "..///",
	"./../", `..\\\`, `\..%2f`, "..0x2f", "0x2e0x2e/", "..;/",
}

// traversalPayloads spells each step one to eight directories deep, ending in the
// file the traversal is after.
func traversalPayloads() []string {
	var payloads []string
	for _, step := range traversalSteps {
		for depth := 1; depth <= 8; depth++ {
			payloads = append(payloads, "/"+strings.Repeat(step, depth)+"test-file.txt")
		}
	}
	return payloads
}

// serveRaw hands m the request a server would build from target sent byte for
// byte. A target net/http cannot parse is one a server answers 400 before any
// handler sees it, so that is its answer here.
func serveRaw(m *Mount, target string) (int, string) {
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET " + target + " HTTP/1.1\r\nHost: site.test\r\n\r\n")))
	if err != nil {
		return http.StatusBadRequest, ""
	}
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

// Next.js: test/e2e/file-serving/file-serving.test.ts ("should prevent traversing
// with ..."). A Mount of a real directory never serves the file beside it, nor a
// dotfile inside it, whichever spelling of ".." or "/" the request uses, and it
// refuses with a 400 or a 404 rather than redirecting to a cleaner spelling.
func TestNextjs_AMountRefusesEveryTraversalSpelling(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public", "nested")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		filepath.Join(root, "test-file.txt"):           "SECRET-OUTSIDE",
		filepath.Join(root, "public", "test-file.txt"): "public copy",
		filepath.Join(root, "public", ".env"):          "SECRET-DOTFILE",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New("/static/", os.DirFS(filepath.Join(root, "public")))
	if err != nil {
		t.Fatal(err)
	}

	for _, payload := range traversalPayloads() {
		for _, target := range []string{"/static" + payload, "/static/nested" + payload} {
			status, body := serveRaw(m, target)
			if strings.Contains(body, "SECRET") {
				t.Fatalf("GET %s = %d served %q", target, status, body)
			}
			// Climbing from nested/ to public/ stays inside the mount, and the
			// file there is the mount's to serve.
			if status == http.StatusOK && body == "public copy" {
				continue
			}
			if status != http.StatusBadRequest && status != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 400 or 404", target, status)
			}
		}
	}
	for _, target := range []string{"/static/.env", "/static/%2eenv", "/static/nested/../.env", "/static/nested/%2e%2e/.env"} {
		if status, body := serveRaw(m, target); status != http.StatusNotFound || strings.Contains(body, "SECRET") {
			t.Errorf("GET %s = %d %q, want 404", target, status, body)
		}
	}
}

// Next.js: test/e2e/file-serving/file-serving.test.ts ("should serve file with
// space correctly", "should serve avif image with correct content-type" and
// "should serve correct error code" for a Range past the end). A name with a space
// is reached at its escaped URL, a type is the extension's, and a range the file
// cannot satisfy is a 416, not the whole file.
func TestNextjs_AMountServesNamesTypesAndRangesAsAStaticServerDoes(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"hello world.txt":       "hi",
		"vercel-icon-dark.avif": "not really an image, but named as one",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New("/static/", os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/hello%20world.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "hi" {
		t.Errorf("GET /static/hello%%20world.txt = %d %q, want 200 \"hi\"", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/vercel-icon-dark.avif", nil))
	if ctype := rec.Header().Get("Content-Type"); ctype != "image/avif" {
		t.Errorf("GET .avif Content-Type = %q, want image/avif", ctype)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/vercel-icon-dark.avif", nil)
	req.Header.Set("Range", "bytes=1000000000-")
	m.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("GET with Range past the end = %d, want 416", rec.Code)
	}
}

// Next.js: test/e2e/invalid-static-asset-404-pages ("should return 404 with plain
// text when fetching invalid asset path"). What a mount does not have is a plain
// "Not Found", never a page.
func TestNextjs_AMissingAssetIsAPlainNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	mustMount(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/invalid-path", nil))
	if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != "Not Found" {
		t.Errorf("GET /static/invalid-path = %d %q, want 404 \"Not Found\"", rec.Code, rec.Body.String())
	}
	if ctype := rec.Header().Get("Content-Type"); !strings.HasPrefix(ctype, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ctype)
	}
}
