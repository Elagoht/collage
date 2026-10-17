package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/datacache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

const (
	warmStoppedLog = "collage: warming stopped, a page failed to render"
	warmRunLog     = "collage: warmed pages"
)

// withWarmLimit sets the handler's per-run warm cap.
func withWarmLimit(limit int) envOption {
	return func(d *Deps) { d.WarmLimit = limit }
}

// orderEngine is a fakeEngine that records the path of every render in order, and
// can hold the render of one path until its gate is closed.
type orderEngine struct {
	*fakeEngine
	mu      sync.Mutex
	paths   []string
	hold    string
	gate    chan struct{}
	entered chan struct{}
}

func newOrderEngine() *orderEngine {
	return &orderEngine{fakeEngine: newFakeEngine(fakeRender{html: "<html>page</html>", tags: []string{"posts"}})}
}

// holdPath makes the next renders of path wait until the returned gate is closed,
// and returns a channel that receives once per render of path entered.
func (e *orderEngine) holdPath(path string) (gate chan struct{}, entered chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hold, e.gate, e.entered = path, make(chan struct{}), make(chan struct{}, 16)
	return e.gate, e.entered
}

func (e *orderEngine) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	e.mu.Lock()
	e.paths = append(e.paths, rc.Request.URL.Path)
	var gate, entered chan struct{}
	if e.hold == rc.Request.URL.Path {
		gate, entered = e.gate, e.entered
	}
	e.mu.Unlock()
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	return e.fakeEngine.Render(ctx, rc)
}

func (e *orderEngine) rendered() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.paths)
}

func (e *orderEngine) renderedSince(n int) []string {
	return e.rendered()[n:]
}

// postsEnv is n posts at /posts/1 … /posts/n, each requested once in order, so
// /posts/n has the newest recipe. It returns their keys, oldest first.
func postsEnv(t *testing.T, n int, opts ...envOption) (*testEnv, *orderEngine, []string) {
	t.Helper()
	engine := newOrderEngine()
	page := testPage("post", "/posts/{id}", types.StrategyIncremental)
	opts = append([]envOption{func(d *Deps) { d.Renderer = engine }}, opts...)
	env := newEnv(t, []*types.Page{page}, opts...)
	t.Cleanup(func() { env.handler.StopWarming(context.Background()) })
	keys := make([]string, n)
	for i := range n {
		path := fmt.Sprintf("/posts/%d", i+1)
		if rec := env.get(path); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		keys[i] = flightKeyFor(t, env, path)
	}
	return env, engine, keys
}

// markAllStale marks every key stale, as an invalidation leaves them.
func markAllStale(t *testing.T, env *testEnv, keys []string) {
	t.Helper()
	for _, key := range keys {
		if err := env.cache.MarkStaleKey(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
}

// waitWarmed waits for the warmer to go idle, failing the test after a while.
func waitWarmed(t *testing.T, h *Handler) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		h.WaitWarmed()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warmer never went idle")
	}
}

// After a run, every key is fresh again with the new content, and each was
// rendered exactly once; the run is reported in one info line.
func TestWarmer_WarmsEveryKeyOnce(t *testing.T) {
	env, engine, keys := postsEnv(t, 12)
	markAllStale(t, env, keys)
	engine.set("post", fakeRender{html: "<html>new</html>", tags: []string{"posts"}})
	before := len(engine.rendered())

	env.handler.Warm(keys)
	waitWarmed(t, env.handler)

	if got := len(engine.renderedSince(before)); got != len(keys) {
		t.Fatalf("renders = %d, want %d: one per key", got, len(keys))
	}
	for _, key := range keys {
		if content, fresh := freshIn(t, env, key); !fresh || content != "<html>new</html>" {
			t.Fatalf("key %q = %q fresh %v, want the new page, fresh", key, content, fresh)
		}
	}
	if got := cacheEvents(env, observability.CacheWarm); got != len(keys) {
		t.Fatalf("CacheWarm events = %d, want %d", got, len(keys))
	}
	if got := env.logs.count(warmRunLog); got != 1 {
		t.Fatalf("%q logged %d times, want once", warmRunLog, got)
	}
}

