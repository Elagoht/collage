package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/internal/asset"
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
// RenderFragment renders one fragment on its own. The body names the fragment, so a
// test can tell which one was asked for without a template engine.
func (e *fakeEngine) RenderFragment(_ context.Context, _ *types.RenderContext, f *types.Fragment) ([]byte, error) {
	e.mu.Lock()
	e.calls["fragment:"+f.Name]++
	e.total++
	out, ok := e.byPage["fragment:"+f.Name]
	e.mu.Unlock()
	if ok && out.err != nil {
		return nil, out.err
	}
	if ok {
		return []byte(out.html), nil
	}
	return []byte("<div>" + f.Name + "</div>"), nil
}

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

// RegisterDocument does nothing: the stub matches by its programmed result
// alone.
func (s *stubRouter) RegisterDocument(*types.Document) error { return nil }

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

// ClaimedPaths returns nothing: the stub registers no route, and nothing in this
// package consults claimed paths — internal/core's mount close-out check is the
// only caller.
func (s *stubRouter) ClaimedPaths() []router.ClaimedPath { return nil }

// RegisterAction is never called: the stub answers from a canned MatchResult.
func (s *stubRouter) RegisterAction(*types.Action) error { return nil }

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
	// cacheWritePages records the Page every OnCacheWrite carried, nils
	// included: CacheWriteEvent.Page is nil for a document, and a test asserting
	// that has to be able to see the nil rather than have it dropped.
	cacheWritePages []*types.Page
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
	p.cacheWritePages = append(p.cacheWritePages, ev.Page)
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

// cacheWritten returns a copy of the Page each OnCacheWrite carried, in dispatch
// order, nils included.
func (p *recordingPlugin) cacheWritten() []*types.Page {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*types.Page(nil), p.cacheWritePages...)
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

	// An option may have replaced the cache; testEnv.cache must follow it, or a
	// test's assertions would be made against a cache the handler never touched.
	if replaced, ok := deps.Cache.(*cache.MemoryCache); ok {
		memoryCache = replaced
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

// withVary adds middleware declaring, through Vary, that every response depends
// on headers.
func withVary(headers ...string) envOption {
	return func(d *Deps) {
		d.Middleware = append(d.Middleware, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, header := range headers {
					_ = Vary(r, header, r.Header.Get(header))
				}
				next.ServeHTTP(w, r)
			})
		})
	}
}

// withDefaultTTL sets the handler's fallback cache TTL.
func withDefaultTTL(ttl time.Duration) envOption {
	return func(d *Deps) { d.DefaultTTL = ttl }
}

// withMounts installs mounts, checked before routing.
func withMounts(mounts ...*asset.Mount) envOption {
	return func(d *Deps) { d.Mounts = mounts }
}

// withCacheConfig replaces the environment's memory cache with one built from cfg,
// so a test can inject a clock or a default TTL. newEnv picks the replacement back up
// for testEnv.cache.
func withCacheConfig(cfg cache.MemoryConfig) envOption {
	return func(d *Deps) { d.Cache = cache.NewMemory(cfg) }
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

// server starts a real HTTP server over the environment's handler, closed when the
// test ends.
//
// It exists for the questions httptest.ResponseRecorder cannot answer. The recorder
// records exactly what the handler wrote and invents no headers; a real server is
// what discards a HEAD response's body and what derives Content-Length from the
// bytes the handler produced. A HEAD assertion made against the recorder is an
// assertion about the recorder.
func (e *testEnv) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(e.handler)
	t.Cleanup(srv.Close)
	return srv
}

// request issues method against srv's path through a real client and returns the
// response and its body, both already read and closed.
func request(t *testing.T, srv *httptest.Server, method, path string) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest(%s %s) = %v, want nil", method, path, err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do(%s %s) = %v, want nil", method, path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", method, path, err)
	}
	return res, body
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

