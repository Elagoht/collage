package collage_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// These are properties Next.js's own test suite pins about caching, output
// escaping and development mode, restated for collage. Each test names the
// Next.js test it was harvested from.

// nextjsApp is a site with no template files: every page here is built from
// inline fragments, so the test says in one place what it renders.
func nextjsApp(t *testing.T, dev bool) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "example.com", Port: 0},
		DevMode:  dev,
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func nextjsGet(h http.Handler, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for name, values := range header {
		req.Header[name] = values
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Harvested from e2e/app-dir/headers-static-bailout: a page whose handler may read
// the request is not rendered once and shared, and one that renders only fixed
// values is. Collage's analog of the bailout is the resolved strategy: a page
// declaring none is dynamic as soon as anything in it has a data handler, because
// nothing outside the handler can tell whether it read a cookie.
func TestNextjs_AHandlerThatMayReadTheRequestIsNotShared(t *testing.T) {
	app := nextjsApp(t, false)
	reader := collage.NewInlineFragment("who", `<p>{{.}}</p>`).WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			c, err := rc.Request.Cookie("user")
			if err != nil {
				return "nobody", nil
			}
			return c.Value, nil
		})).
		Build()
	fixed := collage.NewInlineFragment("fixed", `<p>{{.}}</p>`).WithData(collage.Value("same")).Build()
	for _, page := range []*collage.Page{
		collage.NewPage("reads").WithContent(reader).WithPath("en", "/reads").Build(),
		collage.NewPage("fixed").WithContent(fixed).WithPath("en", "/fixed").Build(),
	} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
	}
	h := app.Handler()

	alice := nextjsGet(h, "/reads", http.Header{"Cookie": {"user=alice"}})
	bob := nextjsGet(h, "/reads", http.Header{"Cookie": {"user=bob"}})
	if !strings.Contains(alice.Body.String(), "alice") || !strings.Contains(bob.Body.String(), "bob") {
		t.Fatalf("bodies = %q, %q: one reader was served another's render", alice.Body, bob.Body)
	}
	if cc := bob.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("a page reading the request: Cache-Control = %q, want no-store", cc)
	}
	if cc := nextjsGet(h, "/fixed", nil).Header().Get("Cache-Control"); !strings.HasPrefix(cc, "public") {
		t.Errorf("a page of fixed values: Cache-Control = %q, want it shared", cc)
	}
}

// Harvested from e2e/app-dir/searchparams-static-bailout: two queries to one
// cached path are two representations unless the page says which parameters it
// reads.
func TestNextjs_TheQueryIsPartOfACachedPagesKey(t *testing.T) {
	app := nextjsApp(t, false)
	var renders atomic.Int32
	echo := collage.NewInlineFragment("echo", `<p>{{.}}</p>`).WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			renders.Add(1)
			return rc.Request.URL.Query().Get("q"), nil
		})).
		Build()
	if err := app.RegisterPage(collage.NewPage("search").WithContent(echo).WithPath("en", "/search").
		Incremental(time.Hour).Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	h := app.Handler()
	if got := nextjsGet(h, "/search?q=one", nil).Body.String(); !strings.Contains(got, "one") {
		t.Fatalf("?q=one = %q", got)
	}
	if got := nextjsGet(h, "/search?q=two", nil).Body.String(); !strings.Contains(got, "two") {
		t.Fatalf("?q=two = %q, want its own render rather than ?q=one's", got)
	}
	nextjsGet(h, "/search?q=one", nil)
	if n := renders.Load(); n != 2 {
		t.Errorf("renders = %d, want 2: the repeated query is a hit", n)
	}
}

// Harvested from e2e/app-dir/draft-mode and e2e/draft-mode: a preview renders
// fresh, is kept by nothing between the server and the editor, and does not
// become the page the next reader is served.
func TestNextjs_APreviewIsNeitherServedFromNorWrittenToTheCache(t *testing.T) {
	app := nextjsApp(t, false)
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Preview") == "1" {
				if err := collage.SkipCache(r); err != nil {
					t.Errorf("SkipCache: %v", err)
				}
			}
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	article := collage.NewInlineFragment("article", `<p>{{.}}</p>`).WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			if rc.Request.Header.Get("X-Preview") == "1" {
				return "draft", nil
			}
			return "published", nil
		})).
		Build()
	if err := app.RegisterPage(collage.NewPage("article").WithContent(article).WithPath("en", "/a").
		Incremental(time.Hour).Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	h := app.Handler()

	if got := nextjsGet(h, "/a", nil).Body.String(); !strings.Contains(got, "published") {
		t.Fatalf("first read = %q", got)
	}
	preview := nextjsGet(h, "/a", http.Header{"X-Preview": {"1"}})
	if !strings.Contains(preview.Body.String(), "draft") {
		t.Errorf("preview = %q, want the draft rendered fresh rather than the cached page", preview.Body)
	}
	if cc := preview.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("preview Cache-Control = %q, want private, no-store", cc)
	}
	if got := nextjsGet(h, "/a", nil).Body.String(); !strings.Contains(got, "published") {
		t.Errorf("read after the preview = %q, want the published page", got)
	}
}