// A down upstream fails the first render of a run, and the run stops there: the
// rest is not rendered, one warn says so, and every page stays stale for visitors.
// The warm requests themselves are quiet: no stale-serve line, no request failure,
// no CacheStale, no error hook.
func TestWarmer_StopsAtTheFirstFailure(t *testing.T) {
	recorder := &recordingPlugin{}
	env, engine, keys := postsEnv(t, 10, withPlugins(t, recorder))
	markAllStale(t, env, keys)
	engine.set("post", fakeRender{err: errors.New("cms down")})
	before := len(engine.rendered())
	hooksBefore := len(recorder.reportedErrors())

	env.handler.Warm(keys)
	waitWarmed(t, env.handler)

	if got := len(engine.renderedSince(before)); got != 1 {
		t.Fatalf("renders = %d, want 1: the run stops at its first failure", got)
	}
	if got := env.logs.count(warmStoppedLog); got != 1 {
		t.Fatalf("%q logged %d times, want once", warmStoppedLog, got)
	}
	if got := env.logs.count(warmRunLog); got != 1 {
		t.Fatalf("%q logged %d times, want once", warmRunLog, got)
	}
	if got := env.logs.count(staleLog); got != 0 {
		t.Fatalf("a warm logged %q %d times, want none", staleLog, got)
	}
	if got := env.logs.count("collage: request failed"); got != 0 {
		t.Fatalf("a warm logged a request failure %d times, want none", got)
	}
	if got := cacheEvents(env, observability.CacheStale); got != 0 {
		t.Fatalf("CacheStale = %d during the warm, want none", got)
	}
	if got := len(recorder.reportedErrors()); got != hooksBefore {
		t.Fatalf("error hooks called %d times by the warm, want none", got-hooksBefore)
	}
	for _, key := range keys {
		content, _, stale, found := env.cache.GetStale(context.Background(), key)
		if !found || !stale || string(content) != "<html>page</html>" {
			t.Fatalf("key %q = %q stale %v found %v, want the old page, stale", key, content, stale, found)
		}
	}
	// A later run is not held back by the stopped one: its keys were let go.
	engine.set("post", fakeRender{html: "<html>new</html>", tags: []string{"posts"}})
	env.handler.Warm(keys)
	waitWarmed(t, env.handler)
	for _, key := range keys {
		if _, fresh := freshIn(t, env, key); !fresh {
			t.Fatalf("key %q not warmed by the run after the stopped one", key)
		}
	}
}

// A run takes at most WarmLimit keys, the most recently written first; the rest
// stay stale for their first request.
func TestWarmer_LimitAndOrder(t *testing.T) {
	env, engine, keys := postsEnv(t, 6, withWarmLimit(3))
	markAllStale(t, env, keys)
	before := len(engine.rendered())

	env.handler.Warm(keys)
	waitWarmed(t, env.handler)

	rendered := engine.renderedSince(before)
	if len(rendered) != 3 {
		t.Fatalf("renders = %v, want three", rendered)
	}
	// The first key of a run renders on its own, so it is the newest for sure.
	if rendered[0] != "/posts/6" {
		t.Fatalf("first render = %q, want the newest, /posts/6", rendered[0])
	}
	slices.Sort(rendered)
	if want := []string{"/posts/4", "/posts/5", "/posts/6"}; !slices.Equal(rendered, want) {
		t.Fatalf("rendered = %v, want %v: the newest three", rendered, want)
	}
	for i, key := range keys {
		_, fresh := freshIn(t, env, key)
		if want := i >= 3; fresh != want {
			t.Fatalf("/posts/%d fresh = %v, want %v", i+1, fresh, want)
		}
	}
}

// A negative WarmLimit turns warming off: Warm does nothing and starts nothing.
func TestWarmer_NegativeLimitDisables(t *testing.T) {
	env, engine, keys := postsEnv(t, 3, withWarmLimit(-1))
	markAllStale(t, env, keys)
	before := len(engine.rendered())
	env.handler.Warm(keys)
	waitWarmed(t, env.handler)
	if got := len(engine.renderedSince(before)); got != 0 {
		t.Fatalf("renders = %d, want none", got)
	}
	if env.handler.warm != nil {
		t.Fatal("a warmer exists with warming off")
	}
}

