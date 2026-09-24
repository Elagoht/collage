package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// slashApp has a home page and an about page in two languages, a blog post, a
// page taking a single segment, a form action on the about page and a feed.
func slashApp(t *testing.T, trailing bool) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/page.html": {Data: []byte(`<a id="about" href="{{pageURL "about"}}"></a>` +
					`<a id="post" href="{{pageURL "post" "slug" "hello"}}"></a>` +
					`<a id="home-tr" href="{{pageURLIn "tr" "home"}}"></a>` +
					`<a id="switch" href="{{localeURL "tr"}}"></a>` +
					`<a id="feed" href="{{pageURL "feed"}}"></a>`)},
			},
			Root: "t",
		},
		Locale:        collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
		TrailingSlash: trailing,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := func(name string, paths map[string]string) *collage.PageBuilder {
		builder := collage.NewPage(name).WithContent(collage.NewFragment(name+"-content", "page.html").Build()).Static()
		for locale, path := range paths {
			builder = builder.WithPath(locale, path)
		}
		return builder
	}
	for _, p := range []*collage.Page{
		page("home", map[string]string{"en": "/", "tr": "/"}).Build(),
		page("about", map[string]string{"en": "/about", "tr": "/hakkinda"}).
			WithAction(http.MethodPost, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
				return collage.NoContent(http.StatusNoContent), nil
			}).
			Build(),
		page("post", map[string]string{"en": "/blog/{slug}"}).Dynamic().Build(),
		page("tag", map[string]string{"en": "/{tag}"}).Dynamic().Build(),
	} {
		if err := app.RegisterPage(p); err != nil {
			t.Fatalf("RegisterPage(%q): %v", p.Name, err)
		}
	}
	feed := collage.NewDocument("feed", "application/rss+xml").WithPath("en", "/feed.xml").
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			return []byte("<rss/>"), nil, nil
		}).
		Dynamic().Build()
	if err := app.RegisterDocument(feed); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	return app
}

func request(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// With TrailingSlash, every link to a page built by name ends in "/", a locale's
// home included; a document's does not.
func TestTrailingSlash_LinksEndInASlash(t *testing.T) {
	app := slashApp(t, true)
	got := body(t, app.Handler(), "/about/")
	for _, want := range []string{
		`id="about" href="/about/"`,
		`id="post" href="/blog/hello/"`,
		`id="home-tr" href="/tr/"`,
		`id="switch" href="/tr/hakkinda/"`,
		`id="feed" href="/feed.xml"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("page has no %s:\n%s", want, got)
		}
	}
	if url, err := app.URL("post", "", map[string]string{"slug": "hello"}); err != nil || url != "/blog/hello/" {
		t.Errorf(`URL("post") = %q, %v; want "/blog/hello/"`, url, err)
	}
	if url, err := app.URL("home", "", nil); err != nil || url != "/" {
		t.Errorf(`URL("home") = %q, %v; want "/"`, url, err)
	}
}

// Each page has one address: the spelling the site does not use redirects,
// permanently and with its query string, to the one it does.
func TestTrailingSlash_TheOtherSpellingRedirects(t *testing.T) {
	for _, c := range []struct {
		trailing     bool
		from, to     string
		servedAtHome string
	}{
		{true, "/about", "/about/", "/"},
		{true, "/about?ref=feed", "/about/?ref=feed", "/"},
		{true, "/tr", "/tr/", "/"},
		{true, "/tr/hakkinda", "/tr/hakkinda/", "/"},
		{false, "/about/", "/about", "/"},
		{false, "/tr/", "/tr", "/"},
		{false, "/blog/hello/", "/blog/hello", "/"},
	} {
		app := slashApp(t, c.trailing)
		rec := request(app.Handler(), http.MethodGet, c.from)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != c.to {
			t.Errorf("TrailingSlash %v: GET %s = %d to %q, want 301 to %q", c.trailing, c.from, rec.Code, rec.Header().Get("Location"), c.to)
		}
		if rec := request(app.Handler(), http.MethodGet, strings.Split(c.to, "?")[0]); rec.Code != http.StatusOK {
			t.Errorf("TrailingSlash %v: GET %s = %d, want 200", c.trailing, c.to, rec.Code)
		}
		if rec := request(app.Handler(), http.MethodGet, c.servedAtHome); rec.Code != http.StatusOK {
			t.Errorf("TrailingSlash %v: GET / = %d, want 200", c.trailing, rec.Code)
		}
	}
}

// A document is a file, and an action answers where it was posted: neither is
// redirected.
func TestTrailingSlash_DocumentsAndActionsAreNotRedirected(t *testing.T) {
	app := slashApp(t, true)
	if rec := request(app.Handler(), http.MethodGet, "/feed.xml"); rec.Code != http.StatusOK {
		t.Errorf("GET /feed.xml = %d, want 200", rec.Code)
	}
	for _, target := range []string{"/about", "/about/"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		app.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusMovedPermanently || rec.Code == http.StatusPermanentRedirect {
			t.Errorf("POST %s was redirected to %q", target, rec.Header().Get("Location"))
		}
	}
}

// A path starting with two slashes is not turned into a Location a browser reads
// as another host.
func TestTrailingSlash_NeverRedirectsOffSite(t *testing.T) {
	for _, trailing := range []bool{true, false} {
		app := slashApp(t, trailing)
		for _, target := range []string{"//evil.example", "//evil.example/"} {
			rec := request(app.Handler(), http.MethodGet, target)
			if location := rec.Header().Get("Location"); strings.HasPrefix(location, "//") {
				t.Errorf("TrailingSlash %v: GET %s redirects to %q", trailing, target, location)
			}
		}
	}
}

// The export writes the same files either way, each rendered at the address it
// is answered at rather than refused as a redirect.
func TestTrailingSlash_ExportWritesEveryPage(t *testing.T) {
	app := slashApp(t, true)
	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, file := range []string{"index.html", "about/index.html", "tr/index.html", "tr/hakkinda/index.html"} {
		data, err := os.ReadFile(filepath.Join(out, file))
		if err != nil {
			t.Errorf("%s was not written: %v", file, err)
			continue
		}
		if !strings.Contains(string(data), `id="about"`) {
			t.Errorf("%s is not the page:\n%s", file, data)
		}
	}
}
