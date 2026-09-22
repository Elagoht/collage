package collage

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeTemplates writes files (path relative to the root, content) into a fresh
// temporary directory and returns it, so a test can name the exact templates it
// needs rather than share one fixture with every other test.
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

// cachedMarker is the body recordingCache hands back on a hit. It is deliberately
// not what the page renders: a response carrying it can only have come through the
// test's own cache, which is exactly what these tests need to prove.
const cachedMarker = "<!-- served by the test's own cache -->"

// recordingCache is a Cache implemented entirely against this package's public
// API — no internal import — that records every call made through it.
type recordingCache struct {
	mu sync.Mutex
	// gets counts Get calls.
	gets int
	// sets counts Set calls.
	sets int
	// setTagged counts SetTagged calls on the tagged variant below.
	setTagged int
	// lastTags is the tag slice the most recent SetTagged was given.
	lastTags []string
	// invalidatedKeys lists the keys InvalidateKey was called with.
	invalidatedKeys []string
	// stored holds whatever was written, keyed by cache key.
	stored map[string][]byte
}

var _ Cache = (*recordingCache)(nil)

// newRecordingCache returns an empty recordingCache.
func newRecordingCache() *recordingCache {
	return &recordingCache{stored: make(map[string][]byte)}
}

// Get reports a hit for any key that has been written, and answers it with
// cachedMarker rather than the stored bytes so a served response proves it came
// from here.
func (c *recordingCache) Get(_ context.Context, key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.gets++
	if _, ok := c.stored[key]; !ok {
		return nil, "", false
	}
	return []byte(cachedMarker), c.etag(key), true
}

// Set stores content under key and returns the ETag it was stored under.
func (c *recordingCache) Set(_ context.Context, key string, content []byte, _ time.Duration) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sets++
	c.stored[key] = content
	return c.etag(key), nil
}

// Invalidate removes nothing: these tests invalidate by key, and a cache is
// allowed to leave tag indexing to the framework's tracker.
func (c *recordingCache) Invalidate(_ context.Context, _ []string) error { return nil }

// InvalidateKey removes the entry under key and records that it was asked to.
func (c *recordingCache) InvalidateKey(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.invalidatedKeys = append(c.invalidatedKeys, key)
	delete(c.stored, key)
	return nil
}

// Clear removes every entry.
func (c *recordingCache) Clear(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stored = make(map[string][]byte)
	return nil
}

// etag returns this cache's own ETag for key. It must be called with c.mu held.
func (c *recordingCache) etag(key string) string {
	return fmt.Sprintf("%q", "rec-"+key[:8])
}

// counts returns the recorded call counts, read under the lock.
func (c *recordingCache) counts() (gets, sets, setTagged int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets, c.sets, c.setTagged
}

// taggedRecordingCache is a recordingCache that also implements TaggedCache, to
// prove the framework prefers SetTagged when a cache offers it.
type taggedRecordingCache struct {
	*recordingCache
}

var _ TaggedCache = taggedRecordingCache{}

// SetTagged stores content under key, recording the tags it was given.
func (c taggedRecordingCache) SetTagged(_ context.Context, key string, content []byte, _ time.Duration, tags []string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.setTagged++
	c.lastTags = append([]string(nil), tags...)
	c.stored[key] = content
	return c.etag(key), nil
}

// cacheTestApp builds an application over a one-page site whose output is cached,
// using store as its cache, and returns the app and its handler.
func cacheTestApp(t *testing.T, store Cache, enabled bool) (*App, http.Handler) {
	t.Helper()

	root := writeTemplates(t, map[string]string{
		"pages/home.html": `<h1>Home</h1>`,
	})

	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: root},
		Cache: CacheConfig{
			Enabled:    enabled,
			Store:      store,
			DefaultTTL: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page := NewPage("home").
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/").
		WithDependency("homepage").
		Static().
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app, app.Handler()
}

