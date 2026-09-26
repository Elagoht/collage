package collage_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// checker is a plugin exercising what v0.21 hands plugins: findings, a build hook,
// a render function, middleware, and the host's URLs.
type checker struct {
	host     collage.Host
	nonces   atomic.Int64
	built    *collage.BuildFinishedEvent
	urls     []collage.PageURL
	defLoc   string
	locales  []string
	aboutURL string
	static   atomic.Int64
	served   atomic.Int64
}

func (c *checker) Name() string                   { return "test/checker" }
func (c *checker) Version() string                { return "0" }
func (c *checker) Shutdown(context.Context) error { return nil }

func (c *checker) Configure(_ context.Context, host collage.ConfigHost) error {
	return host.AddRenderFunc("nonce", func(rc *collage.RenderContext) any {
		v, _ := collage.Get[string](rc, "nonce")
		return func() string { return v }
	})
}

func (c *checker) Init(ctx context.Context, host collage.Host) error {
	c.host = host
	c.defLoc, c.locales = host.Locales()
	var err error
	if _, ok := host.Page("post"); ok {
		if c.urls, err = host.PageURLs(ctx, "post"); err != nil {
			return err
		}
	}
	if _, ok := host.Page("about"); ok {
		if c.aboutURL, err = host.URL("about", "tr", nil); err != nil {
			return err
		}
	}
	return host.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Checked", "1")
			next.ServeHTTP(w, r)
		})
	})
}

func (c *checker) OnBeforeRender(_ context.Context, ev *collage.BeforeRenderEvent) error {
	ev.Context.Set("nonce", fmt.Sprintf("n%d", c.nonces.Add(1)))
	return nil
}

func (c *checker) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if ev.Static {
		c.static.Add(1)
	} else {
		c.served.Add(1)
	}
	if !strings.Contains(string(ev.HTML), "<h1>") {
		ev.Error("one-h1", "the page has no <h1>")
	}
	ev.Warn("demo", "a warning on every page")
	return nil
}

func (c *checker) OnBuildFinished(_ context.Context, ev *collage.BuildFinishedEvent) error {
	c.built = ev
	ev.Warn("/about/", "cross-page", "seen at the end")
	return nil
}

func checkerSite(t *testing.T, dev bool) (*collage.App, *checker) {
	t.Helper()
	c := &checker{}
	app, err := collage.New(&collage.Config{
		DevMode: dev,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/home.html":  {Data: []byte(`<html><body><h1>home</h1><script nonce="{{nonce}}"></script></body></html>`)},
			"t/about.html": {Data: []byte(`<html><body><p>about</p></body></html>`)},
			"t/post.html":  {Data: []byte(`<html><body><h1>{{.}}</h1></body></html>`)},
		}, Root: "t"},
		Locale:  collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
		Plugins: []collage.Plugin{c},
	})
	if err != nil {
		t.Fatal(err)
	}
	pages := []*collage.Page{
		collage.NewPage("home").WithContent(collage.NewFragment("home", "home.html").Build()).WithPath("en", "/").Build(),
		collage.NewPage("about").WithContent(collage.NewFragment("about", "about.html").Build()).
			WithPath("en", "/about").WithPath("tr", "/hakkinda").Build(),
		collage.NewPage("post").WithContent(collage.NewFragment("post", "post.html").WithDataHandler(
			func(_ context.Context, rc *collage.RenderContext) (any, []string, error) {
				return rc.Param("slug"), nil, nil
			}).Static().Build()).
			WithPath("en", "/posts/{slug}").
			WithStaticParams(func(context.Context, string) ([]map[string]string, error) {
				return []map[string]string{{"slug": "a"}, {"slug": "b"}}, nil
			}).Build(),
	}
	for _, p := range pages {
		if err := app.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	return app, c
}

