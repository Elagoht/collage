package collage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// hgPlugin runs initFn in Init and configureFn in Configure, for the tests of
// what Host and ConfigHost guarantee a plugin.
type hgPlugin struct {
	name        string
	initFn      func(ctx context.Context, h collage.Host) error
	configureFn func(ctx context.Context, h collage.ConfigHost) error
}

func (p *hgPlugin) Name() string {
	if p.name == "" {
		return "test/hg"
	}
	return p.name
}
func (p *hgPlugin) Version() string                { return "0" }
func (p *hgPlugin) Shutdown(context.Context) error { return nil }
func (p *hgPlugin) Init(ctx context.Context, h collage.Host) error {
	if p.initFn == nil {
		return nil
	}
	return p.initFn(ctx, h)
}
func (p *hgPlugin) Configure(ctx context.Context, h collage.ConfigHost) error {
	if p.configureFn == nil {
		return nil
	}
	return p.configureFn(ctx, h)
}

// A plugin writing into every container of its Page copy — CacheParams,
// FragmentPaths and its inner maps, Actions and each action's Methods,
// Redirects — reaches neither the page the router serves nor another
// plugin's copy (HOST-01).
func TestHost_PageCopySharesNoContainer(t *testing.T) {
	var fresh *collage.Page
	writer := &hgPlugin{name: "test/writer", initFn: func(_ context.Context, h collage.Host) error {
		page, _ := h.Page("search")
		page.CacheParams[0] = "unrelated"
		page.FragmentPaths["en"]["/injected"] = page.ContentFragment
		page.Actions[0].Methods[0] = http.MethodGet
		page.Redirects[0].To = "/hijacked"
		return nil
	}}
	reader := &hgPlugin{name: "test/reader", initFn: func(_ context.Context, h collage.Host) error {
		fresh, _ = h.Page("search")
		return nil
	}}
	live := collage.NewFragment("live", "live.html").Build()
	search := collage.NewPage("search").
		WithContent(collage.NewFragment("search", "search.html").WithData(collage.Load(
			func(_ context.Context, rc *collage.RenderContext) (string, error) {
				return rc.Request.URL.Query().Get("q"), nil
			})).Build()).
		WithPath("en", "/search").
		WithRedirect("/find", "/search", http.StatusMovedPermanently).
		WithFragmentPath("en", "/search/live", live).
		Incremental(time.Minute).WithCacheParams("q").
		WithAction(http.MethodPost, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.SeeOther("/"), nil
		}).Build()
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(writer, reader),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
		}),
		collagetest.WithTemplates(map[string]string{"search.html": `q={{.}}`, "live.html": `live`}),
		collagetest.WithRegister(search),
	)

	if !slices.Equal(fresh.CacheParams, []string{"q"}) {
		t.Errorf("another plugin's copy has CacheParams %v", fresh.CacheParams)
	}
	if n := len(fresh.FragmentPaths["en"]); n != 1 {
		t.Errorf("another plugin's copy has %d fragment paths, want 1", n)
	}
	if got := fresh.Actions[0].Methods; !slices.Equal(got, []string{http.MethodPost}) {
		t.Errorf("another plugin's copy has action methods %v", got)
	}
	if got := fresh.Redirects[0].To; got != "/search" {
		t.Errorf("another plugin's copy has redirect to %q", got)
	}
	site.Get("/search?q=alice").WantStatus(http.StatusOK)
	if body := site.Get("/search?q=bob").WantStatus(http.StatusOK).Body; body != "q=bob" {
		t.Errorf("GET /search?q=bob = %q: the cache key lost its parameter", body)
	}
	site.Get("/search/injected").WantStatus(http.StatusNotFound)
	if got := site.Get("/find").WantStatus(http.StatusMovedPermanently).Header.Get("Location"); got != "/search" {
		t.Errorf("redirect Location = %q", got)
	}
}

