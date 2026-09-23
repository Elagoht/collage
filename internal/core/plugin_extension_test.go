package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
	"os"
	"time"
)

// extPlugin is a plugin that uses every capability the extension adds. It is one
// type rather than several because the point is that a single plugin can reach all
// of them, and because a plugin that implements Configurer *and* Plugin is the
// arrangement most likely to be wired up wrong.
type extPlugin struct {
	name string

	// what Configure captured
	configured   bool
	configDevOK  bool
	configErr    error
	settings     extSettings
	wrapCalls    int
	addFuncErr   error
	addFuncTwice error

	// what Init captured
	inited      bool
	initCfg     extSettings
	registerErr error
}

type extSettings struct {
	Greeting string `json:"greeting"`
	Enabled  bool   `json:"enabled"`
}

func (p *extPlugin) Name() string    { return p.name }
func (p *extPlugin) Version() string { return "1.0.0" }

func (p *extPlugin) Configure(_ context.Context, host plugin.ConfigHost) error {
	p.configured = true
	p.configDevOK = host.Logger() != nil

	// Defaults in, overlaid by whatever the application supplied.
	p.settings = extSettings{Greeting: "default"}
	p.configErr = host.Config(&p.settings)

	p.addFuncErr = host.AddTemplateFunc("shout", func(s string) string { return strings.ToUpper(s) })
	p.addFuncTwice = host.AddTemplateFunc("shout", func(s string) string { return s })

	host.WrapMount(func(inner fs.FS) fs.FS {
		p.wrapCalls++
		return inner
	})
	return nil
}

func (p *extPlugin) Init(_ context.Context, host plugin.Host) error {
	p.inited = true
	p.initCfg = extSettings{Greeting: "default"}
	_ = host.Config(&p.initCfg)

	p.registerErr = host.RegisterDocument(&types.Document{
		Name:        "plugin-doc",
		ContentType: "text/plain; charset=utf-8",
		Paths:       map[string]string{"en": "/plugin.txt"},
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return []byte("from the plugin\n"), nil, nil
		},
	})
	if p.registerErr != nil {
		return p.registerErr
	}
	return host.Mount("/plugin-assets/", fstest.MapFS{
		"x.txt": &fstest.MapFile{Data: []byte("mounted by a plugin")},
	})
}

func (p *extPlugin) Shutdown(context.Context) error { return nil }

func TestPlugin_ConfigureRunsBeforeTemplatesAndCanAddFunctions(t *testing.T) {
	p := &extPlugin{name: "acme/ext"}
	app := newTestApp(t, func(cfg *Config) {
		cfg.Plugins = []plugin.Plugin{p}
		cfg.PluginConfig = map[string]json.RawMessage{
			"acme/ext": json.RawMessage(`{"greeting":"hello","enabled":true}`),
		}
	})

	if !p.configured {
		t.Fatal("Configure never ran")
	}
	if p.configErr != nil {
		t.Fatalf("Config() = %v, want nil", p.configErr)
	}
	if p.settings.Greeting != "hello" || !p.settings.Enabled {
		t.Errorf("settings = %+v, want the application's section decoded over the defaults", p.settings)
	}
	if p.addFuncErr != nil {
		t.Errorf("AddTemplateFunc = %v, want nil", p.addFuncErr)
	}
	if !errors.Is(p.addFuncTwice, plugin.ErrDuplicateTemplateFunc) {
		t.Errorf("second AddTemplateFunc = %v, want ErrDuplicateTemplateFunc — two plugins overwriting each other's functions is a bug nobody would find from the output", p.addFuncTwice)
	}
	if app == nil {
		t.Fatal("New returned no application")
	}
}

func TestPlugin_ConfigDefaultsSurviveAnAbsentSection(t *testing.T) {
	// A plugin with no section keeps what it passed in. Zeroing it instead would
	// make "not configured" indistinguishable from "configured to the zero value".
	p := &extPlugin{name: "acme/ext"}
	newTestApp(t, func(cfg *Config) { cfg.Plugins = []plugin.Plugin{p} })

	if p.settings.Greeting != "default" {
		t.Errorf("greeting = %q, want the default the plugin passed in", p.settings.Greeting)
	}
}

func TestPlugin_UnknownConfigKeyIsAStartupError(t *testing.T) {
	// The typo case. Ignoring it leaves the operator certain a plugin was
	// configured while it ran on defaults.
	//
	// Reported at Start rather than at New, because RegisterPlugin can add a plugin
	// after New: checking earlier would call a key unknown that a later registration
	// was about to claim.
	app := newTestApp(t, func(cfg *Config) {
		cfg.Plugins = []plugin.Plugin{&extPlugin{name: "acme/ext"}}
		cfg.PluginConfig = map[string]json.RawMessage{
			"acme/exd": json.RawMessage(`{}`),
		}
	})

	if err := app.Start(); !errors.Is(err, plugin.ErrUnknownPluginConfig) {
		t.Fatalf("Start = %v, want ErrUnknownPluginConfig", err)
	}
}