// TestServeHeadWritesHeadersWithoutBody checks HEAD against a real server, on both
// the fresh and the cached path: no body, and the same Content-Length the matching
// GET reports. The length is the point — a HEAD exists to tell a client how large the
// representation is, and this handler used to answer "0" for every one of them by
// skipping the write itself instead of letting net/http discard it.
func TestServeHeadWritesHeadersWithoutBody(t *testing.T) {
	const html = "<html>home</html>"

	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: html})
	srv := env.server(t)

	fresh, freshBody := request(t, srv, http.MethodHead, "/")

	if fresh.StatusCode != http.StatusOK {
		t.Fatalf("fresh HEAD status = %d, want %d", fresh.StatusCode, http.StatusOK)
	}
	if len(freshBody) != 0 {
		t.Errorf("fresh HEAD body = %q, want empty", freshBody)
	}
	if fresh.ContentLength != int64(len(html)) {
		t.Errorf("fresh HEAD Content-Length = %d, want %d", fresh.ContentLength, len(html))
	}
	if got := fresh.Header.Get("ETag"); got == "" {
		t.Error("fresh HEAD ETag = \"\", want the rendered page's ETag")
	}

	// A HEAD renders but never populates the cache: it produced no body to store.
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries after HEAD = %d, want 0", entries)
	}

	get, getBody := request(t, srv, http.MethodGet, "/")
	if string(getBody) != html {
		t.Fatalf("GET body = %q, want %q", getBody, html)
	}

	cached, cachedBody := request(t, srv, http.MethodHead, "/")

	if cached.StatusCode != http.StatusOK {
		t.Fatalf("cached HEAD status = %d, want %d", cached.StatusCode, http.StatusOK)
	}
	if len(cachedBody) != 0 {
		t.Errorf("cached HEAD body = %q, want empty", cachedBody)
	}
	if cached.ContentLength != get.ContentLength {
		t.Errorf("cached HEAD Content-Length = %d, want the GET's %d", cached.ContentLength, get.ContentLength)
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

// A page answers GET and HEAD. A POST to it, with no action declared for that URL,
// is a 405 naming what the path does accept — not a rendered page.
//
// The old behaviour was to render: any method reached the page and got HTML back.
// That is the wrong answer to a form submission, and wrong in the quietest possible
// way, because the reader is handed a page that looks like nothing happened while
// the application never saw the submission at all.
func TestPostToAPageWithNoActionIsRefused(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})

	res := env.do(httptest.NewRequest(http.MethodPost, "/", nil))

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
	allow := res.Header().Get("Allow")
	for _, want := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !strings.Contains(allow, want) {
			t.Errorf("Allow = %q, want it to contain %s", allow, want)
		}
	}
	if strings.Contains(allow, http.MethodPost) {
		t.Errorf("Allow = %q, want it not to offer POST", allow)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries = %d, want 0: a refused method must not populate the cache", entries)
	}
	if env.engine.pageCalls("home") != 0 {
		t.Errorf("render calls = %d, want 0: the page must not render for a method it does not answer", env.engine.pageCalls("home"))
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
	// Both render tags, and the one naming the path the entry was rendered for.
	if len(tags) != 3 || tags[0] != "author:7" || tags[1] != "collage:path:/post" || tags[2] != "post:1" {
		t.Errorf("Tags() = %v, want both render tags and the path's", tags)
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
				t.Errorf("Vary = %q, want %q: a shared cache keys on the URL alone, and a negotiated value is not in the URL", got, tc.want)
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

// Neither kind of 404 is a failure of the site: a route miss is a bad link, a
// content miss is a record that does not exist, and every bot produces both all
// day. Both are logged below error level — and the content miss still names its
// fragment and still reaches OnError as ErrNotFound, so a plugin that counts them
// can.
func TestRouteMissAndContentMissLogAtDebug(t *testing.T) {
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
	if records[1].level != slog.LevelDebug || records[1].stage != stageRender {
		t.Errorf("content miss logged as %+v, want debug level at stage %q: a missing record is an answer, not a failure", records[1], stageRender)
	}
	if records[1].fragment != "post" {
		t.Errorf("content miss record fragment = %q, want the failing fragment named", records[1].fragment)
	}
}

// ---------------------------------------------------------------------------
// Strategy semantics
// ---------------------------------------------------------------------------

// panickingRouter is a router.Router whose Match panics, standing in for any
// collaborator the framework did not write — an application's own Router, Cache,
// Metrics, or Tracer — blowing up in the middle of a request.
type panickingRouter struct {
	stubRouter
}

var _ router.Router = (*panickingRouter)(nil)

// Match panics instead of returning.
func (p *panickingRouter) Match(*http.Request) (*router.MatchResult, error) {
	panic("router exploded")
}

// ClaimedPaths returns nothing; see stubRouter.ClaimedPaths.
func (p *panickingRouter) ClaimedPaths() []router.ClaimedPath { return nil }

// RegisterAction is never called; this router exists to panic from Match.
func (p *panickingRouter) RegisterAction(*types.Action) error { return nil }

// TestStaticPageDoesNotExpireAtTheDefaultTTL is the I3 regression. StrategyStatic is
// documented as "render once and serve until explicitly invalidated", but Static()
// sets no CacheTTL, so the write fell through to DefaultTTL — which the framework's
// own defaulting forces to five minutes — and every static page silently re-rendered
// on that cycle. The cache's injected clock is what makes the elapsed time real
// without waiting for it.
func TestStaticPageDoesNotExpireAtTheDefaultTTL(t *testing.T) {
	const defaultTTL = 5 * time.Minute

	now := time.Now()
	clock := func() time.Time { return now }

	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page},
		withCacheConfig(cache.MemoryConfig{DefaultTTL: defaultTTL, Now: clock}),
		withDefaultTTL(defaultTTL),
	)
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	env.get("/")
	if calls := env.engine.pageCalls("home"); calls != 1 {
		t.Fatalf("render calls after the first request = %d, want 1", calls)
	}

	// Far past the default TTL, and past any plausible one.
	now = now.Add(100 * defaultTTL)

	env.get("/")
	if calls := env.engine.pageCalls("home"); calls != 1 {
		t.Fatalf("render calls after %v = %d, want 1: a static page re-rendered on the default TTL", 100*defaultTTL, calls)
	}
}