// A ConfigHost kept past Configure refuses AddTemplateFunc and AddRenderFunc
// with ErrConfigureClosed, and a late WrapMount changes nothing, while requests
// render: a late render function used to be written into the map every render
// ranges over (HOST-02). Run under -race.
func TestConfigHost_ClosedAfterConfigure(t *testing.T) {
	var kept collage.ConfigHost
	p := &hgPlugin{configureFn: func(_ context.Context, h collage.ConfigHost) error {
		kept = h
		return h.AddRenderFunc("early", func(*collage.RenderContext) any { return func() string { return "e" } })
	}}
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"home.html": `{{early}}`}),
		collagetest.WithSetup(func(app *collage.App) error {
			return app.Mount("/static/", fstest.MapFS{"a.txt": {Data: []byte("original")}})
		}),
	)
	h := site.App.Handler()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}
	}()
	for i := range 50 {
		name := fmt.Sprintf("late%d", i)
		if err := kept.AddRenderFunc(name, func(*collage.RenderContext) any { return func() string { return "l" } }); !errors.Is(err, collage.ErrConfigureClosed) {
			t.Fatalf("late AddRenderFunc = %v, want ErrConfigureClosed", err)
		}
	}
	<-done
	if err := kept.AddTemplateFunc("lateFunc", func() string { return "late" }); !errors.Is(err, collage.ErrConfigureClosed) {
		t.Errorf("late AddTemplateFunc = %v, want ErrConfigureClosed", err)
	}
	kept.WrapMount(func(fs.FS) fs.FS { return fstest.MapFS{"a.txt": {Data: []byte("wrapped")}} })
	if body := site.Get("/static/a.txt").WantStatus(http.StatusOK).Body; body != "original" {
		t.Errorf("a late WrapMount reached a mount: %q", body)
	}
	if !strings.Contains(site.Logs.String(), "WrapMount") {
		t.Errorf("a late WrapMount was not logged:\n%s", site.Logs.String())
	}
}

// A plugin's ConfigHost closes when its own Configure returns, not when every
// plugin's has: a host one plugin kept is refused during the next one's
// Configure.
func TestConfigHost_ClosedPerPlugin(t *testing.T) {
	var kept collage.ConfigHost
	var lateErr error
	first := &hgPlugin{name: "test/first", configureFn: func(_ context.Context, h collage.ConfigHost) error {
		kept = h
		return nil
	}}
	second := &hgPlugin{name: "test/second", configureFn: func(context.Context, collage.ConfigHost) error {
		lateErr = kept.AddTemplateFunc("sneaky", func() string { return "x" })
		return nil
	}}
	collagetest.NewSite(t, collagetest.WithPlugins(first, second))
	if !errors.Is(lateErr, collage.ErrConfigureClosed) {
		t.Errorf("AddTemplateFunc through another plugin's Configure = %v, want ErrConfigureClosed", lateErr)
	}
}

// text/template's builtins and html/template's escapers are refused by both
// AddTemplateFunc and AddRenderFunc: a plugin's "eq" would decide every
// template's comparisons, and its "html" or "urlquery" would be the only
// escaping a value piped through it gets (HOST-03).
func TestConfigHost_RefusesTemplateLanguageBuiltins(t *testing.T) {
	names := []string{"and", "call", "html", "index", "slice", "js", "len", "not", "or", "print",
		"printf", "println", "urlquery", "eq", "ge", "gt", "le", "lt", "ne",
		"_html_template_htmlescaper", "_html_template_urlfilter", "_eval_args_"}
	got := map[string][2]error{}
	p := &hgPlugin{configureFn: func(_ context.Context, h collage.ConfigHost) error {
		for _, name := range names {
			got[name] = [2]error{
				h.AddTemplateFunc(name, func(...string) string { return "PLUGIN" }),
				h.AddRenderFunc(name, func(*collage.RenderContext) any { return func(...string) string { return "PLUGIN" } }),
			}
		}
		return nil
	}}
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{
			"home.html": `{{if eq "guest" "admin"}}ADMIN{{else}}guest{{end}} {{"<b>" | html}} <a href="/s?q={{"x&admin=1" | urlquery}}">s</a>`,
		}),
	)
	for _, name := range names {
		for i, err := range got[name] {
			if !errors.Is(err, collage.ErrDuplicateTemplateFunc) {
				t.Errorf("%s (%s) = %v, want ErrDuplicateTemplateFunc", []string{"AddTemplateFunc", "AddRenderFunc"}[i], name, err)
			}
		}
	}
	body := site.Get("/").WantStatus(http.StatusOK).Body
	if !strings.Contains(body, "guest &lt;b&gt; ") || !strings.Contains(body, "q=x%26admin%3D1") {
		t.Errorf("body = %q", body)
	}
}

