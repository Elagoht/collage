package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// documentEnv builds a Handler serving exactly one document, with a real router, a
// real *render.SlotEngine and a real memory cache, and returns the cache alongside
// it so a test can assert on its entry count. devMode controls whether diagnostic
// detail is served on a failure.
//
// It is deliberately not built from newEnv: newEnv wires a fakeEngine that
// implements only render.Engine's page contract, and a document needs
// ExecuteDocument, which only the real render.SlotEngine provides.
func documentEnv(t *testing.T, doc *types.Document, devMode bool) (*Handler, *cache.MemoryCache) {
	t.Helper()
	return documentEnvWithDeps(t, doc, devMode, nil)
}

// documentEnvWithPlugin is documentEnv with a plugin registry holding p attached, so
// a test can assert on the hooks a document request does and does not fire. devMode
// is always false: no test needs it alongside a plugin.
func documentEnvWithPlugin(t *testing.T, doc *types.Document, p plugin.Plugin) *Handler {
	t.Helper()
	h, _ := documentEnvWithDeps(t, doc, false, p)
	return h
}

// documentEnvWithDeps is the shared construction documentEnv and
// documentEnvWithPlugin build on.
func documentEnvWithDeps(t *testing.T, doc *types.Document, devMode bool, p plugin.Plugin) (*Handler, *cache.MemoryCache) {
	t.Helper()

	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument(%q) = %v, want nil", doc.Name, err)
	}

	engine := render.New(nil, render.Options{})
	memoryCache := cache.NewMemory(cache.MemoryConfig{})
	tracker := dependency.NewMemory()

	deps := Deps{
		Router:   rt,
		Renderer: engine,
		Cache:    memoryCache,
		Tracker:  tracker,
		Logger:   slog.New(slog.DiscardHandler),
		DevMode:  devMode,
	}
	if p != nil {
		registry := plugin.NewRegistry(slog.New(slog.DiscardHandler))
		if err := registry.Register(p); err != nil {
			t.Fatalf("Register(plugin) = %v, want nil", err)
		}
		deps.Plugins = registry
	}

	h, err := New(deps)
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return h, memoryCache
}

func TestDocument_ServesBodyAndContentType(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sitemap.xml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml" {
		t.Fatalf("Content-Type = %q, want application/xml", got)
	}
	if rec.Body.String() != "<urlset/>" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("ETag is empty, want a content hash")
	}
}

func TestDocument_CacheHitDoesNotReExecuteTheHandler(t *testing.T) {
	var calls int
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			calls++
			return []byte("<urlset/>"), nil, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/sitemap.xml", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times across two requests, want 1 — the cache is not being consulted", calls)
	}
}

func TestDocument_NotModified(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), nil, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", "/sitemap.xml", nil))
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest("GET", "/sitemap.xml", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty on 304", second.Body.String())
	}
}

func TestDocument_NotFoundIsPlainTextAndUncached(t *testing.T) {
	doc := &types.Document{
		Name: "post-json", ContentType: "application/json",
		Paths:    map[string]string{"en": "/post.json"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, fmt.Errorf("lookup: %w", types.ErrNotFound)
		},
	}
	h, store := documentEnv(t, doc, false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/post.json", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain — never HTML, and never the document's own type", ct)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if entries := store.Stats().Entries; entries != 0 {
		t.Fatalf("cache holds %d entries after a 404, want 0", entries)
	}
}

func TestDocument_ProductionFailureLeaksNothing(t *testing.T) {
	doc := &types.Document{
		Name: "feed", ContentType: "application/rss+xml",
		Paths:    map[string]string{"en": "/rss.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("dial tcp db.internal:5432: password=hunter2 /srv/app/store.go")
		},
	}
	h, store := documentEnv(t, doc, false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/rss.xml", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	for _, leak := range []string{"dial tcp", "hunter2", "db.internal", "/srv/app", "feed", "collage/internal", "goroutine "} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("production 500 body leaks %q: %q", leak, rec.Body.String())
		}
	}
	if entries := store.Stats().Entries; entries != 0 {
		t.Fatalf("cache holds %d entries after a 500, want 0", entries)
	}
}

func TestDocument_DevFailureShowsTheDetail(t *testing.T) {
	doc := &types.Document{
		Name: "feed", ContentType: "application/rss+xml",
		Paths:    map[string]string{"en": "/rss.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("dial tcp db.internal:5432")
		},
	}
	h, _ := documentEnv(t, doc, true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/rss.xml", nil))

	for _, want := range []string{"feed", "dial tcp"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("dev 500 body should contain %q: %q", want, rec.Body.String())
		}
	}
}