// A key already queued is not queued again, and a key being rendered can be:
// an invalidation during its render leaves it to warm once more.
func TestWarmer_DedupesQueuedKeys(t *testing.T) {
	env, engine, keys := postsEnv(t, 4)
	markAllStale(t, env, keys)
	gate, entered := engine.holdPath("/posts/1")
	before := len(engine.rendered())

	// The run of /posts/1 alone holds the coordinator, so what follows queues.
	env.handler.Warm(keys[:1])
	<-entered
	env.handler.Warm(keys[1:3])
	env.handler.Warm(keys[1:])
	env.handler.Warm(keys[:1])
	close(gate)
	waitWarmed(t, env.handler)

	counts := map[string]int{}
	for _, path := range engine.renderedSince(before) {
		counts[path]++
	}
	want := map[string]int{"/posts/1": 1, "/posts/2": 1, "/posts/3": 1, "/posts/4": 1}
	for path, n := range want {
		if counts[path] != n {
			t.Fatalf("renders = %v, want %v", counts, want)
		}
	}
	// /posts/1 was queued again while it rendered, and that second warm found it
	// fresh: a skip, not a warm, so four CacheWarm for four renders.
	if got := cacheEvents(env, observability.CacheWarm); got != 4 {
		t.Fatalf("CacheWarm events = %d, want 4", got)
	}
	if recs := env.logs.recordsFor(warmRunLog); len(recs) != 4 || recs[3].skipped != "1" {
		t.Fatalf("run lines = %+v, want four, the last with one skipped", recs)
	}
}

// Dropped by StopWarming: the queue, and a render still running once ctx ends;
// Shutdown returns on time, and the warmer's goroutines finish once the render
// does. A Warm after it does nothing.
func TestWarmer_ShutdownDropsTheQueue(t *testing.T) {
	env, engine, keys := postsEnv(t, 6)
	markAllStale(t, env, keys)
	gate, entered := engine.holdPath("/posts/6")
	before := len(engine.rendered())
	env.handler.Warm(keys)
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		env.handler.StopWarming(ctx)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopWarming did not return when its context ended")
	}
	close(gate)
	finished := make(chan struct{})
	go func() {
		env.handler.warm.done.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the warmer's goroutines did not finish")
	}
	waitWarmed(t, env.handler)
	if got := engine.renderedSince(before); len(got) != 1 {
		t.Fatalf("renders = %v, want only the one that was running", got)
	}

	env.handler.Warm(keys)
	waitWarmed(t, env.handler)
	if got := engine.renderedSince(before); len(got) != 1 {
		t.Fatalf("renders after a Warm past StopWarming = %v, want none more", got)
	}
}

// A run queued behind the one running is dropped by StopWarming too, never
// started.
func TestWarmer_ShutdownWithAFullQueue(t *testing.T) {
	env, engine, keys := postsEnv(t, 8)
	markAllStale(t, env, keys)
	gate, entered := engine.holdPath("/posts/6")
	before := len(engine.rendered())
	env.handler.Warm(keys[:6])
	<-entered
	env.handler.Warm(keys[6:])

	stopped := make(chan struct{})
	go func() {
		env.handler.StopWarming(context.Background())
		close(stopped)
	}()
	// StopWarming waits for the held render, so the queue is dropped while it
	// is still held.
	deadline := time.Now().Add(5 * time.Second)
	for {
		env.handler.warm.mu.Lock()
		dropped := env.handler.warm.stopped
		env.handler.warm.mu.Unlock()
		if dropped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("StopWarming never dropped the queue")
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopWarming did not return once the running render ended")
	}
	waitWarmed(t, env.handler)
	if got := engine.renderedSince(before); len(got) != 1 {
		t.Fatalf("renders = %v, want only the one that was running", got)
	}
}

// With no cache there is nothing to warm into: no warmer, and Warm, WaitWarmed
// and StopWarming do nothing.
func TestWarmer_OffWithNoCache(t *testing.T) {
	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)},
		func(d *Deps) { d.Cache = nil })
	if env.handler.warm != nil {
		t.Fatal("a warmer exists with no cache")
	}
	env.get("/")
	renders := env.engine.totalCalls()
	env.handler.Warm([]string{"some-key"})
	env.handler.WaitWarmed()
	env.handler.StopWarming(context.Background())
	if env.engine.totalCalls() != renders {
		t.Fatal("rendered with no cache")
	}
}

// StopWarming on a handler that never warmed returns at once, and a second call
// is harmless.
func TestWarmer_StopWithoutWarming(t *testing.T) {
	env, _, _ := postsEnv(t, 1)
	env.handler.StopWarming(context.Background())
	env.handler.StopWarming(context.Background())
	waitWarmed(t, env.handler)
}

