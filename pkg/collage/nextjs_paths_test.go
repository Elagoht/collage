package collage_test

// Regression tests harvested from Next.js's path, URL-encoding, static-file and
// not-found suites. Each test names the Next.js test it comes from. They are run
// against a whole application, through app.Handler(), because that is where the
// properties are promised: a path is cleaned, routed, mounted and failed by
// different pieces, and a bypass lives in the gap between two of them.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// rawResponse is what came back for a request sent byte for byte.
type rawResponse struct {
	status   int
	location string
	header   http.Header
	body     string
}

// rawGet sends target as the request-target exactly as written, the way Next.js's
// fetchViaRawHttp does. A client library would clean a backslash or a dot segment
// before it left the machine, and then the server would never be asked the
// question the test is about.
func rawGet(t *testing.T, addr, target string) rawResponse {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: site.test\r\nConnection: close\r\n\r\n", target); err != nil {
		t.Fatalf("write %q: %v", target, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response to %q: %v", target, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return rawResponse{status: resp.StatusCode, location: resp.Header.Get("Location"), header: resp.Header, body: string(data)}
}

// sameSite reports whether a browser at http://site.test/ would stay on
// site.test following location. A browser reads "\" as "/" in an http URL and
// drops tabs and newlines, so both are done before the location is resolved;
// net/url does neither, and trusting it alone would call "/\evil.com" safe.
func sameSite(location string) bool {
	if location == "" {
		return false
	}
	cleaned := strings.NewReplacer("\t", "", "\n", "", "\r", "", `\`, "/").Replace(location)
	base, _ := url.Parse("http://site.test/")
	resolved, err := base.Parse(cleaned)
	if err != nil {
		return false
	}
	return resolved.Scheme == "http" && resolved.Host == "site.test"
}

// fileServingApp is a site with a home page, a not-found page of its own, and a
// directory mounted at /static/. Beside the mounted directory, outside it, is
// test-file.txt, and inside it a dotfile: neither may ever be served.
func fileServingApp(t *testing.T) *collage.App {
	t.Helper()
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		filepath.Join(root, "test-file.txt"):     "SECRET-OUTSIDE-THE-MOUNT",
		filepath.Join(public, ".env"):            "SECRET-DOTFILE",
		filepath.Join(public, "hello world.txt"): "hi",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/home.html":     {Data: []byte(`<p>home</p>`)},
				"t/notfound.html": {Data: []byte(`<p>custom 404 page</p>`)},
			},
			Root: "t",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	home := collage.NewPage("home").WithContent(collage.NewFragment("home", "home.html").Build()).WithPath("en", "/").Static().Build()
	if err := app.RegisterPage(home); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	notFound := collage.NewPage("notfound").WithContent(collage.NewFragment("notfound", "notfound.html").Build()).Build()
	if err := app.RegisterNotFoundPage(notFound); err != nil {
		t.Fatalf("RegisterNotFoundPage: %v", err)
	}
	if err := app.Mount("/static/", os.DirFS(public)); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// Next.js: test/e2e/file-serving/file-serving.test.ts ("should prevent traversing
// with ...", every payload in it). Each is asked of the site's root and of the
// mount, as Next.js asks of its root, /_next/ and /static/. What the site may answer
// is a refusal, a 400 or a 404, or a redirect that stays on the site — and wherever
// the redirects lead, never the file outside the mount or the dotfile inside it.
func TestNextjs_TraversalPayloadsNeverLeaveTheMount(t *testing.T) {
	server := httptest.NewServer(fileServingApp(t).Handler())
	defer server.Close()
	addr := server.Listener.Addr().String()

	payloads := strings.Split(strings.TrimSpace(nextjsTraversalPayloads), "\n")
	if len(payloads) != 652 {
		t.Fatalf("%d payloads, want the 652 Next.js tests", len(payloads))
	}
	for _, payload := range payloads {
		for _, target := range []string{payload, "/static" + payload} {
			resp := rawGet(t, addr, target)
			for hop := 0; hop < 5 && resp.status >= 300 && resp.status < 400; hop++ {
				if !sameSite(resp.location) {
					t.Fatalf("GET %s redirects off the site, to %q", target, resp.location)
				}
				next, _ := url.Parse("http://site.test/")
				next, _ = next.Parse(resp.location)
				resp = rawGet(t, addr, next.RequestURI())
			}
			if strings.Contains(resp.body, "SECRET") {
				t.Fatalf("GET %s served a file it must not:\n%s", target, resp.body)
			}
			if resp.status != http.StatusBadRequest && resp.status != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 400 or 404", target, resp.status)
			}
		}
	}
}

// Next.js: test/e2e/file-serving/file-serving.test.ts ("should serve file with
// space correctly from public/"). A name with a space is reached at its escaped URL.
func TestNextjs_MountServesANameWithASpace(t *testing.T) {
	rec := request(fileServingApp(t).Handler(), http.MethodGet, "/static/hello%20world.txt")
	if rec.Code != http.StatusOK || rec.Body.String() != "hi" {
		t.Errorf("GET /static/hello%%20world.txt = %d %q, want 200 \"hi\"", rec.Code, rec.Body.String())
	}
}

// Next.js: test/e2e/invalid-static-asset-404-pages ("should return custom 404 page
// when fetching invalid non-asset path" and "should return 404 with plain text when
// fetching invalid asset path"). A missing page is the site's own not-found page; a
// missing asset is plain text, because whatever asked for a stylesheet cannot use a
// web page.
func TestNextjs_AMissingAssetIsPlainTextAndAMissingPageIsTheSitesOwn(t *testing.T) {
	h := fileServingApp(t).Handler()

	page := request(h, http.MethodGet, "/invalid-path")
	if page.Code != http.StatusNotFound || !strings.Contains(page.Body.String(), "custom 404 page") {
		t.Errorf("GET /invalid-path = %d:\n%s\nwant 404 with the site's not-found page", page.Code, page.Body.String())
	}

	asset := request(h, http.MethodGet, "/static/invalid-path")
	if asset.Code != http.StatusNotFound {
		t.Errorf("GET /static/invalid-path = %d, want 404", asset.Code)
	}
	if ctype := asset.Header().Get("Content-Type"); !strings.HasPrefix(ctype, "text/plain") {
		t.Errorf("GET /static/invalid-path Content-Type = %q, want text/plain", ctype)
	}
	if strings.Contains(asset.Body.String(), "custom 404 page") {
		t.Errorf("GET /static/invalid-path served the HTML not-found page:\n%s", asset.Body.String())
	}
}

// Next.js: test/e2e/404-page/404-page.test.ts ("should set correct status code
// with pages/404", "should not error when visited directly", and "should not cache
// for custom 404 page ..."). The not-found page is a 404 wherever it is reached,
// /404 included, and no cache between the server and the reader may keep it.
func TestNextjs_TheNotFoundPageIsA404AndIsNeverCached(t *testing.T) {
	h := fileServingApp(t).Handler()
	for _, target := range []string{"/abc", "/404", "/static/abc"} {
		rec := request(h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
		if control := rec.Header().Get("Cache-Control"); !strings.Contains(control, "no-store") {
			t.Errorf("GET %s Cache-Control = %q, want no-store", target, control)
		}
	}
}

// Next.js: test/e2e/middleware-trailing-slash ("should respond with 400 on decode
// failure"). A path whose percent-encoding is broken is the client's mistake: a
// 400 or a 404, never a 500 and never a page.
func TestNextjs_ABrokenEscapeIsTheClientsError(t *testing.T) {
	server := httptest.NewServer(slashApp(t, false).Handler())
	defer server.Close()
	for _, target := range []string{"/%2/", "/%2", "/%zz", "/blog/%E0%A4%A"} {
		resp := rawGet(t, server.Listener.Addr().String(), target)
		if resp.status != http.StatusBadRequest && resp.status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 400 or 404", target, resp.status)
		}
	}
}

// Next.js: test/e2e/repeated-slashes/repeated-slashes.test.ts ("should handle
// double slashes correctly", "... with query", "... with encoded", "should handle
// backslashes correctly", "should handle mixed backslashes/forward slashes
// correctly") and test/production/pages-dir/production/test/security.ts ("should
// handle encoded / value for trailing slash correctly"). Whatever spelling of a
// second slash arrives, the answer never sends the browser to another host. The
// site here has a page taking any single segment, which is what a trailing-slash
// redirect would be built from.
func TestNextjs_RepeatedSlashesAndBackslashesStayOnTheSite(t *testing.T) {
	for _, trailing := range []bool{false, true} {
		server := httptest.NewServer(slashApp(t, trailing).Handler())
		addr := server.Listener.Addr().String()
		for _, target := range []string{
			"//google.com", "//google.com/", "//google.com?h=1", "///google.com",
			`/\google.com`, `/\google.com/`, `/\/google.com`, `/\\google.com`, `//\google.com`,
			"/%2Fgoogle.com", "/%2Fgoogle.com?hello=1", "/%2fexample.com/", "/%2F%2Fgoogle.com/",
			"/%5Cgoogle.com", "/%5Cgoogle.com/", "/%5C%5Cgoogle.com/", "/%5C/google.com",
			"/.//google.com", "/..//google.com", "/./%5Cgoogle.com/",
		} {
			resp := rawGet(t, addr, target)
			if resp.status >= 300 && resp.status < 400 && !sameSite(resp.location) {
				t.Errorf("TrailingSlash %v: GET %s = %d to %q, off the site", trailing, target, resp.status, resp.location)
			}
			if resp.status >= 500 {
				t.Errorf("TrailingSlash %v: GET %s = %d", trailing, target, resp.status)
			}
		}
		server.Close()
	}
}