// TestIncrementalPageStillExpiresAtTheDefaultTTL is the control for the test above:
// the static special case must not have turned every cached page into a permanent
// one. An incremental page with no CacheTTL of its own still expires on DefaultTTL.
func TestIncrementalPageStillExpiresAtTheDefaultTTL(t *testing.T) {
	const defaultTTL = 5 * time.Minute

	now := time.Now()
	clock := func() time.Time { return now }

	page := testPage("home", "/", types.StrategyIncremental)
	env := newEnv(t, []*types.Page{page},
		withCacheConfig(cache.MemoryConfig{DefaultTTL: defaultTTL, Now: clock}),
		withDefaultTTL(defaultTTL),
	)
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	env.get("/")
	now = now.Add(defaultTTL + time.Second)
	env.get("/")

	if calls := env.engine.pageCalls("home"); calls != 2 {
		t.Fatalf("render calls after the TTL elapsed = %d, want 2", calls)
	}
}

// ---------------------------------------------------------------------------
// Panic safety
// ---------------------------------------------------------------------------

// TestPanicInACollaboratorBecomesA500 is the I5 regression for the HTTP side. The
// render engine recovers a panic inside a data handler, but a panic anywhere else —
// here in a Router of the application's own — used to unwind into net/http and drop
// the connection with no status line, no ErrorHook dispatch, and no metric.
func TestPanicInACollaboratorBecomesA500(t *testing.T) {
	spy := &recordingPlugin{}
	env := newEnv(t, nil, withRouter(&panickingRouter{}), withPlugins(t, spy))
	srv := env.server(t)

	res, body := request(t, srv, http.MethodGet, "/anything")

	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusInternalServerError)
	}
	if len(body) == 0 {
		t.Error("body is empty: the client got a status with no page")
	}

	if got := spy.recorded(); len(got) != 1 || got[0] != "Error:"+stagePanic {
		t.Fatalf("hooks = %v, want one Error:%s", got, stagePanic)
	}
	reported := spy.reportedErrors()
	if len(reported) != 1 || !errors.Is(reported[0], ErrPanic) {
		t.Fatalf("reported errors = %v, want one ErrPanic", reported)
	}
	if !strings.Contains(reported[0].Error(), "router exploded") {
		t.Errorf("reported error = %q, want it to carry the panic value", reported[0])
	}

	responses := env.metrics.Snapshot().HTTPResponses
	if len(responses) != 1 || responses[0].Status != http.StatusInternalServerError {
		t.Fatalf("HTTPResponse metrics = %+v, want one 500", responses)
	}
}