// A guarded page is not warmed: its guard is not asked, and the key counts as
// skipped, never as a failure.
func TestWarmer_SkipsGuardedPages(t *testing.T) {
	var asked atomic.Int32
	page := testPage("private", "/private", types.StrategyIncremental)
	page.ContentFragment.Guard = func(context.Context, *http.Request) (*types.GuardDecision, error) {
		asked.Add(1)
		return nil, nil
	}
	env := newEnv(t, []*types.Page{page})
	t.Cleanup(func() { env.handler.StopWarming(context.Background()) })
	env.engine.set("private", fakeRender{html: "<html>old</html>", tags: []string{"posts"}})
	if rec := env.get("/private"); rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	key := onlyKey(t, env, "posts")
	markAllStale(t, env, []string{key})
	askedBefore, renders := asked.Load(), env.engine.totalCalls()

	env.handler.Warm([]string{key})
	waitWarmed(t, env.handler)

	if asked.Load() != askedBefore {
		t.Fatal("the warm asked the page's guard")
	}
	if env.engine.totalCalls() != renders {
		t.Fatal("a guarded page was rendered by the warm")
	}
	if got := env.logs.count(warmStoppedLog); got != 0 {
		t.Fatalf("a skipped guarded page stopped the run (%d warns)", got)
	}
	if _, found := env.handler.recipes.get(key); found {
		t.Fatal("a guarded page's recipe was kept: every invalidation would queue it again")
	}
}

// failingTagged is a memory cache whose SetTagged fails while fail is set.
type failingTagged struct {
	*cache.MemoryCache
	fail atomic.Bool
}

func (c *failingTagged) SetTagged(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	if c.fail.Load() {
		return "", errors.New("disk full")
	}
	return c.MemoryCache.SetTagged(ctx, key, content, ttl, tags)
}

// failingTracker is a memory tracker whose Track fails while fail is set.
type failingTracker struct {
	*dependency.MemoryTracker
	fail atomic.Bool
}

func (t *failingTracker) Track(ctx context.Context, key string, tags []string) error {
	if t.fail.Load() {
		return errors.New("tracker unavailable")
	}
	return t.MemoryTracker.Track(ctx, key, tags)
}

// A warm whose cache write or tracking fails is a failure, not a skip: the next
// warm would fail the same way, and an entry written but untracked is out of
// every invalidation's reach. The run stops, and its one warn names the error.
func TestWarmer_AFailedCacheWriteStopsTheRun(t *testing.T) {
	inner := cache.NewMemory(cache.MemoryConfig{})
	store := &failingTagged{MemoryCache: inner}
	tracker := &failingTracker{MemoryTracker: dependency.NewMemory()}
	for name, failing := range map[string]*atomic.Bool{"write": &store.fail, "track": &tracker.fail} {
		t.Run(name, func(t *testing.T) {
			inner.Clear(context.Background())
			store.fail.Store(false)
			tracker.fail.Store(false)
			env, engine, keys := postsEnv(t, 5, func(d *Deps) { d.Cache, d.Tracker = store, tracker })
			env.cache = inner
			markAllStale(t, env, keys)
			failing.Store(true)
			before := len(engine.rendered())

			env.handler.Warm(keys)
			waitWarmed(t, env.handler)

			if got := len(engine.renderedSince(before)); got != 1 {
				t.Fatalf("renders = %d, want 1: the run stops at the failed write", got)
			}
			warns := env.logs.recordsFor(warmStoppedLog)
			want := map[string]string{"write": "disk full", "track": "tracker unavailable"}[name]
			if len(warns) != 1 || !strings.Contains(warns[0].err, want) {
				t.Fatalf("warns = %+v, want one naming %q", warns, want)
			}
			if got := cacheEvents(env, observability.CacheWarm); got != 0 {
				t.Fatalf("CacheWarm = %d, want none", got)
			}
			if got := env.logs.count("collage: request failed"); got != 0 {
				t.Fatalf("the warm logged %d request failures, want none", got)
			}
		})
	}
}

// personaliseCounter counts the PersonaliseHook calls it is given.
type personaliseCounter struct {
	calls atomic.Int32
}

func (*personaliseCounter) Name() string                            { return "acme/personalise" }
func (*personaliseCounter) Version() string                         { return "1.0.0" }
func (*personaliseCounter) Init(context.Context, plugin.Host) error { return nil }
func (*personaliseCounter) Shutdown(context.Context) error          { return nil }
func (p *personaliseCounter) OnPersonalise(context.Context, *plugin.PersonaliseEvent) error {
	p.calls.Add(1)
	return nil
}

