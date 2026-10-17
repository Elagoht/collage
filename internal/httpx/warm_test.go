package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/datacache"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// onlyKey returns the one key the tracker holds for tag, failing the test when
// there is not exactly one.
func onlyKey(t *testing.T, env *testEnv, tag string) string {
	t.Helper()
	keys, err := env.tracker.Resolve(context.Background(), []string{tag})
	if err != nil || len(keys) != 1 {
		t.Fatalf("Resolve(%q) = %v, %v; want one key", tag, keys, err)
	}
	return keys[0]
}

// freshIn reports whether key's entry is in the cache and not stale, and its
// content.
func freshIn(t *testing.T, env *testEnv, key string) (string, bool) {
	t.Helper()
	content, _, stale, found := env.cache.GetStale(context.Background(), key)
	return string(content), found && !stale
}

// A warm render rebuilds the very key a visitor's request wrote: one entry, now
// fresh, and the next visitor is a HIT. A page with no CacheParams keys on its raw
// query, so the query is replayed as it was written, not re-encoded.
func TestWarm_RecipeRebuildsTheSameKey(t *testing.T) {
	for _, target := range []string{"/", "/?b=2&a=1"} {
		t.Run(target, func(t *testing.T) {
			env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)})
			env.engine.set("home", fakeRender{html: "<html>old</html>", tags: []string{"posts"}})
			if rec := env.get(target); rec.Header().Get(CacheStatusHeader) != cacheMiss {
				t.Fatalf("first request %s = %q, want MISS", CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
			}
			key := onlyKey(t, env, "posts")
			if err := env.cache.MarkStaleKey(context.Background(), key); err != nil {
				t.Fatal(err)
			}
			env.engine.set("home", fakeRender{html: "<html>new</html>", tags: []string{"posts"}})

			ok, err := env.handler.warmKey(context.Background(), key)
			if !ok || err != nil {
				t.Fatalf("warmKey = %v, %v; want true, nil", ok, err)
			}
			if got := env.cacheEntries(); got != 1 {
				t.Fatalf("cache entries = %d, want 1: the warm wrote another key", got)
			}
			if content, fresh := freshIn(t, env, key); !fresh || content != "<html>new</html>" {
				t.Fatalf("entry = %q fresh %v, want the new render, fresh", content, fresh)
			}
			renders := env.engine.totalCalls()
			rec := env.get(target)
			if rec.Header().Get(CacheStatusHeader) != cacheHit || rec.Body.String() != "<html>new</html>" {
				t.Fatalf("after warm = %q %s=%q, want a HIT on the new page",
					rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
			}
			if env.engine.totalCalls() != renders {
				t.Fatal("the request after the warm rendered")
			}
		})
	}
}

// A key nobody wrote has no recipe, which is no failure: there is nothing to warm.
func TestWarm_AKeyWithNoRecipeIsSkipped(t *testing.T) {
	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)})
	if ok, err := env.handler.warmKey(context.Background(), "nobody-wrote-this"); ok || err != nil {
		t.Fatalf("warmKey = %v, %v; want false, nil", ok, err)
	}
}

// varyEngine renders the Accept-Language header the render's request carries, so a
// variant's content shows which reader's headers it was made from.
type varyEngine struct {
	*fakeEngine
}

func (e varyEngine) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	result, err := e.fakeEngine.Render(ctx, rc)
	if err == nil {
		result.HTML = []byte("<html>" + rc.Request.Header.Get("Accept-Language") + "</html>")
	}
	return result, err
}

