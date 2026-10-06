package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// links is what the home page's data handler builds with rc.URL and
// rc.ActionURL, rendered one per line so each case can look for its own.
type links struct {
	Story, Tagged, Logout, Vote, Missing string
}

// rcURLApp registers a home page whose data handler builds links in Go, and a
// "go" action that redirects to the story whose id the form sent — the case a
// user-controlled value reaches a Location header through rc.URL.
func rcURLApp(t *testing.T) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/home.html": {Data: []byte("{{.Story}}\n{{.Tagged}}\n{{.Logout}}\n{{.Vote}}\n{{.Missing}}")},
				"t/page.html": {Data: []byte(`page`)},
			},
			Root: "t",
		},
		Locale: collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	home := collage.NewFragment("home", "home.html").WithData(collage.Load(func(_ context.Context, rc *collage.RenderContext) (links, error) {
		var view links
		var err error
		if view.Story, err = rc.URL("story", map[string]string{"id": "42"}); err != nil {
			return view, err
		}
		if view.Tagged, err = rc.URL("tag", map[string]string{"name": "çay"}); err != nil {
			return view, err
		}
		if view.Logout, err = rc.ActionURL("logout", nil); err != nil {
			return view, err
		}
		if view.Vote, err = rc.ActionURL("vote", map[string]string{"id": "42"}); err != nil {
			return view, err
		}
		_, missing := rc.URL("nope", nil)
		view.Missing = "missing: " + errorName(missing)
		return view, nil
	})).
		Build()

	goStory := func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
		location, err := rc.URL("story", map[string]string{"id": rc.Request.FormValue("id")})
		if err != nil {
			return nil, err
		}
		return collage.SeeOther(location), nil
	}
	page := func(name string) *collage.Fragment { return collage.NewFragment(name, "page.html").Build() }
	for _, p := range []*collage.Page{
		collage.NewPage("home").WithContent(home).WithPath("en", "/").WithPath("tr", "/").Build(),
		collage.NewPage("story").WithContent(page("story")).
			WithPath("en", "/stories/{id}").WithPath("tr", "/yazilar/{id}").Build(),
		collage.NewPage("tag").WithContent(page("tag")).WithPath("en", "/tags/{name}").Build(),
	} {
		if err := app.RegisterPage(p); err != nil {
			t.Fatalf("RegisterPage(%q): %v", p.Name, err)
		}
	}
	for _, a := range []*collage.Action{
		collage.NewAction("logout").WithPath("en", "/logout").WithPath("tr", "/cikis").
			WithMethods(http.MethodPost).WithHandler(goStory).Build(),
		collage.NewAction("vote").WithPath("en", "/stories/{id}/vote").
			WithMethods(http.MethodPost).WithHandler(goStory).Build(),
		collage.NewAction("go").WithPath("en", "/go").WithPath("tr", "/git").
			WithMethods(http.MethodPost).WithHandler(goStory).WithoutCSRF().Build(),
	} {
		if err := app.RegisterAction(a); err != nil {
			t.Fatalf("RegisterAction(%q): %v", a.Name, err)
		}
	}
	return app.Handler()
}

func errorName(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, collage.ErrUnknownRoute):
		return "ErrUnknownRoute"
	default:
		return err.Error()
	}
}

func TestRenderContextURL_InADataHandler(t *testing.T) {
	h := rcURLApp(t)
	cases := map[string][]string{
		// The render's own locale, as {{pageURL}} and {{actionURL}} use.
		"/": {"/stories/42", "/tags/%C3%A7ay", "/logout", "/stories/42/vote", "missing: ErrUnknownRoute"},
		// Its own path where the route has one, the default locale's where not.
		"/tr": {"/tr/yazilar/42", "/tags/%C3%A7ay", "/tr/cikis", "/stories/42/vote", "missing: ErrUnknownRoute"},
	}
	for target, want := range cases {
		got := strings.Split(body(t, h, target), "\n")
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("GET %s built %q, want %q", target, got, want)
		}
	}
}

func TestRenderContextURL_InAnAction(t *testing.T) {
	h := rcURLApp(t)
	for target, want := range map[string]string{"/go": "/stories/7", "/tr/git": "/tr/yazilar/7"} {
		rec := postID(h, target, "7")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("POST %s = %d Location %q, want 303 %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}

// A value from the request is only ever one escaped path segment of the named
// route: never a second slash that makes the path an off-site URL, never a path
// step, never a scheme or a host.
func TestRenderContextURL_KeepsAHostileValueInsideItsSegment(t *testing.T) {
	h := rcURLApp(t)
	for _, id := range []string{"/evil.example", "//evil.example", "../admin", "..", ".", "a/b", "", "x?next=//evil.example", "x#//evil.example"} {
		rec := postID(h, "/go", id)
		if rec.Code == http.StatusSeeOther {
			t.Errorf("id %q redirected to %q, want it refused", id, rec.Header().Get("Location"))
		}
	}
	for id, want := range map[string]string{
		`\evil.example`:          "/stories/%5Cevil.example",
		"%2F%2Fevil.example":     "/stories/%252F%252Fevil.example",
		"https:evil.example":     "/stories/https:evil.example",
		"x?next=evil.example":    "/stories/x%3Fnext=evil.example",
		"x#evil.example":         "/stories/x%23evil.example",
		"çay\r\nSet-Cookie: a=b": "/stories/%C3%A7ay%0D%0ASet-Cookie:%20a=b",
	} {
		rec := postID(h, "/go", id)
		location := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther || location != want {
			t.Errorf("id %q = %d Location %q, want 303 %q", id, rec.Code, location, want)
			continue
		}
		parsed, err := url.Parse(location)
		if err != nil || parsed.Scheme != "" || parsed.Host != "" || strings.HasPrefix(location, "//") {
			t.Errorf("id %q built %q, which leaves the site", id, location)
		}
	}
}

// Outside a render collage made — a RenderContext a test built by hand — there
// are no routes to ask, and saying so beats a panic.
func TestRenderContextURL_OutsideARender(t *testing.T) {
	rc := &collage.RenderContext{Request: httptest.NewRequest(http.MethodGet, "/", nil), Locale: "en"}
	if _, err := rc.URL("story", map[string]string{"id": "1"}); !errors.Is(err, collage.ErrUnknownRoute) {
		t.Errorf("URL outside a render: err = %v, want ErrUnknownRoute", err)
	}
	if _, err := rc.ActionURL("logout", nil); !errors.Is(err, collage.ErrUnknownRoute) {
		t.Errorf("ActionURL outside a render: err = %v, want ErrUnknownRoute", err)
	}
	var nilRC *collage.RenderContext
	if _, err := nilRC.URL("story", nil); !errors.Is(err, collage.ErrUnknownRoute) {
		t.Errorf("URL on a nil RenderContext: err = %v, want ErrUnknownRoute", err)
	}
}

func postID(h http.Handler, target, id string) *httptest.ResponseRecorder {
	form := url.Values{"id": {id}}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