func TestDocument_HeadWritesHeadersWithoutBody(t *testing.T) {
	doc := &types.Document{
		Name: "robots", ContentType: "text/plain; charset=utf-8",
		Paths:    map[string]string{"en": "/robots.txt"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("User-agent: *\n"), nil, nil
		},
	}
	h, _ := documentEnv(t, doc, false)

	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := http.Head(server.URL + "/robots.txt")
	if err != nil {
		t.Fatalf("HEAD = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength != int64(len("User-agent: *\n")) {
		t.Fatalf("Content-Length = %d, want %d — a HEAD reports what a GET would send", resp.ContentLength, len("User-agent: *\n"))
	}
}

func TestDocument_RenderHooksDoNotFire(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
	// recordingPlugin implements every hook and appends its name to a slice; it
	// already exists in handler_test.go. Reused rather than writing a second one.
	rec := &recordingPlugin{}
	h := documentEnvWithPlugin(t, doc, rec)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	called := rec.recorded()
	for _, forbidden := range []string{"PageResolved", "BeforeRender", "AfterRender"} {
		if slices.Contains(called, forbidden) {
			t.Fatalf("%s fired for a document; hooks called: %v — no render occurs, and "+
				"AfterRenderEvent.HTML would be a lie for a non-HTML body", forbidden, called)
		}
	}
	if !slices.Contains(called, "CacheWrite") {
		t.Fatalf("CacheWrite did not fire; hooks called: %v", called)
	}
}

// TestDocument_EmptyBodyIsAServerErrorAndUncached pins ErrEmptyDocumentBody: unlike
// a page, whose optional root fragment may legitimately render nothing, a
// document's handler result IS the entire response, so a successful call that
// produces no body is treated as a failure rather than served as an empty 200.
func TestDocument_EmptyBodyIsAServerErrorAndUncached(t *testing.T) {
	doc := &types.Document{
		Name: "empty", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/empty.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, nil
		},
	}
	h, store := documentEnv(t, doc, false)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/empty.xml", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if entries := store.Stats().Entries; entries != 0 {
		t.Fatalf("cache holds %d entries after an empty body, want 0", entries)
	}
}

// pageOnlyEngine is a render.Engine implementing only the page contract — no
// ExecuteDocument — standing in for an application-supplied Renderer that was
// never taught about documents.
type pageOnlyEngine struct{}

var _ render.Engine = pageOnlyEngine{}

// Render is never exercised by these tests; it exists only to satisfy render.Engine.
// RenderFragment is never reached: this engine exists to prove a document never
// consults the page renderer.
func (pageOnlyEngine) RenderFragment(context.Context, *types.RenderContext, *types.Fragment) ([]byte, error) {
	panic("pageOnlyEngine.RenderFragment must not be called")
}

func (pageOnlyEngine) Render(context.Context, *types.RenderContext) (*render.Result, error) {
	return &render.Result{}, nil
}

// TestDocument_RendererWithoutDocumentSupportIs500 pins ErrDocumentRenderingUnsupported:
// a Renderer that only implements render.Engine's page contract cannot serve a
// document, and serveDocument must report that as an ordinary 500 rather than
// panicking on the type assertion.
func TestDocument_RendererWithoutDocumentSupportIs500(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyDynamic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), nil, nil
		},
	}

	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument() = %v, want nil", err)
	}

	h, err := New(Deps{
		Router:   rt,
		Renderer: pageOnlyEngine{},
		Tracker:  dependency.NewMemory(),
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sitemap.xml", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
}

// TestDocument_CacheWriteEventCarriesNoPage pins the nil that
// CacheWriteEvent.Page's doc comment now promises. OnCacheWrite is one of the
// three hooks a document dispatches, so it is the one place a plugin written for
// pages meets a document, and `ev.Page.Name` there panics on every single document
// request. safeCall contains the panic, but the cache write is abandoned with it,
// so the document is never cached — one error line per request and no other
// symptom.
func TestDocument_CacheWriteEventCarriesNoPage(t *testing.T) {
	doc := &types.Document{
		Name: "sitemap", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/sitemap.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts"}, nil
		},
	}
	recorder := &recordingPlugin{}
	h := documentEnvWithPlugin(t, doc, recorder)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	written := recorder.cacheWritten()
	if len(written) != 1 {
		t.Fatalf("OnCacheWrite fired %d times, want 1", len(written))
	}
	if written[0] != nil {
		t.Fatalf("CacheWriteEvent.Page = %v for a document, want nil: a document renders no page", written[0])
	}
}

// TestPage_CacheWriteEventCarriesThePage is the other half: the field is nil only
// for a document, and a page's cache write still carries the live page it was
// rendered from. Without this control, "Page is nil" could be satisfied by a
// regression that stopped populating it everywhere.
func TestPage_CacheWriteEventCarriesThePage(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	recorder := &recordingPlugin{}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder))

	env.get("/")

	written := recorder.cacheWritten()
	if len(written) != 1 {
		t.Fatalf("OnCacheWrite fired %d times, want 1", len(written))
	}
	if written[0] != page {
		t.Fatalf("CacheWriteEvent.Page = %v, want the live page %q", written[0], page.Name)
	}
}
