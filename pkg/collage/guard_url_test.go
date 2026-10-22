package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// guardURLApp has a login page in both locales, an about page in English only,
// a panel guarded by a redirect to login, a page guarded by a redirect to about,
// an action on its own URL with the same login guard, and a middleware that
// records where login is as it sees the request.
func guardURLApp(t *testing.T) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/page.html": {Data: []byte(`page`)}}, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	content := func(name string) *collage.Fragment { return collage.NewFragment(name, "page.html").Build() }
	toLogin := func(ctx context.Context, _ *http.Request) (*collage.GuardDecision, error) {
		return collage.GuardSeeOtherPage(ctx, "login", nil)
	}
	toAbout := func(ctx context.Context, _ *http.Request) (*collage.GuardDecision, error) {
		return collage.GuardSeeOtherPage(ctx, "about", nil)
	}
	toNowhere := func(ctx context.Context, _ *http.Request) (*collage.GuardDecision, error) {
		return collage.GuardSeeOtherPage(ctx, "nope", nil)
	}
	pages := []*collage.Page{
		collage.NewPage("login").WithContent(content("login")).WithPath("en", "/login").WithPath("tr", "/giris").Build(),
		collage.NewPage("about").WithContent(content("about")).WithPath("en", "/about").Build(),
		collage.NewPage("panel").WithContent(collage.NewFragment("panel", "page.html").WithGuard(toLogin).Build()).
			WithPath("en", "/panel").WithPath("tr", "/panel").Dynamic().Build(),
		collage.NewPage("team").WithContent(collage.NewFragment("team", "page.html").WithGuard(toAbout).Build()).
			WithPath("en", "/team").WithPath("tr", "/ekip").Dynamic().Build(),
		collage.NewPage("broken").WithContent(collage.NewFragment("broken", "page.html").WithGuard(toNowhere).Build()).
			WithPath("en", "/broken").Dynamic().Build(),
	}
	for _, p := range pages {
		if err := app.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	act := collage.NewAction("fire").WithPath("en", "/api/fire").WithPath("tr", "/api/ates").
		WithMethods(http.MethodPost).WithGuard(toLogin).WithoutCSRF().
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.SeeOther("/"), nil
		}).
		Build()
	if err := app.RegisterAction(act); err != nil {
		t.Fatal(err)
	}
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			location, err := collage.URL(r.Context(), "login", nil)
			if err != nil {
				location = "error: " + err.Error()
			}
			w.Header().Set("X-Login", location)
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

// A guard sends the reader to a page by its name, in the locale the request is
// in, falling back to the default locale where the page has no path.
func TestGuardSeeOtherPage(t *testing.T) {
	handler := guardURLApp(t)
	for _, c := range []struct {
		method, path string
		status       int
		location     string
	}{
		{http.MethodGet, "/panel", http.StatusSeeOther, "/login"},
		{http.MethodGet, "/tr/panel", http.StatusSeeOther, "/tr/giris"},
		// No Turkish about: the default locale's, as {{pageURL}} falls back.
		{http.MethodGet, "/tr/ekip", http.StatusSeeOther, "/about"},
		// An action's own guard is asked in the action's locale too.
		{http.MethodPost, "/tr/api/ates", http.StatusSeeOther, "/tr/giris"},
		// An unknown name is the guard's error: the request fails, nobody passes.
		{http.MethodGet, "/broken", http.StatusInternalServerError, ""},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status || rec.Header().Get("Location") != c.location {
			t.Errorf("%s %s = %d to %q, want %d to %q", c.method, c.path, rec.Code, rec.Header().Get("Location"), c.status, c.location)
		}
	}
}

// A middleware runs before the request is routed, so it builds in the default
// locale whatever the path.
func TestURL_InMiddleware(t *testing.T) {
	handler := guardURLApp(t)
	for _, path := range []string{"/login", "/tr/giris"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("X-Login"); got != "/login" {
			t.Errorf("GET %s: the middleware built %q, want /login", path, got)
		}
	}
}

// Outside a request collage serves — a guard called by hand in a test — there
// are no routes to build with, and it says so rather than panicking.
func TestURL_OutsideARequest(t *testing.T) {
	if _, err := collage.URL(context.Background(), "login", nil); !errors.Is(err, collage.ErrUnknownRoute) {
		t.Errorf("URL = %v, want ErrUnknownRoute", err)
	}
	if d, err := collage.GuardSeeOtherPage(context.Background(), "login", nil); d != nil || !errors.Is(err, collage.ErrUnknownRoute) {
		t.Errorf("GuardSeeOtherPage = %v, %v, want nil and ErrUnknownRoute", d, err)
	}
}