// A warm's body goes to nobody, so nothing personalises it.
func TestWarmer_SkipsPersonalisation(t *testing.T) {
	counter := &personaliseCounter{}
	env, _, keys := postsEnv(t, 3, withPlugins(t, counter))
	markAllStale(t, env, keys)
	before := counter.calls.Load()
	env.handler.Warm(keys)
	waitWarmed(t, env.handler)
	if got := counter.calls.Load(); got != before {
		t.Fatalf("PersonaliseHook called %d times by the warm, want none", got-before)
	}
	for _, key := range keys {
		if _, fresh := freshIn(t, env, key); !fresh {
			t.Fatalf("key %q not warmed", key)
		}
	}
}

// Content gone upstream is skipped: it does not say the upstream is down, so it
// does not stop the run.
func TestWarmer_GoneIsSkipped(t *testing.T) {
	env, engine, keys := postsEnv(t, 4)
	markAllStale(t, env, keys)
	engine.set("post", fakeRender{notFound: true, err: errors.New("no such post")})
	before := len(engine.rendered())
	env.handler.Warm(keys)
	waitWarmed(t, env.handler)
	if got := len(engine.renderedSince(before)); got != len(keys) {
		t.Fatalf("renders = %d, want %d: a gone page does not stop the run", got, len(keys))
	}
	if got := env.logs.count(warmStoppedLog); got != 0 {
		t.Fatalf("%q logged %d times, want none", warmStoppedLog, got)
	}
}

// gatedData renders one value loaded through a real data store, whose upstream
// fetch can be held, so a test can hold a warm render on the upstream.
type gatedData struct {
	*fakeEngine
	store    *datacache.Store
	mu       sync.Mutex
	upstream string
	gate     chan struct{}
	entered  chan struct{}
	fetches  atomic.Int32
}

func (e *gatedData) set(value string, gate chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.upstream, e.gate = value, gate
}

func (e *gatedData) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	fetch := func(context.Context) (types.CacheValue, error) {
		e.fetches.Add(1)
		e.mu.Lock()
		gate, value := e.gate, e.upstream
		e.mu.Unlock()
		if gate != nil {
			e.entered <- struct{}{}
			<-gate
		}
		return strValue(value), nil
	}
	decode := func(raw []byte) (types.CacheValue, error) {
		var s string
		err := json.Unmarshal(raw, &s)
		return strValue(s), err
	}
	metadata := &render.Metadata{Page: rc.Page.Name, Locale: rc.Locale}
	value, stale, err := e.store.Load(rc.Context(), "posts", 0, []string{"posts"}, fetch, decode)
	if err != nil {
		return &render.Result{Metadata: metadata}, err
	}
	html := "<html>" + string(value.(strValue)) + "</html>"
	return &render.Result{HTML: []byte(html), DependencyTags: []string{"posts"}, Metadata: metadata, Stale: stale}, nil
}

// A visitor who asks while a warm render waits on the upstream is never made to
// wait with it: it does not join the warm's render, and is served the stale page
// at once. The warm then stores the new page.
func TestWarmer_AVisitorNeverWaitsOnAWarm(t *testing.T) {
	engine := &gatedData{
		fakeEngine: newFakeEngine(fakeRender{}),
		store:      datacache.New(datacache.Options{}),
		entered:    make(chan struct{}, 4),
	}
	engine.set("old", nil)
	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)},
		func(d *Deps) { d.Renderer = engine })
	t.Cleanup(func() { env.handler.StopWarming(context.Background()) })
	if rec := env.get("/"); rec.Body.String() != "<html>old</html>" {
		t.Fatalf("first = %q", rec.Body.String())
	}
	key := onlyKey(t, env, "posts")
	gate := make(chan struct{})
	engine.set("new", gate)
	engine.store.Invalidate([]string{"posts"})
	markAllStale(t, env, []string{key})

	env.handler.Warm([]string{key})
	<-engine.entered

	served := make(chan string, 1)
	go func() {
		rec := env.get("/")
		served <- rec.Header().Get(CacheStatusHeader) + " " + rec.Body.String()
	}()
	select {
	case got := <-served:
		if got != "STALE <html>old</html>" {
			t.Fatalf("visitor = %q, want the stale page", got)
		}
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("the visitor waited on the warm render")
	}

	close(gate)
	waitWarmed(t, env.handler)
	if rec := env.get("/"); rec.Header().Get(CacheStatusHeader) != cacheHit || rec.Body.String() != "<html>new</html>" {
		t.Fatalf("after the warm = %q %s=%q, want a HIT on new",
			rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	if got := engine.fetches.Load(); got != 2 {
		t.Fatalf("upstream fetches = %d, want 2: the first page and the warm", got)
	}
}

// Warming is off in development, where the page cache is not read.
func TestWarmer_OffInDevMode(t *testing.T) {
	env, engine, keys := postsEnv(t, 2, withDevMode())
	before := len(engine.rendered())
	env.handler.Warm(keys)
	waitWarmed(t, env.handler)
	if got := len(engine.renderedSince(before)); got != 0 {
		t.Fatalf("renders = %d in dev mode, want none", got)
	}
}

// waitJoined waits until a render of key in the warm flight has a waiter.
func waitJoined(t *testing.T, h *Handler, key string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.warmFlight.waitingOn(key) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("nothing joined the warm render")
		}
		time.Sleep(time.Millisecond)
	}
}