// The host tells a plugin where every page lives, in every locale, the URLs of a
// pattern included; the plugin's middleware wraps every request.
func TestHost_URLsLocalesAndMiddleware(t *testing.T) {
	app, c := checkerSite(t, false)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if c.served.Load() != 1 || c.static.Load() != 0 {
		t.Errorf("a request's render: %d served, %d static", c.served.Load(), c.static.Load())
	}
	if rec.Header().Get("X-Checked") != "1" {
		t.Error("the plugin's middleware did not run")
	}
	if c.defLoc != "en" || strings.Join(c.locales, ",") != "en,tr" {
		t.Errorf("Locales = %q %v", c.defLoc, c.locales)
	}
	if c.aboutURL != "/tr/hakkinda" {
		t.Errorf("URL(about, tr) = %q", c.aboutURL)
	}
	var got []string
	for _, u := range c.urls {
		got = append(got, u.Locale+" "+u.Path+" "+u.Params["slug"])
	}
	if strings.Join(got, "|") != "en /posts/a a|en /posts/b b" {
		t.Errorf("PageURLs(post) = %v", got)
	}
}

// A render function is made anew for each render and reads what that render holds.
func TestRenderFunc_IsPerRender(t *testing.T) {
	app, _ := checkerSite(t, false)
	h := app.Handler()
	body := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Body.String()
	}
	a, b := body(), body()
	if !strings.Contains(a, `nonce="n`) || a == b {
		t.Errorf("two renders: %q and %q", a, b)
	}
}

// Findings are shown over a development page, and the page is served anyway.
func TestFindings_DevOverlay(t *testing.T) {
	app, _ := checkerSite(t, true)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "<p>about</p>") {
		t.Fatalf("page = %d %q", rec.Code, body)
	}
	for _, want := range []string{"collage-dev-overlay", "one-h1", "the page has no &lt;h1&gt;", "test/checker", "checks found something"} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay lacks %q", want)
		}
	}
	prod, _ := checkerSite(t, false)
	rec = httptest.NewRecorder()
	prod.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	if strings.Contains(rec.Body.String(), "collage-dev-overlay") {
		t.Error("a production page carries the overlay")
	}
}

// A build lists every finding, with the page it is about; an error-level one fails
// the build; the build hook sees every file written.
func TestFindings_Build(t *testing.T) {
	app, c := checkerSite(t, false)
	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	report, err := builder.Build(context.Background())
	if !errors.Is(err, collage.ErrBuildFindings) {
		t.Fatalf("Build = %v, want ErrBuildFindings", err)
	}
	var errorsAt, warnings []string
	for _, f := range report.Findings {
		if f.Plugin != "test/checker" {
			t.Errorf("finding without its plugin: %+v", f)
		}
		if f.Level == collage.FindingError {
			errorsAt = append(errorsAt, f.Path)
		} else {
			warnings = append(warnings, f.Path)
		}
	}
	if strings.Join(errorsAt, ",") != "/about,/tr/hakkinda" {
		t.Errorf("errors at %v", errorsAt)
	}
	if len(warnings) != 6 { // one per page — home, about ×2, post ×2 — and the cross-page one
		t.Errorf("warnings at %v", warnings)
	}
	if _, err := os.Stat(out + "/about/index.html"); err != nil {
		t.Errorf("a page with findings was not written: %v", err)
	}
	if c.built == nil {
		t.Fatal("OnBuildFinished did not run")
	}
	if c.static.Load() == 0 || c.served.Load() != 0 {
		t.Errorf("a build's renders: %d static, %d served; want all static", c.static.Load(), c.served.Load())
	}
	paths := map[string]string{}
	for _, f := range c.built.Files {
		paths[f.Path] = f.Kind
	}
	for _, want := range []string{"/", "/about", "/tr/hakkinda", "/posts/a", "/posts/b"} {
		if paths[want] != "page" {
			t.Errorf("built files lack page %q: %v", want, paths)
		}
	}
}

// A page an action answers with is checked and shown like any other.
func TestFindings_OnAnActionsPage(t *testing.T) {
	app, err := collage.New(&collage.Config{
		DevMode:  true,
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<html><body><p>no heading</p></body></html>`)}}, Root: "t"},
		Plugins:  []collage.Plugin{&checker{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	form := collage.NewPage("form").WithContent(collage.NewFragment("form", "p.html").Build()).WithPath("en", "/form").Build()
	if err := app.RegisterPage(form); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterAction(collage.NewAction("show").WithPath("en", "/show").WithMethods(http.MethodGet).
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.RenderPage(form), nil
		}).Build()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/show", nil))
	if body := rec.Body.String(); !strings.Contains(body, "one-h1") || !strings.Contains(body, "collage-dev-overlay") {
		t.Errorf("an action's page carries no findings:\n%s", body)
	}
}
