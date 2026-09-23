package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// These tests pin the rule routeKind exists for, on the path that used to break
// it: the content type of an error follows the route kind, not the request, on
// EVERY failure path including a panic raised in code the framework did not write.
// Before routeRef, a panic anywhere in serveDocument outside ExecuteDocument
// unwound to serveGuarded, which knew nothing about the document and rendered the
// HTML error page for /sitemap.xml.

// countingWriter wraps a ResponseRecorder and counts how many times a status line
// was written. It exists because "exactly one response" is half of what these
// tests assert: a fix that recovers a panic in two places at once would still
// produce plain text, and would still be wrong — the second writer would log a
// superfluous WriteHeader and append a second body to the first one's.
type countingWriter struct {
	*httptest.ResponseRecorder
	headers int
}

var _ http.ResponseWriter = (*countingWriter)(nil)

// newCountingWriter returns a countingWriter over a fresh recorder.
func newCountingWriter() *countingWriter {
	return &countingWriter{ResponseRecorder: httptest.NewRecorder()}
}

// WriteHeader counts the call and forwards it.
func (c *countingWriter) WriteHeader(status int) {
	c.headers++
	c.ResponseRecorder.WriteHeader(status)
}

// panickingCache is a cache.Cache whose Get panics, standing in for an
// application-supplied cache that breaks mid-lookup. Every other method is inert:
// no test reaches them, because Get is the first call the document path makes.
type panickingCache struct{}

var _ cache.Cache = panickingCache{}

// Get panics instead of returning a lookup result.
func (panickingCache) Get(context.Context, string) ([]byte, string, bool) {
	panic("cache exploded")
}

// Set is never reached: Get panics first.
func (panickingCache) Set(context.Context, string, []byte, time.Duration) (string, error) {
	return "", nil
}

// Invalidate is never reached.
func (panickingCache) Invalidate(context.Context, []string) error { return nil }

// InvalidateKey is never reached.
func (panickingCache) InvalidateKey(context.Context, string) error { return nil }

// Clear is never reached.
func (panickingCache) Clear(context.Context) error { return nil }

// panickingTracker is a dependency.Tracker whose Track panics, standing in for an
// application-supplied tracker that breaks on the write that follows a successful
// document execution — after the body exists but before it has gone out.
type panickingTracker struct{}

var _ dependency.Tracker = panickingTracker{}

// Track panics instead of recording the key's tags.
func (panickingTracker) Track(context.Context, string, []string) error {
	panic("tracker exploded")
}

// Resolve is never reached.
func (panickingTracker) Resolve(context.Context, []string) ([]string, error) { return nil, nil }

// Forget is never reached.
func (panickingTracker) Forget(context.Context, string) error { return nil }

// ForgetTags is never reached.
func (panickingTracker) ForgetTags(context.Context, []string) error { return nil }

// Tags is never reached.
func (panickingTracker) Tags(context.Context, string) ([]string, error) { return nil, nil }

// Clear is never reached.
func (panickingTracker) Clear(context.Context) error { return nil }

// panickingMetrics is an observability.Metrics whose CacheEvent panics. It is the
// third collaborator serveDocument calls into that the framework does not own, and
// the one the finding names alongside the cache and the tracker.
//
// It embeds the no-op Metrics rather than a nil interface deliberately: the calls
// ServeHTTP makes outside serveGuarded — HTTPResponse in particular — are NOT
// covered by the panic guard, which serveGuarded's own doc comment now says
// plainly. A nil embedded interface would panic there instead and take the test
// process down, which is exactly the behaviour that comment describes.
type panickingMetrics struct{ observability.Metrics }

var _ observability.Metrics = panickingMetrics{}

// newPanickingMetrics returns a panickingMetrics that is inert except for
// CacheEvent.
func newPanickingMetrics() panickingMetrics {
	return panickingMetrics{Metrics: observability.MetricsOrNoop(nil)}
}

// CacheEvent panics instead of reporting the event.
func (panickingMetrics) CacheEvent(context.Context, observability.CacheEvent, string) {
	panic("metrics exploded")
}

// errorPagePanickingRouter is a router.Router that resolves normally but panics
// from its error-page surface. It proves a negative that matters: a document's
// failure must never consult the site's HTML error pages, so a custom Router's
// NotFoundPage or ErrorPage cannot contaminate — or, as here, take down — a
// document response.
type errorPagePanickingRouter struct {
	router.Router
}

var _ router.Router = (*errorPagePanickingRouter)(nil)

// NotFoundPage panics: reaching it on a document route is itself the failure.
func (e *errorPagePanickingRouter) NotFoundPage() *types.Page {
	panic("NotFoundPage must not be consulted for a document")
}

// ErrorPage panics: reaching it on a document route is itself the failure.
func (e *errorPagePanickingRouter) ErrorPage() *types.Page {
	panic("ErrorPage must not be consulted for a document")
}

// sitemapDoc returns a valid, cacheable document fixture for these tests.
func sitemapDoc() *types.Document {
	return &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
}

// failureEnv builds a Handler serving doc, with deps' Router, Cache, Tracker and
// Metrics overridden where they are non-nil. It is separate from documentEnv
// because every test here replaces one collaborator with a panicking one, which
// documentEnv does not allow.
func failureEnv(t *testing.T, doc *types.Document, override Deps) *Handler {
	t.Helper()

	rt := override.Router
	if rt == nil {
		real := router.New(router.LocaleOptions{Default: "en"})
		if err := real.RegisterDocument(doc); err != nil {
			t.Fatalf("RegisterDocument(%q) = %v, want nil", doc.Name, err)
		}
		rt = real
	}

	deps := Deps{
		Router:   rt,
		Renderer: render.New(nil, render.Options{}),
		Cache:    override.Cache,
		Tracker:  override.Tracker,
		Metrics:  override.Metrics,
		Logger:   slog.New(slog.DiscardHandler),
	}
	if deps.Cache == nil {
		deps.Cache = cache.NewMemory(cache.MemoryConfig{})
	}
	if deps.Tracker == nil {
		deps.Tracker = dependency.NewMemory()
	}

	h, err := New(deps)
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return h
}

