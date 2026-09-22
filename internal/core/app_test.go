package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// ---------------------------------------------------------------------------
// Fixtures
//
// Every test here builds a real App over a real template directory: a real
// template engine, a real router, a real render engine, a real cache, and a real
// dependency tracker. There are no stubs, because the thing under test is the
// wiring, and a stub would be exactly the part that is not being checked.
//
// No fragment in these fixtures declares a DataHandler. types.DataHandlerFunc's
// signature contains "any", and a function literal can only be assigned to it by
// spelling that signature out — which this task is not permitted to do. The
// dependency tags a data handler would emit are supplied through
// Page.DependencyTags instead: the render engine folds both into the same
// Result.DependencyTags, so every path downstream of the render — cache write,
// tracker, invalidation — is exercised identically. See the task report.
// ---------------------------------------------------------------------------

const (
	// layoutTemplate is the layout every fixture page wraps its content in. The
	// <main> marker makes it visible in an assertion that the layout actually
	// rendered, rather than the content alone.
	layoutTemplate = `<!doctype html><title>collage</title><main>{{slot "content"}}</main>`
	// homeTemplate is the home page's content.
	homeTemplate = `<h1>Welcome Home</h1>`
	// aboutTemplate is a second page's content, deliberately distinguishable from
	// homeTemplate so a test cannot mistake one page's output for the other's.
	aboutTemplate = `<h1>About Us</h1>`
)

// defaultTemplates returns the template set every fixture app is built over,
// keyed by the path a fragment names.
func defaultTemplates() map[string]string {
	return map[string]string{
		"layouts/default.html": layoutTemplate,
		"pages/home.html":      homeTemplate,
		"pages/about.html":     aboutTemplate,
	}
}