// TestPanicInACollaboratorDoesNotLeakDiagnosticsOutsideDevMode checks that a
// recovered panic goes through the same built-in page as every other 500: the stack
// it carries is for the log and the ErrorHook, never for the response body.
func TestPanicInACollaboratorDoesNotLeakDiagnosticsOutsideDevMode(t *testing.T) {
	env := newEnv(t, nil, withRouter(&panickingRouter{}))
	srv := env.server(t)

	_, body := request(t, srv, http.MethodGet, "/anything")

	if strings.Contains(string(body), "router exploded") {
		t.Errorf("body = %q, want no panic value in a production response", body)
	}
}

// ---------------------------------------------------------------------------
// Mounted assets
// ---------------------------------------------------------------------------

// slowFS wraps an fs.FS and sleeps for delay on every Open, so a test can prove a
// metric's duration is real elapsed time rather than an untouched zero value,
// without depending on how fast the underlying file system happens to be.
type slowFS struct {
	inner fs.FS
	delay time.Duration
}

// Open sleeps for delay, then delegates to the wrapped file system.
func (s slowFS) Open(name string) (fs.File, error) {
	time.Sleep(s.delay)
	return s.inner.Open(name)
}

// panicFS is a hostile fs.FS whose Open panics instead of returning, standing in
// for a mount's file system misbehaving the same way panickingRouter stands in
// for an application's Router: something the framework did not write blowing up
// mid-request.
type panicFS struct{}

// Open always panics.
func (panicFS) Open(string) (fs.File, error) {
	panic("mount fs exploded")
}

// mustMount builds an asset.Mount at prefix over fsys, failing the test if
// construction fails.
func mustMount(t *testing.T, prefix string, fsys fs.FS) *asset.Mount {
	t.Helper()
	m, err := asset.New(prefix, fsys)
	if err != nil {
		t.Fatalf("asset.New(%q) = %v, want nil", prefix, err)
	}
	return m
}

// TestAssetSuccessProducesHTTPResponseMetric is the base case for asset
// observability: an asset request, which once bypassed every metric, span, and
// hook, produces the same HTTPResponse metric a page does, with a real, non-zero
// duration.
func TestAssetSuccessProducesHTTPResponseMetric(t *testing.T) {
	fsys := fstest.MapFS{"app.css": {Data: []byte("body{color:red}")}}
	mount := mustMount(t, "/static/", slowFS{inner: fsys, delay: time.Millisecond})
	env := newEnv(t, nil, withMounts(mount))

	res := env.get("/static/app.css")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Body.String(); got != "body{color:red}" {
		t.Errorf("body = %q, want the mounted file's content", got)
	}

	responses := env.metrics.Snapshot().HTTPResponses
	if len(responses) != 1 || responses[0].Status != http.StatusOK || responses[0].Path != "/static/app.css" {
		t.Fatalf("HTTPResponse calls = %+v, want one 200 for \"/static/app.css\"", responses)
	}
	if responses[0].Duration <= 0 {
		t.Errorf("HTTPResponse duration = %v, want a non-zero duration", responses[0].Duration)
	}
}

// TestAssetNotFoundDispatchesOnErrorAndMetric checks that a missing file under a
// mount is counted in HTTPResponse and reported to ErrorHook exactly once, the two
// things an asset 404 used to skip entirely.
func TestAssetNotFoundDispatchesOnErrorAndMetric(t *testing.T) {
	fsys := fstest.MapFS{"app.css": {Data: []byte("body{color:red}")}}
	mount := mustMount(t, "/static/", fsys)
	recorder := &recordingPlugin{}
	env := newEnv(t, nil, withMounts(mount), withPlugins(t, recorder))

	res := env.get("/static/missing.css")

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotFound)
	}

	responses := env.metrics.Snapshot().HTTPResponses
	if len(responses) != 1 || responses[0].Status != http.StatusNotFound {
		t.Fatalf("HTTPResponse calls = %+v, want one 404", responses)
	}

	errs := recorder.reportedErrors()
	if len(errs) != 1 {
		t.Fatalf("OnError dispatches = %d, want 1", len(errs))
	}
	if !errors.Is(errs[0], ErrAssetFailed) {
		t.Errorf("reported error = %v, want ErrAssetFailed", errs[0])
	}

	stages := recorder.recorded()
	if len(stages) != 1 || stages[0] != "Error:"+stageAsset {
		t.Errorf("hooks = %v, want one Error:%s", stages, stageAsset)
	}
}

