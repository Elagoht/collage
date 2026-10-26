package collage_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// withQueryApp serves the same links on a dynamic page and on a static one, so
// the two can be compared: the static page is shared between readers.
func withQueryApp(t *testing.T) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Cache:  collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/list.html": {Data: []byte(`<a id="next" href="{{withQuery "page" 2}}">next</a>` +
				`<a id="clear" href="{{withQuery "q" ""}}">clear</a>` +
				`<a id="both" href="{{withQuery "page" 3 "sort" "new"}}">both</a>`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	content := collage.NewFragment("list", "list.html").Build()
	for _, p := range []*collage.Page{
		collage.NewPage("dynamic").WithContent(content).WithPath("en", "/items").Dynamic().Build(),
		collage.NewPage("static").WithContent(content).WithPath("en", "/static").WithCacheParams("page").Static().Build(),
	} {
		if err := app.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func TestWithQuery(t *testing.T) {
	h := withQueryApp(t).Handler()
	got := body(t, h, "/items?q=go&page=1")
	for _, want := range []string{
		`id="next" href="/items?page=2&amp;q=go"`,
		`id="clear" href="/items?page=1"`,
		`id="both" href="/items?page=3&amp;q=go&amp;sort=new"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
}

// A cached page that names its cache parameters is rendered once for every
// reader with the same ones: a parameter outside the key must not end up in the
// links another reader is served.
func TestWithQuery_SharedRenderSeesNoReadersQuery(t *testing.T) {
	h := withQueryApp(t).Handler()
	body(t, h, "/static?secret=x&page=1")
	got := body(t, h, "/static?secret=y&page=1")
	if strings.Contains(got, "secret") {
		t.Errorf("a reader's query reached the shared render:\n%s", got)
	}
	if !strings.Contains(got, `id="next" href="/static?page=2"`) {
		t.Errorf("next link wrong in\n%s", got)
	}
}

func TestWithQuery_BadValue(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/x.html": {Data: []byte(`{{withQuery "page" 1.5}}`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterPage(collage.NewPage("x").WithContent(collage.NewFragment("x", "x.html").Build()).WithPath("en", "/x").Dynamic().Build()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "1.5") {
		t.Errorf("GET /x = %d; a float in withQuery must fail the render", rec.Code)
	}
}

// A fragment a page opens at a URL of its own renders the page's links: a
// pagination link inside it leads to the page, not to the bare fragment.
func TestWithQuery_FragmentPathLinksThePage(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/results.html": {Data: []byte(`<a id="next" href="{{withQuery "page" 2}}">next</a>`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	results := collage.NewFragment("results", "results.html").Build()
	page := collage.NewPage("search").WithContent(results).WithPath("en", "/search").
		WithFragmentPath("en", "/search/results", results).Dynamic().Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatal(err)
	}
	got := body(t, app.Handler(), "/search/results?q=a")
	if !strings.Contains(got, `href="/search?page=2&amp;q=a"`) {
		t.Errorf("fragment path link:\n%s\nwant /search?page=2&amp;q=a", got)
	}
}
