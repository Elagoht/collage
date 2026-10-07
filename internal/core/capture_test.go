package core

import (
	"context"
	"crypto/rand"
	"net/http"
	"slices"
	"testing"
)

// newCaptureApp builds an App with a page at /a, an RSS document at /feed.xml, and
// a middleware that sets one stable header, one that changes on every response,
// and a cookie.
func newCaptureApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t, func(cfg *Config) { cfg.BaseURL = "https://example.com" })
	page := newHomePage()
	page.Paths = map[string]string{"en": "/a"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	doc := newSitemapDocument()
	doc.Name = "feed"
	doc.ContentType = "application/rss+xml"
	doc.Paths = map[string]string{"en": "/feed.xml"}
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Stable", "1")
			w.Header().Set("X-Nonce", rand.Text())
			w.Header().Set("X-Host", r.Host)
			w.Header().Set("Set-Cookie", "s=1")
			next.ServeHTTP(w, r)
		})
	})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

func TestCaptureResponses(t *testing.T) {
	app := newCaptureApp(t)
	got, err := app.CaptureResponses(context.Background(), []string{"/a", "/feed.xml"})
	if err != nil {
		t.Fatal(err)
	}
	a := got["/a"]
	if a.Status != 200 || a.Headers.Get("X-Stable") != "1" || a.Headers.Get("Cache-Control") == "" {
		t.Errorf("/a = %+v", a)
	}
	if h := a.Headers.Get("X-Host"); h != "example.com" {
		t.Errorf("/a was asked with Host %q, want example.com", h)
	}
	for _, dropped := range []string{"Date", "Etag", "Set-Cookie", "Vary", "Content-Length", "X-Nonce"} {
		if a.Headers.Get(dropped) != "" {
			t.Errorf("/a kept %s", dropped)
		}
	}
	if !slices.Contains(a.Unstable, "X-Nonce") {
		t.Errorf("/a Unstable = %v, want X-Nonce", a.Unstable)
	}
	if slices.Contains(a.Unstable, "Etag") || slices.Contains(a.Unstable, "Set-Cookie") {
		t.Errorf("/a Unstable = %v, names a request-specific header", a.Unstable)
	}
	if got["/feed.xml"].Headers.Get("Content-Type") != "application/rss+xml" {
		t.Errorf("/feed.xml Content-Type = %q", got["/feed.xml"].Headers.Get("Content-Type"))
	}
}

// TestCaptureResponses_HeaderOnlyInOneResponseIsUnstable: a header the second
// response carries and the first does not is as per-response as one whose value
// changed.
func TestCaptureResponses_HeaderOnlyInOneResponseIsUnstable(t *testing.T) {
	app := newTestApp(t, nil)
	page := newHomePage()
	page.Paths = map[string]string{"en": "/a"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	n := 0
	err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			if n%2 == 0 {
				w.Header().Set("X-Second", "1")
			} else {
				w.Header().Set("X-First", "1")
			}
			next.ServeHTTP(w, r)
		})
	})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	got, err := app.CaptureResponses(context.Background(), []string{"/a"})
	if err != nil {
		t.Fatal(err)
	}
	a := got["/a"]
	if !slices.Equal(a.Unstable, []string{"X-First", "X-Second"}) {
		t.Errorf("Unstable = %v, want [X-First X-Second]", a.Unstable)
	}
	if a.Headers.Get("X-First") != "" || a.Headers.Get("X-Second") != "" {
		t.Errorf("Headers kept a one-sided header: %v", a.Headers)
	}
}

// TestCaptureResponses_NonOKStatusIsRecorded: a path the application does not
// answer with 200 is recorded with what it did answer.
func TestCaptureResponses_NonOKStatusIsRecorded(t *testing.T) {
	app := newCaptureApp(t)
	got, err := app.CaptureResponses(context.Background(), []string{"/missing", "/with space"})
	if err != nil {
		t.Fatal(err)
	}
	if s := got["/missing"].Status; s != http.StatusNotFound {
		t.Errorf("/missing status = %d, want 404", s)
	}
	if s := got["/with space"].Status; s != http.StatusNotFound {
		t.Errorf("/with space status = %d, want 404", s)
	}
}

// TestCaptureResponses_FollowsTheTrailingSlashSpelling: under TrailingSlash a
// page is answered at "/a/" and its other spelling redirects there; the capture
// records the page's answer, not the redirect, and leaves a document alone.
func TestCaptureResponses_FollowsTheTrailingSlashSpelling(t *testing.T) {
	app := newTestApp(t, func(cfg *Config) { cfg.TrailingSlash = true })
	page := newHomePage()
	page.Paths = map[string]string{"en": "/a"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.RegisterDocument(newSitemapDocument()); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	got, err := app.CaptureResponses(context.Background(), []string{"/a", "/a/", "/sitemap.xml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/a", "/a/"} {
		if r := got[p]; r.Status != http.StatusOK || r.Headers.Get("Location") != "" || r.Headers.Get("Cache-Control") == "" {
			t.Errorf("%s = %+v, want the page's 200", p, r)
		}
	}
	if r := got["/sitemap.xml"]; r.Status != http.StatusOK || r.Headers.Get("Content-Type") != "application/xml" {
		t.Errorf("/sitemap.xml = %+v", r)
	}
}