// serve issues a GET for path through handler and returns the recorder.
func serve(handler http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestCacheConfigStore_IsActuallyReadAndWrittenThrough is the test for the
// pluggable-cache guarantee: a Cache implemented against nothing but this package
// is installed through CacheConfig.Store, and the framework writes the render into
// it and serves the next request back out of it. The second response carries
// cachedMarker, which only this cache can produce, so "the cache was used" is
// asserted rather than assumed.
func TestCacheConfigStore_IsActuallyReadAndWrittenThrough(t *testing.T) {
	store := newRecordingCache()
	app, handler := cacheTestApp(t, store, true)

	first := serve(handler, "/")
	if first.Code != http.StatusOK {
		t.Fatalf("first GET /: status = %d, want 200", first.Code)
	}
	if body := first.Body.String(); !strings.Contains(body, "<h1>Home</h1>") {
		t.Fatalf("first GET /: body = %q, want the rendered page", body)
	}

	gets, sets, _ := store.counts()
	if gets != 1 || sets != 1 {
		t.Fatalf("after one request: gets = %d, sets = %d, want 1 and 1", gets, sets)
	}

	second := serve(handler, "/")
	if second.Code != http.StatusOK {
		t.Fatalf("second GET /: status = %d, want 200", second.Code)
	}
	if body := second.Body.String(); body != cachedMarker {
		t.Fatalf("second GET /: body = %q, want the cache's own content %q", body, cachedMarker)
	}

	gets, sets, _ = store.counts()
	if gets != 2 || sets != 1 {
		t.Fatalf("after two requests: gets = %d, sets = %d, want 2 and 1", gets, sets)
	}

	// The ETag on the wire is the one this cache reported, not a locally
	// recomputed one — otherwise every conditional request would miss.
	store.mu.Lock()
	var key string
	for k := range store.stored {
		key = k
	}
	want := store.etag(key)
	store.mu.Unlock()
	if got := second.Header().Get("ETag"); got != want {
		t.Errorf("second GET /: ETag = %q, want the cache's own %q", got, want)
	}

	// Invalidation reaches the same cache, by key.
	if _, err := app.InvalidateTagsN(context.Background(), "homepage"); err != nil {
		t.Fatalf("InvalidateTagsN: %v", err)
	}
	store.mu.Lock()
	invalidated := len(store.invalidatedKeys)
	store.mu.Unlock()
	if invalidated != 1 {
		t.Fatalf("InvalidateKey was called %d times, want 1", invalidated)
	}

	third := serve(handler, "/")
	if body := third.Body.String(); !strings.Contains(body, "<h1>Home</h1>") {
		t.Errorf("GET / after invalidation: body = %q, want a fresh render", body)
	}
}

// TestCacheConfigStore_TaggedCacheIsPreferred proves the optional extension is
// reached through the public alias: a Store that also implements TaggedCache has
// SetTagged called, with the page's dependency tags, instead of Set.
func TestCacheConfigStore_TaggedCacheIsPreferred(t *testing.T) {
	store := taggedRecordingCache{recordingCache: newRecordingCache()}
	_, handler := cacheTestApp(t, store, true)

	if rec := serve(handler, "/"); rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", rec.Code)
	}

	_, sets, setTagged := store.counts()
	if sets != 0 {
		t.Errorf("Set was called %d times, want 0: SetTagged must be preferred", sets)
	}
	if setTagged != 1 {
		t.Fatalf("SetTagged was called %d times, want 1", setTagged)
	}

	store.mu.Lock()
	tags := append([]string(nil), store.lastTags...)
	store.mu.Unlock()
	if len(tags) != 1 || tags[0] != "homepage" {
		t.Errorf("SetTagged tags = %v, want [homepage]", tags)
	}
}

// TestCacheConfigStore_EnabledIsTheMasterSwitch pins the documented behaviour that
// a Store on a disabled cache is not silently turned on. "Caching is off" has to
// mean off, or a config field means the opposite of what it says.
func TestCacheConfigStore_EnabledIsTheMasterSwitch(t *testing.T) {
	store := newRecordingCache()
	_, handler := cacheTestApp(t, store, false)

	serve(handler, "/")
	serve(handler, "/")

	gets, sets, _ := store.counts()
	if gets != 0 || sets != 0 {
		t.Errorf("with Cache.Enabled false: gets = %d, sets = %d, want 0 and 0", gets, sets)
	}
}

// TestValidate_StoreMakesCacheTypeIrrelevant covers the validation half of the
// same field: a caller who supplies a cache is not then asked to name one of the
// built-ins, and a caller who supplies neither still is.
func TestValidate_StoreMakesCacheTypeIrrelevant(t *testing.T) {
	withStore := &Config{
		Template: TemplateConfig{Root: "templates"},
		Cache:    CacheConfig{Enabled: true, Store: newRecordingCache(), Type: "not-a-real-cache"},
	}
	withStore.ApplyDefaults()
	if err := withStore.Validate(); err != nil {
		t.Errorf("Validate() with a Store = %v, want nil", err)
	}

	withoutStore := &Config{
		Template: TemplateConfig{Root: "templates"},
		Cache:    CacheConfig{Enabled: true, Type: "not-a-real-cache"},
	}
	withoutStore.ApplyDefaults()
	if err := withoutStore.Validate(); !errors.Is(err, ErrInvalidCacheType) {
		t.Errorf("Validate() without a Store = %v, want ErrInvalidCacheType", err)
	}
}