// Each variant a middleware declared through Vary is warmed under its own key, with
// the header its render read, and the warm itself never runs the middleware.
func TestWarm_AVaryVariantIsWarmedUnderItsOwnKey(t *testing.T) {
	var middlewareCalls atomic.Int32
	counting := func(d *Deps) {
		d.Middleware = append(d.Middleware, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				middlewareCalls.Add(1)
				next.ServeHTTP(w, r)
			})
		})
	}
	var engine varyEngine
	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)},
		withVary("Accept-Language"), counting,
		func(d *Deps) {
			engine = varyEngine{fakeEngine: newFakeEngine(fakeRender{tags: []string{"posts"}})}
			d.Renderer = engine
		})
	for _, lang := range []string{"tr", "en"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Language", lang)
		if rec := env.do(req); rec.Body.String() != "<html>"+lang+"</html>" {
			t.Fatalf("%s = %q", lang, rec.Body.String())
		}
	}
	keys, err := env.tracker.Resolve(context.Background(), []string{"posts"})
	if err != nil || len(keys) != 2 {
		t.Fatalf("Resolve = %v, %v; want two variants", keys, err)
	}
	if err := env.cache.MarkStale(context.Background(), []string{"posts"}); err != nil {
		t.Fatal(err)
	}
	before := middlewareCalls.Load()
	for _, key := range keys {
		if ok, err := env.handler.warmKey(context.Background(), key); !ok || err != nil {
			t.Fatalf("warmKey(%q) = %v, %v", key, ok, err)
		}
	}
	if got := middlewareCalls.Load(); got != before {
		t.Fatalf("middleware ran %d times during the warm, want none", got-before)
	}
	if got := env.cacheEntries(); got != 2 {
		t.Fatalf("cache entries = %d, want 2", got)
	}
	var contents []string
	for _, key := range keys {
		content, fresh := freshIn(t, env, key)
		if !fresh {
			t.Fatalf("key %q not fresh after its warm", key)
		}
		contents = append(contents, content)
	}
	slices.Sort(contents)
	if want := []string{"<html>en</html>", "<html>tr</html>"}; !slices.Equal(contents, want) {
		t.Fatalf("warmed contents = %v, want %v: each variant under its own key", contents, want)
	}
}

// strValue is a data-cache value for the fresh-data test.
type strValue string

func (s strValue) Encode() ([]byte, error) { return json.Marshal(string(s)) }

// dataEngine renders one value it loads through a real data store, under the
// render's context, as collage.Cached does. It records whether the render and
// its request were both marked for fresh data.
type dataEngine struct {
	*fakeEngine
	store    *datacache.Store
	mu       sync.Mutex
	upstream string
	down     bool
	fresh    []bool
}

func (e *dataEngine) setUpstream(value string, down bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.upstream, e.down = value, down
}

