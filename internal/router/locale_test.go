package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func TestResolveLocale_PathPrefix(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}
	req := newRequest(t, "/tr/blog/post")

	locale, remaining := resolveLocale(req, req.URL.Path, opts)
	if locale != "tr" {
		t.Fatalf("locale = %q, want tr", locale)
	}
	if remaining != "/blog/post" {
		t.Fatalf("remaining = %q, want /blog/post", remaining)
	}
}

func TestResolveLocale_PathPrefix_RootOnly(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}
	req := newRequest(t, "/tr")

	locale, remaining := resolveLocale(req, req.URL.Path, opts)
	if locale != "tr" || remaining != "/" {
		t.Fatalf("locale=%q remaining=%q, want tr /", locale, remaining)
	}
}

func TestResolveLocale_PathPrefix_UnsupportedFallsThroughToDefault(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}
	req := newRequest(t, "/fr/blog/post")

	locale, remaining := resolveLocale(req, req.URL.Path, opts)
	if locale != "en" {
		t.Fatalf("locale = %q, want en (fallback to default)", locale)
	}
	// The unsupported prefix was not a locale, so it is not stripped: it is
	// matched as an ordinary path segment instead.
	if remaining != "/fr/blog/post" {
		t.Fatalf("remaining = %q, want /fr/blog/post", remaining)
	}
}

func TestResolveLocale_PathDisabled_FallsThroughToHeader(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}, DisablePathLocale: true}
	req := newRequest(t, "/tr/blog/post")
	req.Header.Set("Accept-Language", "tr")

	locale, remaining := resolveLocale(req, req.URL.Path, opts)
	if locale != "tr" {
		t.Fatalf("locale = %q, want tr (from header)", locale)
	}
	if remaining != "/tr/blog/post" {
		t.Fatalf("remaining = %q, want unchanged path since path-locale is disabled", remaining)
	}
}

func TestResolveLocale_Header_QValues(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr", "de"}}
	req := newRequest(t, "/blog")
	req.Header.Set("Accept-Language", "fr;q=0.9,de;q=0.95,tr;q=0.4")

	locale, _ := resolveLocale(req, req.URL.Path, opts)
	if locale != "de" {
		t.Fatalf("locale = %q, want de (highest q among supported)", locale)
	}
}

func TestResolveLocale_Header_PrimarySubtagFallback(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"tr", "en"}}
	req := newRequest(t, "/blog")
	req.Header.Set("Accept-Language", "tr-TR;q=0.9")

	locale, _ := resolveLocale(req, req.URL.Path, opts)
	if locale != "tr" {
		t.Fatalf("locale = %q, want tr (primary-subtag fallback for tr-TR)", locale)
	}
}

func TestResolveLocale_Header_ExactBeatsSubtagAcrossEntries(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"tr", "en"}}
	req := newRequest(t, "/blog")
	// tr-TR would match "tr" only via subtag fallback and has a higher q; "en" is
	// an exact match at a lower q. Exact matches are tried across every entry
	// before any subtag fallback is attempted.
	req.Header.Set("Accept-Language", "tr-TR;q=0.9,en;q=0.1")

	locale, _ := resolveLocale(req, req.URL.Path, opts)
	if locale != "en" {
		t.Fatalf("locale = %q, want en (exact match preferred over subtag fallback)", locale)
	}
}

func TestResolveLocale_Header_Unsupported_FallsThroughToCookie(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}
	req := newRequest(t, "/blog")
	req.Header.Set("Accept-Language", "fr,es")
	req.AddCookie(&http.Cookie{Name: "locale", Value: "tr"})

	locale, _ := resolveLocale(req, req.URL.Path, opts)
	if locale != "tr" {
		t.Fatalf("locale = %q, want tr (from cookie)", locale)
	}
}

func TestResolveLocale_Cookie_CustomName(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}, CookieName: "lang"}
	req := newRequest(t, "/blog")
	req.AddCookie(&http.Cookie{Name: "lang", Value: "tr"})

	locale, _ := resolveLocale(req, req.URL.Path, opts)
	if locale != "tr" {
		t.Fatalf("locale = %q, want tr (from custom-named cookie)", locale)
	}
}

