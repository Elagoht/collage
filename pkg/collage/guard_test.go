package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// guardedApp builds a one-page app whose layout guards every request that does
// not carry the header, mirroring a logged-in reader. renderCount counts data
// handler runs so a test can prove the guard ran before a cached render.
func guardedApp(t *testing.T, strategy func(*collage.PageBuilder), cache bool) (*collage.App, *int) {
	t.Helper()
	renderCount := 0
	cfg := &collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"templates/layouts/private.html": {Data: []byte(`<div class="auth">{{slot "content"}}</div>`)},
				"templates/pages/panel.html":     {Data: []byte(`<p>panel</p>`)},
			},
			Root: "templates",
		},
	}
	if cache {
		cfg.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute}
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	guard := func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
		if r.Header.Get("X-Logged-In") != "" {
			return nil, nil
		}
		return &collage.GuardDecision{
			Status:   http.StatusSeeOther,
			Location: "/login?next=" + url.QueryEscape(r.URL.RequestURI()),
		}, nil
	}
	layout := collage.NewFragment("private", "layouts/private.html").WithGuard(guard).Build()
	content := collage.NewFragment("panel", "pages/panel.html").
		WithDataHandler(collage.Effect(func(ctx context.Context, rc *collage.RenderContext) error {
			renderCount++
			return nil
		})).
		Build()
	b := collage.NewPage("panel").WithLayouts(layout).WithContent(content).WithPath("en", "/panel")
	if strategy != nil {
		strategy(b)
	}
	if err := app.RegisterPage(b.Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app, &renderCount
}

func get(h http.Handler, target, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if header != "" {
		req.Header.Set("X-Logged-In", header)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestGuardRedirectsLoggedOut(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	w := get(app.Handler(), "/panel", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/login?next=%2Fpanel" {
		t.Fatalf("Location = %q", got)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("redirect carried a body: %q", w.Body.String())
	}
}

func TestGuardAllowsLoggedIn(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	w := get(app.Handler(), "/panel", "1")
	if w.Code != http.StatusOK || w.Body.String() != `<div class="auth"><p>panel</p></div>` {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}
}

// The cache must not outrank the guard: the first reader's render is cached,
// and a blocked second reader still never sees it.
func TestGuardRunsBeforeCache(t *testing.T) {
	app, renders := guardedApp(t, func(b *collage.PageBuilder) { b.Static() }, true)
	h := app.Handler()
	if w := get(h, "/panel", "1"); w.Code != http.StatusOK {
		t.Fatalf("allowed request: status = %d", w.Code)
	}
	if w := get(h, "/panel", "1"); w.Code != http.StatusOK || *renders != 1 {
		t.Fatalf("second allowed request should hit cache: status = %d renders = %d", w.Code, *renders)
	}
	if w := get(h, "/panel", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("blocked request on cached page: status = %d, want 303", w.Code)
	}
}

func TestGuardOnHeadRequest(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	req := httptest.NewRequest(http.MethodHead, "/panel", nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
}

func TestGuardErrorFailsRequest(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"templates/layouts/p.html": {Data: []byte(`{{slot "content"}}`)}},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	layout := collage.NewFragment("p", "layouts/p.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			return nil, errBoom
		}).
		Build()
	page := collage.NewPage("x").WithLayouts(layout).
		WithContent(collage.NewFragment("c", "layouts/p.html").Build()).
		WithPath("en", "/x").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

var errBoom = errBoomType{}

type errBoomType struct{}

func (errBoomType) Error() string { return "boom" }

// A blocked request never reaches the page, so a plugin watching page
// resolution is not told about it.
func TestGuardBlocksBeforePageResolved(t *testing.T) {
	spy := &guardSpy{}
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"templates/layouts/p.html": {Data: []byte(`{{slot "content"}}`)}},
			Root: "templates",
		},
		Plugins: []collage.Plugin{spy},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	layout := collage.NewFragment("p", "layouts/p.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			return &collage.GuardDecision{Status: http.StatusUnauthorized}, nil
		}).
		Build()
	page := collage.NewPage("x").WithLayouts(layout).
		WithContent(collage.NewFragment("c", "layouts/p.html").Build()).
		WithPath("en", "/x").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if spy.resolved != 0 {
		t.Fatalf("PageResolved fired %d times for a blocked request", spy.resolved)
	}
}

type guardSpy struct{ resolved int }

func (s *guardSpy) Name() string                             { return "guard-spy" }
func (s *guardSpy) Version() string                          { return "0.0.0" }
func (s *guardSpy) Shutdown(context.Context) error           { return nil }
func (s *guardSpy) Init(context.Context, collage.Host) error { return nil }
func (s *guardSpy) OnPageResolved(ctx context.Context, ev *collage.PageResolvedEvent) error {
	s.resolved++
	return nil
}

// A page's fallback renders carry no guards: an error page that happens to be
// private still renders for whoever hit the error, rather than redirecting
// the reader into a loop.
func TestGuardSkippedOnFallbackRender(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"templates/layouts/private.html": {Data: []byte(`<div class="auth">{{slot "content"}}</div>`)},
				"templates/pages/panel.html":     {Data: []byte(`<p>panel</p>`)},
				"templates/pages/broken.html":    {Data: []byte(`<p>broken</p>`)},
			},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The private error page.
	private := collage.NewFragment("private", "layouts/private.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			return &collage.GuardDecision{Status: http.StatusSeeOther, Location: "/login"}, nil
		}).
		Build()
	errorPage := collage.NewPage("error-page").
		WithLayouts(private).
		WithContent(collage.NewFragment("panel", "pages/panel.html").Build()).
		WithPath("en", "/error-page").
		Build()
	if err := app.RegisterPage(errorPage); err != nil {
		t.Fatalf("RegisterPage(error-page): %v", err)
	}
	// The public page whose render fails.
	broken := collage.NewPage("broken").
		WithContent(collage.NewFragment("broken", "pages/broken.html").
			WithDataHandler(collage.Load(func(ctx context.Context, rc *collage.RenderContext) (string, error) {
				return "", errBoom
			})).
			Required().
			Build()).
		WithErrorPage(errorPage).
		WithPath("en", "/broken").
		Build()
	if err := app.RegisterPage(broken); err != nil {
		t.Fatalf("RegisterPage(broken): %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/broken", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 with the error page rendered, not a guard redirect", w.Code)
	}
	if !strings.Contains(w.Body.String(), `<div class="auth">`) {
		t.Fatalf("error page did not render: %q", w.Body.String())
	}
}

// Both locales of a guarded page are guarded: the guard sees the request as
// routed, locale prefix included, and the next parameter carries what the
// reader actually asked for.
func TestGuardSeesLocalePrefixedPath(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 0},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}, PrefixDefault: true},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"templates/layouts/p.html": {Data: []byte(`{{slot "content"}}`)}},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var seen string
	layout := collage.NewFragment("p", "layouts/p.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			seen = r.URL.RequestURI()
			return &collage.GuardDecision{Status: http.StatusSeeOther, Location: "/tr/login"}, nil
		}).
		Build()
	page := collage.NewPage("x").WithLayouts(layout).
		WithContent(collage.NewFragment("c", "layouts/p.html").Build()).
		WithPath("en", "/x").WithPath("tr", "/x").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tr/x", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/tr/login" {
		t.Fatalf("status = %d Location = %q", w.Code, w.Header().Get("Location"))
	}
	if seen != "/tr/x" {
		t.Fatalf("guard saw %q, want /tr/x", seen)
	}
}