// Next.js: test/e2e/repeated-slashes/repeated-slashes.test.ts ("should handle
// double slashes correctly with query"). A doubled slash is sent to its one-slash
// spelling, on this site, with the query exactly as it came.
func TestNextjs_ADoubledSlashRedirectsToOneKeepingTheQuery(t *testing.T) {
	h := slashApp(t, false).Handler()
	for target, want := range map[string]string{
		"//google.com":     "/google.com",
		"//google.com?h=1": "/google.com?h=1",
		"/blog//hello":     "/blog/hello",
	} {
		rec := request(h, http.MethodGet, target)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("GET %s = %d to %q, want 301 to %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}

// encodedQuery is Next.js's own: a URL carried in a query parameter, every
// reserved character in it escaped.
const encodedQuery = "url=https%3A%2F%2Fgoogle.com%2Fimage%3Fcrop%3Dfocalpoint%26w%3D24&w=1200&q=100"

// Next.js: test/production/pages-dir/production/test/security.ts ("should handle
// encoded value in the query correctly"). A trailing-slash redirect carries the
// query byte for byte: decoding and re-encoding it would turn the %26 inside the
// url parameter into a separator, and hand the next page a parameter nobody sent.
func TestNextjs_TrailingSlashRedirectKeepsAnEncodedQuery(t *testing.T) {
	for _, c := range []struct {
		trailing bool
		from, to string
	}{
		{false, "/about/", "/about"},
		{true, "/about", "/about/"},
	} {
		rec := request(slashApp(t, c.trailing).Handler(), http.MethodGet, c.from+"?"+encodedQuery)
		if want := c.to + "?" + encodedQuery; rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("TrailingSlash %v: GET %s?... = %d to %q, want 301 to %q", c.trailing, c.from, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	rec := request(slashApp(t, false).Handler(), http.MethodGet, "//about?"+encodedQuery)
	if want := "/about?" + encodedQuery; rec.Header().Get("Location") != want {
		t.Errorf("GET //about?... redirects to %q, want %q", rec.Header().Get("Location"), want)
	}
}

// redirectApp has Next.js's security fixture's redirects: a captured value moved
// into the path of the destination.
func redirectApp(t *testing.T) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"t/page.html": {Data: []byte(`<p>about</p>`)}},
			Root: "t",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	about := collage.NewPage("about").WithContent(collage.NewFragment("about", "page.html").Build()).
		WithPath("en", "/about").Static().
		WithRedirect("/redirect/me/to-about/{x}", "/{x}/about", http.StatusTemporaryRedirect).
		Build()
	if err := app.RegisterPage(about); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app
}

// Next.js: test/production/pages-dir/production/test/security.ts ("should handle
// encoded value in the pathname correctly \", "... %" and "... /"). A captured
// value goes into the destination escaped, so a backslash cannot become the
// second slash of "//google.com" and a "%" cannot become the start of an escape it
// was not; an encoded "/" is never let through to be one.
func TestNextjs_RedirectEscapesWhatItCaptured(t *testing.T) {
	server := httptest.NewServer(redirectApp(t).Handler())
	defer server.Close()
	addr := server.Listener.Addr().String()

	for target, want := range map[string]string{
		"/redirect/me/to-about/" + url.PathEscape(`\google.com`): "/%5Cgoogle.com/about",
		`/redirect/me/to-about/\google.com`:                      "/%5Cgoogle.com/about",
		"/redirect/me/to-about/%25google.com":                    "/%25google.com/about",
	} {
		resp := rawGet(t, addr, target)
		if resp.status != http.StatusTemporaryRedirect || resp.location != want {
			t.Errorf("GET %s = %d to %q, want 307 to %q", target, resp.status, resp.location, want)
		}
	}
	for _, target := range []string{"/redirect/me/to-about/%2fgoogle.com", "/redirect/me/to-about/%2F%2Fgoogle.com"} {
		resp := rawGet(t, addr, target)
		if resp.status >= 300 && resp.status < 400 && !sameSite(resp.location) {
			t.Errorf("GET %s = %d to %q, off the site", target, resp.status, resp.location)
		}
	}
}

// xssApp is a site whose not-found page, and one page taking any segment, both
// show something of the URL: the segment as text, and a link back to itself.
func xssApp(t *testing.T, dev bool) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		DevMode: dev,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/tag.html":      {Data: []byte(`<p>{{.}}</p><a href="{{pageURL "tag" "tag" .}}">self</a><a href="{{localeURL "tr"}}">tr</a>`)},
				"t/notfound.html": {Data: []byte(`<p>not found: {{.}}</p>`)},
			},
			Root: "t",
		},
		Locale: collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tag := collage.NewPage("tag").WithContent(collage.NewFragment("tag", "tag.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			return rc.Param("tag"), nil
		})).
		Build()).
		WithPath("en", "/{tag}").WithPath("tr", "/{tag}").Dynamic().Build()
	if err := app.RegisterPage(tag); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	notFound := collage.NewPage("notfound").WithContent(collage.NewFragment("notfound", "notfound.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			return rc.Request.URL.String(), nil
		})).
		Build()).Dynamic().Build()
	if err := app.RegisterNotFoundPage(notFound); err != nil {
		t.Fatalf("RegisterNotFoundPage: %v", err)
	}
	return app
}