func TestResolveLocale_Cookie_Disabled_FallsThroughToDefault(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}, DisableCookieLocale: true}
	req := newRequest(t, "/blog")
	req.AddCookie(&http.Cookie{Name: "locale", Value: "tr"})

	locale, _ := resolveLocale(req, req.URL.Path, opts)
	if locale != "en" {
		t.Fatalf("locale = %q, want en (cookie source disabled)", locale)
	}
}

func TestResolveLocale_Cookie_Unsupported_FallsThroughToDefault(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}
	req := newRequest(t, "/blog")
	req.AddCookie(&http.Cookie{Name: "locale", Value: "fr"})

	locale, _ := resolveLocale(req, req.URL.Path, opts)
	if locale != "en" {
		t.Fatalf("locale = %q, want en (unsupported cookie value)", locale)
	}
}

func TestResolveLocale_NoSourcesMatch_ReturnsDefault(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr"}}
	req := newRequest(t, "/blog")

	locale, remaining := resolveLocale(req, req.URL.Path, opts)
	if locale != "en" || remaining != "/blog" {
		t.Fatalf("locale=%q remaining=%q, want en /blog", locale, remaining)
	}
}

func TestResolveLocale_PathBeatsHeaderBeatsCookie(t *testing.T) {
	opts := LocaleOptions{Default: "en", Supported: []string{"en", "tr", "de"}}
	req := newRequest(t, "/de/blog")
	req.Header.Set("Accept-Language", "tr")
	req.AddCookie(&http.Cookie{Name: "locale", Value: "tr"})

	locale, remaining := resolveLocale(req, req.URL.Path, opts)
	if locale != "de" {
		t.Fatalf("locale = %q, want de (path prefix outranks header and cookie)", locale)
	}
	if remaining != "/blog" {
		t.Fatalf("remaining = %q, want /blog", remaining)
	}
}

// The following tests assert resolveAcceptLanguage never panics on
// attacker-controlled header content, per the task's threat model.

func TestResolveAcceptLanguage_Empty(t *testing.T) {
	if _, ok := resolveAcceptLanguage("", []string{"en"}); ok {
		t.Fatal("expected no match for an empty header")
	}
}

func TestResolveAcceptLanguage_Wildcard(t *testing.T) {
	locale, ok := resolveAcceptLanguage("*", []string{"en", "tr"})
	if ok {
		t.Fatalf("expected no match for a bare wildcard, got %q", locale)
	}
}

func TestResolveAcceptLanguage_QWithNoNumber(t *testing.T) {
	locale, ok := resolveAcceptLanguage("en;q=,tr;q=0.5", []string{"en", "tr"})
	if !ok || locale != "tr" {
		t.Fatalf("locale=%q ok=%v, want tr (malformed en entry skipped)", locale, ok)
	}
}

func TestResolveAcceptLanguage_QExcludedByZero(t *testing.T) {
	locale, ok := resolveAcceptLanguage("en;q=0,tr;q=0.5", []string{"en", "tr"})
	if !ok || locale != "tr" {
		t.Fatalf("locale=%q ok=%v, want tr (q=0 excludes en)", locale, ok)
	}
}

func TestResolveAcceptLanguage_ManyMalformedEntriesDoNotPanic(t *testing.T) {
	header := ";;;,q=abc,,en;q=,;q=1.5,tr;q=not-a-number,*,en-US;q=0.3"
	locale, ok := resolveAcceptLanguage(header, []string{"en", "tr"})
	if !ok || locale != "en" {
		t.Fatalf("locale=%q ok=%v, want en (from en-US subtag fallback)", locale, ok)
	}
}

func TestResolveAcceptLanguage_AbsurdlyManyEntriesDoNotPanic(t *testing.T) {
	header := ""
	for i := 0; i < 500; i++ {
		if header != "" {
			header += ","
		}
		header += "xx;q=0.1"
	}
	header += ",tr;q=0.9"
	locale, ok := resolveAcceptLanguage(header, []string{"en", "tr"})
	if !ok || locale != "tr" {
		t.Fatalf("locale=%q ok=%v, want tr", locale, ok)
	}
}