func TestPlugin_ConfigForALateRegisteredPluginIsNotUnknown(t *testing.T) {
	// A plugin added through RegisterPlugin is as configurable as one supplied in
	// Config.Plugins. It was not while the key check ran in New.
	p := &configInInitPlugin{}
	app := newTestApp(t, func(cfg *Config) {
		cfg.PluginConfig = map[string]json.RawMessage{
			"acme/late": json.RawMessage(`{"greeting":"configured"}`),
		}
	})
	if err := app.RegisterPlugin(p); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p.settings.Greeting != "configured" {
		t.Errorf("greeting = %q, want %q", p.settings.Greeting, "configured")
	}
}

func TestPlugin_ConfigurerRegisteredLateIsRefused(t *testing.T) {
	// Silently skipping Configure would leave a plugin that registered a template
	// function wondering why no template can call it.
	app := newTestApp(t, nil)

	err := app.RegisterPlugin(&extPlugin{name: "acme/ext"})
	if !errors.Is(err, ErrConfigurerRegisteredLate) {
		t.Fatalf("RegisterPlugin = %v, want ErrConfigurerRegisteredLate", err)
	}
}

func TestPlugin_ContributesADocumentAndAMount(t *testing.T) {
	p := &extPlugin{name: "acme/ext"}
	app := newTestApp(t, func(cfg *Config) { cfg.Plugins = []plugin.Plugin{p} })

	handler := app.Handler()
	if !p.inited {
		t.Fatal("Init never ran")
	}
	if p.registerErr != nil {
		t.Fatalf("RegisterDocument through Host = %v", p.registerErr)
	}

	for _, tc := range []struct{ path, want string }{
		{"/plugin.txt", "from the plugin"},
		{"/plugin-assets/x.txt", "mounted by a plugin"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("GET %s body = %q, want it to contain %q", tc.path, rec.Body.String(), tc.want)
		}
	}
}

func TestPlugin_MountWrapperWrapsEveryFilesystem(t *testing.T) {
	p := &extPlugin{name: "acme/ext"}
	app := newTestApp(t, func(cfg *Config) { cfg.Plugins = []plugin.Plugin{p} })

	if err := app.Mount("/static/", fstest.MapFS{"a.css": &fstest.MapFile{Data: []byte("a{}")}}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.Handler()

	// The application's mount and the one the plugin registers from Init.
	if p.wrapCalls != 2 {
		t.Errorf("wrapper ran %d times, want 2 — every mounted filesystem, the plugin's own included", p.wrapCalls)
	}
}

// rewritingPlugin post-processes output the way a minifier or an image optimiser
// does, and counts the hooks it was given.
type rewritingPlugin struct {
	beforeRender    int
	afterRender     int
	documentRendere int
}

func (*rewritingPlugin) Name() string                            { return "acme/rewrite" }
func (*rewritingPlugin) Version() string                         { return "1.0.0" }
func (*rewritingPlugin) Init(context.Context, plugin.Host) error { return nil }
func (*rewritingPlugin) Shutdown(context.Context) error          { return nil }

func (p *rewritingPlugin) OnBeforeRender(_ context.Context, _ *plugin.BeforeRenderEvent) error {
	p.beforeRender++
	return nil
}

func (p *rewritingPlugin) OnAfterRender(_ context.Context, ev *plugin.AfterRenderEvent) error {
	p.afterRender++
	ev.HTML = append([]byte("<!--rewritten-->"), ev.HTML...)
	return nil
}

func (p *rewritingPlugin) OnDocumentRendered(_ context.Context, ev *plugin.DocumentRenderedEvent) error {
	p.documentRendere++
	ev.Body = append([]byte("rewritten\n"), ev.Body...)
	return nil
}

// TestRenderPath_RunsThePostProcessingHooks is about a divergence rather than a
// missing feature.
//
// RenderPath is what a static build renders through, and it used to skip the plugin
// hooks entirely — those are dispatched by the HTTP handler. So a built site was not
// what the server served: unminified where the server minified, unannotated where it
// annotated, with image URLs the server had rewritten left pointing at the origin.
// Nothing said so, which is the shape of failure this framework refuses everywhere
// else.
func TestRenderPath_RunsThePostProcessingHooks(t *testing.T) {
	p := &rewritingPlugin{}
	app := newTestApp(t, nil)
	if err := app.RegisterPlugin(p); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	result, err := app.RenderPath(t.Context(), "/", "en", nil)
	if err != nil {
		t.Fatalf("RenderPath: %v", err)
	}

	if p.afterRender != 1 {
		t.Errorf("OnAfterRender ran %d times, want 1 — a build must produce what the server produces", p.afterRender)
	}
	if p.beforeRender != 1 {
		t.Errorf("OnBeforeRender ran %d times, want 1 — a plugin pairing the two would see only half of each render", p.beforeRender)
	}
	if !bytes.HasPrefix(result.HTML, []byte("<!--rewritten-->")) {
		t.Errorf("HTML = %q, want the hook's rewrite to be what RenderPath returns", result.HTML)
	}
}

func TestRenderDocumentPath_RunsTheDocumentHook(t *testing.T) {
	p := &rewritingPlugin{}
	app := newTestApp(t, nil)
	if err := app.RegisterPlugin(p); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if err := app.RegisterDocument(&types.Document{
		Name:        "robots",
		ContentType: "text/plain; charset=utf-8",
		Paths:       map[string]string{"en": "/robots.txt"},
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return []byte("User-agent: *\n"), nil, nil
		},
	}); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	result, err := app.RenderDocumentPath(t.Context(), "/robots.txt", "en", nil)
	if err != nil {
		t.Fatalf("RenderDocumentPath: %v", err)
	}

	if p.documentRendere != 1 {
		t.Errorf("OnDocumentRendered ran %d times, want 1", p.documentRendere)
	}
	if !bytes.HasPrefix(result.Body, []byte("rewritten\n")) {
		t.Errorf("Body = %q, want the hook's rewrite", result.Body)
	}
}