func (e *dataEngine) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	e.mu.Lock()
	e.fresh = append(e.fresh, types.WantsFreshData(rc.Context()) && types.WantsFreshData(rc.Request.Context()))
	e.mu.Unlock()
	fetch := func(context.Context) (types.CacheValue, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.down {
			return nil, errors.New("cms down")
		}
		return strValue(e.upstream), nil
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

// A warm render waits for the upstream instead of rendering from stale data, so
// what it stores is the new content, fresh — not a stale page refreshed from stale
// data. The marker reaches both the render's context and its request's, through
// the shared render's context that hides every per-reader value.
func TestWarm_RendersFromFreshData(t *testing.T) {
	engine := &dataEngine{fakeEngine: newFakeEngine(fakeRender{}), store: datacache.New(datacache.Options{})}
	engine.setUpstream("old", false)
	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)},
		func(d *Deps) { d.Renderer = engine })
	if rec := env.get("/"); rec.Body.String() != "<html>old</html>" {
		t.Fatalf("first = %q", rec.Body.String())
	}
	key := onlyKey(t, env, "posts")
	engine.setUpstream("new", false)
	engine.store.Invalidate([]string{"posts"})
	if err := env.cache.MarkStaleKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}

	ok, err := env.handler.warmKey(context.Background(), key)
	if !ok || err != nil {
		t.Fatalf("warmKey = %v, %v; want true, nil", ok, err)
	}
	if content, fresh := freshIn(t, env, key); !fresh || content != "<html>new</html>" {
		t.Fatalf("entry = %q fresh %v, want the new content, fresh", content, fresh)
	}
	engine.mu.Lock()
	marks := slices.Clone(engine.fresh)
	engine.mu.Unlock()
	if want := []bool{false, true}; !slices.Equal(marks, want) {
		t.Fatalf("fresh-data marks = %v, want %v: only the warm render is marked, in both contexts", marks, want)
	}
	if rec := env.get("/"); rec.Header().Get(CacheStatusHeader) != cacheHit || rec.Body.String() != "<html>new</html>" {
		t.Fatalf("after warm = %q %s=%q, want a HIT on new", rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
}

// A document is warmed the way a page is.
func TestWarm_DocumentsToo(t *testing.T) {
	env, engine := staleDocEnv(t)
	key := onlyKey(t, env, "feed")
	engine.set(render.DocumentResult{Body: []byte("new"), Tags: []string{"feed"}}, nil)
	ok, err := env.handler.warmKey(context.Background(), key)
	if !ok || err != nil {
		t.Fatalf("warmKey = %v, %v; want true, nil", ok, err)
	}
	if content, fresh := freshIn(t, env, key); !fresh || content != "new" {
		t.Fatalf("entry = %q fresh %v, want new, fresh", content, fresh)
	}
	if rec := env.get("/feed.xml"); rec.Header().Get(CacheStatusHeader) != cacheHit || rec.Body.String() != "new" {
		t.Fatalf("after warm = %q %s=%q, want a HIT on new", rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
}

// A document keyed on some of its query is warmed under the key its reader wrote,
// though the reader's request, not a shared one, is what the document path records
// from: the parameters the key ignores are left out of the recipe.
func TestWarm_ADocumentKeyedOnPartOfItsQuery(t *testing.T) {
	doc := &types.Document{
		Name:        "feed",
		ContentType: "application/xml",
		Paths:       map[string]string{"en": "/feed.xml"},
		Strategy:    types.StrategyIncremental,
		CacheTTL:    time.Hour,
		CacheParams: []string{"page"},
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, nil
		},
	}
	engine := &staleDocEngine{fakeEngine: newFakeEngine(fakeRender{})}
	engine.set(render.DocumentResult{Body: []byte("old"), Tags: []string{"feed"}}, nil)
	rt := router.New(router.LocaleOptions{Default: "en"})
	if err := rt.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, nil, func(d *Deps) { d.Router, d.Renderer = rt, engine })
	const target = "/feed.xml?utm=x&page=2"
	if rec := env.get(target); rec.Header().Get(CacheStatusHeader) != cacheMiss {
		t.Fatalf("first %s = %q, want MISS", CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
	key := onlyKey(t, env, "feed")
	if err := env.cache.MarkStaleKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	engine.set(render.DocumentResult{Body: []byte("new"), Tags: []string{"feed"}}, nil)
	if ok, err := env.handler.warmKey(context.Background(), key); !ok || err != nil {
		t.Fatalf("warmKey = %v, %v; want true, nil", ok, err)
	}
	if got := env.cacheEntries(); got != 1 {
		t.Fatalf("cache entries = %d, want 1", got)
	}
	if rec := env.get(target); rec.Header().Get(CacheStatusHeader) != cacheHit || rec.Body.String() != "new" {
		t.Fatalf("after warm = %q %s=%q, want a HIT on new", rec.Body.String(), CacheStatusHeader, rec.Header().Get(CacheStatusHeader))
	}
}

// Content that is gone upstream is no failure of the upstream: the warm says so
// apart, and forgets the recipe, so later runs do not render it again.
func TestWarm_ContentGoneDropsTheRecipe(t *testing.T) {
	env, key := staleEnv(t)
	env.engine.set("home", fakeRender{notFound: true, err: errors.New("no such post")})
	ok, err := env.handler.warmKey(context.Background(), key)
	if ok || !errors.Is(err, errWarmGone) {
		t.Fatalf("warmKey = %v, %v; want false, errWarmGone", ok, err)
	}
	if _, found := env.handler.recipes.get(key); found {
		t.Fatal("the recipe of a key whose content is gone was kept")
	}
	if _, _, _, found := env.cache.GetStale(context.Background(), key); found {
		t.Fatal("the stale entry of content that is gone was kept")
	}
}

// A warm render that fails reports why, and the stale page stays for visitors.
func TestWarm_UpstreamDownReportsFailure(t *testing.T) {
	env, key := staleEnv(t)
	ok, err := env.handler.warmKey(context.Background(), key)
	if ok || err == nil {
		t.Fatalf("warmKey = %v, %v; want false and the failure", ok, err)
	}
	content, _, stale, found := env.cache.GetStale(context.Background(), key)
	if !found || !stale || string(content) != "<html>old</html>" {
		t.Fatalf("entry = %q stale %v found %v, want the old page kept stale", content, stale, found)
	}
}

// Development does not read the page cache, so it is not warmed either.
func TestWarm_NotInDevMode(t *testing.T) {
	env := newEnv(t, []*types.Page{testPage("home", "/", types.StrategyIncremental)}, withDevMode())
	env.engine.set("home", fakeRender{html: "<html>old</html>", tags: []string{"posts"}})
	env.get("/")
	key := onlyKey(t, env, "posts")
	renders := env.engine.totalCalls()
	if ok, err := env.handler.warmKey(context.Background(), key); ok || err != nil {
		t.Fatalf("warmKey = %v, %v; want false, nil", ok, err)
	}
	if env.engine.totalCalls() != renders {
		t.Fatal("a warm rendered in dev mode")
	}
}

// The recipe store keeps the newest recipes up to its cap, and answers in
// most-recently-written order.
func TestRecipes_BoundedNewestFirst(t *testing.T) {
	store := newRecipes(3)
	for _, key := range []string{"a", "b", "c"} {
		store.put(key, recipe{host: "example.com", path: "/" + key})
	}
	store.put("a", recipe{host: "example.com", path: "/a"})
	store.put("d", recipe{host: "example.com", path: "/a"})
	if _, ok := store.get("b"); ok {
		t.Fatal("b survived past the cap: the oldest is evicted first")
	}
	if got := store.newestFirst([]string{"a", "b", "c", "d", "zz"}); !slices.Equal(got, []string{"d", "a", "c"}) {
		t.Fatalf("newestFirst = %v, want [d a c]", got)
	}
	got := store.forPath("EXAMPLE.com", "/a")
	slices.Sort(got)
	if !slices.Equal(got, []string{"a", "d"}) {
		t.Fatalf("forPath = %v, want [a d]", got)
	}
	if got := store.forPath("other.com", "/a"); len(got) != 0 {
		t.Fatalf("forPath on another host = %v, want none", got)
	}
}

// An action's Location names a page of this cache only on the request's own
// origin; the fragment is never part of the target, the query is.
func TestSameOriginTarget(t *testing.T) {
	for _, tc := range []struct {
		location, path, query string
		ok                    bool
	}{
		{"/p/x", "/p/x", "", true},
		{"/p/x?saved=1#top", "/p/x", "saved=1", true},
		{"/p/x#top", "/p/x", "", true},
		{"x", "/admin/x", "", true},
		{"../p/x", "/p/x", "", true},
		{"?page=2", "/admin/save", "page=2", true},
		{"/p/%C3%BC", "/p/ü", "", true},
		{"http://Example.com/p/x?b=2&a=1", "/p/x", "b=2&a=1", true},
		{"https://example.com", "/", "", true},
		{"https://other.example/p/x", "", "", false},
		{"//other.example/p/x", "", "", false},
		{"ftp://example.com/p/x", "", "", false},
		{"http:/p/x", "", "", false},
		{"mailto:x@example.com", "", "", false},
		{"http://[::1", "", "", false},
	} {
		r := httptest.NewRequest(http.MethodPost, "/admin/save?from=form", nil)
		target, ok := sameOriginTarget(r, tc.location)
		var path, query string
		if target != nil {
			path, query = target.Path, target.RawQuery
			if target.Fragment != "" {
				t.Errorf("sameOriginTarget(%q) kept the fragment %q", tc.location, target.Fragment)
			}
		}
		if path != tc.path || query != tc.query || ok != tc.ok {
			t.Errorf("sameOriginTarget(%q) = %q ?%q, %v; want %q ?%q, %v",
				tc.location, path, query, ok, tc.path, tc.query, tc.ok)
		}
	}
}