// The application's own Config.Template.Funcs win over a plugin's
// AddRenderFunc of the same name, as they already did over AddTemplateFunc, and
// the collision is logged once when the application is built (PLUGB-05).
func TestConfigHost_ApplicationFuncBeatsRenderFunc(t *testing.T) {
	p := &hgPlugin{configureFn: func(_ context.Context, h collage.ConfigHost) error {
		return h.AddRenderFunc("brand", func(*collage.RenderContext) any { return func() string { return "PLUGIN" } })
	}}
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"home.html": `{{brand}}`}),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Template.Funcs = map[string]any{"brand": func() string { return "APP" }}
		}),
	)
	for range 2 {
		if body := site.Get("/").WantStatus(http.StatusOK).Body; !strings.Contains(body, "APP") {
			t.Errorf("body = %q, want the application's function", body)
		}
	}
	if n := strings.Count(site.Logs.String(), `func=brand`); n != 1 {
		t.Errorf("the collision was logged %d times, want once:\n%s", n, site.Logs.String())
	}
}

// exportedStrings collects every string and byte slice reachable from v
// through exported fields, map values and slice elements: what a plugin reads
// off a value with reflect alone, without unsafe.
func exportedStrings(v reflect.Value, depth int, out *[]string) {
	if depth > 8 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			exportedStrings(v.Elem(), depth+1, out)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				exportedStrings(v.Field(i), depth+1, out)
			}
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			exportedStrings(it.Key(), depth+1, out)
			exportedStrings(it.Value(), depth+1, out)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			*out = append(*out, string(v.Bytes()))
			return
		}
		for i := range v.Len() {
			exportedStrings(v.Index(i), depth+1, out)
		}
	case reflect.String:
		*out = append(*out, v.String())
	}
}

// Neither host a plugin receives carries another plugin's configuration in a
// field reflect can read: each holds its own plugin's section only, unexported
// (HOST-08). The plugin still reads its own through PluginConfig.
func TestHost_NoOtherPluginsConfig(t *testing.T) {
	var seen []string
	type own struct{ Mine string }
	var mine own
	spy := &hgPlugin{name: "test/spy",
		configureFn: func(_ context.Context, h collage.ConfigHost) error {
			exportedStrings(reflect.ValueOf(h), 0, &seen)
			return nil
		},
		initFn: func(_ context.Context, h collage.Host) error {
			exportedStrings(reflect.ValueOf(h), 0, &seen)
			var err error
			mine, err = collage.PluginConfig(h, own{})
			return err
		},
	}
	collagetest.NewSite(t,
		collagetest.WithPlugins(spy, &hgPlugin{name: "test/victim"}),
		collagetest.WithPluginConfig("test/spy", `{"Mine":"spy-own"}`),
		collagetest.WithPluginConfig("test/victim", `{"Secret":"victim-secret"}`),
	)
	for _, s := range seen {
		if strings.Contains(s, "victim") || strings.Contains(s, "spy-own") {
			t.Errorf("a host exposes configuration through an exported field: %q", s)
		}
	}
	if mine.Mine != "spy-own" {
		t.Errorf("PluginConfig read %+v", mine)
	}
}

// A request under a plugin's Host.Handle prefix runs inside the middleware
// chain, so its body is bounded at MaxBodyBytes before the middleware reads
// it, as for a page; the application's own App.Handle, WithMiddleware or not,
// keeps its documented unbounded body (HOST-10).
func TestHost_HandleBodyBoundedBeforeMiddleware(t *testing.T) {
	var mu sync.Mutex
	read := map[string]int{}
	reader := &hgPlugin{name: "test/reader", initFn: func(_ context.Context, h collage.Host) error {
		return h.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					read[r.URL.Path] = len(b)
					mu.Unlock()
					r.Body = io.NopCloser(bytes.NewReader(b))
				}
				next.ServeHTTP(w, r)
			})
		})
	}}
	noContent := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	stream := &hgPlugin{name: "test/stream", initFn: func(_ context.Context, h collage.Host) error {
		return h.Handle("/_stream/", noContent)
	}}
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(reader, stream),
		collagetest.WithConfig(func(c *collage.Config) { c.Server.MaxBodyBytes = 1 << 10 }),
		collagetest.WithSetup(func(app *collage.App) error {
			return app.Handle("/api/", noContent, collage.WithMiddleware())
		}),
	)
	body := strings.Repeat("A", 1<<16)
	for _, path := range []string{"/_stream/x", "/api/x"} {
		req := site.Request(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/octet-stream")
		site.Do(req)
	}
	if n := read["/_stream/x"]; n > 1<<10 {
		t.Errorf("middleware read %d bytes under a plugin's Handle prefix, limit 1024", n)
	}
	if n := read["/api/x"]; n != 1<<16 {
		t.Errorf("middleware read %d bytes under App.Handle WithMiddleware, want the whole body", n)
	}
}