// configInInitPlugin reads its configuration in Init, the way a plugin with nothing
// to contribute at construction time reasonably would.
type configInInitPlugin struct {
	settings extSettings
}

func (*configInInitPlugin) Name() string    { return "acme/late" }
func (*configInInitPlugin) Version() string { return "1.0.0" }

func (p *configInInitPlugin) Init(_ context.Context, host plugin.Host) error {
	p.settings = extSettings{Greeting: "default"}
	return host.Config(&p.settings)
}

func (*configInInitPlugin) Shutdown(context.Context) error { return nil }

// TestRenderPath_RunsPluginInit covers the other half of the build divergence.
//
// Dispatching the render hooks is not enough on its own: a plugin that reads its
// configuration in Init ran on defaults during a static build and configured on the
// server, so one source produced two different sites. RenderPath now goes through
// startup, memoised, so it renders in the state a served render renders in.
func TestRenderPath_RunsPluginInit(t *testing.T) {
	p := &configInInitPlugin{}
	app := newTestApp(t, func(cfg *Config) {
		cfg.PluginConfig = map[string]json.RawMessage{
			"acme/late": json.RawMessage(`{"greeting":"configured"}`),
		}
	})
	if err := app.RegisterPlugin(p); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	// No Start: RenderPath is what a static build calls, and a build does not serve.
	if _, err := app.RenderPath(t.Context(), "/", "en", nil); err != nil {
		t.Fatalf("RenderPath: %v", err)
	}

	if p.settings.Greeting != "configured" {
		t.Errorf("greeting = %q, want %q — the plugin never saw its configuration", p.settings.Greeting, "configured")
	}
}

func TestCache_DiskCacheIsNotUsedInDevMode(t *testing.T) {
	// Development is exactly where the output changes between runs, and nobody
	// bumps a version to save a file. The version guard catches a released build;
	// it does not catch a developer, so dev mode substitutes memory outright.
	dir := t.TempDir()
	app := newTestApp(t, func(cfg *Config) {
		cfg.DevMode = true
		cfg.Cache = CacheConfig{Enabled: true, Type: "disk", Dir: dir, Version: "v1", DefaultTTL: time.Minute}
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := app.RenderPath(t.Context(), "/", "en", nil); err != nil {
		t.Fatalf("RenderPath: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dev mode wrote %d entries to the disk cache directory", len(entries))
	}
}

func TestCache_DiskCacheSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	build := func() *App {
		app := newTestAppWith(t, defaultTemplates(), func(cfg *Config) {
			cfg.Cache = CacheConfig{Enabled: true, Type: "disk", Dir: dir, Version: "v1", DefaultTTL: time.Minute}
		})
		page := newHomePage()
		page.Strategy = types.StrategyIncremental
		page.CacheTTL = time.Minute
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
		return app
	}

	first := build()
	rec := httptest.NewRecorder()
	first.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request = %d: %s", rec.Code, rec.Body.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("nothing was written to the cache directory (%v)", err)
	}

	// A second application over the same directory and version: a restart.
	second := build()
	rec2 := httptest.NewRecorder()
	second.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request = %d", rec2.Code)
	}
	if rec2.Body.String() != rec.Body.String() {
		t.Errorf("the restarted application served different bytes")
	}
	if got, want := rec2.Header().Get("ETag"), rec.Header().Get("ETag"); got != want {
		t.Errorf("ETag = %q, want %q — a restart must not change what a client is holding", got, want)
	}
}