// Harvested from e2e/app-dir/segment-cache/cdn-cache-busting ("ignores invalid
// RSC header values when serving a document request"): a request header the
// framework reads elsewhere does not change what a page URL answers with, so a
// CDN keyed on the URL alone cannot be handed the wrong representation. Collage's
// one such header is Collage-Fetch, and it shapes only an action's redirect.
func TestNextjs_TheFetchHeaderDoesNotChangeAPage(t *testing.T) {
	app := nextjsApp(t, false)
	body := collage.NewInlineFragment("body", `<p id="target">Target page</p>`).Build()
	if err := app.RegisterPage(collage.NewPage("target").WithContent(body).WithPath("en", "/target").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	h := app.Handler()
	plain := nextjsGet(h, "/target", nil)
	marked := nextjsGet(h, "/target", http.Header{"Collage-Fetch": {"1"}})
	if marked.Code != plain.Code || marked.Body.String() != plain.Body.String() ||
		marked.Header().Get("Content-Type") != plain.Header().Get("Content-Type") {
		t.Errorf("with Collage-Fetch: %d %q %q; without: %d %q %q", marked.Code, marked.Header().Get("Content-Type"),
			marked.Body, plain.Code, plain.Header().Get("Content-Type"), plain.Body)
	}
}

// Harvested from e2e/app-dir/csp-nonce-segment-scripts and
// e2e/app-dir/next-dynamic-csp-nonce, whose concern is a page that runs under a
// strict Content-Security-Policy without violations. Collage's answer is to write
// no script of its own into a production page at all — not for a form, not for a
// hoisted head — so there is nothing a nonce would have to reach. The development
// reload script is the one exception, and development only.
func TestNextjs_AProductionPageCarriesNoFrameworkScript(t *testing.T) {
	build := func(dev bool) string {
		app, err := collage.New(&collage.Config{
			Server:   collage.ServerConfig{Host: "example.com", Port: 0},
			DevMode:  dev,
			Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
			Security: collage.SecurityConfig{CSRFKey: []byte(strings.Repeat("k", 32))},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		layout := collage.NewInlineFragment("layout", `<html><head>{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`).Build()
		form := collage.NewInlineFragment("form", `<form method="post">{{csrfToken}}<button>go</button></form>`).
			WithTitle("Form").Build()
		page := collage.NewPage("form").WithLayouts(layout).WithContent(form).WithPath("en", "/form").
			WithAction(http.MethodPost, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
				return collage.SeeOther("/form"), nil
			}).Build()
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
		return nextjsGet(app.Handler(), "/form", nil).Body.String()
	}

	prod := build(false)
	if !strings.Contains(prod, "<form") {
		t.Fatalf("production body = %q, want the form", prod)
	}
	lower := strings.ToLower(prod)
	for _, bad := range []string{"<script", " onclick=", " onload=", "javascript:"} {
		if strings.Contains(lower, bad) {
			t.Errorf("production page carries %q, which a strict CSP refuses: %s", bad, prod)
		}
	}
	if dev := build(true); !strings.Contains(dev, "<script") {
		t.Errorf("development page = %q, want the reload script (the exception this test pins as dev-only)", dev)
	}
}

// Harvested from development/error-overlay and development/app-dir/error-overlay:
// what went wrong is shown to the developer in development and never to a reader
// in production — neither on the page that degraded around a broken part, nor on
// the error page of a page that could not render.
func TestNextjs_ErrorDetailIsDevelopmentOnly(t *testing.T) {
	const secret = "dial tcp 10.0.0.7:5432: password authentication failed"
	build := func(dev bool) http.Handler {
		app := nextjsApp(t, dev)
		broken := collage.NewInlineFragment("broken", `<p>never</p>`).WithData(collage.Effect(
			func(context.Context, *collage.RenderContext) error {
				return errors.New(secret)
			})).
			Build()
		host := collage.NewInlineFragment("host", `<html><body><main>ok</main>{{slot "side"}}</body></html>`).
			WithSlotFragment("side", broken).Build()
		fatal := collage.NewInlineFragment("fatal", `<p>never</p>`).WithData(collage.Effect(
			func(context.Context, *collage.RenderContext) error {
				return errors.New(secret)
			})).
			Required().Build()
		for _, page := range []*collage.Page{
			collage.NewPage("degraded").WithContent(host).WithPath("en", "/degraded").Build(),
			collage.NewPage("fatal").WithContent(fatal).WithPath("en", "/fatal").Build(),
		} {
			if err := app.RegisterPage(page); err != nil {
				t.Fatalf("RegisterPage: %v", err)
			}
		}
		return app.Handler()
	}

	prod := build(false)
	for _, path := range []string{"/degraded", "/fatal"} {
		rec := nextjsGet(prod, path, nil)
		if strings.Contains(rec.Body.String(), "10.0.0.7") || strings.Contains(rec.Body.String(), "password") {
			t.Errorf("production %s (%d) carries the error: %s", path, rec.Code, rec.Body)
		}
		if rec.Header().Get("X-Collage-Render-Time") != "" {
			t.Errorf("production %s advertises its render time", path)
		}
	}
	if fatal := nextjsGet(prod, "/fatal", nil); fatal.Code != http.StatusInternalServerError {
		t.Errorf("production /fatal = %d, want 500", fatal.Code)
	}

	dev := build(true)
	if got := nextjsGet(dev, "/degraded", nil).Body.String(); !strings.Contains(got, "password authentication failed") {
		t.Errorf("development /degraded = %q, want the failure shown to the developer", got)
	}
}

// Harvested from development/dev-cache-control, which pins what the development
// tooling itself answers: its reload channel exists only in development, and
// what it serves there is kept by no cache.
func TestNextjs_TheReloadChannelIsDevelopmentOnly(t *testing.T) {
	for _, path := range []string{"/_collage/reload", "/_collage/reload-worker.js"} {
		if rec := nextjsGet(nextjsApp(t, false).Handler(), path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("production %s = %d, want 404", path, rec.Code)
		}
	}
	rec := nextjsGet(nextjsApp(t, true).Handler(), "/_collage/reload-worker.js", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("development worker = %d, Cache-Control %q; want 200 and no-store", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

// Harvested from e2e/app-dir/script-before-interactive-xss: a value placed in an
// inline script cannot close the script and open another, and the framework's own
// passes over the finished page — hoisting, the development overlay's insertion
// before </body> — do not undo the escaping.
func TestNextjs_DataInAnInlineScriptCannotBreakOut(t *testing.T) {
	const payload = `</script><script>window.__xss=true</script>`
	app := nextjsApp(t, false)
	layout := collage.NewInlineFragment("layout", `<html><head>{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`).Build()
	page := collage.NewInlineFragment("page", `<script>var v = {{.}};</script><p title="{{.}}">{{.}}</p>`).WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			rc.HoistTitle(payload)
			rc.HoistMeta("description", payload)
			return payload, nil
		})).
		Build()
	if err := app.RegisterPage(collage.NewPage("xss").WithLayouts(layout).WithContent(page).WithPath("en", "/xss").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	body := nextjsGet(app.Handler(), "/xss", nil).Body.String()
	if strings.Contains(body, "<script>window.__xss") || strings.Count(body, "<script>") != 1 {
		t.Errorf("the payload opened a script of its own: %s", body)
	}
}

// Harvested from e2e/app-dir/javascript-urls: a link the framework builds cannot
// be made into a javascript: URL, or into a protocol-relative one, by the value
// that fills it.
func TestNextjs_ABuiltLinkIsAlwaysAPathOnThisSite(t *testing.T) {
	app := nextjsApp(t, false)
	post := collage.NewInlineFragment("post", `<p>post</p>`).Build()
	files := collage.NewInlineFragment("files", `<p>files</p>`).Build()
	for _, page := range []*collage.Page{
		collage.NewPage("post").WithContent(post).WithPath("en", "/{slug}").Build(),
		collage.NewPage("files").WithContent(files).WithPath("en", "/f/{path...}").Build(),
	} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
	}
	for _, slug := range []string{"javascript:alert(1)", `\evil.com`, "%2F%2Fevil.com", " javascript:x"} {
		got, err := app.URL("post", "en", map[string]string{"slug": slug})
		if err != nil {
			continue
		}
		if !strings.HasPrefix(got, "/") || strings.HasPrefix(got, "//") || strings.HasPrefix(got, `/\`) {
			t.Errorf("URL(slug=%q) = %q, want a path on this site", slug, got)
		}
	}
	for _, path := range []string{"/evil.com", "//evil.com", `\evil.com/x`, "javascript:alert(1)/x"} {
		got, err := app.URL("files", "en", map[string]string{"path": path})
		if err != nil {
			continue
		}
		if !strings.HasPrefix(got, "/f/") || strings.Contains(got, `\`) {
			t.Errorf("URL(path=%q) = %q, want a path under /f/", path, got)
		}
	}
}

// Harvested from test/unit/htmlescape.test.ts: JSON an action answers with has
// "<", ">", "&" and the two JavaScript line terminators escaped, so the same bytes
// are safe inlined into a page's script as well as served, and still parse back
// to what was sent.
func TestNextjs_JSONOfEscapesWhatAScriptWouldMisread(t *testing.T) {
	sent := map[string]string{"evil": "<script></script>&\u2028\u2029"}
	result, err := collage.JSONOf(http.StatusOK, sent)
	if err != nil {
		t.Fatalf("JSONOf: %v", err)
	}
	const want = "{\"evil\":\"\\u003cscript\\u003e\\u003c/script\\u003e\\u0026\\u2028\\u2029\"}"
	if got := string(result.Body); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	var back map[string]string
	if err := json.Unmarshal(result.Body, &back); err != nil || back["evil"] != sent["evil"] {
		t.Errorf("parsed back = %q (%v), want %q", back["evil"], err, sent["evil"])
	}
	if result.ContentType != "application/json" {
		t.Errorf("ContentType = %q", result.ContentType)
	}
}
