package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeRender is the outcome fakeEngine produces for one page: the HTML it renders,
// the dependency tags it reports, the fatal error it fails with, and the name of a
// fragment recorded as failed (which is what makes Result.Degraded true).
type fakeRender struct {
	html     string
	tags     []string
	err      error
	degraded string
	notFound bool
}

// fakeEngine is a render.Engine that returns a pre-programmed outcome per page name
// and counts how many times each page was rendered. Counting is the point: a cache
// hit can only be proven by the render that did not happen. It is safe for
// concurrent use, so the same engine backs the -race test.
type fakeEngine struct {
	mu       sync.Mutex
	byPage   map[string]fakeRender
	fallback fakeRender
	calls    map[string]int
	total    int
}

var _ render.Engine = (*fakeEngine)(nil)

// newFakeEngine returns an engine rendering fallback for every page that has no
// outcome of its own.
func newFakeEngine(fallback fakeRender) *fakeEngine {
	return &fakeEngine{
		byPage:   make(map[string]fakeRender),
		fallback: fallback,
		calls:    make(map[string]int),
	}
}

// set programs the outcome the engine produces for the page named name.
func (e *fakeEngine) set(name string, out fakeRender) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.byPage[name] = out
}

// Render records the call and returns the programmed outcome, mirroring the real
// engine's contract: a non-nil Result with non-nil Metadata on every path, and nil
// HTML whenever the error is non-nil.
func (e *fakeEngine) Render(_ context.Context, rc *types.RenderContext) (*render.Result, error) {
	e.mu.Lock()
	e.calls[rc.Page.Name]++
	e.total++
	out, ok := e.byPage[rc.Page.Name]
	if !ok {
		out = e.fallback
	}
	e.mu.Unlock()

	metadata := &render.Metadata{Page: rc.Page.Name, Locale: rc.Locale}
	if out.degraded != "" {
		metadata.Fragments = []render.FragmentMetadata{{Name: out.degraded, Failed: true, Err: out.err}}
	}
	result := &render.Result{DependencyTags: out.tags, Metadata: metadata, NotFound: out.notFound}
	if out.err != nil {
		return result, out.err
	}
	result.HTML = []byte(out.html)
	return result, nil
}

// totalCalls returns how many renders the engine has performed in all.
func (e *fakeEngine) totalCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.total
}

// pageCalls returns how many times the page named name was rendered.
func (e *fakeEngine) pageCalls(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[name]
}

// stubRouter is a router.Router that returns one pre-built MatchResult, or an error,
// for every request. It covers the two outcomes the real router cannot be made to
// produce — a Match error, and a not-found result that still carries a page, which
// an application supplying its own Router implementation can.
type stubRouter struct {
	result    *router.MatchResult
	err       error
	notFound  *types.Page
	errorPage *types.Page
}

var _ router.Router = (*stubRouter)(nil)