// writeTemplates writes files into a fresh temporary directory and returns its
// path, creating any parent directories a key names.
func writeTemplates(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// testConfig returns a Config over root with caching on and a single "en" locale,
// already in the shape pkg/collage.New would hand core.New.
func testConfig(root string) Config {
	return Config{
		Server: ServerConfig{
			Host:            "127.0.0.1",
			Port:            0,
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    5 * time.Second,
			IdleTimeout:     5 * time.Second,
			ShutdownTimeout: 5 * time.Second,
		},
		Template: TemplateConfig{
			Root:      root,
			Extension: ".html",
			Timeout:   time.Second,
		},
		Cache: CacheConfig{
			Enabled:    true,
			Type:       "memory",
			DefaultTTL: time.Minute,
		},
		Locale: LocaleConfig{
			Default:   "en",
			Supported: []string{"en"},
		},
	}
}

// newTestApp builds an App over the default template set, applying mutate to the
// configuration first when it is non-nil.
func newTestApp(t *testing.T, mutate func(*Config)) *App {
	t.Helper()
	return newTestAppWith(t, defaultTemplates(), mutate)
}

// newTestAppWith builds an App over files, applying mutate to the configuration
// first when it is non-nil.
func newTestAppWith(t *testing.T, files map[string]string, mutate func(*Config)) *App {
	t.Helper()

	cfg := testConfig(writeTemplates(t, files))
	if mutate != nil {
		mutate(&cfg)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

// newLayout returns a layout fragment declaring the required content slot, the
// shape the specification's own example uses.
func newLayout(name string) *types.Fragment {
	return &types.Fragment{
		Name:         name,
		TemplatePath: "layouts/default.html",
		Slots: map[string]*types.SlotDefinition{
			types.DefaultContentSlot: {Name: types.DefaultContentSlot, Required: true},
		},
	}
}

// newHomePage returns the framework's minimal example page: a layout, a content
// fragment, one path, an incremental strategy, and one dependency tag.
func newHomePage() *types.Page {
	return &types.Page{
		Name:            "home",
		LayoutFragment:  newLayout("layout"),
		ContentFragment: &types.Fragment{Name: "home-content", TemplatePath: "pages/home.html"},
		Paths:           map[string]string{"en": "/"},
		Strategy:        types.StrategyIncremental,
		CacheTTL:        5 * time.Minute,
		DependencyTags:  []string{"homepage"},
	}
}

// renderCounts splits the RenderDuration calls a RecordingMetrics collected into
// fresh renders and cache hits. The split is only meaningful because both the
// render engine and the HTTP handler report through the same Metrics instance:
// the engine reports every fresh render and the handler reports every cache hit,
// so one recorder sees both halves and neither is counted twice.
func renderCounts(m *observability.RecordingMetrics) (fresh, hits int) {
	for _, call := range m.Snapshot().RenderDurations {
		if call.CacheHit {
			hits++
			continue
		}
		fresh++
	}
	return fresh, hits
}

// get serves a GET for path through handler and returns the recorder.
func get(handler http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

// ---------------------------------------------------------------------------
// The end-to-end test
// ---------------------------------------------------------------------------

// TestApp_ServesRendersCachesAndInvalidates is the test that exercises the whole
// framework in one path: routing, rendering, caching, dependency tracking, and
// invalidation. A page is registered, served, served again from cache, its tag is
// invalidated, and it is served once more — and each step is proved by the render
// the framework did or did not perform, not merely by the status code.
func TestApp_ServesRendersCachesAndInvalidates(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	app := newTestApp(t, func(cfg *Config) { cfg.Observability.Metrics = metrics })

	page := newHomePage()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	handler := app.Handler()

	first := get(handler, "/")
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", first.Code, http.StatusOK)
	}
	body := first.Body.String()
	if !strings.Contains(body, "Welcome Home") {
		t.Fatalf("first request body = %q, want it to contain the rendered content", body)
	}
	if !strings.Contains(body, "<main>") {
		t.Fatalf("first request body = %q, want it wrapped in the layout", body)
	}
	if got := first.Header().Get("Vary"); got != "Accept-Language, Cookie" {
		t.Fatalf("Vary = %q, want the enabled locale sources", got)
	}

	if fresh, hits := renderCounts(metrics); fresh != 1 || hits != 0 {
		t.Fatalf("after first request: fresh=%d hits=%d, want 1 and 0", fresh, hits)
	}

	second := get(handler, "/")
	if second.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want %d", second.Code, http.StatusOK)
	}
	if got := second.Body.String(); got != body {
		t.Fatalf("second request body = %q, want the first request's body %q", got, body)
	}
	if fresh, hits := renderCounts(metrics); fresh != 1 || hits != 1 {
		t.Fatalf("after second request: fresh=%d hits=%d, want 1 and 1 (the second must come from cache)", fresh, hits)
	}

	invalidated, err := app.InvalidateTagsN(context.Background(), "homepage")
	if err != nil {
		t.Fatalf("InvalidateTagsN: %v", err)
	}
	if invalidated != 1 {
		t.Fatalf("InvalidateTagsN = %d, want 1", invalidated)
	}

	third := get(handler, "/")
	if third.Code != http.StatusOK {
		t.Fatalf("third request status = %d, want %d", third.Code, http.StatusOK)
	}
	if got := third.Body.String(); got != body {
		t.Fatalf("third request body = %q, want the same rendered body %q", got, body)
	}
	if fresh, hits := renderCounts(metrics); fresh != 2 || hits != 1 {
		t.Fatalf("after invalidation: fresh=%d hits=%d, want 2 and 1 (the third must re-render)", fresh, hits)
	}

	snapshot := metrics.Snapshot()
	if len(snapshot.Invalidations) != 1 {
		t.Fatalf("Invalidation calls = %d, want 1", len(snapshot.Invalidations))
	}
	if snapshot.Invalidations[0].Keys != 1 {
		t.Fatalf("Invalidation keys = %d, want 1", snapshot.Invalidations[0].Keys)
	}
}

// TestApp_InvalidateTagsLeavesOtherPagesCached pins that invalidation removes
// exactly the entries matching the tag and nothing else. Without this, an
// invalidation that cleared the whole cache would pass the test above.
func TestApp_InvalidateTagsLeavesOtherPagesCached(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	app := newTestApp(t, func(cfg *Config) { cfg.Observability.Metrics = metrics })

	home := newHomePage()
	about := &types.Page{
		Name:            "about",
		LayoutFragment:  newLayout("about-layout"),
		ContentFragment: &types.Fragment{Name: "about-content", TemplatePath: "pages/about.html"},
		Paths:           map[string]string{"en": "/about"},
		Strategy:        types.StrategyStatic,
		DependencyTags:  []string{"about-page"},
	}
	for _, page := range []*types.Page{home, about} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage(%s): %v", page.Name, err)
		}
	}

	handler := app.Handler()
	get(handler, "/")
	get(handler, "/about")
	if fresh, _ := renderCounts(metrics); fresh != 2 {
		t.Fatalf("fresh renders after warming = %d, want 2", fresh)
	}

	if err := app.InvalidateTags(context.Background(), "homepage"); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}

	get(handler, "/about")
	if fresh, _ := renderCounts(metrics); fresh != 2 {
		t.Fatalf("fresh renders = %d after /about was served post-invalidation, want 2 (it must still be cached)", fresh)
	}

	get(handler, "/")
	if fresh, _ := renderCounts(metrics); fresh != 3 {
		t.Fatalf("fresh renders = %d after / was served post-invalidation, want 3 (it must have been dropped)", fresh)
	}
}