// assertPlainTextFailure checks that exactly one 500 went out, as plain text, with
// no HTML anywhere in it.
func assertPlainTextFailure(t *testing.T, w *countingWriter, what string) {
	t.Helper()

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("%s: status = %d, want 500", what, w.Code)
	}
	if w.headers != 1 {
		t.Fatalf("%s: wrote %d status lines, want exactly 1", what, w.headers)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("%s: Content-Type = %q, want text/plain — an error's content type follows the route kind, "+
			"and a document is never an HTML route", what, ct)
	}
	if body := w.Body.String(); strings.Contains(body, "<!DOCTYPE html>") || strings.Contains(body, "<html") {
		t.Fatalf("%s: body is the HTML error page, want plain text: %q", what, body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("%s: Cache-Control = %q, want no-store", what, got)
	}
}

// TestDocument_PanickingCacheIsPlainText is the finding's own reproduction: an
// application Cache whose Get panics on a document route used to answer
// /sitemap.xml with text/html and a <!DOCTYPE html> body, because the panic
// unwound past the only frame that knew it was serving a document.
func TestDocument_PanickingCacheIsPlainText(t *testing.T) {
	h := failureEnv(t, sitemapDoc(), Deps{Cache: panickingCache{}})

	w := newCountingWriter()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))

	assertPlainTextFailure(t, w, "panicking Cache.Get")
}

// TestDocument_PanickingTrackerIsPlainText covers the panic raised after the
// document has executed: the body exists, the cache write is under way, and the
// tracker breaks. The response is still the document's plain text, and still one
// response.
func TestDocument_PanickingTrackerIsPlainText(t *testing.T) {
	h := failureEnv(t, sitemapDoc(), Deps{Tracker: panickingTracker{}})

	w := newCountingWriter()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))

	assertPlainTextFailure(t, w, "panicking Tracker.Track")
}

// TestDocument_PanickingMetricsIsPlainText covers the third collaborator
// serveDocument calls into that the framework does not own.
func TestDocument_PanickingMetricsIsPlainText(t *testing.T) {
	h := failureEnv(t, sitemapDoc(), Deps{Metrics: newPanickingMetrics()})

	w := newCountingWriter()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))

	assertPlainTextFailure(t, w, "panicking Metrics.CacheEvent")
}

// TestDocument_FailureNeverConsultsTheRoutersErrorPages is the custom-Router half
// of the finding, and it is a negative: a document's failure must not reach the
// router's NotFoundPage or ErrorPage at all. Those two methods are the only ones a
// custom Router exposes after a route has resolved, so this is what "a custom
// Router cannot turn a document's error into an HTML one" means concretely. A fix
// that routed a document failure through errorPageFor would panic here.
//
// The document's handler reports ErrNotFound, so this exercises the 404 path,
// where resolveNotFound would be consulted; the 500 path resolves through
// resolveError, and both are covered because either call panics.
func TestDocument_FailureNeverConsultsTheRoutersErrorPages(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, types.ErrNotFound
		},
	}

	real := router.New(router.LocaleOptions{Default: "en"})
	if err := real.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument() = %v, want nil", err)
	}
	h := failureEnv(t, doc, Deps{Router: &errorPagePanickingRouter{Router: real}})

	w := newCountingWriter()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if w.headers != 1 {
		t.Fatalf("wrote %d status lines, want exactly 1", w.headers)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
}

// TestDocument_PanickingRouterIsStillOneResponse covers the one panic site a
// custom Router really has: Match itself. It is deliberately NOT asserted to be
// plain text, and that is the honest boundary of the rule rather than a gap in the
// fix. Match panicked before it said what the path resolves to, so there is no
// route kind to follow — nothing in the process knows that "/sitemap.xml" is a
// document rather than a page — and routeKindPage, the site's HTML surface, is the
// only answer available. What this pins is that the panic still becomes exactly
// one well-formed 500 rather than a dropped connection.
func TestDocument_PanickingRouterIsStillOneResponse(t *testing.T) {
	h := failureEnv(t, sitemapDoc(), Deps{Router: &panickingRouter{}})

	w := newCountingWriter()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if w.headers != 1 {
		t.Fatalf("wrote %d status lines, want exactly 1", w.headers)
	}
}

// TestPage_PanickingCacheIsStillHTML is the control for all of the above: the page
// path's behaviour must be exactly what it was. A panic on a page route still
// renders the HTML error page, because a page IS the site's HTML surface.
func TestPage_PanickingCacheIsStillHTML(t *testing.T) {
	page := &types.Page{
		Name:     "home",
		Paths:    map[string]string{"en": "/"},
		Strategy: types.StrategyStatic,
	}
	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.Register(page); err != nil {
		t.Fatalf("Register() = %v, want nil", err)
	}

	h, err := New(Deps{
		Router:   rt,
		Renderer: &fakeEngine{},
		Cache:    panickingCache{},
		Tracker:  dependency.NewMemory(),
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	w := newCountingWriter()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if w.headers != 1 {
		t.Fatalf("wrote %d status lines, want exactly 1", w.headers)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html — a page's error surface is unchanged", ct)
	}
	if !strings.Contains(w.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("page 500 body = %q, want the built-in HTML error page", w.Body.String())
	}
}