// RegisterCommand refuses a name it could never run: one the dispatcher
// answers itself ("collage-check", and every "collage-" name), one that is not
// a single shell word, and, from a plugin, one of the collage binary's own
// commands. After Start it is ErrAppStarted, as every registrar is (HOST-04).
func TestRegisterCommand_RefusesUnreachableNames(t *testing.T) {
	run := func(context.Context, []string) error { return nil }
	got := map[string]error{}
	var kept collage.Host
	p := &hgPlugin{initFn: func(_ context.Context, h collage.Host) error {
		kept = h
		for _, name := range []string{"collage-check", "collage-inspect", "collage-routes", " seed", "-h", "a b", "x\ny", "build", "serve", "seed-db"} {
			got[name] = h.RegisterCommand(collage.Command{Name: name, Run: run})
		}
		return nil
	}}
	var appBuild error
	site := collagetest.NewSite(t, collagetest.WithPlugins(p), collagetest.WithSetup(func(app *collage.App) error {
		appBuild = app.RegisterCommand(collage.Command{Name: "build", Run: run})
		return nil
	}))
	for _, name := range []string{"collage-check", "collage-inspect", "collage-routes", "build", "serve"} {
		if !errors.Is(got[name], collage.ErrDuplicateCommand) {
			t.Errorf("RegisterCommand(%q) = %v, want ErrDuplicateCommand", name, got[name])
		}
	}
	for _, name := range []string{" seed", "-h", "a b", "x\ny"} {
		if !errors.Is(got[name], collage.ErrInvalidCommandName) {
			t.Errorf("RegisterCommand(%q) = %v, want ErrInvalidCommandName", name, got[name])
		}
	}
	if got["seed-db"] != nil {
		t.Errorf("RegisterCommand(seed-db) = %v", got["seed-db"])
	}
	if appBuild != nil {
		t.Errorf("the application's own command named build = %v; it runs as go run . build", appBuild)
	}
	if err := kept.RegisterCommand(collage.Command{Name: "late", Run: run}); !errors.Is(err, collage.ErrAppStarted) {
		t.Errorf("RegisterCommand after Start = %v, want ErrAppStarted", err)
	}
	if err := site.App.RegisterCommand(collage.Command{Name: "later", Run: run}); !errors.Is(err, collage.ErrAppStarted) {
		t.Errorf("App.RegisterCommand after Start = %v, want ErrAppStarted", err)
	}
}

// invalidateWatcher is a CacheInvalidateHook that fails as told, or counts.
type invalidateWatcher struct {
	hgPlugin
	fail  func() error
	calls int
}

func (p *invalidateWatcher) OnCacheInvalidate(context.Context, *collage.CacheInvalidateEvent) error {
	p.calls++
	if p.fail != nil {
		return p.fail()
	}
	return nil
}

// Every CacheInvalidateHook hears of an invalidation, even after one before it
// failed or panicked; InvalidateTags returns their failures joined (HOOKS-04).
func TestInvalidateTags_DispatchSurvivesFailingHooks(t *testing.T) {
	panics := &invalidateWatcher{hgPlugin: hgPlugin{name: "test/panics"}, fail: func() error { panic("cdn client nil") }}
	fails := &invalidateWatcher{hgPlugin: hgPlugin{name: "test/fails"}, fail: func() error { return errors.New("purge refused") }}
	last := &invalidateWatcher{hgPlugin: hgPlugin{name: "test/last"}}
	site := collagetest.NewSite(t, collagetest.WithPlugins(panics, fails, last))
	err := site.App.InvalidateTags(context.Background(), "post:1")
	if last.calls != 1 || fails.calls != 1 {
		t.Errorf("hooks after a failing one were called %d and %d times, want 1", fails.calls, last.calls)
	}
	if err == nil || !strings.Contains(err.Error(), "cdn client nil") || !strings.Contains(err.Error(), "purge refused") {
		t.Errorf("InvalidateTags = %v, want both failures", err)
	}
}