// Match returns the stub's programmed result or error.
func (s *stubRouter) Match(*http.Request) (*router.MatchResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

// Register does nothing: the stub matches by its programmed result alone.
func (s *stubRouter) Register(*types.Page) error { return nil }

// RegisterNotFound stores page as the global not-found page.
func (s *stubRouter) RegisterNotFound(page *types.Page) error {
	s.notFound = page
	return nil
}

// RegisterError stores page as the global error page.
func (s *stubRouter) RegisterError(page *types.Page) error {
	s.errorPage = page
	return nil
}

// NotFoundPage returns the stored global not-found page.
func (s *stubRouter) NotFoundPage() *types.Page { return s.notFound }

// ErrorPage returns the stored global error page.
func (s *stubRouter) ErrorPage() *types.Page { return s.errorPage }

// plainCache wraps a Cache so that it does NOT satisfy cache.TaggedCache, forcing
// the handler down its Set path. It exists to prove that tags still reach the
// tracker when the cache cannot store them itself.
type plainCache struct {
	inner cache.Cache
}

var _ cache.Cache = (*plainCache)(nil)

// Get delegates to the wrapped cache.
func (c *plainCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	return c.inner.Get(ctx, key)
}

// Set delegates to the wrapped cache.
func (c *plainCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return c.inner.Set(ctx, key, content, ttl)
}

// Invalidate delegates to the wrapped cache.
func (c *plainCache) Invalidate(ctx context.Context, tags []string) error {
	return c.inner.Invalidate(ctx, tags)
}

// InvalidateKey delegates to the wrapped cache.
func (c *plainCache) InvalidateKey(ctx context.Context, key string) error {
	return c.inner.InvalidateKey(ctx, key)
}

// Clear delegates to the wrapped cache.
func (c *plainCache) Clear(ctx context.Context) error { return c.inner.Clear(ctx) }

// recordingPlugin records every hook it receives, in dispatch order, and can be told
// to replace the rendered HTML or to suppress the cache write.
type recordingPlugin struct {
	mu          sync.Mutex
	events      []string
	errs        []error
	replaceHTML []byte
	skipCache   bool
}

var (
	_ plugin.Plugin           = (*recordingPlugin)(nil)
	_ plugin.PageResolvedHook = (*recordingPlugin)(nil)
	_ plugin.BeforeRenderHook = (*recordingPlugin)(nil)
	_ plugin.AfterRenderHook  = (*recordingPlugin)(nil)
	_ plugin.CacheWriteHook   = (*recordingPlugin)(nil)
	_ plugin.ErrorHook        = (*recordingPlugin)(nil)
)

// Name identifies the plugin.
func (p *recordingPlugin) Name() string { return "recorder" }

// Version reports the plugin's version.
func (p *recordingPlugin) Version() string { return "1.0.0" }

// Init does nothing.
func (p *recordingPlugin) Init(context.Context, plugin.Host) error { return nil }

// Shutdown does nothing.
func (p *recordingPlugin) Shutdown(context.Context) error { return nil }

// record appends one hook name to the plugin's event log.
func (p *recordingPlugin) record(event string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

// recorded returns a copy of the hooks received so far, in dispatch order.
func (p *recordingPlugin) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}

// OnPageResolved records the hook.
func (p *recordingPlugin) OnPageResolved(context.Context, *plugin.PageResolvedEvent) error {
	p.record("PageResolved")
	return nil
}

// OnBeforeRender records the hook.
func (p *recordingPlugin) OnBeforeRender(context.Context, *plugin.BeforeRenderEvent) error {
	p.record("BeforeRender")
	return nil
}

// OnAfterRender records the hook and replaces the HTML when configured to.
func (p *recordingPlugin) OnAfterRender(_ context.Context, ev *plugin.AfterRenderEvent) error {
	p.record("AfterRender")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.replaceHTML != nil {
		ev.HTML = p.replaceHTML
	}
	return nil
}

// OnCacheWrite records the hook and suppresses the write when configured to.
func (p *recordingPlugin) OnCacheWrite(_ context.Context, ev *plugin.CacheWriteEvent) error {
	p.record("CacheWrite")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.skipCache {
		ev.Skip = true
	}
	return nil
}

// OnError records the hook along with the stage the failure came from, and keeps the
// error itself so a test can assert which sentinel it carries.
func (p *recordingPlugin) OnError(_ context.Context, ev *plugin.ErrorEvent) error {
	p.record("Error:" + ev.Stage)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errs = append(p.errs, ev.Err)
	return nil
}

// reportedErrors returns a copy of the errors passed to OnError, in dispatch order.
func (p *recordingPlugin) reportedErrors() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]error(nil), p.errs...)
}

// logRecord is one record a countingHandler saw, flattened to the parts tests
// assert on.
type logRecord struct {
	level    slog.Level
	message  string
	stage    string
	fragment string
}

// countingHandler is a slog.Handler that counts records by message and keeps their
// level and attributes, so a test can assert that a failure was logged exactly once
// and at the level an operator would actually see.
type countingHandler struct {
	mu      sync.Mutex
	counts  map[string]int
	records []logRecord
}

var _ slog.Handler = (*countingHandler)(nil)

// newCountingHandler returns an empty countingHandler.
func newCountingHandler() *countingHandler {
	return &countingHandler{counts: make(map[string]int)}
}

// Enabled reports true for every level, so nothing is dropped before counting.
func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

// Handle counts the record by its message and keeps its level, stage, and fragment.
func (h *countingHandler) Handle(_ context.Context, rec slog.Record) error {
	entry := logRecord{level: rec.Level, message: rec.Message}
	rec.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "stage":
			entry.stage = attr.Value.String()
		case "fragment":
			entry.fragment = attr.Value.String()
		}
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[rec.Message]++
	h.records = append(h.records, entry)
	return nil
}

// WithAttrs returns the handler unchanged: attributes are irrelevant to counting.
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup returns the handler unchanged.
func (h *countingHandler) WithGroup(string) slog.Handler { return h }

// count returns how many records carried the message msg.
func (h *countingHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts[msg]
}

// recordsFor returns every record carrying the message msg, in order.
func (h *countingHandler) recordsFor(msg string) []logRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var found []logRecord
	for _, rec := range h.records {
		if rec.message == msg {
			found = append(found, rec)
		}
	}
	return found
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// testPage returns a minimal page registered at path for locale "en".
func testPage(name, path string, strategy types.RenderStrategy) *types.Page {
	return &types.Page{
		Name:            name,
		ContentFragment: &types.Fragment{Name: name + "-content", TemplatePath: name + ".tmpl"},
		Paths:           map[string]string{"en": path},
		Strategy:        strategy,
	}
}