// TestAssetMethodNotAllowedDispatchesOnErrorAndMetric mirrors the 404 case for a
// 405: a disallowed method is still an asset request that failed, and must be
// counted and reported the same way.
func TestAssetMethodNotAllowedDispatchesOnErrorAndMetric(t *testing.T) {
	fsys := fstest.MapFS{"app.css": {Data: []byte("body{color:red}")}}
	mount := mustMount(t, "/static/", fsys)
	recorder := &recordingPlugin{}
	env := newEnv(t, nil, withMounts(mount), withPlugins(t, recorder))

	res := env.do(httptest.NewRequest(http.MethodPost, "/static/app.css", nil))

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}

	responses := env.metrics.Snapshot().HTTPResponses
	if len(responses) != 1 || responses[0].Status != http.StatusMethodNotAllowed {
		t.Fatalf("HTTPResponse calls = %+v, want one 405", responses)
	}

	errs := recorder.reportedErrors()
	if len(errs) != 1 || !errors.Is(errs[0], ErrAssetFailed) {
		t.Fatalf("OnError dispatches = %v, want one ErrAssetFailed", errs)
	}
}

// nonSeekableFile is a file whose Read works but is not an io.ReadSeeker, which is
// what forces asset.Mount into its own 500 path rather than a 404 — see
// internal/asset/mount_test.go's TestMount_FailsLoudlyOnNonReadSeeker, which this
// mirrors.
type nonSeekableFile struct {
	io.Reader
	info fs.FileInfo
}

// Stat returns the wrapped info.
func (f *nonSeekableFile) Stat() (fs.FileInfo, error) { return f.info, nil }

// Close does nothing.
func (f *nonSeekableFile) Close() error { return nil }

// nonSeekableFS wraps an fs.FS so every file it opens loses io.ReadSeeker.
type nonSeekableFS struct{ inner fs.FS }

// Open opens name through the wrapped file system and strips io.ReadSeeker from
// the result.
func (n nonSeekableFS) Open(name string) (fs.File, error) {
	f, err := n.inner.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &nonSeekableFile{Reader: f, info: info}, nil
}

// TestAssetLogLevelsFollowStatus is the fix-round regression for the noise a naive
// implementation would have produced: every asset failure shares one stage,
// "asset", so the debug/error split that keeps a page's routine route miss out of
// the error log cannot be made on stage alone the way stageNotFound's can. A 404
// and a 405 are the most routine kind of request a mount ever sees — a stale link,
// a disallowed method — and must log at debug, exactly like a page's route miss.
// A 500 means the mount's own file system genuinely broke and must stay at error.
func TestAssetLogLevelsFollowStatus(t *testing.T) {
	fsys := fstest.MapFS{"app.css": {Data: []byte("body{color:red}")}}
	mount := mustMount(t, "/static/", nonSeekableFS{inner: fsys})
	env := newEnv(t, nil, withMounts(mount))

	env.get("/static/missing.css")
	env.do(httptest.NewRequest(http.MethodPost, "/static/app.css", nil))
	env.get("/static/app.css")

	records := env.logs.recordsFor("collage: request failed")
	if len(records) != 3 {
		t.Fatalf("log records = %+v, want one per asset failure", records)
	}
	if records[0].level != slog.LevelDebug || records[0].stage != stageAsset {
		t.Errorf("asset 404 logged as %+v, want debug level at stage %q", records[0], stageAsset)
	}
	if records[1].level != slog.LevelDebug || records[1].stage != stageAsset {
		t.Errorf("asset 405 logged as %+v, want debug level at stage %q", records[1], stageAsset)
	}
	if records[2].level != slog.LevelError || records[2].stage != stageAsset {
		t.Errorf("asset 500 logged as %+v, want error level: it means the mount's own file system genuinely broke", records[2])
	}
}