// Next.js: test/production/pages-dir/production/test/security.ts ("should prevent
// URI based XSS attacks", and its single-quote, double-quote, semicolon, src and
// querystring variants). Wherever the URL is echoed — a page's text, a link built
// from it, the not-found page, development's error detail — it is echoed escaped,
// so no payload in it reaches the document as markup or as script.
func TestNextjs_AURLIsNeverEchoedAsMarkup(t *testing.T) {
	targets := []string{
		`/',document.body.innerHTML=%22INJECTED%22,'`,
		`/'-(document.body.innerHTML='INJECTED')-'`,
		`/%22-(document.body.innerHTML='INJECTED')-%22`,
		`/;%22-(document.body.innerHTML='INJECTED')-%22`,
		`/;'-(document.body.innerHTML='INJECTED')-'`,
		`/javascript:(document.body.innerHTML='INJECTED')`,
		`/?javascript=(document.body.innerHTML='INJECTED')`,
		`/?javascript=%22(document.body.innerHTML='INJECTED')%22`,
		`/%22%3E%3Cimg%20src=x%20onerror=alert('INJECTED')%3E`,
		`/tr/%22%3E%3Cimg%20src=x%20onerror=alert('INJECTED')%3E`,
		`/a/b/%22%3E%3Cimg%20src=x%20onerror=alert('INJECTED')%3E`,
		`/a/b/?x=%22%3E%3Cimg%20src=x%20onerror=alert('INJECTED')%3E`,
	}
	for _, dev := range []bool{false, true} {
		h := xssApp(t, dev).Handler()
		for _, target := range targets {
			rec := request(h, http.MethodGet, target)
			got := rec.Body.String()
			for _, raw := range []string{`innerHTML='INJECTED'`, `innerHTML="INJECTED"`, `<img`, `'INJECTED')-'`, `"INJECTED"`} {
				if strings.Contains(got, raw) {
					t.Errorf("dev %v: GET %s (%d) echoes %q unescaped:\n%s", dev, target, rec.Code, raw, got)
				}
			}
		}
	}
}

// Next.js: test/e2e/error-handler-not-found-req-url ("should log the correct
// request url and asPath for not found _error page"). The not-found page is
// rendered for the request that failed, and sees that request's own URL.
func TestNextjs_TheNotFoundPageSeesTheRequestedURL(t *testing.T) {
	rec := request(xssApp(t, false).Handler(), http.MethodGet, "/a/3")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not found: /a/3") {
		t.Errorf("GET /a/3 = %d:\n%s\nwant 404 naming /a/3", rec.Code, rec.Body.String())
	}
}

// Next.js: test/e2e/404-page/404-page.test.ts ("should render _error for a 500
// error still"). A page that fails is a 500 with the error page, not a 404 dressed
// as the not-found page.
func TestNextjs_AFailingPageIsNotTheNotFoundPage(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/err.html":      {Data: []byte(`<p>{{.}}</p>`)},
				"t/notfound.html": {Data: []byte(`<p>custom 404 page</p>`)},
			},
			Root: "t",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	failing := collage.NewPage("err").WithContent(collage.NewFragment("err", "err.html").WithData(collage.Load(
		func(context.Context, *collage.RenderContext) (string, error) {
			return "", errors.New("oops")
		})).
		Build()).WithPath("en", "/err").Dynamic().Build()
	if err := app.RegisterPage(failing); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	notFound := collage.NewPage("notfound").WithContent(collage.NewFragment("notfound", "notfound.html").Build()).Build()
	if err := app.RegisterNotFoundPage(notFound); err != nil {
		t.Fatalf("RegisterNotFoundPage: %v", err)
	}

	rec := request(app.Handler(), http.MethodGet, "/err")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /err = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "custom 404 page") {
		t.Errorf("GET /err served the not-found page:\n%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "oops") {
		t.Errorf("GET /err shows the error outside development:\n%s", rec.Body.String())
	}
}

// queryApp is a cached page that shows one query parameter, and names it as the
// only one its cache key varies on.
func queryApp(t *testing.T) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"t/q.html": {Data: []byte(`<p id="q">{{.}}</p>`)}},
			Root: "t",
		},
		Cache: collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := collage.NewPage("q").WithContent(collage.NewFragment("q", "q.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			return strconv.Quote(rc.Request.URL.Query().Get("test")), nil
		})).
		Build()).
		WithPath("en", "/").Incremental(time.Minute).WithCacheParams("test").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app
}

// Next.js: test/production/query-with-encoding ("should have correct query on
// SSR", for a new line, a space, a percent and a plus). Each query value reaches
// the page as it was encoded — and, the page being cached on that parameter, two
// values that differ only in how they were escaped are two entries, not one page
// served for the other.
func TestNextjs_QueryValuesKeepTheirEncodingThroughTheCache(t *testing.T) {
	h := queryApp(t).Handler()
	for _, round := range []string{"first", "cached"} {
		for query, want := range map[string]string{
			"test=abc%0A": "abc\n",
			"test=abc%20": "abc ",
			"test=abc+":   "abc ",
			"test=abc%25": "abc%",
			"test=abc%2B": "abc+",
		} {
			if got := shown(t, body(t, h, "/?"+query), "q"); got != strconv.Quote(want) {
				t.Errorf("%s GET /?%s shows %s, want %s", round, query, got, strconv.Quote(want))
			}
		}
	}
}