func TestCache_DiskCacheDerivesItsVersion(t *testing.T) {
	// The friendly path: a directory and nothing else. The version comes from a
	// hash of the running executable, which changes exactly when the output might
	// — so nobody has to remember to pass one, and nobody can pass a stale one.
	dir := t.TempDir()
	build := func() *App {
		app := newTestAppWith(t, defaultTemplates(), func(cfg *Config) {
			cfg.Cache = CacheConfig{Enabled: true, Type: "disk", Dir: dir, DefaultTTL: time.Minute}
			// Set for the same reason a deployment sets one. A generated key
			// differs every run, and the forgery marker derived from it is part
			// of the cache namespace, so without one these two runs would not
			// share a directory — which is the thing this test is about.
			cfg.Security.CSRFKey = []byte("a stable key for this test")
		})
		page := newHomePage()
		page.Strategy = types.StrategyIncremental
		page.CacheTTL = time.Minute
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage: %v", err)
		}
		return app
	}

	rec := httptest.NewRecorder()
	build().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request = %d: %s", rec.Code, rec.Body.String())
	}

	// One version directory, named for something rather than empty.
	versions, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(versions) != 1 || !versions[0].IsDir() || versions[0].Name() == "" {
		t.Fatalf("cache directory holds %v, want one version subdirectory", versions)
	}

	// And a second run of the same binary finds it: the fingerprint is stable.
	// The key is set for the same reason a deployment sets one — a generated key
	// differs every run, and the forgery marker it produces is part of the cache
	// namespace, so without one this run and the last would not share a directory.
	rec2 := httptest.NewRecorder()
	build().Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if got, want := rec2.Header().Get("ETag"), rec.Header().Get("ETag"); got != want {
		t.Errorf("ETag = %q, want %q — the derived version is not stable across runs", got, want)
	}
	if versions, _ := os.ReadDir(dir); len(versions) != 1 {
		t.Errorf("a second run made %d version directories, want 1", len(versions))
	}
}

// hoistingPlugin contributes to the page the way a structured-data or preload
// plugin does: from BeforeRender, before any fragment has run.
type hoistingPlugin struct{ ran bool }

func (*hoistingPlugin) Name() string                            { return "acme/hoist" }
func (*hoistingPlugin) Version() string                         { return "1.0.0" }
func (*hoistingPlugin) Init(context.Context, plugin.Host) error { return nil }
func (*hoistingPlugin) Shutdown(context.Context) error          { return nil }

func (p *hoistingPlugin) OnBeforeRender(_ context.Context, ev *plugin.BeforeRenderEvent) error {
	p.ran = true
	ev.Context.Hoist("head", "plugin", `<meta name="by" content="acme">`)
	return nil
}

// TestPlugin_CanHoistFromBeforeRender is why BeforeRenderEvent carries the render
// context and why the hook is dispatched after it is built.
//
// A plugin contributing to the page has to declare before the tree renders: the
// markers are resolved when it finishes, so by AfterRender the only thing left is
// to splice the finished HTML — which is what having a mechanism was meant to stop.
func TestPlugin_CanHoistFromBeforeRender(t *testing.T) {
	p := &hoistingPlugin{}
	app := newTestAppWith(t, map[string]string{
		"layout.html": `<html><head>{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`,
		"home.html":   `<h1>Welcome Home</h1>`,
	}, nil)
	if err := app.RegisterPlugin(p); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	layout := &types.Fragment{
		Name: "layout", TemplatePath: "layout.html",
		Slots: map[string]*types.SlotDefinition{
			types.DefaultContentSlot: {Name: types.DefaultContentSlot, Required: true},
		},
	}
	content := &types.Fragment{Name: "home-content", TemplatePath: "home.html"}
	page := &types.Page{
		Name: "home", LayoutFragment: layout, ContentFragment: content,
		Paths: map[string]string{"en": "/"},
	}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	result, err := app.RenderPath(t.Context(), "/", "en", nil)
	if err != nil {
		t.Fatalf("RenderPath: %v", err)
	}
	if !p.ran {
		t.Fatal("OnBeforeRender never ran")
	}
	if !bytes.Contains(result.HTML, []byte(`<meta name="by" content="acme">`)) {
		t.Errorf("the plugin's declaration did not reach the head:\n%s", result.HTML)
	}
}