// An action's redirect target the background run has claimed and is rendering
// is not rendered a second time: the action's warm joins that render.
func TestActionWarm_JoinsAClaimedRender(t *testing.T) {
	env, engine, keys := postsEnv(t, 3)
	markAllStale(t, env, keys)
	engine.set("post", fakeRender{html: "<html>new</html>", tags: []string{"posts"}})
	// /posts/3 is the newest recipe, so the run's probe.
	gate, entered := engine.holdPath("/posts/3")
	before := len(engine.rendered())

	env.handler.Warm(keys)
	<-entered
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil), "save", "/posts/3")
	}()
	waitJoined(t, env.handler, keys[2])
	close(gate)
	<-done
	waitWarmed(t, env.handler)

	counts := map[string]int{}
	for _, path := range engine.renderedSince(before) {
		counts[path]++
	}
	if counts["/posts/3"] != 1 {
		t.Fatalf("renders = %v, want /posts/3 once", counts)
	}
	if content, fresh := freshIn(t, env, keys[2]); !fresh || content != "<html>new</html>" {
		t.Fatalf("target = %q fresh %v, want the new page", content, fresh)
	}
}

// A render that began before the action's invalidation is not stored, being
// overlapped by it. The action's warm, having joined it and taken the key out
// of the queue, renders the key again itself rather than leave it stale.
func TestActionWarm_RetriesAJoinedRenderThatWasNotStored(t *testing.T) {
	var epoch atomic.Uint64
	env, engine, keys := postsEnv(t, 2, withEpoch(&epoch))
	markAllStale(t, env, keys)
	gate, entered := engine.holdPath("/posts/1")

	// An earlier run, rendering /posts/1 with what the upstream said then.
	env.handler.Warm(keys[:1])
	<-entered
	// The action's invalidation, during that render: it changes the page and
	// queues /posts/1 again.
	epoch.Add(1)
	engine.set("post", fakeRender{html: "<html>new</html>", tags: []string{"posts"}})
	env.handler.Warm(keys[:1])

	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil), "save", "/posts/1")
	}()
	waitJoined(t, env.handler, keys[0])
	// Later renders of /posts/1 are not held.
	engine.mu.Lock()
	engine.hold = ""
	engine.mu.Unlock()
	close(gate)
	<-done

	if content, fresh := freshIn(t, env, keys[0]); !fresh || content != "<html>new</html>" {
		t.Fatalf("target = %q fresh %v right after the action, want the new page", content, fresh)
	}
	waitWarmed(t, env.handler)
}

// After StopWarming an action warms nothing.
func TestActionWarm_NothingAfterStopWarming(t *testing.T) {
	env, engine, keys := postsEnv(t, 1)
	markAllStale(t, env, keys)
	env.handler.StopWarming(context.Background())
	before := len(engine.rendered())
	env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil), "save", "/posts/1")
	if got := engine.renderedSince(before); len(got) != 0 {
		t.Fatalf("rendered %v after StopWarming", got)
	}
}