// paramApp has a page taking one segment, and a home page linking it with values
// that must survive the round trip through a URL.
func paramApp(t *testing.T, values ...string) *collage.App {
	t.Helper()
	var links strings.Builder
	for i := range values {
		fmt.Fprintf(&links, `<a id="l%d" href="{{pageURL "post" "slug" (index . %d)}}"></a>`, i, i)
	}
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/home.html": {Data: []byte(links.String())},
				"t/post.html": {Data: []byte(`<p id="slug">{{.}}</p>`)},
			},
			Root: "t",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	home := collage.NewPage("home").WithContent(collage.NewFragment("home", "home.html").WithData(collage.Load(
		func(context.Context, *collage.RenderContext) ([]string, error) {
			return values, nil
		})).
		Build()).WithPath("en", "/").Static().Build()
	post := collage.NewPage("post").WithContent(collage.NewFragment("post", "post.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			return strconv.Quote(rc.Param("slug")), nil
		})).
		Build()).WithPath("en", "/blog/{slug}").Static().
		WithStaticParams(func(context.Context, string) ([]map[string]string, error) {
			var sets []map[string]string
			for _, value := range values {
				sets = append(sets, map[string]string{"slug": value})
			}
			return sets, nil
		}).Build()
	for _, page := range []*collage.Page{home, post} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
	}
	return app
}

// shown returns the text of the <p> with the given id in page, unescaped.
// html/template escapes more than html.EscapeString does — "+" among it — so the
// markup is compared decoded rather than against a spelling of the escaping.
func shown(t *testing.T, page, id string) string {
	t.Helper()
	marker := `<p id="` + id + `">`
	start := strings.Index(page, marker)
	if start < 0 {
		t.Fatalf("no #%s in:\n%s", id, page)
	}
	text := page[start+len(marker):]
	return html.UnescapeString(text[:strings.Index(text, "</p>")])
}

// encodedSlugs are Next.js's prerender-fallback-encoding and prerender-encoding
// values: reserved characters, a literal percent, a literal "%2F" and "%252F"
// that are text rather than escapes, a space, and a script outside ASCII.
var encodedSlugs = []string{
	"%2Fmy-post%2F", "%252Fmy-post%252F", "+my-post+", "?my-post?", "&my-post&",
	"商業日語", "%E5%95%86", " my-post ", "sticks & stones", "100%", "mixed-商業日語", "#hash", `back\slash`,
}

// Next.js: test/e2e/prerender-fallback-encoding ("should respond with the
// prerendered pages correctly") and test/e2e/app-dir/prerender-encoding ("should
// serve an exact closed catch-all path containing a literal percent"). A link built
// for a value leads to the page with that value, character for character: a "%"
// in it is not read as an escape and a "?" is not read as the start of a query.
func TestNextjs_AParamValueRoundTripsThroughItsLink(t *testing.T) {
	h := paramApp(t, encodedSlugs...).Handler()
	home := body(t, h, "/")
	for i, value := range encodedSlugs {
		marker := fmt.Sprintf(`<a id="l%d" href="`, i)
		start := strings.Index(home, marker)
		if start < 0 {
			t.Fatalf("no link %d in:\n%s", i, home)
		}
		href := home[start+len(marker):]
		href = html.UnescapeString(href[:strings.IndexByte(href, '"')])
		if got := shown(t, body(t, h, href), "slug"); got != strconv.Quote(value) {
			t.Errorf("GET %s (the link for %q) shows %s", href, value, got)
		}
	}
}

// Next.js: test/e2e/prerender-fallback-encoding ("should output paths
// correctly"). A static build writes each value under a name of its own, one
// directory deep: a "%2F" that is text stays text, and nothing a value holds
// becomes a directory of its own or lands outside the one it belongs in.
func TestNextjs_ABuildWritesEachValueUnderItsOwnName(t *testing.T) {
	app := paramApp(t, encodedSlugs...)
	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, value := range encodedSlugs {
		file := filepath.Join(out, "blog", value, "index.html")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("value %q: %v", value, err)
			continue
		}
		if got := shown(t, string(data), "slug"); got != strconv.Quote(value) {
			t.Errorf("value %q: %s shows %s", value, file, got)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(out, "blog"))
	if len(entries) != len(encodedSlugs) {
		t.Errorf("blog/ holds %d entries, want %d", len(entries), len(encodedSlugs))
	}
}

// Next.js: test/production/prerender-invalid-paths ("should fail the build" when
// getStaticPaths returns keys the route has no parameter for). A value set naming a
// parameter the pattern does not have, or a value a browser would read as a path
// step, fails the build for that file and writes nothing for it.
func TestNextjs_ABuildRefusesValuesThatDoNotFitThePattern(t *testing.T) {
	for _, c := range []struct {
		name string
		set  map[string]string
	}{
		{"extra key", map[string]string{"slug": "hello", "foo": "bad"}},
		{"only other keys", map[string]string{"foo": "bad", "baz": "herro"}},
		{"dot dot", map[string]string{"slug": ".."}},
		{"dot", map[string]string{"slug": "."}},
		{"slash", map[string]string{"slug": "../escape"}},
		{"empty", map[string]string{"slug": ""}},
	} {
		app, err := collage.New(&collage.Config{
			Server: collage.ServerConfig{Host: "localhost", Port: 3000},
			Template: collage.TemplateConfig{
				FS:   fstest.MapFS{"t/post.html": {Data: []byte(`<p>post</p>`)}},
				Root: "t",
			},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		set := c.set
		post := collage.NewPage("post").WithContent(collage.NewFragment("post", "post.html").Build()).
			WithPath("en", "/blog/{slug}").Static().
			WithStaticParams(func(context.Context, string) ([]map[string]string, error) {
				return []map[string]string{set}, nil
			}).Build()
		if err := app.RegisterPage(post); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
		out := t.TempDir()
		builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := builder.Build(context.Background()); !errors.Is(err, collage.ErrRouteParams) {
			t.Errorf("%s: Build = %v, want ErrRouteParams", c.name, err)
		}
		var written []string
		_ = filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				written = append(written, path)
			}
			return nil
		})
		if len(written) != 0 {
			t.Errorf("%s: wrote %v", c.name, written)
		}
	}
}

