package collage_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// prefixApp is an English and Turkish site whose English pages carry "/en" too:
// a home page, an about page with a form, a robots.txt and a search index in
// each language.
func prefixApp(t *testing.T, trailing bool) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/page.html": {Data: []byte(`<a id="about" href="{{pageURL "about"}}"></a>` +
					`<a id="home-en" href="{{pageURLIn "en" "home"}}"></a>` +
					`<a id="switch" href="{{localeURL "tr"}}"></a>` +
					`<a id="robots" href="{{pageURL "robots"}}"></a>` +
					`<a id="search" href="{{pageURL "search"}}"></a>`)},
			},
			Root: "t",
		},
		Locale:        collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}, PrefixDefault: true},
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
	} {
		if err := app.RegisterPage(p); err != nil {
			t.Fatalf("RegisterPage(%q): %v", p.Name, err)
		}
	}
	text := func(context.Context, *collage.RenderContext) ([]byte, []string, error) { return []byte("ok"), nil, nil }
	for _, d := range []*collage.Document{
		collage.NewDocument("robots", "text/plain").WithPath("en", "/robots.txt").WithHandler(text).Static().Build(),
		collage.NewDocument("search", "application/json").WithPath("en", "/search.json").WithPath("tr", "/search.json").WithHandler(text).Static().Build(),
	} {
		if err := app.RegisterDocument(d); err != nil {
			t.Fatalf("RegisterDocument(%q): %v", d.Name, err)
		}
	}
	return app
}

// Links to the default locale's pages carry its prefix; its documents' do not.
func TestPrefixDefault_Links(t *testing.T) {
	app := prefixApp(t, true)
	got := body(t, app.Handler(), "/en/about/")
	for _, want := range []string{
		`id="about" href="/en/about/"`,
		`id="home-en" href="/en/"`,
		`id="switch" href="/tr/hakkinda/"`,
		`id="robots" href="/robots.txt"`,
		`id="search" href="/search.json"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("page has no %s:\n%s", want, got)
		}
	}
	if url, _ := app.URL("home", "en", nil); url != "/en/" {
		t.Errorf(`URL("home", "en") = %q, want "/en/"`, url)
	}
	if url, _ := app.URL("search", "tr", nil); url != "/tr/search.json" {
		t.Errorf(`URL("search", "tr") = %q, want "/tr/search.json"`, url)
	}
}

// A page without the prefix, the root included, redirects to it — in one hop,
// with the site's slash; a document with the default prefix redirects to its
// address without.
func TestPrefixDefault_Redirects(t *testing.T) {
	for _, c := range []struct {
		trailing bool
		from, to string
	}{
		{true, "/", "/en/"},
		{true, "/about", "/en/about/"},
		{true, "/about/?ref=x", "/en/about/?ref=x"},
		{true, "/en", "/en/"},
		{true, "/en/robots.txt", "/robots.txt"},
		{true, "/EN/about/", "/en/about/"},
		{false, "/", "/en"},
		{false, "/about/", "/en/about"},
		{false, "/en/", "/en"},
	} {
		app := prefixApp(t, c.trailing)
		rec := request(app.Handler(), http.MethodGet, c.from)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != c.to {
			t.Errorf("TrailingSlash %v: GET %s = %d to %q, want 301 to %q", c.trailing, c.from, rec.Code, rec.Header().Get("Location"), c.to)
		}
	}
	app := prefixApp(t, true)
	for _, target := range []string{"/en/", "/en/about/", "/tr/", "/robots.txt", "/search.json", "/tr/search.json"} {
		if rec := request(app.Handler(), http.MethodGet, target); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
		}
	}
	if rec := request(app.Handler(), http.MethodPost, "/about"); rec.Code == http.StatusMovedPermanently || rec.Code == http.StatusPermanentRedirect {
		t.Errorf("POST /about was redirected to %q; an action answers where it was posted", rec.Header().Get("Location"))
	}
}

// The export writes the default locale's pages under its directory, its documents
// at the root, and a root page that sends the reader to its home.
func TestPrefixDefault_Export(t *testing.T) {
	app := prefixApp(t, true)
	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, file := range []string{"en/index.html", "en/about/index.html", "tr/index.html", "tr/hakkinda/index.html"} {
		data, err := os.ReadFile(filepath.Join(out, file))
		if err != nil || !strings.Contains(string(data), `id="about"`) {
			t.Errorf("%s is not the page: %v\n%s", file, err, data)
		}
	}
	for _, file := range []string{"robots.txt", "search.json", "tr/search.json"} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Errorf("%s was not written: %v", file, err)
		}
	}
	root, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatalf("no root page: %v", err)
	}
	for _, want := range []string{`<meta http-equiv="refresh" content="0; url=/en/">`, `<link rel="canonical" href="/en/">`, `noindex`} {
		if !strings.Contains(string(root), want) {
			t.Errorf("root page has no %s:\n%s", want, root)
		}
	}
}