// TestPanickingMountBecomesA500 is the asset-side counterpart to
// TestPanicInACollaboratorBecomesA500: a mount's file system panicking used to
// unwind straight into net/http, dropping the connection with no status line, no
// hook, and no metric. It must now go through a panic guard — a 500, one
// ErrorHook dispatch, and the process left standing — but not the *same* one a
// page or a document uses: the response must stay text/plain, matching every
// other asset error this feature ever serves, never the framework's HTML error
// page a page's own panic gets.
func TestPanickingMountBecomesA500(t *testing.T) {
	mount := mustMount(t, "/static/", panicFS{})
	spy := &recordingPlugin{}
	env := newEnv(t, nil, withMounts(mount), withPlugins(t, spy))
	srv := env.server(t)

	res, body := request(t, srv, http.MethodGet, "/static/app.css")

	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusInternalServerError)
	}
	if len(body) == 0 {
		t.Error("body is empty: the client got a status with no page")
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain: a mount's error surface follows the route kind, not the page router's HTML", got)
	}
	if strings.Contains(string(body), "<html") {
		t.Errorf("body = %q, want the mount's own plain-text 500, never the page router's built-in HTML page", body)
	}

	if got := spy.recorded(); len(got) != 1 || got[0] != "Error:"+stagePanic {
		t.Fatalf("hooks = %v, want one Error:%s", got, stagePanic)
	}
	reported := spy.reportedErrors()
	if len(reported) != 1 || !errors.Is(reported[0], ErrPanic) {
		t.Fatalf("reported errors = %v, want one ErrPanic", reported)
	}
	if !strings.Contains(reported[0].Error(), "mount fs exploded") {
		t.Errorf("reported error = %q, want it to carry the panic value", reported[0])
	}

	responses := env.metrics.Snapshot().HTTPResponses
	if len(responses) != 1 || responses[0].Status != http.StatusInternalServerError {
		t.Fatalf("HTTPResponse metrics = %+v, want one 500", responses)
	}
}

// TestMountContentTypeSurvivesThroughRealRequest checks, through a real server
// rather than httptest.ResponseRecorder, that wrapping the ResponseWriter to
// capture the status never changes what the mount actually served: the real mime
// type on a hit, and the mount's own plain-text error body — never the page
// router's HTML — on a 404, a 405, and a panic recovered mid-request.
func TestMountContentTypeSurvivesThroughRealRequest(t *testing.T) {
	fsys := fstest.MapFS{"app.css": {Data: []byte("body{color:red}")}}
	mount := mustMount(t, "/static/", fsys)
	broken := mustMount(t, "/broken/", panicFS{})
	env := newEnv(t, nil, withMounts(mount, broken))
	srv := env.server(t)

	hit, hitBody := request(t, srv, http.MethodGet, "/static/app.css")
	if got := hit.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("hit Content-Type = %q, want text/css", got)
	}
	if string(hitBody) != "body{color:red}" {
		t.Errorf("hit body = %q, want the mounted file's content", hitBody)
	}

	miss, missBody := request(t, srv, http.MethodGet, "/static/missing.css")
	if miss.StatusCode != http.StatusNotFound {
		t.Fatalf("miss status = %d, want %d", miss.StatusCode, http.StatusNotFound)
	}
	if got := miss.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("miss Content-Type = %q, want text/plain, never the page router's HTML", got)
	}
	if strings.Contains(string(missBody), "<html") {
		t.Errorf("miss body = %q, want the mount's own plain-text 404, not HTML", missBody)
	}

	disallowed, disallowedBody := request(t, srv, http.MethodPost, "/static/app.css")
	if disallowed.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("disallowed status = %d, want %d", disallowed.StatusCode, http.StatusMethodNotAllowed)
	}
	if got := disallowed.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("disallowed Content-Type = %q, want text/plain", got)
	}
	if strings.Contains(string(disallowedBody), "<html") {
		t.Errorf("disallowed body = %q, want the mount's own plain-text body, not HTML", disallowedBody)
	}

	panicked, panickedBody := request(t, srv, http.MethodGet, "/broken/app.css")
	if panicked.StatusCode != http.StatusInternalServerError {
		t.Fatalf("panicked status = %d, want %d", panicked.StatusCode, http.StatusInternalServerError)
	}
	if got := panicked.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("panicked Content-Type = %q, want text/plain, never the page router's HTML", got)
	}
	if strings.Contains(string(panickedBody), "<html") {
		t.Errorf("panicked body = %q, want a plain-text 500, not the framework's built-in HTML page", panickedBody)
	}
}