// errorOnlyPage returns a page with no registered path, for use as a not-found or
// error page.
func errorOnlyPage(name string) *types.Page {
	return &types.Page{
		Name:            name,
		ContentFragment: &types.Fragment{Name: name + "-content", TemplatePath: name + ".tmpl"},
	}
}

// testEnv bundles a Handler with the collaborators a test asserts against.
type testEnv struct {
	handler *Handler
	engine  *fakeEngine
	cache   *cache.MemoryCache
	tracker *dependency.MemoryTracker
	metrics *observability.RecordingMetrics
	router  router.Router
	logs    *countingHandler
}

// envOption adjusts the Deps a testEnv is built from.
type envOption func(*Deps)

// newEnv builds a Handler over a real router with pages registered, a memory cache,
// a memory tracker, recording metrics, and a fake render engine, then applies opts.
func newEnv(t *testing.T, pages []*types.Page, opts ...envOption) *testEnv {
	t.Helper()

	rt := router.New(router.LocaleOptions{Default: "en"})
	for _, page := range pages {
		if err := rt.Register(page); err != nil {
			t.Fatalf("Register(%q) = %v, want nil", page.Name, err)
		}
	}

	engine := newFakeEngine(fakeRender{html: "<html>page</html>"})
	memoryCache := cache.NewMemory(cache.MemoryConfig{})
	tracker := dependency.NewMemory()
	metrics := observability.NewRecordingMetrics()
	logs := newCountingHandler()

	deps := Deps{
		Router:   rt,
		Renderer: engine,
		Cache:    memoryCache,
		Tracker:  tracker,
		Metrics:  metrics,
		Logger:   slog.New(logs),
	}
	for _, opt := range opts {
		opt(&deps)
	}

	handler, err := New(deps)
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	return &testEnv{
		handler: handler,
		engine:  engine,
		cache:   memoryCache,
		tracker: tracker,
		metrics: metrics,
		router:  deps.Router,
		logs:    logs,
	}
}

// withDevMode turns on dev mode.
func withDevMode() envOption {
	return func(d *Deps) { d.DevMode = true }
}

// withPlugins installs a registry holding p.
func withPlugins(t *testing.T, p plugin.Plugin) envOption {
	t.Helper()
	return func(d *Deps) {
		registry := plugin.NewRegistry(slog.New(slog.DiscardHandler))
		if err := registry.Register(p); err != nil {
			t.Fatalf("Register(plugin) = %v, want nil", err)
		}
		d.Plugins = registry
	}
}

// withRouter replaces the router.
func withRouter(rt router.Router) envOption {
	return func(d *Deps) { d.Router = rt }
}

// withVary sets the request headers the handler advertises in Vary.
func withVary(headers ...string) envOption {
	return func(d *Deps) { d.Vary = headers }
}

// withDefaultTTL sets the handler's fallback cache TTL.
func withDefaultTTL(ttl time.Duration) envOption {
	return func(d *Deps) { d.DefaultTTL = ttl }
}

// get issues a GET for path and returns the recorded response.
func (e *testEnv) get(path string) *httptest.ResponseRecorder {
	return e.do(httptest.NewRequest(http.MethodGet, path, nil))
}