// TestTemplateConfigFuncs_AddsAndOverrides is the test for the custom-FuncMap
// guarantee: a function supplied through TemplateConfig.Funcs is callable from a
// template, and an entry under a built-in name replaces that built-in.
func TestTemplateConfigFuncs_AddsAndOverrides(t *testing.T) {
	root := writeTemplates(t, map[string]string{
		"pages/home.html": `<p>{{shout "hi"}}</p><p>{{upper "hi"}}</p><p>{{lower "HI"}}</p>`,
	})

	app, err := New(&Config{
		Server: ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{
			Root: root,
			Funcs: template.FuncMap{
				// A function the framework does not ship.
				"shout": func(s string) string { return strings.ToUpper(s) + "!" },
				// A replacement for one it does.
				"upper": func(s string) string { return "overridden:" + s },
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page := NewPage("home").
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/").
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	rec := serve(app.Handler(), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<p>HI!</p>") {
		t.Errorf("custom function was not called: body = %q", body)
	}
	if !strings.Contains(body, "<p>overridden:hi</p>") {
		t.Errorf("built-in \"upper\" was not overridden: body = %q", body)
	}
	if !strings.Contains(body, "<p>hi</p>") {
		t.Errorf("built-in \"lower\" stopped working: body = %q", body)
	}
}

// TestTemplateConfigFuncs_UnknownFunctionFailsAtStartup pins the other half of the
// contract: html/template can only call a name that existed when the template was
// parsed, so a template calling a function nobody registered is a New error rather
// than a first-request 500.
func TestTemplateConfigFuncs_UnknownFunctionFailsAtStartup(t *testing.T) {
	root := writeTemplates(t, map[string]string{
		"pages/home.html": `<p>{{shout "hi"}}</p>`,
	})

	if _, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: root},
	}); err == nil {
		t.Fatal("New() = nil error, want a parse failure for the unregistered function")
	}
}

// registrationTestApp builds an application over a template root holding one page
// template, ready for registration-failure tests.
func registrationTestApp(t *testing.T) *App {
	t.Helper()

	root := writeTemplates(t, map[string]string{
		"pages/home.html": `<h1>Home</h1>`,
	})
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: root},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

// simplePage returns a page named name whose content is the shared home template.
func simplePage(name string) *PageBuilder {
	return NewPage(name).WithContent(NewFragment(name+"-content", "pages/home.html").Build())
}

// TestRegistrationErrors_AreMatchable pins that every routing failure a caller can
// cause is reachable with errors.Is rather than only by reading the message. Each
// case registers something genuinely malformed and asserts on the sentinel the
// caller would branch on.
func TestRegistrationErrors_AreMatchable(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, app *App) error
		want  error
	}{
		{
			name: "invalid pattern",
			setup: func(_ *testing.T, app *App) error {
				return app.RegisterPage(simplePage("catch-all").WithPath("en", "/blog/{rest...}/more").Build())
			},
			want: ErrInvalidPattern,
		},
		{
			name: "duplicate route",
			setup: func(t *testing.T, app *App) error {
				if err := app.RegisterPage(simplePage("first").WithPath("en", "/blog").Build()); err != nil {
					t.Fatalf("first RegisterPage: %v", err)
				}
				return app.RegisterPage(simplePage("second").WithPath("en", "/blog").Build())
			},
			want: ErrDuplicateRoute,
		},
		{
			name: "ambiguous parameter name",
			setup: func(t *testing.T, app *App) error {
				if err := app.RegisterPage(simplePage("by-slug").WithPath("en", "/blog/{slug}").Build()); err != nil {
					t.Fatalf("first RegisterPage: %v", err)
				}
				return app.RegisterPage(simplePage("by-id").WithPath("en", "/blog/{id}/edit").Build())
			},
			want: ErrAmbiguousParameterName,
		},
		{
			name: "redirect shadows a page",
			setup: func(t *testing.T, app *App) error {
				if err := app.RegisterPage(simplePage("target").WithPath("en", "/blog").Build()); err != nil {
					t.Fatalf("first RegisterPage: %v", err)
				}
				return app.RegisterPage(simplePage("shadower").
					WithPath("en", "/news").
					WithRedirect("/blog", "/news", 301).
					Build())
			},
			want: ErrRedirectShadowsPage,
		},
		{
			name: "unsubstituted placeholder",
			setup: func(_ *testing.T, app *App) error {
				return app.RegisterPage(simplePage("moved").
					WithPath("en", "/blog/{slug}").
					WithRedirect("/old/{slug}", "/blog/{id}", 301).
					Build())
			},
			want: ErrUnsubstitutedPlaceholder,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.setup(t, registrationTestApp(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want one wrapping %v", err, tc.want)
			}
		})
	}
}