// TestPageRequestUnaffectedByMountsBeingConfigured is the page-side control for
// this task: a page request's HTTPResponse metric and hook dispatch order must be
// exactly what they were before mounts ran through the same serve path, whether or
// not any mounts are configured alongside it.
func TestPageRequestUnaffectedByMountsBeingConfigured(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	mount := mustMount(t, "/static/", fstest.MapFS{"app.css": {Data: []byte("body{}")}})
	recorder := &recordingPlugin{}
	env := newEnv(t, []*types.Page{page}, withMounts(mount), withPlugins(t, recorder))
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	res := env.get("/")

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if got := res.Body.String(); got != "<html>home</html>" {
		t.Errorf("body = %q, want the rendered page", got)
	}

	want := []string{"PageResolved", "BeforeRender", "AfterRender", "CacheWrite"}
	if got := recorder.recorded(); len(got) != len(want) {
		t.Fatalf("hooks = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("hooks = %v, want %v", got, want)
			}
		}
	}

	responses := env.metrics.Snapshot().HTTPResponses
	if len(responses) != 1 || responses[0].Status != http.StatusOK || responses[0].Path != "/" {
		t.Errorf("HTTPResponse calls = %+v, want one 200 for \"/\"", responses)
	}
}

// panickingTracer panics from StartSpan. It is the hostile double for the one
// tracer call that runs before anything has been written.
type panickingTracer struct{ observability.NoopTracer }

func (panickingTracer) StartSpan(ctx context.Context, name string) (context.Context, observability.Span) {
	panic("tracer: start span exploded")
}

// panickingSpan starts fine and panics when an attribute is set — the other call
// that runs before the response.
type panickingSpanTracer struct{ observability.NoopTracer }

func (panickingSpanTracer) StartSpan(ctx context.Context, name string) (context.Context, observability.Span) {
	return ctx, panickingSpan{}
}

type panickingSpan struct{ observability.NoopSpan }

func (panickingSpan) SetAttribute(key, value string) { panic("tracer: set attribute exploded") }

func TestHandler_TracerPanicStillServesTheRequest(t *testing.T) {
	// A tracer that cannot start a span is a reason to serve the page without
	// tracing, not a reason not to serve it. Unguarded, the panic unwinds into
	// net/http, which closes the connection with no status line — the caller sees a
	// network failure rather than a request that was answered.
	for _, tc := range []struct {
		name   string
		tracer observability.Tracer
	}{
		{"StartSpan panics", panickingTracer{}},
		{"SetAttribute panics", panickingSpanTracer{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := testPage("home", "/", types.StrategyStatic)
			env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Tracer = tc.tracer })
			env.engine.set("home", fakeRender{html: "<html>home</html>"})

			res := env.get("/")

			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — the request must still be answered", res.Code)
			}
			if res.Body.Len() == 0 {
				t.Error("no body was written")
			}
			if env.logs.count("collage: tracer panicked starting the request span") == 0 {
				t.Error("the tracer panic was swallowed without a log line")
			}
		})
	}
}

// TestServe_CacheParamsRestrictTheKey is the defect a real application hits first.
//
// The raw query string is a cache-key dimension, which is correct — a data handler
// receives the whole request and may render from it — but it means every tracking
// parameter mints its own entry. A crawler walking "?utm_source=..." variants
// evicts the real archive from a bounded cache without ever requesting a distinct
// page. WithCacheParams names the parameters the page actually reads; everything
// else stops discriminating.
func TestServe_CacheParamsRestrictTheKey(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	page.CacheTTL = time.Minute
	page.CacheParams = []string{"page"}
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	env.get("/?page=2")
	after := env.engine.pageCalls("home")

	// Same "page", different tracking junk: one representation, one entry.
	env.get("/?page=2&utm_source=newsletter")
	if got := env.engine.pageCalls("home"); got != after {
		t.Errorf("renders = %d, want %d — an unlisted parameter must not mint a cache entry", got, after)
	}

	// The listed parameter still discriminates.
	env.get("/?page=3")
	if got := env.engine.pageCalls("home"); got != after+1 {
		t.Errorf("renders = %d, want %d — a listed parameter must still be a cache dimension", got, after+1)
	}
}

