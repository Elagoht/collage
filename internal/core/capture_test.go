package core

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
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
	for name, base := range map[string]string{"no BaseURL": "", "https": "https://example.com"} {
		t.Run(name, func(t *testing.T) { followsTheTrailingSlashSpelling(t, base) })
	}
}

func followsTheTrailingSlashSpelling(t *testing.T, base string) {
	app := newTestApp(t, func(cfg *Config) { cfg.TrailingSlash, cfg.BaseURL = true, base })
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

// TestCaptureResponses_NeverSharesAReadersRender: a capture and a reader asking
// for one page at once each render on their own, whichever came first. The
// reader's render is unmarked and cached; the capture's writes nothing. Each
// render waits on a gate until both have entered, so without the separation
// the second would join the first's flight and never enter at all.
func TestCaptureResponses_NeverSharesAReadersRender(t *testing.T) {
	for _, readerFirst := range []bool{true, false} {
		name := "capture first"
		if readerFirst {
			name = "reader first"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan bool, 8)
			gate := make(chan struct{})
			content := &types.Fragment{Name: "gated", TemplatePath: "pages/home.html"}
			content.SetDataSource(types.FetchedData(func(ctx context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
				entered <- types.IsCapture(rc.Context())
				<-gate
				return homeData{Title: "Gated"}, nil, nil
			}, nil))
			// The capture's Host is BaseURL's, the reader's httptest's: one
			// cache key, so one flight they could share.
			app := newTestApp(t, func(cfg *Config) { cfg.BaseURL = "http://example.com" })
			page := newHomePage()
			page.ContentFragment = content
			page.Paths = map[string]string{"en": "/a"}
			if err := app.RegisterPage(page); err != nil {
				t.Fatalf("RegisterPage: %v", err)
			}
			counter := &cacheWriteCounter{}
			if err := app.RegisterPlugin(counter); err != nil {
				t.Fatalf("RegisterPlugin: %v", err)
			}
			handler := app.Handler()

			var wg sync.WaitGroup
			var readerCode int
			reader := func() {
				defer wg.Done()
				readerCode = get(handler, "/a").Code
			}
			var captured map[string]types.CapturedResponse
			var captureErr error
			capture := func() {
				defer wg.Done()
				captured, captureErr = app.CaptureResponses(context.Background(), []string{"/a"})
			}
			first, second := capture, reader
			if readerFirst {
				first, second = reader, capture
			}
			wait := func(what string) bool {
				select {
				case marked := <-entered:
					return marked
				case <-time.After(5 * time.Second):
					t.Fatalf("%s never rendered: it joined the other's render", what)
					return false
				}
			}
			wg.Add(2)
			go first()
			firstMarked := wait("the first request")
			go second()
			secondMarked := wait("the second request")
			if firstMarked == secondMarked {
				t.Fatalf("marks %v and %v: want one capture and one reader", firstMarked, secondMarked)
			}
			close(gate)
			wg.Wait()
			// The capture's second request renders too, past the open gate.
			for len(entered) > 0 {
				if !<-entered {
					t.Error("an unexpected reader render")
				}
			}

			if captureErr != nil || captured["/a"].Status != http.StatusOK || readerCode != http.StatusOK {
				t.Fatalf("capture %+v %v, reader %d", captured["/a"], captureErr, readerCode)
			}
			if n := app.store.(*cache.MemoryCache).Stats().Entries; n != 1 || counter.n.Load() != 1 {
				t.Errorf("%d entries, %d CacheWrite calls; want the reader's one and one", n, counter.n.Load())
			}
		})
	}
}

// TestCaptureResponses_NeverSharesAReadersDocument is the document path's
// TestCaptureResponses_NeverSharesAReadersRender, with the reader first.
func TestCaptureResponses_NeverSharesAReadersDocument(t *testing.T) {
	entered := make(chan bool, 8)
	gate := make(chan struct{})
	app := newTestApp(t, func(cfg *Config) { cfg.BaseURL = "http://example.com" })
	doc := newSitemapDocument()
	doc.Strategy = types.StrategyIncremental
	doc.CacheTTL = time.Minute
	doc.Handler = func(ctx context.Context, _ *types.RenderContext) ([]byte, []string, error) {
		entered <- types.IsCapture(ctx)
		<-gate
		return []byte("<urlset/>"), nil, nil
	}
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	counter := &cacheWriteCounter{}
	if err := app.RegisterPlugin(counter); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	handler := app.Handler()
	wait := func(what string) bool {
		select {
		case marked := <-entered:
			return marked
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never rendered: it joined the other's render", what)
			return false
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); get(handler, "/sitemap.xml") }()
	if wait("the reader") {
		t.Fatal("the reader's render is marked")
	}
	go func() {
		defer wg.Done()
		_, _ = app.CaptureResponses(context.Background(), []string{"/sitemap.xml"})
	}()
	if !wait("the capture") {
		t.Fatal("the capture's render is unmarked")
	}
	close(gate)
	wg.Wait()
	if n := app.store.(*cache.MemoryCache).Stats().Entries; n != 1 || counter.n.Load() != 1 {
		t.Errorf("%d entries, %d CacheWrite calls; want the reader's one and one", n, counter.n.Load())
	}
}