// nextjsTraversalPayloads is every distinct path Next.js's file-serving test sends,
// taken from PayloadsAllTheThings' traversals-8-deep-exotic-encoding list: "..",
// "/" and "\" in every escaping, overlong UTF-8, %u, and double encoding, one to
// eight levels deep. JavaScript's "\\" is written here as the one backslash it is.
const nextjsTraversalPayloads = `
/%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%32%66%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%32%66test-file.txt
/%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65%%35%63%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65%%35%63test-file.txt
/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/test-file.txt
/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/test-file.txt
/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/test-file.txt
/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/test-file.txt
/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/test-file.txt
/%%32%65%%32%65/%%32%65%%32%65/%%32%65%%32%65/test-file.txt
/%%32%65%%32%65/%%32%65%%32%65/test-file.txt
/%%32%65%%32%65/test-file.txt
/%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252ftest-file.txt
/%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252ftest-file.txt
/%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252ftest-file.txt
/%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252ftest-file.txt
/%252e%252e%252f%252e%252e%252f%252e%252e%252f%252e%252e%252ftest-file.txt
/%252e%252e%252f%252e%252e%252f%252e%252e%252ftest-file.txt
/%252e%252e%252f%252e%252e%252ftest-file.txt
/%252e%252e%252ftest-file.txt
/%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255ctest-file.txt
/%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255ctest-file.txt
/%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255ctest-file.txt
/%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255ctest-file.txt
/%252e%252e%255c%252e%252e%255c%252e%252e%255c%252e%252e%255ctest-file.txt
/%252e%252e%255c%252e%252e%255c%252e%252e%255ctest-file.txt
/%252e%252e%255c%252e%252e%255ctest-file.txt
/%252e%252e%255ctest-file.txt
/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/test-file.txt
/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/test-file.txt
/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/test-file.txt
/%252e%252e/%252e%252e/%252e%252e/%252e%252e/%252e%252e/test-file.txt
/%252e%252e/%252e%252e/%252e%252e/%252e%252e/test-file.txt
/%252e%252e/%252e%252e/%252e%252e/test-file.txt
/%252e%252e/%252e%252e/test-file.txt
/%252e%252e/test-file.txt
/%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\test-file.txt
/%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\test-file.txt
/%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\test-file.txt
/%252e%252e\%252e%252e\%252e%252e\%252e%252e\%252e%252e\test-file.txt
/%252e%252e\%252e%252e\%252e%252e\%252e%252e\test-file.txt
/%252e%252e\%252e%252e\%252e%252e\test-file.txt
/%252e%252e\%252e%252e\test-file.txt
/%252e%252e\test-file.txt
/%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c0%25af%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c0%25aftest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259c%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae%25c1%259ctest-file.txt
/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae/test-file.txt
/%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\test-file.txt
/%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\test-file.txt
/%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\test-file.txt
/%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\test-file.txt
/%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\test-file.txt
/%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\test-file.txt
/%25c0%25ae%25c0%25ae\%25c0%25ae%25c0%25ae\test-file.txt
/%25c0%25ae%25c0%25ae\test-file.txt
/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
/%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
/%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
/%2e%2e%2f%2e%2e%2ftest-file.txt
/%2e%2e%2ftest-file.txt
/%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/%2e%2e%5c%2e%2e%5ctest-file.txt
/%2e%2e%5ctest-file.txt
/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/test-file.txt
/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/test-file.txt
/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/test-file.txt
/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/test-file.txt
/%2e%2e/%2e%2e/%2e%2e/%2e%2e/test-file.txt
/%2e%2e/%2e%2e/%2e%2e/test-file.txt
/%2e%2e/%2e%2e/test-file.txt
/%2e%2e/test-file.txt
/%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\test-file.txt
/%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\test-file.txt
/%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\test-file.txt
/%2e%2e\%2e%2e\%2e%2e\%2e%2e\%2e%2e\test-file.txt
/%2e%2e\%2e%2e\%2e%2e\%2e%2e\test-file.txt
/%2e%2e\%2e%2e\%2e%2e\test-file.txt
/%2e%2e\%2e%2e\test-file.txt
/%2e%2e\test-file.txt
/%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%2f%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%2ftest-file.txt
/%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e%c0%5c%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e%c0%5ctest-file.txt
/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e/%c0%2e%c0%2e/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e/test-file.txt
/%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\test-file.txt
/%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\test-file.txt
/%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\test-file.txt
/%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\test-file.txt
/%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\test-file.txt
/%c0%2e%c0%2e\%c0%2e%c0%2e\%c0%2e%c0%2e\test-file.txt
/%c0%2e%c0%2e\%c0%2e%c0%2e\test-file.txt
/%c0%2e%c0%2e\test-file.txt
/%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c0%af%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c0%aftest-file.txt
/%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae%c1%9c%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae%c1%9ctest-file.txt
/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae/%c0%ae%c0%ae/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae/test-file.txt
/%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\test-file.txt
/%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\test-file.txt
/%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\test-file.txt
/%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\test-file.txt
/%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\test-file.txt
/%c0%ae%c0%ae\%c0%ae%c0%ae\%c0%ae%c0%ae\test-file.txt
/%c0%ae%c0%ae\%c0%ae%c0%ae\test-file.txt
/%c0%ae%c0%ae\test-file.txt
/%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2215%uff0e%uff0e%u2215%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2215%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2215test-file.txt
/%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e%u2216%uff0e%uff0e%u2216%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e%u2216%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e%u2216test-file.txt
/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e/%uff0e%uff0e/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e/test-file.txt
/%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\test-file.txt
/%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\test-file.txt
/%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\test-file.txt
/%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\test-file.txt
/%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\test-file.txt
/%uff0e%uff0e\%uff0e%uff0e\%uff0e%uff0e\test-file.txt
/%uff0e%uff0e\%uff0e%uff0e\test-file.txt
/%uff0e%uff0e\test-file.txt
/..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66test-file.txt
/..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66test-file.txt
/..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66test-file.txt
/..%%32%66..%%32%66..%%32%66..%%32%66..%%32%66test-file.txt
/..%%32%66..%%32%66..%%32%66..%%32%66test-file.txt
/..%%32%66..%%32%66..%%32%66test-file.txt
/..%%32%66..%%32%66test-file.txt
/..%%32%66test-file.txt
/..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63test-file.txt
/..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63test-file.txt
/..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63test-file.txt
/..%%35%63..%%35%63..%%35%63..%%35%63..%%35%63test-file.txt
/..%%35%63..%%35%63..%%35%63..%%35%63test-file.txt
/..%%35%63..%%35%63..%%35%63test-file.txt
/..%%35%63..%%35%63test-file.txt
/..%%35%63test-file.txt
/..%252f..%252f..%252f..%252f..%252f..%252f..%252f..%252ftest-file.txt
/..%252f..%252f..%252f..%252f..%252f..%252f..%252ftest-file.txt
/..%252f..%252f..%252f..%252f..%252f..%252ftest-file.txt
/..%252f..%252f..%252f..%252f..%252ftest-file.txt
/..%252f..%252f..%252f..%252ftest-file.txt
/..%252f..%252f..%252ftest-file.txt
/..%252f..%252ftest-file.txt
/..%252ftest-file.txt
/..%255c..%255c..%255c..%255c..%255c..%255c..%255c..%255ctest-file.txt
/..%255c..%255c..%255c..%255c..%255c..%255c..%255ctest-file.txt
/..%255c..%255c..%255c..%255c..%255c..%255ctest-file.txt
/..%255c..%255c..%255c..%255c..%255ctest-file.txt
/..%255c..%255c..%255c..%255ctest-file.txt
/..%255c..%255c..%255ctest-file.txt
/..%255c..%255ctest-file.txt
/..%255ctest-file.txt
/..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25aftest-file.txt
/..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25aftest-file.txt
/..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25aftest-file.txt
/..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25aftest-file.txt
/..%25c0%25af..%25c0%25af..%25c0%25af..%25c0%25aftest-file.txt
/..%25c0%25af..%25c0%25af..%25c0%25aftest-file.txt
/..%25c0%25af..%25c0%25aftest-file.txt
/..%25c0%25aftest-file.txt
/..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259ctest-file.txt
/..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259ctest-file.txt
/..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259ctest-file.txt
/..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259ctest-file.txt
/..%25c1%259c..%25c1%259c..%25c1%259c..%25c1%259ctest-file.txt
/..%25c1%259c..%25c1%259c..%25c1%259ctest-file.txt
/..%25c1%259c..%25c1%259ctest-file.txt
/..%25c1%259ctest-file.txt
/..%2f..%2f..%2f..%2f..%2f..%2f..%2f..%2ftest-file.txt
/..%2f..%2f..%2f..%2f..%2f..%2f..%2ftest-file.txt
/..%2f..%2f..%2f..%2f..%2f..%2ftest-file.txt
/..%2f..%2f..%2f..%2f..%2ftest-file.txt
/..%2f..%2f..%2f..%2ftest-file.txt
/..%2f..%2f..%2ftest-file.txt
/..%2f..%2ftest-file.txt
/..%2ftest-file.txt
/..%5c..%5c..%5c..%5c..%5c..%5c..%5c..%5ctest-file.txt
/..%5c..%5c..%5c..%5c..%5c..%5c..%5ctest-file.txt
/..%5c..%5c..%5c..%5c..%5c..%5ctest-file.txt
/..%5c..%5c..%5c..%5c..%5ctest-file.txt
/..%5c..%5c..%5c..%5ctest-file.txt
/..%5c..%5c..%5ctest-file.txt
/..%5c..%5ctest-file.txt
/..%5ctest-file.txt
/..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2ftest-file.txt
/..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2ftest-file.txt
/..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2ftest-file.txt
/..%c0%2f..%c0%2f..%c0%2f..%c0%2f..%c0%2ftest-file.txt
/..%c0%2f..%c0%2f..%c0%2f..%c0%2ftest-file.txt
/..%c0%2f..%c0%2f..%c0%2ftest-file.txt
/..%c0%2f..%c0%2ftest-file.txt
/..%c0%2ftest-file.txt
/..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5ctest-file.txt
/..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5ctest-file.txt
/..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5ctest-file.txt
/..%c0%5c..%c0%5c..%c0%5c..%c0%5c..%c0%5ctest-file.txt
/..%c0%5c..%c0%5c..%c0%5c..%c0%5ctest-file.txt
/..%c0%5c..%c0%5c..%c0%5ctest-file.txt
/..%c0%5c..%c0%5ctest-file.txt
/..%c0%5ctest-file.txt
/..%c0%af..%c0%af..%c0%af..%c0%af..%c0%af..%c0%af..%c0%af..%c0%aftest-file.txt
/..%c0%af..%c0%af..%c0%af..%c0%af..%c0%af..%c0%af..%c0%aftest-file.txt
/..%c0%af..%c0%af..%c0%af..%c0%af..%c0%af..%c0%aftest-file.txt
/..%c0%af..%c0%af..%c0%af..%c0%af..%c0%aftest-file.txt
/..%c0%af..%c0%af..%c0%af..%c0%aftest-file.txt
/..%c0%af..%c0%af..%c0%aftest-file.txt
/..%c0%af..%c0%aftest-file.txt
/..%c0%aftest-file.txt
/..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9ctest-file.txt
/..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9ctest-file.txt
/..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9ctest-file.txt
/..%c1%9c..%c1%9c..%c1%9c..%c1%9c..%c1%9ctest-file.txt
/..%c1%9c..%c1%9c..%c1%9c..%c1%9ctest-file.txt
/..%c1%9c..%c1%9c..%c1%9ctest-file.txt
/..%c1%9c..%c1%9ctest-file.txt
/..%c1%9ctest-file.txt
/..%u2215..%u2215..%u2215..%u2215..%u2215..%u2215..%u2215..%u2215test-file.txt
/..%u2215..%u2215..%u2215..%u2215..%u2215..%u2215..%u2215test-file.txt
/..%u2215..%u2215..%u2215..%u2215..%u2215..%u2215test-file.txt
/..%u2215..%u2215..%u2215..%u2215..%u2215test-file.txt
/..%u2215..%u2215..%u2215..%u2215test-file.txt
/..%u2215..%u2215..%u2215test-file.txt
/..%u2215..%u2215test-file.txt
/..%u2215test-file.txt
/..%u2216..%u2216..%u2216..%u2216..%u2216..%u2216..%u2216..%u2216test-file.txt
/..%u2216..%u2216..%u2216..%u2216..%u2216..%u2216..%u2216test-file.txt
/..%u2216..%u2216..%u2216..%u2216..%u2216..%u2216test-file.txt
/..%u2216..%u2216..%u2216..%u2216..%u2216test-file.txt
/..%u2216..%u2216..%u2216..%u2216test-file.txt
/..%u2216..%u2216..%u2216test-file.txt
/..%u2216..%u2216test-file.txt
/..%u2216test-file.txt
/..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8test-file.txt
/..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8test-file.txt
/..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8test-file.txt
/..%uEFC8..%uEFC8..%uEFC8..%uEFC8..%uEFC8test-file.txt
/..%uEFC8..%uEFC8..%uEFC8..%uEFC8test-file.txt
/..%uEFC8..%uEFC8..%uEFC8test-file.txt
/..%uEFC8..%uEFC8test-file.txt
/..%uEFC8test-file.txt
/..%uF025..%uF025..%uF025..%uF025..%uF025..%uF025..%uF025..%uF025test-file.txt
/..%uF025..%uF025..%uF025..%uF025..%uF025..%uF025..%uF025test-file.txt
/..%uF025..%uF025..%uF025..%uF025..%uF025..%uF025test-file.txt
/..%uF025..%uF025..%uF025..%uF025..%uF025test-file.txt
/..%uF025..%uF025..%uF025..%uF025test-file.txt
/..%uF025..%uF025..%uF025test-file.txt
/..%uF025..%uF025test-file.txt
/..%uF025test-file.txt
/........................................................................../../../../../../../../test-file.txt
/........................................................................../../../../../../../test-file.txt
/........................................................................../../../../../../test-file.txt
/........................................................................../../../../../test-file.txt
/........................................................................../../../../test-file.txt
/........................................................................../../../test-file.txt
/........................................................................../../test-file.txt
/........................................................................../test-file.txt
/..........................................................................\..\..\..\..\..\..\..\test-file.txt
/..........................................................................\..\..\..\..\..\..\test-file.txt
/..........................................................................\..\..\..\..\..\test-file.txt
/..........................................................................\..\..\..\..\test-file.txt
/..........................................................................\..\..\..\test-file.txt
/..........................................................................\..\..\test-file.txt
/..........................................................................\..\test-file.txt
/..........................................................................\test-file.txt
/..../..../..../..../..../..../..../..../test-file.txt
/..../..../..../..../..../..../..../test-file.txt
/..../..../..../..../..../..../test-file.txt
/..../..../..../..../..../test-file.txt
/..../..../..../..../test-file.txt
/..../..../..../test-file.txt
/..../..../test-file.txt
/..../test-file.txt
/....\....\....\....\....\....\....\....\test-file.txt
/....\....\....\....\....\....\....\test-file.txt
/....\....\....\....\....\....\test-file.txt
/....\....\....\....\....\test-file.txt
/....\....\....\....\test-file.txt
/....\....\....\test-file.txt
/....\....\test-file.txt
/....\test-file.txt
/.../.../.../.../.../.../.../.../test-file.txt
/.../.../.../.../.../.../.../test-file.txt
/.../.../.../.../.../.../test-file.txt
/.../.../.../.../.../test-file.txt
/.../.../.../.../test-file.txt
/.../.../.../test-file.txt
/.../.../test-file.txt
/.../test-file.txt
/...\...\...\...\...\...\...\...\test-file.txt
/...\...\...\...\...\...\...\test-file.txt
/...\...\...\...\...\...\test-file.txt
/...\...\...\...\...\test-file.txt
/...\...\...\...\test-file.txt
/...\...\...\test-file.txt
/...\...\test-file.txt
/...\test-file.txt
/../../../../../../../../test-file.txt
/../../../../../../../test-file.txt
/../../../../../../test-file.txt
/../../../../../test-file.txt
/../../../../test-file.txt
/../../../test-file.txt
/../..//../..//../..//../..///test-file.txt
/../..//../..//../..//../..//test-file.txt
/../..//../..//../..//..///test-file.txt
/../..//../..//../..//../test-file.txt
/../..//../..//../..///test-file.txt
/../..//../..//../..//test-file.txt
/../..//../..//..///test-file.txt
/../..//../..//../test-file.txt
/../..//../..///test-file.txt
/../..//../..//test-file.txt
/../..//..///test-file.txt
/../..//../test-file.txt
/../..///test-file.txt
/../..//test-file.txt
/../../test-file.txt
/..//..//..//..//..//..//..//..//test-file.txt
/..//..//..//..//..//..//..//test-file.txt
/..//..//..//..//..//..//test-file.txt
/..//..//..//..//..//test-file.txt
/..//..//..//..//test-file.txt
/..//..//..//test-file.txt
/..//..//test-file.txt
/..///..///..///..///..///..///..///..///test-file.txt
/..///..///..///..///..///..///..///test-file.txt
/..///..///..///..///..///..///test-file.txt
/..///..///..///..///..///test-file.txt
/..///..///..///..///test-file.txt
/..///..///..///test-file.txt
/..///..///test-file.txt
/..///test-file.txt
/..//test-file.txt
/../test-file.txt
/..0x2f..0x2f..0x2f..0x2f..0x2f..0x2f..0x2f..0x2ftest-file.txt
/..0x2f..0x2f..0x2f..0x2f..0x2f..0x2f..0x2ftest-file.txt
/..0x2f..0x2f..0x2f..0x2f..0x2f..0x2ftest-file.txt
/..0x2f..0x2f..0x2f..0x2f..0x2ftest-file.txt
/..0x2f..0x2f..0x2f..0x2ftest-file.txt
/..0x2f..0x2f..0x2ftest-file.txt
/..0x2f..0x2ftest-file.txt
/..0x2ftest-file.txt
/..0x5c..0x5c..0x5c..0x5c..0x5c..0x5c..0x5c..0x5ctest-file.txt
/..0x5c..0x5c..0x5c..0x5c..0x5c..0x5c..0x5ctest-file.txt
/..0x5c..0x5c..0x5c..0x5c..0x5c..0x5ctest-file.txt
/..0x5c..0x5c..0x5c..0x5c..0x5ctest-file.txt
/..0x5c..0x5c..0x5c..0x5ctest-file.txt
/..0x5c..0x5c..0x5ctest-file.txt
/..0x5c..0x5ctest-file.txt
/..0x5ctest-file.txt
/..\..\..\..\..\..\..\..\test-file.txt
/..\..\..\..\..\..\..\test-file.txt
/..\..\..\..\..\..\test-file.txt
/..\..\..\..\..\test-file.txt
/..\..\..\..\test-file.txt
/..\..\..\test-file.txt
/..\..\\..\..\\..\..\\..\..\\\test-file.txt
/..\..\\..\..\\..\..\\..\..\\test-file.txt
/..\..\\..\..\\..\..\\..\\\test-file.txt
/..\..\\..\..\\..\..\\..\test-file.txt
/..\..\\..\..\\..\..\\\test-file.txt
/..\..\\..\..\\..\..\\test-file.txt
/..\..\\..\..\\..\\\test-file.txt
/..\..\\..\..\\..\test-file.txt
/..\..\\..\..\\\test-file.txt
/..\..\\..\..\\test-file.txt
/..\..\\..\\\test-file.txt
/..\..\\..\test-file.txt
/..\..\\\test-file.txt
/..\..\\test-file.txt
/..\..\test-file.txt
/..\\..\\..\\..\\..\\..\\..\\..\\test-file.txt
/..\\..\\..\\..\\..\\..\\..\\test-file.txt
/..\\..\\..\\..\\..\\..\\test-file.txt
/..\\..\\..\\..\\..\\test-file.txt
/..\\..\\..\\..\\test-file.txt
/..\\..\\..\\test-file.txt
/..\\..\\test-file.txt
/..\\\..\\\..\\\..\\\..\\\..\\\..\\\..\\\test-file.txt
/..\\\..\\\..\\\..\\\..\\\..\\\..\\\test-file.txt
/..\\\..\\\..\\\..\\\..\\\..\\\test-file.txt
/..\\\..\\\..\\\..\\\..\\\test-file.txt
/..\\\..\\\..\\\..\\\test-file.txt
/..\\\..\\\..\\\test-file.txt
/..\\\..\\\test-file.txt
/..\\\test-file.txt
/..\\test-file.txt
/..\test-file.txt
/./.././.././.././.././.././.././.././../test-file.txt
/./.././.././.././.././.././.././../test-file.txt
/./.././.././.././.././.././../test-file.txt
/./.././.././.././.././../test-file.txt
/./.././.././.././../test-file.txt
/./.././.././../test-file.txt
/./.././../test-file.txt
/./../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../../../../../../../../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../../../../../../../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../../../../../../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../../../../../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../../../../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../../../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../../test-file.txt
/././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././././../test-file.txt
/.//..//.//..//.//..//.//..//.//..//.//..//.//..//.//..//test-file.txt
/.//..//.//..//.//..//.//..//.//..//.//..//.//..//test-file.txt
/.//..//.//..//.//..//.//..//.//..//.//..//test-file.txt
/.//..//.//..//.//..//.//..//.//..//test-file.txt
/.//..//.//..//.//..//.//..//test-file.txt
/.//..//.//..//.//..//test-file.txt
/.//..//.//..//test-file.txt
/.//..//test-file.txt
/./\/././\/././\/././\/././\/././\/././\/././\/./test-file.txt
/./\/././\/././\/././\/././\/././\/././\/./test-file.txt
/./\/././\/././\/././\/././\/././\/./test-file.txt
/./\/././\/././\/././\/././\/./test-file.txt
/./\/././\/././\/././\/./test-file.txt
/./\/././\/././\/./test-file.txt
/./\/././\/./test-file.txt
/./\/./test-file.txt
/.\..\.\..\.\..\.\..\.\..\.\..\.\..\.\..\test-file.txt
/.\..\.\..\.\..\.\..\.\..\.\..\.\..\test-file.txt
/.\..\.\..\.\..\.\..\.\..\.\..\test-file.txt
/.\..\.\..\.\..\.\..\.\..\test-file.txt
/.\..\.\..\.\..\.\..\test-file.txt
/.\..\.\..\.\..\test-file.txt
/.\..\.\..\test-file.txt
/.\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\..\..\..\..\..\..\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\..\..\..\..\..\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\..\..\..\..\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\..\..\..\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\..\..\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\..\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\..\test-file.txt
/.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\.\..\test-file.txt
/.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\test-file.txt
/.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\test-file.txt
/.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\test-file.txt
/.\/\.\.\/\.\.\/\.\.\/\.\.\/\.\test-file.txt
/.\/\.\.\/\.\.\/\.\.\/\.\test-file.txt
/.\/\.\.\/\.\.\/\.\test-file.txt
/.\/\.\.\/\.\test-file.txt
/.\/\.\test-file.txt
/.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\test-file.txt
/.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\test-file.txt
/.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\test-file.txt
/.\\..\\.\\..\\.\\..\\.\\..\\.\\..\\test-file.txt
/.\\..\\.\\..\\.\\..\\.\\..\\test-file.txt
/.\\..\\.\\..\\.\\..\\test-file.txt
/.\\..\\.\\..\\test-file.txt
/.\\..\\test-file.txt
//..\/..\/..\/..\/..\/..\/..\/..\test-file.txt
//..\/..\/..\/..\/..\/..\/..\test-file.txt
//..\/..\/..\/..\/..\/..\test-file.txt
//..\/..\/..\/..\/..\test-file.txt
//..\/..\/..\/..\test-file.txt
//..\/..\/..\test-file.txt
//..\/..\test-file.txt
//..\test-file.txt
////%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
////%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
////%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
////%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
////%2e%2e%2f%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
////%2e%2e%2f%2e%2e%2f%2e%2e%2ftest-file.txt
////%2e%2e%2f%2e%2e%2ftest-file.txt
////%2e%2e%2ftest-file.txt
/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/test-file.txt
/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/test-file.txt
/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/test-file.txt
/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/test-file.txt
/0x2e0x2e/0x2e0x2e/0x2e0x2e/0x2e0x2e/test-file.txt
/0x2e0x2e/0x2e0x2e/0x2e0x2e/test-file.txt
/0x2e0x2e/0x2e0x2e/test-file.txt
/0x2e0x2e/test-file.txt
/0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x2f0x2e0x2e0x2f0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x2f0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x2ftest-file.txt
/0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5ctest-file.txt
/0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5ctest-file.txt
/0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5ctest-file.txt
/0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5ctest-file.txt
/0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5ctest-file.txt
/0x2e0x2e0x5c0x2e0x2e0x5c0x2e0x2e0x5ctest-file.txt
/0x2e0x2e0x5c0x2e0x2e0x5ctest-file.txt
/0x2e0x2e0x5ctest-file.txt
/0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\test-file.txt
/0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\test-file.txt
/0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\test-file.txt
/0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\test-file.txt
/0x2e0x2e\0x2e0x2e\0x2e0x2e\0x2e0x2e\test-file.txt
/0x2e0x2e\0x2e0x2e\0x2e0x2e\test-file.txt
/0x2e0x2e\0x2e0x2e\test-file.txt
/0x2e0x2e\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/../test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\..\test-file.txt
/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\..\test-file.txt
/\..%2f
/\..%2f\..%2f
/\..%2f\..%2f\..%2f
/\..%2f\..%2f\..%2f\..%2f
/\..%2f\..%2f\..%2f\..%2f\..%2f
/\..%2f\..%2f\..%2f\..%2f\..%2f\..%2f
/\..%2f\..%2f\..%2f\..%2f\..%2f\..%2f\..%2f
/\..%2f\..%2f\..%2f\..%2f\..%2f\..%2f\..%2f\..%2ftest-file.txt
/\../\../\../\../\../\../\../\../test-file.txt
/\../\../\../\../\../\../\../test-file.txt
/\../\../\../\../\../\../test-file.txt
/\../\../\../\../\../test-file.txt
/\../\../\../\../test-file.txt
/\../\../\../test-file.txt
/\../\../test-file.txt
/\../test-file.txt
/\\\%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/\\\%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/\\\%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/\\\%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/\\\%2e%2e%5c%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/\\\%2e%2e%5c%2e%2e%5c%2e%2e%5ctest-file.txt
/\\\%2e%2e%5c%2e%2e%5ctest-file.txt
/\\\%2e%2e%5ctest-file.txt
`