func TestServe_CacheParamsIgnoreParameterOrder(t *testing.T) {
	// The raw query is not canonicalised, so "?a=1&b=2" and "?b=2&a=1" are two
	// entries for one representation. Restricting the key is the point at which
	// that can be fixed without guessing which parameters matter.
	page := testPage("home", "/", types.StrategyIncremental)
	page.CacheTTL = time.Minute
	page.CacheParams = []string{"a", "b"}
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	env.get("/?a=1&b=2")
	after := env.engine.pageCalls("home")

	env.get("/?b=2&a=1")
	if got := env.engine.pageCalls("home"); got != after {
		t.Errorf("renders = %d, want %d — parameter order is not a representation", got, after)
	}
}

func TestServe_NoCacheParamsKeepsEveryQueryAsADimension(t *testing.T) {
	// The default must stay "everything". Narrowing by default would silently
	// merge two representations of a page whose handler reads a parameter nobody
	// remembered to declare.
	page := testPage("home", "/", types.StrategyIncremental)
	page.CacheTTL = time.Minute
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	env.get("/?anything=1")
	after := env.engine.pageCalls("home")

	env.get("/?anything=2")
	if got := env.engine.pageCalls("home"); got != after+1 {
		t.Errorf("renders = %d, want %d — with no allowlist every query is a dimension", got, after+1)
	}
}

func TestServe_EmptyCacheParamsDropsTheQueryEntirely(t *testing.T) {
	// A declared-but-empty allowlist is a statement, not an omission: this page
	// renders the same whatever the query says. It has to be distinguishable from
	// nil, or a page that genuinely ignores its query has no way to say so.
	page := testPage("home", "/", types.StrategyIncremental)
	page.CacheTTL = time.Minute
	page.CacheParams = []string{}
	env := newEnv(t, []*types.Page{page})
	env.engine.set("home", fakeRender{html: "<html>home</html>"})

	env.get("/?anything=1")
	after := env.engine.pageCalls("home")

	env.get("/?anything=2&more=3")
	if got := env.engine.pageCalls("home"); got != after {
		t.Errorf("renders = %d, want %d — an empty allowlist means no query discriminates", got, after)
	}
}

// In development a page is rendered again for every request, even one that was
// cached a moment ago. Templates reload from disk in dev mode, and a cached page
// would hide that reload for as long as its TTL — on exactly the pages someone is
// most likely to be editing.
func TestHandler_DevModeNeverServesACachedPage(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	env := newEnv(t, []*types.Page{page}, withDevMode())

	for range 3 {
		if rec := env.get("/"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}

	if got := env.engine.totalCalls(); got != 3 {
		t.Errorf("renders = %d, want 3: dev mode must not serve a page rendered before an edit", got)
	}
	// The entry is still written. Someone developing a plugin or an invalidation
	// rule needs the write path to keep working.
	if got := env.cacheEntries(); got == 0 {
		t.Error("nothing was written to the cache; the write path must still run in dev mode")
	}
}

// A path with dot segments or doubled slashes is sent to its clean spelling
// before anything reads it: a middleware skipping "/_collage/" must not be walked
// past with "/_collage/../admin".
func TestCleanPath_RedirectsBeforeMiddleware(t *testing.T) {
	cases := []struct{ in, clean string }{
		{"/_collage/../admin", "/admin"},
		{"/a//b", "/a/b"},
		{"/a/./b/", "/a/b/"},
		{"/..", "/"},
		{"/blog/.", "/blog"},
	}
	for _, tc := range cases {
		got, dirty := cleanPath(tc.in)
		if !dirty || got != tc.clean {
			t.Errorf("cleanPath(%q) = %q, %v; want %q", tc.in, got, dirty, tc.clean)
		}
	}
	for _, clean := range []string{"/", "/a/b", "/a/b/", "/file.v2.txt", "/.well-known/x"} {
		if _, dirty := cleanPath(clean); dirty {
			t.Errorf("cleanPath(%q) reports dirty", clean)
		}
	}

	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyDynamic)})
	rec := env.get("/x/../?q=1")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/?q=1" {
		t.Errorf("GET = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	post := env.do(httptest.NewRequest(http.MethodPost, "/a//b", nil))
	if post.Code != http.StatusPermanentRedirect || post.Header().Get("Location") != "/a/b" {
		t.Errorf("POST = %d %q", post.Code, post.Header().Get("Location"))
	}
}