// TestCaptureResponses_StopsAfterTooManyTimeoutsInARow: five paths in a row not
// answered in time end the capture, naming how many were left; an answered path
// between timeouts starts the count again.
func TestCaptureResponses_StopsAfterTooManyTimeoutsInARow(t *testing.T) {
	shortCaptureTimeout(t, 20*time.Millisecond)
	app := newCaptureApp(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/slow") {
				<-r.Context().Done()
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	paths := []string{"/slow1", "/slow2", "/slow3", "/slow4", "/a", "/slow5", "/slow6", "/slow7", "/slow8", "/slow9", "/feed.xml", "/slow10"}
	got, err := app.CaptureResponses(context.Background(), paths)
	if !errors.Is(err, types.ErrCaptureStopped) || !strings.Contains(err.Error(), "2 path(s) left uncaptured") {
		t.Fatalf("err = %v, want ErrCaptureStopped with 2 paths left", err)
	}
	if len(got) != 10 || got["/a"].Status != http.StatusOK {
		t.Errorf("captured %d paths, /a = %+v; want the first ten, /a answered", len(got), got["/a"])
	}
	for _, p := range []string{"/slow1", "/slow9"} {
		if !errors.Is(got[p].Err, types.ErrCaptureTimeout) {
			t.Errorf("%s Err = %v, want ErrCaptureTimeout", p, got[p].Err)
		}
	}
	if _, ok := got["/feed.xml"]; ok {
		t.Error("a path after the capture stopped was asked for")
	}
}

// TestCaptureResponses_AsksOverHTTPSForAnHTTPSBaseURL: a middleware that sends a
// header only over HTTPS, as collage-secure sends Strict-Transport-Security, is
// captured for an https BaseURL — every static host serves HTTPS — and not for
// an http one.
func TestCaptureResponses_AsksOverHTTPSForAnHTTPSBaseURL(t *testing.T) {
	for _, tc := range []struct {
		base string
		want string
	}{
		{"https://example.com", "max-age=63072000"},
		{"http://example.com", ""},
		{"", ""},
	} {
		app := newTestApp(t, func(cfg *Config) { cfg.BaseURL = tc.base })
		page := newHomePage()
		page.Paths = map[string]string{"en": "/a"}
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
		var schemes []string
		err := app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				schemes = append(schemes, r.URL.Scheme)
				if r.TLS != nil {
					w.Header().Set("Strict-Transport-Security", "max-age=63072000")
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
		if a.Status != http.StatusOK {
			t.Errorf("%q: /a = %+v, want 200", tc.base, a)
		}
		if h := a.Headers.Get("Strict-Transport-Security"); h != tc.want {
			t.Errorf("%q: Strict-Transport-Security = %q, want %q", tc.base, h, tc.want)
		}
		wantScheme := ""
		if tc.want != "" {
			wantScheme = "https"
		}
		for _, s := range schemes {
			if s != wantScheme {
				t.Errorf("%q: asked with URL.Scheme %q, want %q", tc.base, s, wantScheme)
			}
		}
	}
}

// A development build captures with the production BaseURL's host, which the
// development Host check does not know. A capture is the application asking
// itself, in process — the marker cannot come from the wire — so it is not
// refused.
func TestCaptureResponses_DevModeWithAProductionBaseURL(t *testing.T) {
	app := newTestApp(t, func(cfg *Config) {
		cfg.DevMode = true
		cfg.BaseURL = "https://mysite.dev"
	})
	page := newHomePage()
	page.Paths = map[string]string{"en": "/a"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	got, err := app.CaptureResponses(context.Background(), []string{"/a"})
	if err != nil {
		t.Fatal(err)
	}
	if a := got["/a"]; a.Status != http.StatusOK || a.Err != nil {
		t.Errorf("/a = %d, %v; want 200", a.Status, a.Err)
	}
}
