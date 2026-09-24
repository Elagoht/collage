package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func TestResolveLocale_PathPrefix(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}

	locale, remaining := resolveLocale("/tr/blog/post", opts)
	if locale != "tr" || remaining != "/blog/post" {
		t.Fatalf("locale=%q remaining=%q, want tr /blog/post", locale, remaining)
	}
}

func TestResolveLocale_PathPrefix_RootOnly(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}

	locale, remaining := resolveLocale("/tr", opts)
	if locale != "tr" || remaining != "/" {
		t.Fatalf("locale=%q remaining=%q, want tr /", locale, remaining)
	}
}

func TestResolveLocale_PathPrefix_UnsupportedIsAnOrdinarySegment(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}

	locale, remaining := resolveLocale("/fr/blog/post", opts)
	if locale != "en" || remaining != "/fr/blog/post" {
		t.Fatalf("locale=%q remaining=%q, want en /fr/blog/post", locale, remaining)
	}
}

func TestResolveLocale_PathDisabled(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}, DisablePathLocale: true}

	locale, remaining := resolveLocale("/tr/blog/post", opts)
	if locale != "en" || remaining != "/tr/blog/post" {
		t.Fatalf("locale=%q remaining=%q, want en and the path unchanged", locale, remaining)
	}
}

func TestResolveLocale_NoPrefixIsTheDefault(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}

	locale, remaining := resolveLocale("/blog", opts)
	if locale != "en" || remaining != "/blog" {
		t.Fatalf("locale=%q remaining=%q, want en /blog", locale, remaining)
	}
}

// The URL is the only source of a locale. A Turkish browser following a link to
// an English page gets the English page — it used to get a 404, because the
// header moved the lookup into a tree where that path did not exist — and a URL
// means the same thing to every reader, which is what lets it be cached, crawled
// and shared.
func TestMatch_HeadersAndCookiesDoNotChooseTheLocale(t *testing.T) {
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
	for _, page := range []*types.Page{
		{Name: "about", Paths: map[string]string{"en": "/about", "tr": "/hakkinda"}},
		{Name: "pricing", Paths: map[string]string{"en": "/pricing"}},
	} {
		if err := rt.Register(page); err != nil {
			t.Fatalf("Register(%q) = %v", page.Name, err)
		}
	}

	for _, c := range []struct {
		path, header, cookie string
		wantLocale, wantPage string
	}{
		{"/about", "tr-TR,tr;q=0.9", "", "en", "about"},
		{"/pricing", "tr", "", "en", "pricing"},
		{"/about", "", "tr", "en", "about"},
		{"/tr/hakkinda", "en", "en", "tr", "about"},
		{"/hakkinda", "tr", "tr", "en", ""},
	} {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.header != "" {
			req.Header.Set("Accept-Language", c.header)
		}
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "locale", Value: c.cookie})
		}
		match, err := rt.Match(req)
		if err != nil {
			t.Fatalf("Match(%s) = %v", c.path, err)
		}
		page := ""
		if match.Page != nil {
			page = match.Page.Name
		}
		if match.Locale != c.wantLocale || page != c.wantPage {
			t.Errorf("%s (Accept-Language %q, cookie %q) = locale %q page %q, want %q %q",
				c.path, c.header, c.cookie, match.Locale, page, c.wantLocale, c.wantPage)
		}
	}
}

// The default locale's URLs carry no prefix, and a locale has one spelling: any
// other form of a URL is a permanent redirect to the one that is right, query and
// all, rather than a second URL for the same page.
func TestMatch_NonCanonicalLocalePrefixesRedirect(t *testing.T) {
	rt := New(LocaleOptions{Default: "en", Supported: []string{"en", "tr"}})
	if err := rt.Register(&types.Page{Name: "about", Paths: map[string]string{"en": "/about", "tr": "/hakkinda"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, target, want string
		status               int
	}{
		{http.MethodGet, "/en/about", "/about", http.StatusMovedPermanently},
		{http.MethodGet, "/EN/about?x=1", "/about?x=1", http.StatusMovedPermanently},
		{http.MethodGet, "/en", "/", http.StatusMovedPermanently},
		{http.MethodGet, "/TR/hakkinda", "/tr/hakkinda", http.StatusMovedPermanently},
		{http.MethodGet, "/TR", "/tr", http.StatusMovedPermanently},
		{http.MethodPost, "/en/about", "/about", http.StatusPermanentRedirect},
	} {
		match, err := rt.Match(httptest.NewRequest(c.method, c.target, nil))
		if err != nil {
			t.Fatal(err)
		}
		if match.RedirectTo != c.want || match.RedirectStatus != c.status {
			t.Errorf("%s %s = %q %d, want %q %d", c.method, c.target, match.RedirectTo, match.RedirectStatus, c.want, c.status)
		}
	}
	for _, target := range []string{"/about", "/tr/hakkinda", "/tr"} {
		if match, _ := rt.Match(httptest.NewRequest(http.MethodGet, target, nil)); match.RedirectTo != "" {
			t.Errorf("%s redirected to %q, want it served", target, match.RedirectTo)
		}
	}
}