// TestApp_InvalidateTagsWithNoTags does nothing and reports nothing, rather than
// resolving the empty set and reporting an invalidation of zero keys.
func TestApp_InvalidateTagsWithNoTags(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	app := newTestApp(t, func(cfg *Config) { cfg.Observability.Metrics = metrics })

	count, err := app.InvalidateTagsN(context.Background())
	if err != nil {
		t.Fatalf("InvalidateTagsN: %v", err)
	}
	if count != 0 {
		t.Fatalf("InvalidateTagsN = %d, want 0", count)
	}
	if got := len(metrics.Snapshot().Invalidations); got != 0 {
		t.Fatalf("Invalidation calls = %d, want 0", got)
	}
}

// TestApp_InvalidateTagsDispatchesToPlugins checks the hook fires with the tags
// that were asked for, and that the event carries its own copy of them.
func TestApp_InvalidateTagsDispatchesToPlugins(t *testing.T) {
	app := newTestApp(t, nil)
	spy := &invalidateSpy{}
	if err := app.RegisterPlugin(spy); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	app.Handler()

	tags := []string{"homepage"}
	if err := app.InvalidateTags(context.Background(), tags...); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}

	got := spy.observed()
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "homepage" {
		t.Fatalf("OnCacheInvalidate observed %v, want one dispatch carrying [homepage]", got)
	}

	got[0][0] = "mutated"
	if tags[0] != "homepage" {
		t.Fatalf("caller's tags = %v, want the event to have carried its own copy", tags)
	}
}

// TestApp_ConcurrentUse drives the App the way a running server does: requests
// being served while a plugin reads the page registry and an operator invalidates
// tags. It exists for the race detector — every one of these paths takes a lock,
// and the point is that they are the right locks.
func TestApp_ConcurrentUse(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	handler := app.Handler()

	var workers sync.WaitGroup
	for range 8 {
		workers.Add(3)
		go func() {
			defer workers.Done()
			if recorder := get(handler, "/"); recorder.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
		}()
		go func() {
			defer workers.Done()
			if err := app.InvalidateTags(context.Background(), "homepage"); err != nil {
				t.Errorf("InvalidateTags: %v", err)
			}
		}()
		go func() {
			defer workers.Done()
			if pages := app.Pages(); len(pages) != 1 {
				t.Errorf("Pages = %d entries, want 1", len(pages))
			}
			if _, ok := app.Page("home"); !ok {
				t.Error("Page(home) not found")
			}
			if err := app.RegisterCommand(plugin.Command{Name: "cmd"}); err != nil &&
				!errors.Is(err, ErrDuplicateCommand) {
				t.Errorf("RegisterCommand: %v", err)
			}
		}()
	}
	workers.Wait()
}

// ---------------------------------------------------------------------------
// RenderPath
// ---------------------------------------------------------------------------

// TestApp_RenderPath renders a page outside the HTTP path and must not touch the
// cache, which is what makes it usable as the static builder's entry point.
func TestApp_RenderPath(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	app := newTestApp(t, func(cfg *Config) { cfg.Observability.Metrics = metrics })
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	result, err := app.RenderPath(context.Background(), "/", "en", nil)
	if err != nil {
		t.Fatalf("RenderPath: %v", err)
	}
	if !strings.Contains(string(result.HTML), "Welcome Home") {
		t.Fatalf("RenderPath HTML = %q, want the rendered content", result.HTML)
	}
	if !strings.Contains(string(result.HTML), "<main>") {
		t.Fatalf("RenderPath HTML = %q, want it wrapped in the layout", result.HTML)
	}
	if len(result.DependencyTags) != 1 || result.DependencyTags[0] != "homepage" {
		t.Fatalf("RenderPath tags = %v, want [homepage]", result.DependencyTags)
	}

	if events := metrics.Snapshot().CacheEvents; len(events) != 0 {
		t.Fatalf("cache events = %v, want none: RenderPath bypasses the cache", events)
	}

	if _, err := app.RenderPath(context.Background(), "/nowhere", "en", nil); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("RenderPath on an unregistered path = %v, want ErrPageNotFound", err)
	}
}