// do serves req and returns the recorded response.
func (e *testEnv) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// cacheEntries returns how many entries the environment's cache currently holds.
func (e *testEnv) cacheEntries() uint64 {
	return e.cache.Stats().Entries
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewRequiresDependencies(t *testing.T) {
	full := func() Deps {
		return Deps{
			Router:   router.New(router.LocaleOptions{Default: "en"}),
			Renderer: newFakeEngine(fakeRender{html: "x"}),
			Tracker:  dependency.NewMemory(),
			Logger:   slog.New(slog.DiscardHandler),
		}
	}

	tests := []struct {
		name  string
		clear func(*Deps)
	}{
		{"router", func(d *Deps) { d.Router = nil }},
		{"renderer", func(d *Deps) { d.Renderer = nil }},
		{"tracker", func(d *Deps) { d.Tracker = nil }},
		{"logger", func(d *Deps) { d.Logger = nil }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := full()
			tc.clear(&deps)
			handler, err := New(deps)
			if handler != nil {
				t.Errorf("New() handler = %v, want nil", handler)
			}
			if err == nil || !strings.Contains(err.Error(), "missing handler dependency") {
				t.Fatalf("New() error = %v, want ErrMissingDependency", err)
			}
		})
	}

	t.Run("optional dependencies may be nil", func(t *testing.T) {
		if _, err := New(full()); err != nil {
			t.Fatalf("New() = %v, want nil with no cache, metrics, tracer, or plugins", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func TestServeRendersPage(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	res := env.get("/")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Body.String(); got != "<html>home</html>" {
		t.Errorf("body = %q, want the rendered page", got)
	}
	if got := res.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := res.Header().Get("ETag"); got != cache.ETag([]byte("<html>home</html>")) {
		t.Errorf("ETag = %q, want the ETag of the served body", got)
	}
	if got := res.Header().Get(renderTimeHeader); got != "" {
		t.Errorf("%s = %q, want it absent outside dev mode", renderTimeHeader, got)
	}

	responses := env.metrics.Snapshot().HTTPResponses
	if len(responses) != 1 || responses[0].Status != http.StatusOK || responses[0].Path != "/" {
		t.Errorf("HTTPResponse calls = %+v, want one 200 for \"/\"", responses)
	}
}

func TestServeDevModeAddsRenderTimeHeader(t *testing.T) {
	page := testPage("home", "/", types.StrategyDynamic)
	env := newEnv(t, []*types.Page{page}, withDevMode())

	res := env.get("/")

	if got := res.Header().Get(renderTimeHeader); got == "" {
		t.Errorf("%s = %q, want a duration in dev mode", renderTimeHeader, got)
	}
}

func TestServeRedirect(t *testing.T) {
	page := testPage("new", "/new", types.StrategyStatic)
	page.Redirects = []*types.Redirect{{From: "/old", To: "/new", Permanent: true}}
	env := newEnv(t, []*types.Page{page})

	res := env.get("/old")

	if res.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMovedPermanently)
	}
	if got := res.Header().Get("Location"); got != "/new" {
		t.Errorf("Location = %q, want \"/new\"", got)
	}
	if res.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", res.Body.String())
	}
	if env.engine.totalCalls() != 0 {
		t.Errorf("render calls = %d, want 0 for a redirect", env.engine.totalCalls())
	}
}

func TestServeHeadWritesHeadersWithoutBody(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	fresh := env.do(httptest.NewRequest(http.MethodHead, "/", nil))

	if fresh.Code != http.StatusOK {
		t.Fatalf("fresh HEAD status = %d, want %d", fresh.Code, http.StatusOK)
	}
	if fresh.Body.Len() != 0 {
		t.Errorf("fresh HEAD body = %q, want empty", fresh.Body.String())
	}
	if got := fresh.Header().Get("ETag"); got == "" {
		t.Error("fresh HEAD ETag = \"\", want the rendered page's ETag")
	}

	// A HEAD renders but never populates the cache: it produced no body to store.
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries after HEAD = %d, want 0", entries)
	}

	env.get("/")
	cached := env.do(httptest.NewRequest(http.MethodHead, "/", nil))

	if cached.Code != http.StatusOK {
		t.Fatalf("cached HEAD status = %d, want %d", cached.Code, http.StatusOK)
	}
	if cached.Body.Len() != 0 {
		t.Errorf("cached HEAD body = %q, want empty", cached.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Caching
// ---------------------------------------------------------------------------

func TestCacheHitServesWithoutRerendering(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	first := env.get("/")
	second := env.get("/")

	if env.engine.pageCalls("home") != 1 {
		t.Fatalf("render calls = %d across two requests, want 1", env.engine.pageCalls("home"))
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("cached body = %q, want the first render %q", second.Body.String(), first.Body.String())
	}
	if second.Code != http.StatusOK {
		t.Errorf("cached status = %d, want %d", second.Code, http.StatusOK)
	}
	if first.Header().Get("ETag") != second.Header().Get("ETag") {
		t.Errorf("cached ETag = %q, want the rendered one %q", second.Header().Get("ETag"), first.Header().Get("ETag"))
	}

	renders := env.metrics.Snapshot().RenderDurations
	if len(renders) != 1 || !renders[0].CacheHit || renders[0].Page != "home" {
		t.Errorf("RenderDuration calls = %+v, want one cache-hit report for \"home\"", renders)
	}

	events := env.metrics.Snapshot().CacheEvents
	if len(events) != 3 ||
		events[0].Event != observability.CacheMiss ||
		events[1].Event != observability.CacheSet ||
		events[2].Event != observability.CacheHit {
		t.Errorf("cache events = %+v, want miss, set, hit", events)
	}
}

func TestNotModifiedOnIfNoneMatch(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})

	first := env.get("/")
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	res := env.do(req)

	if res.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotModified)
	}
	if res.Body.Len() != 0 {
		t.Errorf("body = %q, want empty on a 304", res.Body.String())
	}
	if got := res.Header().Get("ETag"); got != etag {
		t.Errorf("ETag = %q, want %q", got, etag)
	}
	if env.engine.pageCalls("home") != 1 {
		t.Errorf("render calls = %d, want 1: a 304 must not re-render", env.engine.pageCalls("home"))
	}
}

func TestNotModifiedAcceptsWeakAndListedETags(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	etag := env.get("/").Header().Get("ETag")

	for _, header := range []string{`W/` + etag, `"nomatch", ` + etag, "*"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("If-None-Match", header)
		if res := env.do(req); res.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %q: status = %d, want %d", header, res.Code, http.StatusNotModified)
		}
	}
}

