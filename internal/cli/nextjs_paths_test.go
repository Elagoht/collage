package cli

// Regression tests harvested from Next.js's file-serving and prerender-encoding
// suites, asked of "collage serve": it is what a developer checks an export with,
// and it is served on a port like any other server.

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serveTraversalSteps are the "go up one directory" spellings of
// PayloadsAllTheThings' traversals-8-deep-exotic-encoding list, which Next.js's
// file-serving test sends in full.
var serveTraversalSteps = []string{
	"../", `..\`, "..%2f", "..%5c", "%2e%2e/", `%2e%2e\`, "%2e%2e%2f", "%2e%2e%5c",
	"..%252f", "%252e%252e%252f", "%c0%ae%c0%ae/", "%c0%ae%c0%ae%c0%af", "..%c0%af",
	"%uff0e%uff0e/", "..%u2215", "%%32%65%%32%65/", ".../", "....//", "..///", "./../", `\..%2f`,
}

// rawServe hands h the request a server would build from target sent byte for
// byte; one net/http cannot parse is answered 400 before a handler sees it.
func rawServe(h http.Handler, target string) (int, http.Header, string) {
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET " + target + " HTTP/1.1\r\nHost: localhost\r\n\r\n")))
	if err != nil {
		return http.StatusBadRequest, http.Header{}, ""
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, rec.Header(), string(body)
}

// Next.js: test/e2e/file-serving/file-serving.test.ts ("should prevent traversing
// with ...") and test/production/pages-dir/production/test/security.ts ("should
// only access files inside .next directory"). Nothing outside the export is served,
// whatever spelling of ".." the request uses and whether or not a symlink inside
// the export points out of it.
func TestNextjs_ServeNeverLeavesTheExport(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	for name, content := range map[string]string{
		filepath.Join(root, "test-file.txt"):           "SECRET-OUTSIDE",
		filepath.Join(dist, "index.html"):              "<h1>home</h1>",
		filepath.Join(dist, "404.html"):                "<h1>custom 404</h1>",
		filepath.Join(dist, "blog", "a", "index.html"): "<h1>a</h1>",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "test-file.txt"), filepath.Join(dist, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(dist, "up")); err != nil {
		t.Fatal(err)
	}
	h, _, err := staticHandler(dist)
	if err != nil {
		t.Fatal(err)
	}

	targets := []string{"/linked.txt", "/up/test-file.txt", "/up/", "/up"}
	for _, step := range serveTraversalSteps {
		for depth := 1; depth <= 8; depth++ {
			payload := "/" + strings.Repeat(step, depth) + "test-file.txt"
			targets = append(targets, payload, "/blog/a"+payload)
		}
	}
	for _, target := range targets {
		status, _, body := rawServe(h, target)
		if strings.Contains(body, "SECRET") {
			t.Fatalf("GET %s = %d served %q", target, status, body)
		}
		if status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 400 or 404", target, status)
		}
	}
}

// Next.js: test/e2e/repeated-slashes/repeated-slashes.test.ts, in its export
// mode. A static host asked for "//google.com" or "/\google.com" answers from
// the export or not at all; it never sends the browser to another host.
func TestNextjs_ServeNeverRedirectsOffSite(t *testing.T) {
	h := serving(t, map[string]string{"index.html": "<h1>home</h1>", "google.com/index.html": "<h1>page</h1>"})
	for _, target := range []string{"//google.com", "//google.com/", `/\google.com`, `/\/google.com`, "/%2Fgoogle.com", "/%5Cgoogle.com"} {
		status, header, _ := rawServe(h, target)
		if location := header.Get("Location"); location != "" && (strings.HasPrefix(location, "//") || strings.HasPrefix(location, `/\`)) {
			t.Errorf("GET %s = %d to %q", target, status, location)
		}
	}
}

// Next.js: test/e2e/prerender-fallback-encoding ("should respond with the
// prerendered pages correctly") and test/e2e/app-dir/prerender-encoding. A page
// the export wrote under a name holding a reserved character, a literal percent or
// a script outside ASCII is served at that name's escaped URL — the address a link
// to it carries.
func TestNextjs_ServeAnswersAnEncodedNameAtItsEscapedURL(t *testing.T) {
	names := []string{
		"%2Fmy-post%2F", "%252Fmy-post%252F", "+my-post+", "?my-post?", "&my-post&",
		"商業日語", " my-post ", "sticks & stones", "100%", "#hash",
	}
	files := map[string]string{"index.html": "<h1>home</h1>"}
	for _, name := range names {
		files["blog/"+name+"/index.html"] = "page:" + name
	}
	h := serving(t, files)
	for _, name := range names {
		for _, target := range []string{"/blog/" + url.PathEscape(name) + "/", "/blog/" + url.PathEscape(name)} {
			if rec := fetch(t, h, target); rec.Code != http.StatusOK || rec.Body.String() != "page:"+name {
				t.Errorf("GET %s = %d %q, want the page written for %q", target, rec.Code, rec.Body.String(), name)
			}
		}
	}
}

// Next.js: test/e2e/file-serving/file-serving.test.ts ("should serve correct
// error code") and test/e2e/404-page ("should set correct status code"). A range
// the file cannot satisfy is a 416, and a missing page is the export's 404.html
// with a 404 that nothing caches.
func TestNextjs_ServeAnswersRangesAndMissesAsAStaticHostDoes(t *testing.T) {
	h := serving(t, map[string]string{"index.html": "<h1>home</h1>", "404.html": "<h1>custom 404</h1>", "icon.avif": "avif bytes"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/icon.avif", nil)
	req.Header.Set("Range", "bytes=1000000000-")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("GET with Range past the end = %d, want 416", rec.Code)
	}
	if ctype := fetch(t, h, "/icon.avif").Header().Get("Content-Type"); ctype != "image/avif" {
		t.Errorf("GET /icon.avif Content-Type = %q, want image/avif", ctype)
	}

	for _, target := range []string{"/abc", "/404", "/abc/def/"} {
		rec := fetch(t, h, target)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "custom 404") {
			t.Errorf("GET %s = %d %q, want 404 with 404.html", target, rec.Code, rec.Body.String())
		}
		if control := rec.Header().Get("Cache-Control"); control != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", target, control)
		}
	}
}