// TestApp_RenderPathResolvesLocale checks that a page registered only for a
// non-default locale is reachable, both through the locale-prefixed path the
// server would use and through the bare path with the locale named explicitly.
func TestApp_RenderPathResolvesLocale(t *testing.T) {
	app := newTestApp(t, func(cfg *Config) {
		cfg.Locale.Supported = []string{"en", "tr"}
	})

	page := newHomePage()
	page.Paths = map[string]string{"tr": "/"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	prefixed, err := app.RenderPath(context.Background(), "/tr", "", nil)
	if err != nil {
		t.Fatalf("RenderPath(/tr): %v", err)
	}
	if !strings.Contains(string(prefixed.HTML), "Welcome Home") {
		t.Fatalf("RenderPath(/tr) HTML = %q", prefixed.HTML)
	}
	if prefixed.Metadata.Locale != "tr" {
		t.Fatalf("RenderPath(/tr) locale = %q, want tr", prefixed.Metadata.Locale)
	}

	named, err := app.RenderPath(context.Background(), "/", "tr", nil)
	if err != nil {
		t.Fatalf("RenderPath(/, tr): %v", err)
	}
	if named.Metadata.Locale != "tr" {
		t.Fatalf("RenderPath(/, tr) locale = %q, want tr", named.Metadata.Locale)
	}
}

// TestApp_RenderPathRejectsRedirect: a redirect is not a page, so there is
// nothing for a static build to write at that path.
func TestApp_RenderPathRejectsRedirect(t *testing.T) {
	app := newTestApp(t, nil)

	page := newHomePage()
	page.Redirects = []*types.Redirect{{From: "/old", To: "/", Permanent: true}}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	_, err := app.RenderPath(context.Background(), "/old", "en", nil)
	if !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("RenderPath on a redirect = %v, want ErrPageNotFound", err)
	}
	if !strings.Contains(err.Error(), "redirects to") {
		t.Fatalf("RenderPath error = %q, want it to say the path redirects", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// TestApp_HandlerIsMemoised checks the handler is built once, which is what makes
// "the first Handler call closes registration" a coherent rule.
func TestApp_HandlerIsMemoised(t *testing.T) {
	app := newTestApp(t, nil)
	if first, second := app.Handler(), app.Handler(); first != second {
		t.Fatal("Handler returned a different handler on the second call, want the memoised one")
	}
}

// TestApp_HandlerRunsPluginInit checks Init runs on the first Handler call — so a
// plugin is initialised in a test that never starts a server — and runs only once.
func TestApp_HandlerRunsPluginInit(t *testing.T) {
	app := newTestApp(t, nil)
	spy := &lifecyclePlugin{}
	if err := app.RegisterPlugin(spy); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	app.Handler()
	app.Handler()

	if got := spy.inits(); got != 1 {
		t.Fatalf("plugin Init calls = %d, want 1", got)
	}
	if !spy.sawHost() {
		t.Fatal("plugin Init received no Host")
	}
}

// TestApp_HandlerReportsPluginInitFailure: a plugin that fails to initialise must
// not leave the application serving as if nothing happened. Handler has no error
// return, so it answers 503; ListenAndServe returns the failure itself.
func TestApp_HandlerReportsPluginInitFailure(t *testing.T) {
	app := newTestApp(t, nil)
	boom := errors.New("plugin exploded")
	if err := app.RegisterPlugin(&failingPlugin{err: boom}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	recorder := get(app.Handler(), "/")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if err := app.ListenAndServe(); !errors.Is(err, boom) {
		t.Fatalf("ListenAndServe = %v, want the plugin's own error", err)
	}
}

// TestApp_ShutdownIsIdempotent: shutting down twice must not panic, double-stop
// the plugins, or report a second, different outcome.
func TestApp_ShutdownIsIdempotent(t *testing.T) {
	app := newTestApp(t, nil)
	spy := &lifecyclePlugin{}
	if err := app.RegisterPlugin(spy); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	app.Handler()

	ctx := context.Background()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if got := spy.shutdowns(); got != 1 {
		t.Fatalf("plugin Shutdown calls = %d, want 1", got)
	}
}

// TestApp_ShutdownIsIdempotentUnderConcurrency runs the same guarantee from many
// goroutines at once, which is the shape a signal handler and an operator-initiated
// shutdown actually produce.
func TestApp_ShutdownIsIdempotentUnderConcurrency(t *testing.T) {
	app := newTestApp(t, nil)
	spy := &lifecyclePlugin{}
	if err := app.RegisterPlugin(spy); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	app.Handler()

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := app.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
		}()
	}
	wait.Wait()

	if got := spy.shutdowns(); got != 1 {
		t.Fatalf("plugin Shutdown calls = %d, want 1", got)
	}
}

// TestApp_ListenAndServeGracefulShutdown: a server stopped because it was asked to
// stop did not fail, so ListenAndServe returns nil and not http.ErrServerClosed.
func TestApp_ListenAndServeGracefulShutdown(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()

	waitListening(t, app)

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ListenAndServe = %v, want nil on a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after Shutdown")
	}
}

// TestApp_ListenAndServeTrapsSignals sends the process a real SIGTERM once the
// listener is up — the signal handler is registered before the port opens, so the
// signal can only be delivered to a server already trapping it — and requires the
// same clean nil return.
func TestApp_ListenAndServeTrapsSignals(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()

	waitListening(t, app)

	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := self.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ListenAndServe = %v, want nil after a trapped signal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after SIGTERM")
	}
}

