package collage_test

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// newFragmentApp is an application whose templates are files, for the tests of
// fragments served at their own URLs.
func newFragmentApp(t *testing.T, files map[string]string, configure ...func(*collage.Config)) *collage.App {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["t/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	cfg := &collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fsys, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	}
	for _, c := range configure {
		c(cfg)
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func serveFragment(app *collage.App, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

// A fragment path spelled like a page's path used to hide the page without a word:
// the fragment answered, the page was gone. Refused now, in either order.
func TestFragmentPath_CollidingWithAPageIsRefused(t *testing.T) {
	files := map[string]string{"a.html": `<p>page</p>`, "b.html": `<p>fragment</p>`}
	for _, fragmentFirst := range []bool{false, true} {
		app := newFragmentApp(t, files)
		about := collage.NewPage("about").WithContent(collage.NewFragment("a", "a.html").Build()).WithPath("en", "/about").Build()
		frag := collage.NewFragment("b", "b.html").Build()
		home := collage.NewPage("home").WithContent(frag).WithPath("en", "/").WithFragmentPath("en", "/about", frag).Build()

		first, second := about, home
		if fragmentFirst {
			first, second = home, about
		}
		if err := app.RegisterPage(first); err != nil {
			t.Fatalf("first RegisterPage: %v", err)
		}
		if err := app.RegisterPage(second); !errors.Is(err, collage.ErrDuplicateRoute) {
			t.Errorf("fragment first %v: second RegisterPage = %v, want ErrDuplicateRoute", fragmentFirst, err)
		}
	}
}

// Two pages opening fragments at one path is a coin toss, and refused like one.
func TestFragmentPath_TwoPagesCannotShareOne(t *testing.T) {
	app := newFragmentApp(t, map[string]string{"x.html": `<p>x</p>`})
	fa := collage.NewFragment("a", "x.html").Build()
	fb := collage.NewFragment("b", "x.html").Build()
	if err := app.RegisterPage(collage.NewPage("one").WithContent(fa).WithPath("en", "/one").WithFragmentPath("en", "/live", fa).Build()); err != nil {
		t.Fatal(err)
	}
	err := app.RegisterPage(collage.NewPage("two").WithContent(fb).WithPath("en", "/two").WithFragmentPath("en", "/live", fb).Build())
	if !errors.Is(err, collage.ErrDuplicateRoute) {
		t.Errorf("RegisterPage = %v, want ErrDuplicateRoute", err)
	}
}

// A redirect from a fragment's path would hide it; refused as a redirect over a
// page is.
func TestFragmentPath_ShadowedByARedirectIsRefused(t *testing.T) {
	app := newFragmentApp(t, map[string]string{"x.html": `<p>x</p>`})
	f := collage.NewFragment("a", "x.html").Build()
	page := collage.NewPage("one").WithContent(f).WithPath("en", "/one").
		WithFragmentPath("en", "/live", f).
		WithRedirect("/live", "/one", http.StatusMovedPermanently).Build()
	if err := app.RegisterPage(page); err == nil {
		t.Error("RegisterPage = nil, want the redirect over the fragment path refused")
	}
}

func fragmentURLSite(t *testing.T) *collage.App {
	t.Helper()
	app := newFragmentApp(t, map[string]string{
		"home.html":     `<div data-live="{{fragmentURL "home" "cpu"}}">{{slot "cpu"}}</div><a href="{{fragmentURLIn "tr" "home" "cpu"}}">tr</a><a href="{{fragmentURL "post" "comments" "slug" "hello"}}">c</a>`,
		"cpu.html":      `<p>cpu</p>`,
		"post.html":     `<p>post</p>`,
		"comments.html": `<p>comments</p>`,
	})
	cpu := collage.NewFragment("cpu", "cpu.html").Build()
	home := collage.NewFragment("home", "home.html").WithSlotFragment("cpu", cpu).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(home).
		WithPath("en", "/").WithPath("tr", "/").
		WithFragmentPath("en", "/live/cpu", cpu).
		WithFragmentPath("tr", "/canli/cpu", cpu).Build()); err != nil {
		t.Fatal(err)
	}
	comments := collage.NewFragment("comments", "comments.html").Build()
	if err := app.RegisterPage(collage.NewPage("post").WithContent(collage.NewFragment("post", "post.html").Build()).
		WithPath("en", "/posts/{slug}").
		WithFragmentPath("en", "/posts/{slug}/comments", comments).Build()); err != nil {
		t.Fatal(err)
	}
	return app
}

// A fragment's URL is built by name, as a page's is, so the path is written once.
func TestFragmentURL_Template(t *testing.T) {
	app := fragmentURLSite(t)
	body := serveFragment(app, httptest.NewRequest(http.MethodGet, "/", nil)).Body.String()
	for _, want := range []string{`data-live="/live/cpu"`, `href="/tr/canli/cpu"`, `href="/posts/hello/comments"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %s:\n%s", want, body)
		}
	}
	// The Turkish page links the Turkish path.
	body = serveFragment(app, httptest.NewRequest(http.MethodGet, "/tr", nil)).Body.String()
	if !strings.Contains(body, `data-live="/tr/canli/cpu"`) {
		t.Errorf("Turkish page does not link the Turkish fragment path:\n%s", body)
	}
	// And the link answers.
	if rec := serveFragment(app, httptest.NewRequest(http.MethodGet, "/posts/hello/comments", nil)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "comments") {
		t.Errorf("GET the built link = %d %q", rec.Code, rec.Body.String())
	}
}

// As strict as App.URL: a link that cannot be built is a failure, not a guess.
func TestFragmentURL_Strict(t *testing.T) {
	app := fragmentURLSite(t)
	if got, err := app.FragmentURL("post", "comments", "en", map[string]string{"slug": "a b"}); err != nil || got != "/posts/a%20b/comments" {
		t.Errorf("FragmentURL = %q, %v", got, err)
	}
	cases := []struct {
		page, fragment, locale string
		params                 map[string]string
		want                   error
	}{
		{"nope", "cpu", "en", nil, collage.ErrUnknownRoute},
		{"home", "nope", "en", nil, collage.ErrUnknownFragmentPath},
		{"home", "home", "en", nil, collage.ErrUnknownFragmentPath}, // rendered, but not opened
		{"post", "comments", "tr", nil, collage.ErrNoPathInLocale},
		{"post", "comments", "en", nil, collage.ErrRouteParams},
		{"home", "cpu", "de", nil, collage.ErrLocaleUnreachable},
	}
	for _, tc := range cases {
		if _, err := app.FragmentURL(tc.page, tc.fragment, tc.locale, tc.params); !errors.Is(err, tc.want) {
			t.Errorf("FragmentURL(%q, %q, %q) = %v, want %v", tc.page, tc.fragment, tc.locale, err, tc.want)
		}
	}
}

func TestFragmentURL_AmbiguousWhenOpenedTwice(t *testing.T) {
	app := newFragmentApp(t, map[string]string{"x.html": `<p>x</p>`})
	f := collage.NewFragment("a", "x.html").Build()
	if err := app.RegisterPage(collage.NewPage("one").WithContent(f).WithPath("en", "/one").
		WithFragmentPath("en", "/a", f).WithFragmentPath("en", "/b", f).Build()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.FragmentURL("one", "a", "en", nil); !errors.Is(err, collage.ErrAmbiguousFragmentPath) {
		t.Errorf("FragmentURL = %v, want ErrAmbiguousFragmentPath", err)
	}
}

// hoistingSite opens two fragments: one hoisting into the layout's head, one
// writing its own marker.
func hoistingSite(t *testing.T) *collage.App {
	t.Helper()
	app := newFragmentApp(t, map[string]string{
		"page.html":  `<html><head>{{hoist "head"}}</head><body>{{slot "chart"}}{{slot "own"}}</body></html>`,
		"chart.html": `<p>chart</p>`,
		"own.html":   `<div>{{hoist "head"}}</div>`,
	})
	hoister := collage.Effect(func(_ context.Context, rc *collage.RenderContext) error {
		rc.Hoist("head", "stylesheet:/static/chart.css", template.HTML(`<link rel="stylesheet" href="/static/chart.css">`))
		return nil
	})
	chart := collage.NewFragment("chart", "chart.html").WithDataHandler(hoister).Build()
	own := collage.NewFragment("own", "own.html").WithDataHandler(hoister).Build()
	page := collage.NewFragment("page", "page.html").WithSlotFragment("chart", chart).WithSlotFragment("own", own).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(page).WithPath("en", "/").
		WithFragmentPath("en", "/live/chart", chart).
		WithFragmentPath("en", "/live/own", own).Build()); err != nil {
		t.Fatal(err)
	}
	return app
}

// What a fragment hoists reaches the client in a template element ahead of the
// markup, carrying its area and key; a fragment that placed its own marker keeps
// it where it put it.
func TestFragmentPath_HoistsTravelWithTheResponse(t *testing.T) {
	app := hoistingSite(t)
	body := serveFragment(app, httptest.NewRequest(http.MethodGet, "/live/chart", nil)).Body.String()
	want := `<template data-collage-hoist="head" data-collage-key="stylesheet:/static/chart.css"><link rel="stylesheet" href="/static/chart.css"></template><p>chart</p>`
	if body != want {
		t.Errorf("body = %q\nwant   %q", body, want)
	}

	body = serveFragment(app, httptest.NewRequest(http.MethodGet, "/live/own", nil)).Body.String()
	if want := `<div><link rel="stylesheet" href="/static/chart.css"></div>`; body != want {
		t.Errorf("a fragment with its own marker: body = %q, want %q", body, want)
	}
}

// An unchanged fragment is answered 304 with no body to a reader holding its ETag.
func TestFragmentPath_ETag(t *testing.T) {
	app := hoistingSite(t)
	first := serveFragment(app, httptest.NewRequest(http.MethodGet, "/live/chart", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a fragment answer")
	}
	if got := first.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control = %q, want private, no-cache", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/live/chart", nil)
	req.Header.Set("If-None-Match", etag)
	again := serveFragment(app, req)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("revalidation = %d with %d bytes, want 304 and none", again.Code, again.Body.Len())
	}

	req = httptest.NewRequest(http.MethodGet, "/live/chart", nil)
	req.Header.Set("If-None-Match", `"something else"`)
	if rec := serveFragment(app, req); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Errorf("stale ETag = %d with %d bytes, want the body", rec.Code, rec.Body.Len())
	}
}

// RenderFragment hands a plugin the parts a stream needs, for exactly the
// fragments HTTP reaches.
func TestRenderFragment_Parts(t *testing.T) {
	app := newFragmentApp(t, map[string]string{
		"page.html": `<main>{{slot "a"}}{{slot "b"}}</main>`,
		"a.html":    `<p>{{.}}</p>`,
		"b.html":    `<p>fixed</p>`,
	})
	a := collage.NewFragment("a", "a.html").WithDataHandler(func(_ context.Context, rc *collage.RenderContext) (any, []string, error) {
		rc.Hoist("head", "k", template.HTML(`<meta name="k">`))
		return "cpu 12%", []string{"system:cpu"}, nil
	}).Build()
	b := collage.NewFragment("b", "b.html").Build()
	page := collage.NewFragment("page", "page.html").WithSlotFragment("a", a).WithSlotFragment("b", b).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(page).WithPath("en", "/").
		WithFragmentPath("en", "/live/a", a).WithFragmentPath("en", "/live/b", b).Build()); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	got, err := app.RenderFragment(r, collage.FragmentRequest{Page: "home", Fragment: "a"})
	if err != nil {
		t.Fatalf("RenderFragment: %v", err)
	}
	if string(got.HTML) != "<p>cpu 12%</p>" {
		t.Errorf("HTML = %q", got.HTML)
	}
	if len(got.DependencyTags) != 1 || got.DependencyTags[0] != "system:cpu" {
		t.Errorf("DependencyTags = %v", got.DependencyTags)
	}
	if len(got.Head) != 1 || got.Head[0].Key != "k" || got.Head[0].Area != "head" {
		t.Errorf("Head = %+v", got.Head)
	}
	if got.Shared {
		t.Error("a fragment with a data handler is Shared; its render may hold one reader's data")
	}

	fixed, err := app.RenderFragment(r, collage.FragmentRequest{Page: "home", Fragment: "b"})
	if err != nil || !fixed.Shared {
		t.Errorf("fixed fragment: Shared = %v, err %v; want shared", fixed != nil && fixed.Shared, err)
	}

	if _, err := app.RenderFragment(r, collage.FragmentRequest{Page: "home", Fragment: "page"}); !errors.Is(err, collage.ErrUnknownFragmentPath) {
		t.Errorf("a fragment the page did not open: err = %v, want ErrUnknownFragmentPath", err)
	}
}