func TestDegradedRenderIsNotCached(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>partial</html>", degraded: "sidebar"})

	first := env.get("/")

	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: a degraded render is still served", first.Code, http.StatusOK)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Fatalf("cache entries = %d, want 0: a degraded render must not be cached", entries)
	}

	env.get("/")
	if env.engine.pageCalls("home") != 2 {
		t.Errorf("render calls = %d, want 2: the degraded render must not have been served from cache", env.engine.pageCalls("home"))
	}
}

func TestDynamicStrategyIsNeverCached(t *testing.T) {
	page := testPage("live", "/live", types.StrategyDynamic)
	env := newEnv(t, []*types.Page{page})

	env.get("/live")
	env.get("/live")

	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries = %d, want 0 for a dynamic page", entries)
	}
	if env.engine.pageCalls("live") != 2 {
		t.Errorf("render calls = %d, want 2 for a dynamic page", env.engine.pageCalls("live"))
	}
}

func TestPostIsNeverCached(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})

	res := env.do(httptest.NewRequest(http.MethodPost, "/", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries = %d, want 0: an unsafe method must not populate the cache", entries)
	}

	// And the POST must not have been served from a cache it could not read
	// either: the following GET renders for itself.
	env.get("/")
	if env.engine.pageCalls("home") != 2 {
		t.Errorf("render calls = %d, want 2", env.engine.pageCalls("home"))
	}
}

func TestCacheControlPerStrategy(t *testing.T) {
	tests := []struct {
		name     string
		strategy types.RenderStrategy
		ttl      time.Duration
		want     string
	}{
		{"static", types.StrategyStatic, 0, "public, max-age=0, must-revalidate"},
		{"incremental", types.StrategyIncremental, 90 * time.Second, "public, max-age=90"},
		{"dynamic", types.StrategyDynamic, 0, "no-store"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page := testPage("p", "/p", tc.strategy)
			page.CacheTTL = tc.ttl
			env := newEnv(t, []*types.Page{page})

			if got := env.get("/p").Header().Get("Cache-Control"); got != tc.want {
				t.Errorf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIncrementalCacheControlUsesDefaultTTL(t *testing.T) {
	page := testPage("p", "/p", types.StrategyIncremental)
	env := newEnv(t, []*types.Page{page}, withDefaultTTL(2*time.Minute))

	if got := env.get("/p").Header().Get("Cache-Control"); got != "public, max-age=120" {
		t.Errorf("Cache-Control = %q, want the handler's DefaultTTL", got)
	}
}

func TestCacheHitCarriesCacheControl(t *testing.T) {
	page := testPage("p", "/p", types.StrategyIncremental)
	page.CacheTTL = time.Minute
	env := newEnv(t, []*types.Page{page})

	env.get("/p")
	res := env.get("/p")

	if got := res.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control on a cache hit = %q, want \"public, max-age=60\"", got)
	}
}

func TestTrackerReceivesTagsOnTaggedWrite(t *testing.T) {
	page := testPage("post", "/post", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("post", fakeRender{html: "<html>post</html>", tags: []string{"post:1", "author:7"}})

	env.get("/post")

	keys, err := env.tracker.Resolve(context.Background(), []string{"post:1"})
	if err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Resolve(\"post:1\") = %v, want exactly the rendered page's key", keys)
	}
	if _, _, found := env.cache.Get(context.Background(), keys[0]); !found {
		t.Errorf("the tracked key %q is not in the cache: the two indexes disagree", keys[0])
	}

	tags, err := env.tracker.Tags(context.Background(), keys[0])
	if err != nil {
		t.Fatalf("Tags() = %v, want nil", err)
	}
	if len(tags) != 2 || tags[0] != "author:7" || tags[1] != "post:1" {
		t.Errorf("Tags() = %v, want both render tags", tags)
	}
}

func TestTrackerReceivesTagsOnPlainCacheWrite(t *testing.T) {
	page := testPage("post", "/post", types.StrategyStatic)
	memory := cache.NewMemory(cache.MemoryConfig{})
	env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Cache = &plainCache{inner: memory} })
	env.engine.set("post", fakeRender{html: "<html>post</html>", tags: []string{"post:1"}})

	env.get("/post")

	keys, err := env.tracker.Resolve(context.Background(), []string{"post:1"})
	if err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Resolve(\"post:1\") = %v, want the key written through the plain Set path", keys)
	}
	if entries := memory.Stats().Entries; entries != 1 {
		t.Errorf("cache entries = %d, want 1", entries)
	}

	// The plain path still serves from cache on the next request.
	env.get("/post")
	if env.engine.pageCalls("post") != 1 {
		t.Errorf("render calls = %d, want 1", env.engine.pageCalls("post"))
	}
}

