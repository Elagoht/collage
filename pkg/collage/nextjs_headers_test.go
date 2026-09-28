package collage_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// These pin, against collage, properties Next.js's redirect, header, cookie and
// method suites pin against Next.js. Each names the Next.js test it came from; the
// property is the same even where the mechanism is not.

// nextSite builds an app from a page with a form and whatever register adds, with
// the forgery check on when csrf is set.
func nextSite(t *testing.T, csrf bool, tweak func(*collage.Config), register func(*collage.App)) http.Handler {
	t.Helper()
	cfg := &collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/form.html": {Data: []byte(`<form method="post">{{csrfToken}}<button>go</button></form>`)},
				"t/p.html":    {Data: []byte(`<p>page</p>`)},
			},
			Root: "t",
		},
	}
	if csrf {
		cfg.Security = collage.SecurityConfig{CSRFKey: bytes.Repeat([]byte("k"), 32)}
	}
	if tweak != nil {
		tweak(cfg)
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	register(app)
	return app.Handler()
}

func plainPage(name, path string) *collage.Page {
	return collage.NewPage(name).WithContent(collage.NewFragment(name+"-content", "p.html").Build()).WithPath("en", path).Build()
}

func mustRegister(t *testing.T, app *collage.App, pages ...*collage.Page) {
	t.Helper()
	for _, p := range pages {
		if err := app.RegisterPage(p); err != nil {
			t.Fatalf("RegisterPage %q: %v", p.Name, err)
		}
	}
}