// TestApp_ListenAndServeAfterShutdown: Shutdown before the server ever started
// must stop a later ListenAndServe from blocking forever with nothing left to
// stop it.
func TestApp_ListenAndServeAfterShutdown(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ListenAndServe = %v, want nil after Shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe blocked although the App was already shut down")
	}
}

// TestApp_ListenAndServeReportsBindFailure: a port that cannot be bound is a real
// failure and must be reported, not swallowed by the clean-shutdown mapping.
func TestApp_ListenAndServeReportsBindFailure(t *testing.T) {
	app := newTestApp(t, func(cfg *Config) { cfg.Server.Host = "203.0.113.1" })
	if err := app.ListenAndServe(); err == nil {
		t.Fatal("ListenAndServe on an unbindable address = nil, want an error")
	}
}

// waitListening blocks until app's server is accepting connections, or fails the
// test. It reaches into the unexported listening channel deliberately: polling a
// port or sleeping would make every lifecycle test above flaky for no benefit.
func waitListening(t *testing.T, app *App) {
	t.Helper()
	select {
	case <-app.listening:
	case <-time.After(5 * time.Second):
		t.Fatal("server never started listening")
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// TestNew_Failures covers the configurations core.New itself rejects.
func TestNew_Failures(t *testing.T) {
	root := writeTemplates(t, defaultTemplates())

	t.Run("empty template root", func(t *testing.T) {
		cfg := testConfig(root)
		cfg.Template.Root = ""
		if _, err := New(cfg); !errors.Is(err, ErrEmptyTemplateRoot) {
			t.Fatalf("New = %v, want ErrEmptyTemplateRoot", err)
		}
	})

	t.Run("missing template root", func(t *testing.T) {
		cfg := testConfig(filepath.Join(root, "does-not-exist"))
		if _, err := New(cfg); err == nil {
			t.Fatal("New over a missing template root = nil, want an error")
		}
	})

	t.Run("unsupported cache type", func(t *testing.T) {
		cfg := testConfig(root)
		cfg.Cache.Type = "redis"
		if _, err := New(cfg); !errors.Is(err, ErrUnsupportedCache) {
			t.Fatalf("New = %v, want ErrUnsupportedCache", err)
		}
	})
}

// TestApp_DevMode: the effective flag is the disjunction of the two, so neither
// can be set without development mode actually taking effect.
func TestApp_DevMode(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mutate   func(*Config)
		expected bool
	}{
		{name: "neither", mutate: nil, expected: false},
		{name: "config", mutate: func(cfg *Config) { cfg.DevMode = true }, expected: true},
		{name: "template", mutate: func(cfg *Config) { cfg.Template.DevMode = true }, expected: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := newTestApp(t, testCase.mutate).DevMode(); got != testCase.expected {
				t.Fatalf("DevMode = %t, want %t", got, testCase.expected)
			}
		})
	}
}