func TestNilCacheRendersEveryRequest(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Cache = nil })

	env.get("/")
	res := env.get("/")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if env.engine.pageCalls("home") != 2 {
		t.Errorf("render calls = %d, want 2 with no cache configured", env.engine.pageCalls("home"))
	}
}

// ---------------------------------------------------------------------------
// Plugins
// ---------------------------------------------------------------------------

func TestPluginHooksDispatchedInOrder(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	recorder := &recordingPlugin{}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder))

	env.get("/")

	want := []string{"PageResolved", "BeforeRender", "AfterRender", "CacheWrite"}
	got := recorder.recorded()
	if len(got) != len(want) {
		t.Fatalf("hooks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hooks = %v, want %v", got, want)
		}
	}
}

func TestCacheHitFiresPageResolvedButNotBeforeRender(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	recorder := &recordingPlugin{}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder))

	env.get("/")
	before := len(recorder.recorded())
	env.get("/")

	second := recorder.recorded()[before:]
	if len(second) != 1 || second[0] != "PageResolved" {
		t.Fatalf("hooks on a cache hit = %v, want only PageResolved: BeforeRenderHook documents that it does not fire when a cached render is served", second)
	}
	if env.engine.pageCalls("home") != 1 {
		t.Errorf("render calls = %d, want 1: the second request must have been a cache hit", env.engine.pageCalls("home"))
	}
}

func TestAfterRenderHTMLReplacementReachesWireAndCache(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	recorder := &recordingPlugin{replaceHTML: []byte("<html>rewritten</html>")}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder))
	env.engine.set("home", fakeRender{html: "<html>original</html>"})

	first := env.get("/")
	second := env.get("/")

	if got := first.Body.String(); got != "<html>rewritten</html>" {
		t.Errorf("body = %q, want the plugin's replacement", got)
	}
	if got := first.Header().Get("ETag"); got != cache.ETag([]byte("<html>rewritten</html>")) {
		t.Errorf("ETag = %q, want the ETag of the replacement", got)
	}
	if got := second.Body.String(); got != "<html>rewritten</html>" {
		t.Errorf("cached body = %q, want the plugin's replacement", got)
	}
	if env.engine.pageCalls("home") != 1 {
		t.Errorf("render calls = %d, want 1", env.engine.pageCalls("home"))
	}
}

func TestCacheWriteSkipPreventsWrite(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	recorder := &recordingPlugin{skipCache: true}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder))
	env.engine.set("home", fakeRender{html: "<html>home</html>", tags: []string{"home"}})

	res := env.get("/")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: a skipped cache write still serves the page", res.Code, http.StatusOK)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries = %d, want 0 when a plugin skipped the write", entries)
	}

	keys, err := env.tracker.Resolve(context.Background(), []string{"home"})
	if err != nil {
		t.Fatalf("Resolve() = %v, want nil", err)
	}
	if len(keys) != 0 {
		t.Errorf("tracked keys = %v, want none: nothing was written to track", keys)
	}
}

func TestOnErrorDispatchedForNotFoundAndServerError(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	recorder := &recordingPlugin{}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder))
	env.engine.set("home", fakeRender{err: errBoom})

	notFound := env.get("/missing")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", notFound.Code, http.StatusNotFound)
	}
	serverError := env.get("/")
	if serverError.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", serverError.Code, http.StatusInternalServerError)
	}

	var stages []string
	for _, event := range recorder.recorded() {
		if strings.HasPrefix(event, "Error:") {
			stages = append(stages, event)
		}
	}
	if len(stages) != 2 || stages[0] != "Error:"+stageNotFound || stages[1] != "Error:"+stageRender {
		t.Errorf("OnError dispatches = %v, want one for the 404 and one for the 500", stages)
	}
}

func TestPluginHookErrorProduces500(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page}, withPlugins(t, &failingResolvePlugin{}))

	res := env.get("/")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if env.engine.totalCalls() != 0 {
		t.Errorf("render calls = %d, want 0: a failing OnPageResolved stops before rendering", env.engine.totalCalls())
	}
}

// failingResolvePlugin fails OnPageResolved, to exercise the hook-error path.
type failingResolvePlugin struct{}

var (
	_ plugin.Plugin           = (*failingResolvePlugin)(nil)
	_ plugin.PageResolvedHook = (*failingResolvePlugin)(nil)
)

// Name identifies the plugin.
func (p *failingResolvePlugin) Name() string { return "failing-resolve" }

// Version reports the plugin's version.
func (p *failingResolvePlugin) Version() string { return "1.0.0" }

// Init does nothing.
func (p *failingResolvePlugin) Init(context.Context, plugin.Host) error { return nil }

// Shutdown does nothing.
func (p *failingResolvePlugin) Shutdown(context.Context) error { return nil }