// TestRegisterPlugin_DuplicateIsMatchable pins the plugin registry's own sentinel,
// which App.RegisterPlugin returns unchanged.
func TestRegisterPlugin_DuplicateIsMatchable(t *testing.T) {
	app := registrationTestApp(t)

	if err := app.RegisterPlugin(&namedPlugin{name: "twice"}); err != nil {
		t.Fatalf("first RegisterPlugin: %v", err)
	}
	if err := app.RegisterPlugin(&namedPlugin{name: "twice"}); !errors.Is(err, ErrDuplicatePlugin) {
		t.Fatalf("second RegisterPlugin = %v, want ErrDuplicatePlugin", err)
	}
	if err := app.RegisterPlugin(&namedPlugin{name: ""}); !errors.Is(err, ErrEmptyPluginName) {
		t.Fatalf("RegisterPlugin with an empty name = %v, want ErrEmptyPluginName", err)
	}
	if err := app.RegisterPlugin(nil); !errors.Is(err, ErrNilPlugin) {
		t.Fatalf("RegisterPlugin(nil) = %v, want ErrNilPlugin", err)
	}
}

// namedPlugin is the smallest possible Plugin, for the registration tests.
type namedPlugin struct {
	name string
}

var _ Plugin = (*namedPlugin)(nil)

// Name returns the plugin's configured name.
func (p *namedPlugin) Name() string { return p.name }

// Version reports a fixed version.
func (p *namedPlugin) Version() string { return "0.0.1" }

// Init does nothing.
func (p *namedPlugin) Init(_ context.Context, _ Host) error { return nil }

// Shutdown does nothing.
func (p *namedPlugin) Shutdown(_ context.Context) error { return nil }

// TestNew_MissingTemplateRootIsMatchable pins the most common startup failure:
// New wraps it, so only a sentinel makes it distinguishable from a template that
// exists but does not parse.
func TestNew_MissingTemplateRootIsMatchable(t *testing.T) {
	_, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: filepath.Join(t.TempDir(), "no-such-directory")},
	})
	if !errors.Is(err, ErrTemplateRootMissing) {
		t.Fatalf("New() = %v, want an error wrapping ErrTemplateRootMissing", err)
	}
}

// TestDefaultContentSlot_IsTheSlotRegistrationFills proves the exported constant
// names the slot a page's content is actually bound to, rather than merely holding
// the string "content".
func TestDefaultContentSlot_IsTheSlotRegistrationFills(t *testing.T) {
	root := writeTemplates(t, map[string]string{
		"layouts/default.html": `<main>{{slot "` + DefaultContentSlot + `"}}</main>`,
		"pages/home.html":      `<h1>Home</h1>`,
	})
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: root},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page := NewPage("home").
		WithLayout(NewFragment("layout", "layouts/default.html").
			WithSlot(DefaultContentSlot, true, false).
			Build()).
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/").
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	rec := serve(app.Handler(), "/")
	if want := "<main><h1>Home</h1></main>"; rec.Body.String() != want {
		t.Errorf("GET / = %q, want %q", rec.Body.String(), want)
	}
}

// TestRenderPath_ResultIsNameable is the whole point of aliasing Result: a caller
// can now declare a variable of that type and write a helper that takes one, not
// merely use the value inline.
func TestRenderPath_ResultIsNameable(t *testing.T) {
	root := writeTemplates(t, map[string]string{
		"pages/home.html": `<h1>Home</h1>`,
	})
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: root},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page := NewPage("home").
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/").
		WithDependency("homepage").
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	var result *Result
	result, err = app.RenderPath(context.Background(), "/", "en", nil)
	if err != nil {
		t.Fatalf("RenderPath: %v", err)
	}
	if got := string(result.HTML); got != "<h1>Home</h1>" {
		t.Errorf("Result.HTML = %q", got)
	}
	if result.Degraded() {
		t.Error("Result.Degraded() = true, want false")
	}
	if len(result.DependencyTags) != 1 || result.DependencyTags[0] != "homepage" {
		t.Errorf("Result.DependencyTags = %v, want [homepage]", result.DependencyTags)
	}
	if fragments := describe(result.Metadata); fragments != 1 {
		t.Errorf("Metadata reported %d fragments, want 1", fragments)
	}
}

// describe takes the aliased Metadata by name — the declaration that would not
// compile without the alias — and returns how many fragments it recorded.
func describe(metadata *Metadata) int {
	var timing Timing = metadata.Timing
	var first FragmentMetadata = metadata.Fragments[0]
	if first.Failed || timing.Total < 0 {
		return 0
	}
	return len(metadata.Fragments)
}