// A joined render that failed is upstream trouble, and the action's warm does
// not render the key again: the submit waits for one render, not two, and the
// target stays stale.
func TestActionWarm_DoesNotRetryAJoinedRenderThatFailed(t *testing.T) {
	env, engine, keys := postsEnv(t, 2)
	markAllStale(t, env, keys)
	gate, entered := engine.holdPath("/posts/2")

	env.handler.Warm(keys[1:])
	<-entered
	engine.set("post", fakeRender{err: errors.New("cms down")})
	before := len(engine.rendered())

	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil), "save", "/posts/2")
	}()
	waitJoined(t, env.handler, keys[1])
	engine.mu.Lock()
	engine.hold = ""
	engine.mu.Unlock()
	close(gate)
	<-done
	waitWarmed(t, env.handler)

	if got := engine.renderedSince(before); len(got) != 0 {
		t.Fatalf("rendered %v after joining the failed render, want nothing more", got)
	}
	if _, fresh := freshIn(t, env, keys[1]); fresh {
		t.Fatal("the target is fresh after a failed render")
	}
}

// isPending reports whether key is queued for the background run.
func isPending(h *Handler, key string) bool {
	h.warm.mu.Lock()
	defer h.warm.mu.Unlock()
	_, ok := h.warm.pending[key]
	return ok
}

// queue puts keys in the background queue by hand, as a run with nobody to work
// it through: what an action's warm takes from it, or leaves, is then all there.
func queue(h *Handler, keys ...string) {
	h.warm.mu.Lock()
	defer h.warm.mu.Unlock()
	for _, key := range keys {
		h.warm.pending[key] = struct{}{}
	}
}

// On a route with no CacheParams the raw query is part of the key: an action
// redirecting to the bare path warms the bare key only, and the query variant
// stays in the background queue, to be warmed there.
func TestActionWarm_OnlyTheLocationsQueryVariant(t *testing.T) {
	env, engine, keys := postsEnv(t, 0)
	for _, path := range []string{"/posts/1?page=2", "/posts/1", "/posts/2"} {
		if rec := env.get(path); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		keys = append(keys, flightKeyFor(t, env, path))
	}
	paged, bare, probe := keys[0], keys[1], keys[2]
	markAllStale(t, env, keys)
	engine.set("post", fakeRender{html: "<html>new</html>", tags: []string{"posts"}})
	// /posts/2 is the newest recipe, the run's probe: held, the run goes no
	// further, and what the action leaves queued stays queued.
	gate, entered := engine.holdPath("/posts/2")
	// Closed on a failure too, or the cleanup's StopWarming waits on the held
	// render forever.
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	env.handler.Warm(keys)
	<-entered

	env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil), "save", "/posts/1")
	if content, fresh := freshIn(t, env, bare); !fresh || content != "<html>new</html>" {
		t.Fatalf("bare key = %q fresh %v, want the new page", content, fresh)
	}
	if _, fresh := freshIn(t, env, paged); fresh {
		t.Fatal("the ?page=2 variant was warmed by the action")
	}
	if !isPending(env.handler, paged) {
		t.Fatal("the ?page=2 variant was taken out of the background queue")
	}
	release.Do(func() { close(gate) })
	waitWarmed(t, env.handler)
	for _, key := range []string{paged, probe} {
		if _, fresh := freshIn(t, env, key); !fresh {
			t.Fatalf("key %q not warmed by the background run", key)
		}
	}
}

// On a route with CacheParams the key holds the canonical selection: the action's
// Location, with its parameters in another order and one that is no cache
// dimension, lands on the key a reader's request wrote, and the bare variant is
// not warmed.
func TestActionWarm_CacheParamsSelectTheVariant(t *testing.T) {
	engine := newOrderEngine()
	page := testPage("list", "/list", types.StrategyIncremental)
	page.CacheParams = []string{"page", "sort"}
	env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Renderer = engine })
	t.Cleanup(func() { env.handler.StopWarming(context.Background()) })
	var keys []string
	for _, path := range []string{"/list?utm=a&sort=new&page=2", "/list"} {
		if rec := env.get(path); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		keys = append(keys, flightKeyFor(t, env, path))
	}
	markAllStale(t, env, keys)

	env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil), "save",
		"/list?page=2&utm=z&sort=new")
	if _, fresh := freshIn(t, env, keys[0]); !fresh {
		t.Fatal("the page=2 variant was not warmed")
	}
	if _, fresh := freshIn(t, env, keys[1]); fresh {
		t.Fatal("the bare variant was warmed")
	}
}

// slowVaryEngine is a varyEngine whose every render takes delay.
type slowVaryEngine struct {
	varyEngine
	delay time.Duration
}