// OnPageResolved always fails.
func (p *failingResolvePlugin) OnPageResolved(context.Context, *plugin.PageResolvedEvent) error {
	return errBoom
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestConcurrentRequests(t *testing.T) {
	static := testPage("home", "/", types.StrategyStatic)
	dynamic := testPage("live", "/live", types.StrategyDynamic)
	broken := testPage("broken", "/broken", types.StrategyStatic)
	env := newEnv(t, []*types.Page{static, dynamic, broken})
	env.engine.set("broken", fakeRender{err: errBoom, degraded: "sidebar"})

	paths := []string{"/", "/live", "/broken", "/missing"}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			if res := env.get(path); res.Code == 0 {
				t.Errorf("%s: no status written", path)
			}
		}(paths[i%len(paths)])
	}
	wg.Wait()
}

func TestRedirectWithoutStatusDefaultsToFound(t *testing.T) {
	stub := &stubRouter{result: &router.MatchResult{Locale: "en", RedirectTo: "/new"}}
	env := newEnv(t, nil, withRouter(stub))

	res := env.get("/old")

	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d when the router left RedirectStatus unset", res.Code, http.StatusFound)
	}
	if got := res.Header().Get("Location"); got != "/new" {
		t.Errorf("Location = %q, want \"/new\"", got)
	}
}

// customETagCache returns an ETag of its own rather than one derived from the
// content the way cache.ETag derives it. Cache is an interface an application may
// implement, and its contract is that Set returns the content's ETag, so the handler
// must advertise what the cache returned instead of recomputing.
type customETagCache struct {
	inner *cache.MemoryCache
	etag  string
}

var _ cache.TaggedCache = (*customETagCache)(nil)

// Get returns the wrapped entry under the cache's own ETag.
func (c *customETagCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	content, _, found := c.inner.Get(ctx, key)
	if !found {
		return nil, "", false
	}
	return content, c.etag, true
}

// Set stores content and returns the cache's own ETag.
func (c *customETagCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return c.SetTagged(ctx, key, content, ttl, nil)
}

// SetTagged stores content and returns the cache's own ETag.
func (c *customETagCache) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	if _, err := c.inner.SetTagged(ctx, key, content, ttl, tags); err != nil {
		return "", err
	}
	return c.etag, nil
}

// Invalidate delegates to the wrapped cache.
func (c *customETagCache) Invalidate(ctx context.Context, tags []string) error {
	return c.inner.Invalidate(ctx, tags)
}

// InvalidateKey delegates to the wrapped cache.
func (c *customETagCache) InvalidateKey(ctx context.Context, key string) error {
	return c.inner.InvalidateKey(ctx, key)
}

// Clear delegates to the wrapped cache.
func (c *customETagCache) Clear(ctx context.Context) error { return c.inner.Clear(ctx) }

func TestFreshResponseAdvertisesTheCachesETag(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	custom := &customETagCache{inner: cache.NewMemory(cache.MemoryConfig{}), etag: `"cache-chosen"`}
	env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Cache = custom })
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	fresh := env.get("/")
	if got := fresh.Header().Get("ETag"); got != `"cache-chosen"` {
		t.Fatalf("fresh ETag = %q, want the ETag the cache returned: a recomputed one would not match later hits", got)
	}

	hit := env.get("/")
	if got := hit.Header().Get("ETag"); got != `"cache-chosen"` {
		t.Errorf("cache-hit ETag = %q, want %q", got, `"cache-chosen"`)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", `"cache-chosen"`)
	if res := env.do(req); res.Code != http.StatusNotModified {
		t.Errorf("revalidation with the fresh response's ETag = %d, want %d", res.Code, http.StatusNotModified)
	}
}

func TestUncachedResponseFallsBackToComputedETag(t *testing.T) {
	page := testPage("live", "/live", types.StrategyDynamic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("live", fakeRender{html: "<html>live</html>"})

	res := env.get("/live")

	if got := res.Header().Get("ETag"); got != cache.ETag([]byte("<html>live</html>")) {
		t.Errorf("ETag = %q, want the computed one when nothing was cached", got)
	}
}

func TestVaryOnPubliclyCacheableResponses(t *testing.T) {
	tests := []struct {
		name     string
		strategy types.RenderStrategy
		ttl      time.Duration
		want     string
	}{
		{"static", types.StrategyStatic, 0, "Accept-Language, Cookie"},
		{"incremental", types.StrategyIncremental, time.Minute, "Accept-Language, Cookie"},
		{"dynamic", types.StrategyDynamic, 0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page := testPage("p", "/p", tc.strategy)
			page.CacheTTL = tc.ttl
			env := newEnv(t, []*types.Page{page}, withVary("Accept-Language", "Cookie"))

			req := httptest.NewRequest(http.MethodGet, "/p", nil)
			req.Header.Set("Accept-Language", "tr")
			res := env.do(req)

			if got := res.Header().Get("Vary"); got != tc.want {
				t.Errorf("Vary = %q, want %q: a shared cache keys on the URL alone, and a negotiated locale is not in the URL", got, tc.want)
			}
		})
	}
}