func do(h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for name, values := range header {
		for _, v := range values {
			r.Header.Add(name, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Next.js custom-routes: "should redirect with params successfully" and "should
// redirect successfully with provided statusCode".
func TestNextJS_RedirectSubstitutesParamsAndKeepsItsStatus(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308} {
		h := nextSite(t, false, nil, func(app *collage.App) {
			blog := collage.NewPage("blog").WithContent(collage.NewFragment("blog-content", "p.html").Build()).
				WithPath("en", "/blog/{id}").WithRedirect("/hello/{id}/another", "/blog/{id}", status).Build()
			mustRegister(t, app, blog)
		})
		w := do(h, http.MethodGet, "/hello/123/another", nil)
		if w.Code != status || w.Header().Get("Location") != "/blog/123" {
			t.Errorf("status %d: got %d Location %q, want %d /blog/123", status, w.Code, w.Header().Get("Location"), status)
		}
	}
}

// Next.js custom-routes: "should handle encoded value in the pathname correctly" —
// a captured "\google.com" lands in the Location escaped, so no browser reads the
// redirect as one to google.com.
func TestNextJS_RedirectedBackslashStaysOnTheSite(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		about := collage.NewPage("about").WithContent(collage.NewFragment("about-content", "p.html").Build()).
			WithPath("en", "/{x}/about").WithRedirect("/redirect/me/to-about/{x}", "/{x}/about", 307).Build()
		mustRegister(t, app, about)
	})
	w := do(h, http.MethodGet, "/redirect/me/to-about/%5Cgoogle.com", nil)
	location := w.Header().Get("Location")
	if w.Code != http.StatusTemporaryRedirect || location != "/%5Cgoogle.com/about" {
		t.Fatalf("got %d Location %q, want 307 /%%5Cgoogle.com/about", w.Code, location)
	}
	resolved, err := url.Parse("http://site.example/redirect")
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolved.Parse(location)
	if err != nil || target.Host != "site.example" {
		t.Errorf("Location %q resolves to host %q, want site.example", location, target.Host)
	}
}

// Next.js custom-routes: "should redirect successfully with catchall" — and a
// catch-all's tail, however it is spelled, never becomes a protocol-relative
// Location.
func TestNextJS_CatchAllRedirectNeverLeavesTheSite(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		docs := collage.NewPage("docs").WithContent(collage.NewFragment("docs-content", "p.html").Build()).
			WithPath("en", "/somewhere/{path...}").WithRedirect("/catchall-redirect/{path...}", "/somewhere/{path}", 307).Build()
		top := collage.NewPage("top").WithContent(collage.NewFragment("top-content", "p.html").Build()).
			WithPath("en", "/top").WithRedirect("/up/{path...}", "/{path}", 307).Build()
		mustRegister(t, app, docs, top)
	})
	if w := do(h, http.MethodGet, "/catchall-redirect/hello/world", nil); w.Code != 307 || w.Header().Get("Location") != "/somewhere/hello/world" {
		t.Errorf("catch-all: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, target := range []string{"/up//evil.example", "/up/%5Cevil.example", "/up/%2F%2Fevil.example", "/up/./%2Fevil.example"} {
		w := do(h, http.MethodGet, target, nil)
		location := w.Header().Get("Location")
		if strings.HasPrefix(location, "//") || strings.HasPrefix(location, `/\`) {
			t.Errorf("%s: Location %q leaves the site", target, location)
		}
	}
}

// Next.js custom-routes: "should redirect with hash successfully" — a fragment in
// the destination reaches the Location as written.
func TestNextJS_RedirectKeepsTheDestinationsFragment(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		codes := collage.NewPage("codes").WithContent(collage.NewFragment("codes-content", "p.html").Build()).
			WithPath("en", "/docs/v2/network/status-codes").
			WithRedirect("/docs/router-status/{code}", "/docs/v2/network/status-codes#{code}", 301).Build()
		mustRegister(t, app, codes)
	})
	w := do(h, http.MethodGet, "/docs/router-status/500", nil)
	if w.Code != 301 || w.Header().Get("Location") != "/docs/v2/network/status-codes#500" {
		t.Errorf("got %d %q", w.Code, w.Header().Get("Location"))
	}
}

// Next.js middleware-redirects: "does not include the locale in redirects by
// default" — a registered redirect, reached under a locale prefix, goes where it
// says and nowhere the locale adds.
func TestNextJS_RedirectAddsNoLocale(t *testing.T) {
	h := nextSite(t, false, func(cfg *collage.Config) {
		cfg.Locale = collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}}
	}, func(app *collage.App) {
		p := collage.NewPage("about").WithContent(collage.NewFragment("about-content", "p.html").Build()).
			WithPath("en", "/about").WithPath("tr", "/hakkinda").WithRedirect("/old-home", "/about", 307).Build()
		mustRegister(t, app, p)
	})
	for _, target := range []string{"/old-home", "/tr/old-home"} {
		if w := do(h, http.MethodGet, target, nil); w.Code != 307 || w.Header().Get("Location") != "/about" {
			t.Errorf("%s: got %d %q, want 307 /about", target, w.Code, w.Header().Get("Location"))
		}
	}
}

// Next.js custom-routes-i18n-index-redirect: the default locale's prefixed index
// is sent to the bare one, with no query string invented on the way.
func TestNextJS_DefaultLocaleIndexRedirect(t *testing.T) {
	h := nextSite(t, false, func(cfg *collage.Config) {
		cfg.Locale = collage.LocaleConfig{Default: "en", Supported: []string{"en", "fr"}}
	}, func(app *collage.App) {
		home := collage.NewPage("home").WithContent(collage.NewFragment("home-content", "p.html").Build()).
			WithPath("en", "/").WithPath("fr", "/").Build()
		mustRegister(t, app, home)
	})
	w := do(h, http.MethodGet, "/en", nil)
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/" {
		t.Errorf("/en: got %d %q, want 301 /", w.Code, w.Header().Get("Location"))
	}
	if w := do(h, http.MethodGet, "/fr", nil); w.Code != http.StatusOK {
		t.Errorf("/fr: got %d, want 200", w.Code)
	}
}

// actionSite registers one action at /act answering with result.
func actionSite(t *testing.T, csrf bool, methods []string, handler collage.ActionHandlerFunc, register func(*collage.App)) http.Handler {
	t.Helper()
	return nextSite(t, csrf, nil, func(app *collage.App) {
		b := collage.NewAction("act").WithPath("en", "/act").WithMethods(methods...).WithHandler(handler)
		if !csrf {
			b = b.WithoutCSRF()
		}
		if err := app.RegisterAction(b.Build()); err != nil {
			t.Fatalf("RegisterAction: %v", err)
		}
		if register != nil {
			register(app)
		}
	})
}

// Next.js gssp-redirect: "should apply statusCode 301/303 redirect", and the 307
// and 308 of temporary and permanent — the status an application chose is the one
// written; with none, a form post is answered 303.
func TestNextJS_ActionRedirectKeepsItsStatus(t *testing.T) {
	for _, status := range []int{0, 301, 302, 303, 307, 308} {
		h := actionSite(t, false, []string{http.MethodPost}, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return &collage.ActionResult{Status: status, Location: "/404"}, nil
		}, nil)
		want := status
		if want == 0 {
			want = http.StatusSeeOther
		}
		if w := do(h, http.MethodPost, "/act", nil); w.Code != want || w.Header().Get("Location") != "/404" {
			t.Errorf("status %d: got %d %q", status, w.Code, w.Header().Get("Location"))
		}
	}
}

// Next.js server-actions-relative-redirect: a relative destination is written as
// given, for the browser to resolve against the page that posted — and in a
// script's request, handed back rather than followed, with no Location a fetch
// would chase (middleware-redirects: "should redirect to data urls with data
// requests": x-nextjs-redirect set, location null).
func TestNextJS_ActionRelativeRedirect(t *testing.T) {
	h := actionSite(t, false, []string{http.MethodPost}, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
		return collage.SeeOther("../nested"), nil
	}, nil)
	if w := do(h, http.MethodPost, "/act", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "../nested" {
		t.Errorf("form post: %d %q", w.Code, w.Header().Get("Location"))
	}
	w := do(h, http.MethodPost, "/act", http.Header{"Collage-Fetch": {"1"}})
	if w.Code != http.StatusNoContent || w.Header().Get("Collage-Location") != "../nested" || w.Header().Get("Location") != "" {
		t.Errorf("fetch: %d Collage-Location %q Location %q", w.Code, w.Header().Get("Collage-Location"), w.Header().Get("Location"))
	}
}

// Next.js server-action-headers-redirect: "redirects after a server action that
// reads headers()" — reading the request and setting cookies does not cost the
// redirect, and every cookie the handler set is sent with it.
func TestNextJS_ActionReadsHeadersSetsCookiesAndRedirects(t *testing.T) {
	h := actionSite(t, false, []string{http.MethodPost}, func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
		header := http.Header{}
		header.Add("Set-Cookie", "a=1; Path=/")
		header.Add("Set-Cookie", "b=2; Path=/")
		return &collage.ActionResult{Location: "/destination?ua=" + url.QueryEscape(rc.Request.Header.Get("User-Agent")), Header: header}, nil
	}, nil)
	w := do(h, http.MethodPost, "/act", http.Header{"User-Agent": {"test"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/destination?ua=test" {
		t.Errorf("got %d %q", w.Code, w.Header().Get("Location"))
	}
	if cookies := w.Header().Values("Set-Cookie"); len(cookies) != 2 || cookies[0] != "a=1; Path=/" || cookies[1] != "b=2; Path=/" {
		t.Errorf("Set-Cookie = %q, want both, in order", cookies)
	}
}

// Next.js set-cookies: "should set two set-cookie headers", and four with the
// config's — cookies from middleware and from the handler, and the forgery
// token's own, each arrive as a Set-Cookie of its own; none is folded into or
// overwrites another.
func TestNextJS_EverySetCookieArrives(t *testing.T) {
	var formPage *collage.Page
	h := nextSite(t, true, nil, func(app *collage.App) {
		formPage = collage.NewPage("form").WithContent(collage.NewFragment("form-content", "form.html").Build()).
			WithPath("en", "/form").
			WithAction(http.MethodPost, func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
				header := http.Header{}
				header.Add("Set-Cookie", "handler1=a; Path=/")
				header.Add("Set-Cookie", "handler2=b; Path=/")
				return &collage.ActionResult{Status: http.StatusUnprocessableEntity, Page: rc.Page, Header: header}, nil
			}).Build()
		mustRegister(t, app, formPage)
		if err := app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "mw1", Value: "x", Path: "/"})
				http.SetCookie(w, &http.Cookie{Name: "mw2", Value: "y", Path: "/"})
				next.ServeHTTP(w, r)
			})
		}); err != nil {
			t.Fatal(err)
		}
	})

	names := func(w *httptest.ResponseRecorder) []string {
		var out []string
		for _, c := range w.Result().Cookies() {
			out = append(out, c.Name)
		}
		return out
	}

	page := do(h, http.MethodGet, "/form", nil)
	if got := names(page); strings.Join(got, ",") != "mw1,mw2,collage_csrf" {
		t.Errorf("page: cookies %v, want mw1, mw2 and the token's", got)
	}
	var token *http.Cookie
	for _, c := range page.Result().Cookies() {
		if c.Name != "mw1" && c.Name != "mw2" {
			token = c
		}
	}
	if token == nil {
		t.Fatalf("page set no token cookie: %v", page.Header().Values("Set-Cookie"))
	}

	// The re-rendered form carries a token too, and the handler's cookies beside it.
	body := url.Values{"_csrf": {token.Value}}
	r := httptest.NewRequest(http.MethodPost, "/form", strings.NewReader(body.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("post: %d %s", w.Code, w.Body.String())
	}
	got := names(w)
	want := map[string]bool{"mw1": true, "mw2": true, "handler1": true, "handler2": true, token.Name: true}
	if len(got) != len(want) {
		t.Errorf("post: cookies %v, want %d distinct", got, len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("post: unexpected cookie %q", name)
		}
	}
}

// Next.js no-duplicate-headers-middleware and -next-config: a header middleware set
// and the framework also sets is sent once, not twice.
func TestNextJS_NoDuplicateCacheControl(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		mustRegister(t, app, plainPage("home", "/"))
		if err := app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Add("Cache-Control", "max-age=1234")
				next.ServeHTTP(w, r)
			})
		}); err != nil {
			t.Fatal(err)
		}
	})
	if values := do(h, http.MethodGet, "/", nil).Header().Values("Cache-Control"); len(values) != 1 {
		t.Errorf("Cache-Control = %q, want one value", values)
	}
}

// Next.js middleware-request-header-overrides: headers middleware adds, deletes and
// updates on the request are what the handler reads, and none of the machinery
// leaks into the response. Next.js middleware-fetches-with-any-http-method: the
// middleware sees the request's own method.
func TestNextJS_MiddlewareRequestHeaderOverrides(t *testing.T) {
	var seen http.Header
	var method string
	h := actionSite(t, false, []string{http.MethodPost}, func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
		seen = rc.Request.Header.Clone()
		return collage.NoContent(http.StatusNoContent), nil
	}, func(app *collage.App) {
		if err := app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method = r.Method
				r = r.Clone(r.Context())
				r.Header.Set("X-From-Middleware", "hello-from-middleware")
				r.Header.Del("X-From-Client1")
				r.Header.Set("X-From-Client2", "new-value2")
				next.ServeHTTP(w, r)
			})
		}); err != nil {
			t.Fatal(err)
		}
	})
	w := do(h, http.MethodPost, "/act", http.Header{
		"X-From-Client1": {"old"}, "X-From-Client2": {"old"}, "X-From-Client3": {"old-value3"},
	})
	if w.Code != http.StatusNoContent || method != http.MethodPost {
		t.Fatalf("got %d, middleware saw %q", w.Code, method)
	}
	if seen.Get("X-From-Middleware") != "hello-from-middleware" || seen.Get("X-From-Client1") != "" ||
		seen.Get("X-From-Client2") != "new-value2" || seen.Get("X-From-Client3") != "old-value3" {
		t.Errorf("handler saw %v", seen)
	}
	for name := range w.Header() {
		if strings.HasPrefix(strings.ToLower(name), "x-from") {
			t.Errorf("request header %q leaked into the response", name)
		}
	}
}

// Next.js options-request: an OPTIONS request to a page or a route is answered
// with no body, and to a URL that is not one, 404.
func TestNextJS_OptionsRequest(t *testing.T) {
	h := actionSite(t, false, []string{http.MethodPost}, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
		return collage.NoContent(http.StatusNoContent), nil
	}, func(app *collage.App) {
		mustRegister(t, app, plainPage("static", "/static"))
	})
	for _, target := range []string{"/static", "/act"} {
		w := do(h, http.MethodOptions, target, nil)
		if w.Code != http.StatusNoContent || w.Body.Len() != 0 || w.Header().Get("Allow") == "" {
			t.Errorf("OPTIONS %s: %d Allow %q body %q", target, w.Code, w.Header().Get("Allow"), w.Body.String())
		}
	}
	if w := do(h, http.MethodOptions, "/non-existent-route", nil); w.Code != http.StatusNotFound {
		t.Errorf("OPTIONS to no route: %d, want 404", w.Code)
	}
}

// Next.js ipc-forbidden-headers: "should not error on content-length: 0 if request
// shouldn't contain a payload" and "should not error if expect header is
// included" — over a real connection, since both are about the wire.
func TestNextJS_ExpectContinueAndEmptyDelete(t *testing.T) {
	h := actionSite(t, false, []string{http.MethodPost, http.MethodDelete}, func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
		return &collage.ActionResult{Body: []byte("Hello, collage!"), ContentType: "text/plain; charset=utf-8"}, nil
	}, nil)
	server := httptest.NewServer(h)
	defer server.Close()

	post, err := http.NewRequest(http.MethodPost, server.URL+"/act", strings.NewReader("x=1"))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Expect", "100-continue")
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := server.Client().Do(post)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	_, _ = body.ReadFrom(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || body.String() != "Hello, collage!" {
		t.Errorf("Expect: 100-continue: %d %q", res.StatusCode, body.String())
	}

	del, err := http.NewRequest(http.MethodDelete, server.URL+"/act", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	del.ContentLength = 0
	del.Header.Set("Content-Length", "0")
	res, err = server.Client().Do(del)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("DELETE with Content-Length: 0: %d", res.StatusCode)
	}
}

// Next.js x-forwarded-headers: "should work with multiple x-forwarded-* headers" —
// a list-valued X-Forwarded-Proto is served, not refused.
func TestNextJS_ListValuedForwardedProto(t *testing.T) {
	h := nextSite(t, true, nil, func(app *collage.App) {
		mustRegister(t, app, collage.NewPage("form").WithContent(collage.NewFragment("form-content", "form.html").Build()).WithPath("en", "/").Build())
	})
	w := do(h, http.MethodGet, "/", http.Header{"X-Forwarded-Proto": {"https, https"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
}

// Next.js options-request: "should return a 405 status code when invoking
// /pages-page/static" with a method it does not answer — here a POST to a page
// with no action — naming what the URL does accept.
func TestNextJS_UnansweredMethodIs405WithAllow(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		mustRegister(t, app, plainPage("static", "/static"))
	})
	w := do(h, http.MethodPost, "/static", nil)
	if w.Code != http.StatusMethodNotAllowed || !strings.Contains(w.Header().Get("Allow"), http.MethodGet) {
		t.Errorf("got %d Allow %q, want 405 naming GET", w.Code, w.Header().Get("Allow"))
	}
}

// Next.js middleware-redirects: "should have relative path for same host redirect"
// — a middleware's redirect to a path of the site is written as that path, not
// made absolute against a Host the request chose.
func TestNextJS_MiddlewareRedirectStaysRelative(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		mustRegister(t, app, plainPage("another", "/another"))
		if err := app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/to" {
					http.Redirect(w, r, r.URL.Query().Get("pathname"), http.StatusFound)
					return
				}
				next.ServeHTTP(w, r)
			})
		}); err != nil {
			t.Fatal(err)
		}
	})
	w := do(h, http.MethodGet, "http://evil.example/to?pathname=/another", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/another" {
		t.Errorf("got %d %q, want 302 /another", w.Code, w.Header().Get("Location"))
	}
}

// Next.js custom-routes: "should redirect successfully with permanent: false" —
// and for HEAD as for GET: the same status and Location, with no body.
func TestNextJS_RedirectAnswersHeadLikeGet(t *testing.T) {
	h := nextSite(t, false, nil, func(app *collage.App) {
		home := collage.NewPage("home").WithContent(collage.NewFragment("home-content", "p.html").Build()).
			WithPath("en", "/").WithRedirect("/redirect1", "/", 0).Build()
		mustRegister(t, app, home)
	})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := do(h, method, "/redirect1", nil)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/" || w.Body.Len() != 0 {
			t.Errorf("%s: got %d %q body %q, want 302 / and no body", method, w.Code, w.Header().Get("Location"), w.Body.String())
		}
	}
}