func (e slowVaryEngine) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	time.Sleep(e.delay)
	return e.varyEngine.Render(ctx, rc)
}

// With a slow upstream the action's wait is held to its budget, even while a
// render is under way: the redirect goes out, and the variants it did not take
// are warmed by the background run.
func TestActionWarm_HeldToItsBudget(t *testing.T) {
	// A render longer than the margin below, so a budget checked only between
	// keys, not while one renders, still fails the test.
	const delay, budget = 2 * time.Second, 100 * time.Millisecond
	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)},
		withVary("Accept-Language"),
		func(d *Deps) {
			d.Renderer = slowVaryEngine{
				varyEngine: varyEngine{fakeEngine: newFakeEngine(fakeRender{tags: []string{"posts"}})},
				delay:      delay,
			}
		})
	t.Cleanup(func() { env.handler.StopWarming(context.Background()) })
	langs := []string{"tr", "en", "de", "fr", "es", "it"}
	for _, lang := range langs {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Language", lang)
		if rec := env.do(req); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", lang, rec.Code)
		}
	}
	keys, err := env.tracker.Resolve(context.Background(), []string{"posts"})
	if err != nil || len(keys) != len(langs) {
		t.Fatalf("Resolve = %v, %v; want %d variants", keys, err, len(langs))
	}
	markAllStale(t, env, keys)
	env.handler.warm.targetBudget = budget
	env.handler.Warm(keys)

	began := time.Now()
	env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil), "save", "/")
	// A second of margin: under -race the redirect's own goroutines are slow
	// to be scheduled, never two seconds slow.
	if took := time.Since(began); took > budget+time.Second {
		t.Fatalf("the action waited %v, want its budget of %v", took, budget)
	}

	deadline := time.Now().Add(20 * time.Second)
	for _, key := range keys {
		for {
			if _, fresh := freshIn(t, env, key); fresh {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("variant %q never warmed", key)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// A request deadline shorter than the budget cuts the wait shorter.
func TestActionWarm_HeldToTheRequestDeadline(t *testing.T) {
	env, engine, keys := postsEnv(t, 1)
	markAllStale(t, env, keys)
	gate, _ := engine.holdPath("/posts/1")
	t.Cleanup(func() { close(gate) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.warmRedirectTarget(httptest.NewRequest(http.MethodPost, "/save", nil).WithContext(ctx), "save", "/posts/1")
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the action waited past a 50ms request deadline")
	}
}

// A failed render ends the action's warm at once: the next variant is not taken,
// and stays in the background queue.
func TestActionWarm_StopsAtAFailure(t *testing.T) {
	env, engine, keys := postsEnv(t, 0)
	for _, path := range []string{"/posts/1?b=1", "/posts/1?a=1"} {
		env.get(path)
		keys = append(keys, flightKeyFor(t, env, path))
	}
	markAllStale(t, env, keys)
	engine.set("post", fakeRender{err: errors.New("cms down")})
	queue(env.handler, keys...)
	before := len(engine.rendered())

	env.handler.warmTargetKeys(env.handler.warm, "save", "/posts/1", keys, time.Now().Add(time.Minute))
	if got := engine.renderedSince(before); len(got) != 1 {
		t.Fatalf("rendered %v, want one render and a stop", got)
	}
	if !isPending(env.handler, keys[1]) {
		t.Fatal("the key after the failure was taken out of the queue")
	}
}

// So does joining a render that failed: the upstream is in trouble either way.
func TestActionWarm_StopsAtAJoinedFailure(t *testing.T) {
	env, engine, keys := postsEnv(t, 2)
	markAllStale(t, env, keys)
	gate, entered := engine.holdPath("/posts/1")
	env.handler.Warm(keys[:1])
	<-entered
	engine.set("post", fakeRender{err: errors.New("cms down")})
	queue(env.handler, keys[1])
	before := len(engine.rendered())

	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.warmTargetKeys(env.handler.warm, "save", "/posts", keys, time.Now().Add(time.Minute))
	}()
	waitJoined(t, env.handler, keys[0])
	engine.mu.Lock()
	engine.hold = ""
	engine.mu.Unlock()
	close(gate)
	<-done
	if got := engine.renderedSince(before); len(got) != 0 {
		t.Fatalf("rendered %v after the joined failure, want nothing", got)
	}
	if !isPending(env.handler, keys[1]) {
		t.Fatal("the key after the joined failure was taken out of the queue")
	}
}
