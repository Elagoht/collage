package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// urlApp has an about page in two languages, a blog post in two, and a pricing
// page only in English, all sharing a layout with a language switcher.
func urlApp(t *testing.T) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/layout.html": {Data: []byte(`<nav>` +
					`{{with localeURL "en"}}<a hreflang="en" href="{{.}}">EN</a>{{end}}` +
					`{{with localeURL "tr"}}<a hreflang="tr" href="{{.}}">TR</a>{{end}}` +
					`</nav>{{slot "content"}}`)},
				"t/about.html": {Data: []byte(`<a id="post" href="{{pageURL "post" "slug" "hello world"}}">post</a>` +
					`<a id="pricing" href="{{pageURL "pricing"}}">pricing</a>` +
					`<a id="about-en" href="{{pageURLIn "en" "about"}}">about</a>` +
					`<a id="feed" href="{{pageURL "feed"}}">feed</a>`)},
				"t/post.html":    {Data: []byte(`post`)},
				"t/pricing.html": {Data: []byte(`pricing`)},
			},
			Root: "t",
		},
		Locale: collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	page := func(name, template string, paths map[string]string) *collage.Page {
		builder := collage.NewPage(name).WithLayout(layout).
			WithContent(collage.NewFragment(name+"-content", template).Build()).Dynamic()
		for locale, path := range paths {
			builder = builder.WithPath(locale, path)
		}
		return builder.Build()
	}
	for _, p := range []*collage.Page{
		page("about", "about.html", map[string]string{"en": "/about", "tr": "/hakkinda"}),
		page("post", "post.html", map[string]string{"en": "/blog/{slug}", "tr": "/yazi/{slug}"}),
		page("pricing", "pricing.html", map[string]string{"en": "/pricing"}),
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

func body(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d:\n%s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestURLFunctions_InTheDefaultLocale(t *testing.T) {
	got := body(t, urlApp(t).Handler(), "/about")
	for _, want := range []string{
		`<a hreflang="en" href="/about">`,
		`<a hreflang="tr" href="/tr/hakkinda">`,
		`id="post" href="/blog/hello%20world"`,
		`id="pricing" href="/pricing"`,
		`id="about-en" href="/about"`,
		`id="feed" href="/feed.xml"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("GET /about does not contain %s:\n%s", want, got)
		}
	}
}

// In Turkish, links stay in Turkish; a page with no Turkish path links its English
// one rather than failing the render.
func TestURLFunctions_InAnotherLocale(t *testing.T) {
	got := body(t, urlApp(t).Handler(), "/tr/hakkinda")
	for _, want := range []string{
		`<a hreflang="en" href="/about">`,
		`<a hreflang="tr" href="/tr/hakkinda">`,
		`id="post" href="/tr/yazi/hello%20world"`,
		`id="pricing" href="/pricing"`,
		`id="about-en" href="/about"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("GET /tr/hakkinda does not contain %s:\n%s", want, got)
		}
	}
}

// A switcher skips a language the page has not been translated into, and keeps
// the path parameters of the page it is on.
func TestLocaleURL_SkipsAMissingTranslationAndKeepsParams(t *testing.T) {
	h := urlApp(t).Handler()
	if got := body(t, h, "/pricing"); strings.Contains(got, `hreflang="tr"`) {
		t.Errorf("pricing has no Turkish path, but the switcher offered one:\n%s", got)
	}
	if got := body(t, h, "/blog/merhaba"); !strings.Contains(got, `hreflang="tr" href="/tr/yazi/merhaba"`) {
		t.Errorf("the switcher lost the slug:\n%s", got)
	}
}

func TestAppURL(t *testing.T) {
	app := urlApp(t)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	got, err := app.URL("post", "tr", map[string]string{"slug": "merhaba"})
	if err != nil || got != "/tr/yazi/merhaba" {
		t.Errorf("URL(post, tr) = %q, %v; want /tr/yazi/merhaba", got, err)
	}
	if got, err := app.URL("about", "", nil); err != nil || got != "/about" {
		t.Errorf("URL(about, default) = %q, %v; want /about", got, err)
	}

	for _, c := range []struct {
		name, locale string
		params       map[string]string
		want         error
	}{
		{"nope", "en", nil, collage.ErrUnknownRoute},
		{"pricing", "tr", nil, collage.ErrNoPathInLocale},
		{"post", "en", nil, collage.ErrRouteParams},
		{"post", "en", map[string]string{"slug": "x", "page": "2"}, collage.ErrRouteParams},
		{"about", "de", nil, collage.ErrLocaleUnreachable},
	} {
		if _, err := app.URL(c.name, c.locale, c.params); !errors.Is(err, c.want) {
			t.Errorf("URL(%q, %q, %v) = %v, want %v", c.name, c.locale, c.params, err, c.want)
		}
	}
}
