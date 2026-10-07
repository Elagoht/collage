package core

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// newCaptureApp builds an App with a page at /a, an RSS document at /feed.xml, a
// middleware that sets one stable header, one that changes on every response,
// and a cookie, and inside it the extra middleware given.
func newCaptureApp(t *testing.T, extra ...func(http.Handler) http.Handler) *App {
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
	doc.Strategy = types.StrategyIncremental // cached, so a capture's cache write could show
	doc.CacheTTL = time.Minute
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
	for _, m := range extra {
		if err := app.Use(m); err != nil {
			t.Fatalf("Use: %v", err)
		}
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

// TestCaptureResponses_KeepsTheHeadersSentWithTheStatus: a header a middleware
// sets after the response is written is never sent, so it is neither kept nor
// called unstable.
func TestCaptureResponses_KeepsTheHeadersSentWithTheStatus(t *testing.T) {
	app := newCaptureApp(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			w.Header().Set("X-Late", rand.Text())
		})
	})
	got, err := app.CaptureResponses(context.Background(), []string{"/a", "/feed.xml"})
	if err != nil {
		t.Fatal(err)
	}
	for p, r := range got {
		if r.Headers.Get("X-Late") != "" || slices.Contains(r.Unstable, "X-Late") {
			t.Errorf("%s = %+v, want no X-Late anywhere", p, r)
		}
	}
}

// cacheWriteCounter counts CacheWriteHook calls.
type cacheWriteCounter struct{ n atomic.Int32 }

func (*cacheWriteCounter) Name() string                            { return "test/cachewrites" }
func (*cacheWriteCounter) Version() string                         { return "0" }
func (*cacheWriteCounter) Init(context.Context, plugin.Host) error { return nil }
func (*cacheWriteCounter) Shutdown(context.Context) error          { return nil }
func (c *cacheWriteCounter) OnCacheWrite(context.Context, *plugin.CacheWriteEvent) error {
	c.n.Add(1)
	return nil
}

// TestCaptureResponses_LeavesTheCacheAlone: a capture writes no cache entry and
// calls no CacheWriteHook, for a page or a document; a reader's request after it
// does both, which is what shows the check can fail.
func TestCaptureResponses_LeavesTheCacheAlone(t *testing.T) {
	app := newCaptureApp(t)
	counter := &cacheWriteCounter{}
	if err := app.RegisterPlugin(counter); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if _, err := app.CaptureResponses(context.Background(), []string{"/a", "/feed.xml"}); err != nil {
		t.Fatal(err)
	}
	store := app.store.(*cache.MemoryCache)
	if n := store.Stats().Entries; n != 0 || counter.n.Load() != 0 {
		t.Fatalf("after capture: %d entries, %d CacheWrite calls; want none", n, counter.n.Load())
	}
	get(app.Handler(), "/a")
	get(app.Handler(), "/feed.xml")
	if n := store.Stats().Entries; n != 2 || counter.n.Load() != 2 {
		t.Fatalf("after two reads: %d entries, %d CacheWrite calls; want 2 and 2", n, counter.n.Load())
	}
}

// TestCaptureResponses_MarksItsRequests: a middleware sees IsCapture on a
// capture's request, and not on a reader's.
func TestCaptureResponses_MarksItsRequests(t *testing.T) {
	var seen []bool
	app := newCaptureApp(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, types.IsCapture(r.Context()))
			next.ServeHTTP(w, r)
		})
	})
	if _, err := app.CaptureResponses(context.Background(), []string{"/a"}); err != nil {
		t.Fatal(err)
	}
	get(app.Handler(), "/a")
	if !slices.Equal(seen, []bool{true, true, false}) {
		t.Errorf("IsCapture per request = %v, want [true true false]", seen)
	}
}

// shortCaptureTimeout shortens captureTimeout for one test.
func shortCaptureTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := captureTimeout
	captureTimeout = d
	t.Cleanup(func() { captureTimeout = old })
}

// TestCaptureResponses_APathNotAnsweredInTimeFails: a handler that waits on its
// context, and one that ignores it, are both given up on at the deadline and
// recorded with Err; the paths after them are still captured.
func TestCaptureResponses_APathNotAnsweredInTimeFails(t *testing.T) {
	shortCaptureTimeout(t, 50*time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	app := newCaptureApp(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/waits":
				<-r.Context().Done()
				http.Error(w, "gone", http.StatusServiceUnavailable)
				return
			case "/ignores":
				<-release
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	start := time.Now()
	got, err := app.CaptureResponses(context.Background(), []string{"/waits", "/ignores", "/a"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("capture took %s", elapsed)
	}
	for _, p := range []string{"/waits", "/ignores"} {
		if r := got[p]; r.Err == nil || r.Status != 0 || r.Headers != nil {
			t.Errorf("%s = %+v, want only Err", p, r)
		}
	}
	if got["/a"].Status != http.StatusOK {
		t.Errorf("/a = %+v", got["/a"])
	}
}

// TestCaptureResponses_ReturnsTheContextsError: a cancelled build stops the
// capture with its error.
func TestCaptureResponses_ReturnsTheContextsError(t *testing.T) {
	app := newCaptureApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.CaptureResponses(ctx, []string{"/a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestCaptureResponses_ADifferentSecondStatusIsRecorded: two answers with two
// statuses are recorded with the first and the other.
func TestCaptureResponses_ADifferentSecondStatusIsRecorded(t *testing.T) {
	n := 0
	app := newCaptureApp(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if n++; n == 2 {
				http.Error(w, "busy", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	got, err := app.CaptureResponses(context.Background(), []string{"/a"})
	if err != nil {
		t.Fatal(err)
	}
	if r := got["/a"]; r.Status != http.StatusOK || r.OtherStatus != http.StatusTooManyRequests {
		t.Errorf("/a = %+v, want 200 then 429", r)
	}
}
