package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// notFoundDataHandler returns a DataHandler that always fails with an error wrapping
// types.ErrNotFound, as a real DataHandler would for a slug that does not resolve to
// any content.
func notFoundDataHandler() types.DataHandlerFunc {
	return func(ctx context.Context, rc *types.RenderContext) (any, []string, error) { // any: matches types.DataHandlerFunc
		return nil, nil, fmt.Errorf("blog: slug %q: %w", rc.Param("slug"), types.ErrNotFound)
	}
}

// realEndToEndEnv wires a Handler over the real router AND the real render engine
// (not the fakeEngine test double used elsewhere in this package): this is the only
// way a DataHandler's ErrNotFound can actually reach the handler, since fakeEngine
// never executes one. It is what makes the per-page NotFoundPage branch
// reachable in a test at all.
type realEndToEndEnv struct {
	handler *Handler
	cache   *cache.MemoryCache
	plugins *recordingPlugin
}

// newRealEndToEndEnv builds the environment described above, registering pages
// against rt (already populated by the caller) and installing a recordingPlugin so
// the OnError dispatch can be asserted.
func newRealEndToEndEnv(t *testing.T, rt router.Router) *realEndToEndEnv {
	t.Helper()

	tmpl, err := template.NewHTML(template.HTMLConfig{Root: "testdata", Extension: ".html"})
	if err != nil {
		t.Fatalf("template.NewHTML() error = %v", err)
	}
	engine := render.New(tmpl, render.Options{})

	recorder := &recordingPlugin{}
	registry := plugin.NewRegistry(slog.New(slog.DiscardHandler))
	if err := registry.Register(recorder); err != nil {
		t.Fatalf("Register(plugin) error = %v", err)
	}

	memoryCache := cache.NewMemory(cache.MemoryConfig{})

	handler, err := New(Deps{
		Router:   rt,
		Renderer: engine,
		Cache:    memoryCache,
		Tracker:  dependency.NewMemory(),
		Plugins:  registry,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return &realEndToEndEnv{handler: handler, cache: memoryCache, plugins: recorder}
}

// get issues a GET for path and returns the recorded response.
func (e *realEndToEndEnv) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// blogFragment returns a required content fragment whose data handler always
// reports ErrNotFound, as the primary content of a dynamic "/blog/{slug}"-style page.
func blogFragment(name string) *types.Fragment {
	return &types.Fragment{
		Name:         name,
		TemplatePath: "blog.html",
		Required:     true,
		DataHandler:  notFoundDataHandler(),
	}
}

// TestNotFound_RequiredFragmentReachesPageOwnNotFoundPage is the end-to-end test
// this task exists to make possible: a real router matches a dynamic route
// perfectly, a real render engine runs the matched page's required DataHandler,
// which reports ErrNotFound, and the handler must render the *matched page's own*
// NotFoundPage — a branch that was dead code before ErrNotFound existed, because the
// router could never hand back a matched Page alongside a not-found outcome.
func TestNotFound_RequiredFragmentReachesPageOwnNotFoundPage(t *testing.T) {
	blog404 := &types.Page{
		Name:            "blog-404",
		ContentFragment: &types.Fragment{Name: "blog-404-content", TemplatePath: "blog-notfound.html"},
	}
	blogPage := &types.Page{
		Name:            "blog",
		Paths:           map[string]string{"en": "/blog/{slug}"},
		ContentFragment: blogFragment("blog-content"),
		Strategy:        types.StrategyStatic, // cacheable, so "never cached" is a meaningful assertion
		NotFoundPage:    blog404,
	}

	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.Register(blogPage); err != nil {
		t.Fatalf("Register(blog) error = %v", err)
	}

	env := newRealEndToEndEnv(t, rt)
	rec := env.get("/blog/no-such-slug")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if body := rec.Body.String(); !strings.Contains(body, "this blog post does not exist") {
		t.Errorf("body = %q, want the page's own NotFoundPage content", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
	if entries := env.cache.Stats().Entries; entries != 0 {
		t.Errorf("cache entries = %d, want 0: a not-found render must never be cached", entries)
	}

	found := false
	for _, event := range env.plugins.recorded() {
		if strings.HasPrefix(event, "Error:") {
			found = true
		}
	}
	if !found {
		t.Errorf("recorded events = %v, want an Error hook dispatched", env.plugins.recorded())
	}
}

// TestNotFound_FallsBackToGlobalNotFoundPage covers the other half of resolution:
// a page with no NotFoundPage of its own falls back to the router's registered
// global not-found page, exactly as a router-miss 404 already does.
func TestNotFound_FallsBackToGlobalNotFoundPage(t *testing.T) {
	global404 := &types.Page{
		Name:            "global-404",
		ContentFragment: &types.Fragment{Name: "global-404-content", TemplatePath: "global-notfound.html"},
	}
	blogPage := &types.Page{
		Name:            "blog-no-custom-404",
		Paths:           map[string]string{"en": "/articles/{slug}"},
		ContentFragment: blogFragment("articles-content"),
		Strategy:        types.StrategyStatic,
		// NotFoundPage deliberately left nil.
	}

	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.Register(blogPage); err != nil {
		t.Fatalf("Register(blog-no-custom-404) error = %v", err)
	}
	if err := rt.RegisterNotFound(global404); err != nil {
		t.Fatalf("RegisterNotFound() error = %v", err)
	}

	env := newRealEndToEndEnv(t, rt)
	rec := env.get("/articles/no-such-slug")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if body := rec.Body.String(); !strings.Contains(body, "nothing here") {
		t.Errorf("body = %q, want the router's global NotFoundPage content", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
	if entries := env.cache.Stats().Entries; entries != 0 {
		t.Errorf("cache entries = %d, want 0: a not-found render must never be cached", entries)
	}
}