// TestApp_VaryFollowsLocaleSources: Vary is populated from the negative locale
// flags, so the zero value — every source enabled — varies on both headers. A
// disabled source must drop out, and a page with nothing left to vary on must
// carry no Vary header at all.
func TestApp_VaryFollowsLocaleSources(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mutate   func(*Config)
		expected string
	}{
		{name: "both enabled", mutate: nil, expected: "Accept-Language, Cookie"},
		{
			name:     "header disabled",
			mutate:   func(cfg *Config) { cfg.Locale.DisableHeaderLocale = true },
			expected: "Cookie",
		},
		{
			name:     "cookie disabled",
			mutate:   func(cfg *Config) { cfg.Locale.DisableCookieLocale = true },
			expected: "Accept-Language",
		},
		{
			name: "both disabled",
			mutate: func(cfg *Config) {
				cfg.Locale.DisableHeaderLocale = true
				cfg.Locale.DisableCookieLocale = true
			},
			expected: "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := newTestApp(t, testCase.mutate)
			if err := app.RegisterPage(newHomePage()); err != nil {
				t.Fatalf("RegisterPage: %v", err)
			}
			if got := get(app.Handler(), "/").Header().Get("Vary"); got != testCase.expected {
				t.Fatalf("Vary = %q, want %q", got, testCase.expected)
			}
		})
	}
}

// TestApp_CacheDisabled: with caching off every request renders, and the App is
// still perfectly usable — the nil cache must be a genuinely nil interface, not a
// typed nil the handler would call into.
func TestApp_CacheDisabled(t *testing.T) {
	metrics := observability.NewRecordingMetrics()
	app := newTestApp(t, func(cfg *Config) {
		cfg.Cache.Enabled = false
		cfg.Observability.Metrics = metrics
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	handler := app.Handler()
	for range 2 {
		if recorder := get(handler, "/"); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}
	if fresh, hits := renderCounts(metrics); fresh != 2 || hits != 0 {
		t.Fatalf("fresh=%d hits=%d, want 2 and 0 with the cache disabled", fresh, hits)
	}
}

// ---------------------------------------------------------------------------
// Test plugins
// ---------------------------------------------------------------------------

// lifecyclePlugin counts its own lifecycle calls and records whether Init was
// handed a Host.
type lifecyclePlugin struct {
	mu            sync.Mutex
	initCount     int
	shutdownCount int
	host          plugin.Host
}

var _ plugin.Plugin = (*lifecyclePlugin)(nil)

// Name identifies the plugin.
func (p *lifecyclePlugin) Name() string { return "lifecycle" }

// Version reports the plugin's version.
func (p *lifecyclePlugin) Version() string { return "1.0.0" }

// Init records the call and the Host it was given.
func (p *lifecyclePlugin) Init(_ context.Context, host plugin.Host) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initCount++
	p.host = host
	return nil
}

// Shutdown records the call.
func (p *lifecyclePlugin) Shutdown(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shutdownCount++
	return nil
}

// inits returns how many times Init was called.
func (p *lifecyclePlugin) inits() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initCount
}

// shutdowns returns how many times Shutdown was called.
func (p *lifecyclePlugin) shutdowns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shutdownCount
}

// sawHost reports whether Init received a non-nil Host.
func (p *lifecyclePlugin) sawHost() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.host != nil
}

// failingPlugin fails its Init with a fixed error.
type failingPlugin struct {
	err error
}

var _ plugin.Plugin = (*failingPlugin)(nil)

// Name identifies the plugin.
func (p *failingPlugin) Name() string { return "failing" }

// Version reports the plugin's version.
func (p *failingPlugin) Version() string { return "1.0.0" }

// Init fails with the plugin's fixed error.
func (p *failingPlugin) Init(context.Context, plugin.Host) error { return p.err }

// Shutdown does nothing.
func (p *failingPlugin) Shutdown(context.Context) error { return nil }

// invalidateSpy records the tags of every OnCacheInvalidate dispatch it receives.
type invalidateSpy struct {
	mu   sync.Mutex
	seen [][]string
}

var (
	_ plugin.Plugin              = (*invalidateSpy)(nil)
	_ plugin.CacheInvalidateHook = (*invalidateSpy)(nil)
)

// Name identifies the plugin.
func (p *invalidateSpy) Name() string { return "invalidate-spy" }

// Version reports the plugin's version.
func (p *invalidateSpy) Version() string { return "1.0.0" }

// Init does nothing.
func (p *invalidateSpy) Init(context.Context, plugin.Host) error { return nil }

// Shutdown does nothing.
func (p *invalidateSpy) Shutdown(context.Context) error { return nil }

// OnCacheInvalidate records the event's tags.
func (p *invalidateSpy) OnCacheInvalidate(_ context.Context, ev *plugin.CacheInvalidateEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, ev.Tags)
	return nil
}

// observed returns the tag slices recorded so far.
func (p *invalidateSpy) observed() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen
}