func TestVaryOnCacheHitAndNotModified(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page}, withVary("Accept-Language"))

	etag := env.get("/").Header().Get("ETag")

	hit := env.get("/")
	if got := hit.Header().Get("Vary"); got != "Accept-Language" {
		t.Errorf("Vary on a cache hit = %q, want \"Accept-Language\"", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	if got := env.do(req).Header().Get("Vary"); got != "Accept-Language" {
		t.Errorf("Vary on a 304 = %q, want \"Accept-Language\": the 304 revalidates the same stored representation", got)
	}
}

func TestNoVaryOnErrorResponses(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page}, withVary("Accept-Language"))
	env.engine.set("home", fakeRender{err: errBoom})

	if got := env.get("/").Header().Get("Vary"); got != "" {
		t.Errorf("Vary on a 500 = %q, want none: a no-store response has no representation to select", got)
	}
	if got := env.get("/missing").Header().Get("Vary"); got != "" {
		t.Errorf("Vary on a 404 = %q, want none", got)
	}
}

func TestNoVaryHeaderWhenNoneConfigured(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})

	if got := env.get("/").Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want none when Deps.Vary is empty", got)
	}
}

func TestQueryStringIsPartOfTheCacheKey(t *testing.T) {
	page := testPage("search", "/search", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})

	env.get("/search?q=a")
	env.get("/search?q=b")

	if calls := env.engine.pageCalls("search"); calls != 2 {
		t.Fatalf("render calls = %d, want 2: two queries are two representations, not one", calls)
	}
	if entries := env.cacheEntries(); entries != 2 {
		t.Errorf("cache entries = %d, want 2", entries)
	}

	// The same query is still a hit.
	env.get("/search?q=a")
	if calls := env.engine.pageCalls("search"); calls != 2 {
		t.Errorf("render calls = %d, want 2: repeating a query must still hit the cache", calls)
	}
}

func TestRouteMissAndContentMissCarryDistinctSentinels(t *testing.T) {
	page := testPage("blog", "/blog", types.StrategyStatic)
	page.NotFoundPage = errorOnlyPage("blog-404")
	recorder := &recordingPlugin{}
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder))
	env.engine.set("blog", fakeRender{err: fmt.Errorf("blog: slug: %w", types.ErrNotFound), notFound: true})
	env.engine.set("blog-404", fakeRender{html: "<html>no such post</html>"})

	routeMiss := env.get("/nowhere")
	contentMiss := env.get("/blog")

	if routeMiss.Code != http.StatusNotFound || contentMiss.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d and %d, want two 404s", routeMiss.Code, contentMiss.Code)
	}
	if got := contentMiss.Body.String(); got != "<html>no such post</html>" {
		t.Errorf("body = %q, want the matched page's own NotFoundPage", got)
	}

	errs := recorder.reportedErrors()
	if len(errs) != 2 {
		t.Fatalf("OnError dispatches = %d, want 2", len(errs))
	}
	if !errors.Is(errs[0], ErrNoRoute) {
		t.Errorf("route miss error = %v, want ErrNoRoute", errs[0])
	}
	if errors.Is(errs[0], types.ErrNotFound) {
		t.Errorf("route miss error = %v, must not also satisfy types.ErrNotFound: a plugin has to tell a bad link from a missing record", errs[0])
	}
	if !errors.Is(errs[1], types.ErrNotFound) {
		t.Errorf("content miss error = %v, want types.ErrNotFound", errs[1])
	}
	if errors.Is(errs[1], ErrNoRoute) {
		t.Errorf("content miss error = %v, must not also satisfy ErrNoRoute", errs[1])
	}
}

func TestRouteMissLogsAtDebugAndContentMissAtError(t *testing.T) {
	page := testPage("blog", "/blog", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("blog", fakeRender{
		err:      fmt.Errorf("blog: slug: %w", types.ErrNotFound),
		notFound: true,
		degraded: "post",
	})

	env.get("/nowhere")
	env.get("/blog")

	records := env.logs.recordsFor("collage: request failed")
	if len(records) != 2 {
		t.Fatalf("log records = %+v, want one per 404", records)
	}
	if records[0].level != slog.LevelDebug || records[0].stage != stageNotFound {
		t.Errorf("route miss logged as %+v, want debug level at stage %q", records[0], stageNotFound)
	}
	if records[1].level != slog.LevelError || records[1].stage != stageRender {
		t.Errorf("content miss logged as %+v, want error level at stage %q: it is a failure inside the application", records[1], stageRender)
	}
	if records[1].fragment != "post" {
		t.Errorf("content miss record fragment = %q, want the failing fragment named", records[1].fragment)
	}
}
