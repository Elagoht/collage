package collage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// templateRoot writes the minimal template set the tests below render and returns
// its directory.
func templateRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"layouts/default.html": `<!doctype html><main>{{slot "content"}}</main>`,
		"pages/home.html":      `<h1>Welcome Home</h1>`,
	}
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

// TestNew_ServesThroughThePublicAPI builds an application the way the
// specification's minimal example does — public Config, public builders, public
// App — and serves a request through it. It is the check that the public surface
// and the internal one are actually connected, which type aliases alone do not
// guarantee.
func TestNew_ServesThroughThePublicAPI(t *testing.T) {
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: templateRoot(t)},
		Cache:    CacheConfig{Enabled: true, Type: "memory", DefaultTTL: 5 * time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	layout := NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		Build()
	content := NewFragment("home-content", "pages/home.html").Build()
	page := NewPage("home").
		WithLayout(layout).
		WithContent(content).
		WithPath("en", "/").
		Incremental(5 * time.Minute).
		WithDependency("homepage").
		Build()

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Welcome Home") {
		t.Fatalf("body = %q, want the rendered page", body)
	}

	if _, ok := app.Page("home"); !ok {
		t.Fatal("Page(home) not found through the public App")
	}
}

// TestNew_AppliesDefaults: New defaults the config in place, so the caller can read
// back exactly what the application was built with.
func TestNew_AppliesDefaults(t *testing.T) {
	cfg := &Config{Template: TemplateConfig{Root: templateRoot(t)}}
	if _, err := New(cfg); err != nil {
		t.Fatalf("New: %v", err)
	}

	if cfg.Server.Port != 3000 {
		t.Fatalf("Server.Port = %d, want the default 3000", cfg.Server.Port)
	}
	if cfg.Locale.Default != "en" {
		t.Fatalf("Locale.Default = %q, want the default en", cfg.Locale.Default)
	}
	if cfg.Server.ShutdownTimeout != 10*time.Second {
		t.Fatalf("Server.ShutdownTimeout = %v, want the default 10s", cfg.Server.ShutdownTimeout)
	}
}

// TestNew_Rejections covers every way New refuses to build an application.
func TestNew_Rejections(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		if _, err := New(nil); !errors.Is(err, ErrNilConfig) {
			t.Fatalf("New(nil) = %v, want ErrNilConfig", err)
		}
	})

	t.Run("invalid config", func(t *testing.T) {
		cfg := &Config{
			Server:   ServerConfig{Port: 70000},
			Template: TemplateConfig{Root: templateRoot(t)},
		}
		if _, err := New(cfg); !errors.Is(err, ErrInvalidPort) {
			t.Fatalf("New = %v, want ErrInvalidPort", err)
		}
	})

	t.Run("unsupported cache type", func(t *testing.T) {
		cfg := &Config{
			Template: TemplateConfig{Root: templateRoot(t)},
			Cache:    CacheConfig{Enabled: true, Type: "redis"},
		}
		// Config.Validate rejects it before the conversion ever happens, which is
		// why ErrUnsupportedCache is the internal backstop and not this error.
		if _, err := New(cfg); !errors.Is(err, ErrInvalidCacheType) {
			t.Fatalf("New = %v, want ErrInvalidCacheType", err)
		}
	})

	t.Run("missing template root", func(t *testing.T) {
		cfg := &Config{Template: TemplateConfig{Root: filepath.Join(t.TempDir(), "nope")}}
		if _, err := New(cfg); err == nil {
			t.Fatal("New over a missing template root = nil, want an error")
		}
	})
}

// TestToCoreConfig_CarriesEveryField sets every field of the public Config to a
// value distinguishable from its zero value and checks each one arrives in the
// internal mirror. The two structs are kept aligned by hand — no reflection is
// allowed here — so this test is what makes a field that was added to one and
// forgotten in the conversion visible.
func TestToCoreConfig_CarriesEveryField(t *testing.T) {
	metrics := &countingMetrics{}
	tracer := inertTracer{}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &Config{
		DevMode: true,
		Logger:  logger,
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			ReadTimeout:     time.Second,
			WriteTimeout:    2 * time.Second,
			IdleTimeout:     3 * time.Second,
			ShutdownTimeout: 4 * time.Second,
		},
		Template: TemplateConfig{
			Root:      "/tmp/templates",
			Extension: ".gohtml",
			DevMode:   true,
			Timeout:   6 * time.Second,
		},
		Cache: CacheConfig{
			Enabled:    true,
			Type:       "memory",
			DefaultTTL: 7 * time.Second,
			MaxEntries: 42,
		},
		Locale: LocaleConfig{
			Default:             "tr",
			Supported:           []string{"tr", "en"},
			DisablePathLocale:   true,
			DisableHeaderLocale: true,
			CookieName:          "lang",
			DisableCookieLocale: true,
		},
		Observability: ObservabilityConfig{Metrics: metrics, Tracer: tracer},
	}

	core := toCoreConfig(cfg)

	if !core.DevMode {
		t.Error("DevMode did not carry over")
	}
	if core.Logger != logger {
		t.Error("Logger did not carry over")
	}
	if core.Server.Host != "0.0.0.0" || core.Server.Port != 8080 {
		t.Errorf("Server address = %s:%d, want 0.0.0.0:8080", core.Server.Host, core.Server.Port)
	}
	if core.Server.ReadTimeout != time.Second {
		t.Errorf("Server.ReadTimeout = %v, want 1s", core.Server.ReadTimeout)
	}
	if core.Server.WriteTimeout != 2*time.Second {
		t.Errorf("Server.WriteTimeout = %v, want 2s", core.Server.WriteTimeout)
	}
	if core.Server.IdleTimeout != 3*time.Second {
		t.Errorf("Server.IdleTimeout = %v, want 3s", core.Server.IdleTimeout)
	}
	if core.Server.ShutdownTimeout != 4*time.Second {
		t.Errorf("Server.ShutdownTimeout = %v, want 4s", core.Server.ShutdownTimeout)
	}
	if core.Template.Root != "/tmp/templates" || core.Template.Extension != ".gohtml" {
		t.Errorf("Template = %+v, want the configured root and extension", core.Template)
	}
	if !core.Template.DevMode || core.Template.Timeout != 6*time.Second {
		t.Errorf("Template = %+v, want dev mode and a 6s timeout", core.Template)
	}
	if !core.Cache.Enabled || core.Cache.Type != "memory" {
		t.Errorf("Cache = %+v, want it enabled and of type memory", core.Cache)
	}
	if core.Cache.DefaultTTL != 7*time.Second || core.Cache.MaxEntries != 42 {
		t.Errorf("Cache = %+v, want a 7s TTL and 42 entries", core.Cache)
	}
	if core.Locale.Default != "tr" || core.Locale.CookieName != "lang" {
		t.Errorf("Locale = %+v, want tr and the lang cookie", core.Locale)
	}
	if len(core.Locale.Supported) != 2 || core.Locale.Supported[0] != "tr" {
		t.Errorf("Locale.Supported = %v, want [tr en]", core.Locale.Supported)
	}
	if !core.Locale.DisablePathLocale || !core.Locale.DisableHeaderLocale || !core.Locale.DisableCookieLocale {
		t.Errorf("Locale = %+v, want every source disabled", core.Locale)
	}
	if core.Observability.Metrics != metrics {
		t.Error("Observability.Metrics did not carry over")
	}
	if core.Observability.Tracer != tracer {
		t.Error("Observability.Tracer did not carry over")
	}
}

// ---------------------------------------------------------------------------
// The plugin surface, implemented from outside
//
// Every identifier below that is not from the standard library is exported by this
// package. That is the point of the test: internal/ is unimportable outside the
// module, so if a hook interface or an event type were reachable only from there,
// an external user could not write a plugin at all — and this file would not
// compile. It imports nothing but the standard library.
// ---------------------------------------------------------------------------

// hookPlugin implements Plugin and every one of the six hooks, recording which ones
// fired and appending a marker to the rendered HTML.
type hookPlugin struct {
	mu    sync.Mutex
	fired map[string]int
}

var (
	_ Plugin              = (*hookPlugin)(nil)
	_ PageResolvedHook    = (*hookPlugin)(nil)
	_ BeforeRenderHook    = (*hookPlugin)(nil)
	_ AfterRenderHook     = (*hookPlugin)(nil)
	_ CacheWriteHook      = (*hookPlugin)(nil)
	_ CacheInvalidateHook = (*hookPlugin)(nil)
	_ ErrorHook           = (*hookPlugin)(nil)
)

// Name identifies the plugin.
func (p *hookPlugin) Name() string { return "hooks" }

// Version reports the plugin's version.
func (p *hookPlugin) Version() string { return "1.0.0" }

// Init registers a command, exercising the Host surface.
func (p *hookPlugin) Init(_ context.Context, host Host) error {
	p.record("Init")
	return host.RegisterCommand(Command{
		Name: "hooks",
		Run:  func(context.Context, []string) error { return nil },
	})
}

// Shutdown records the call.
func (p *hookPlugin) Shutdown(context.Context) error {
	p.record("Shutdown")
	return nil
}

// OnPageResolved records the call.
func (p *hookPlugin) OnPageResolved(_ context.Context, _ *PageResolvedEvent) error {
	p.record("OnPageResolved")
	return nil
}

// OnBeforeRender records the call.
func (p *hookPlugin) OnBeforeRender(_ context.Context, _ *BeforeRenderEvent) error {
	p.record("OnBeforeRender")
	return nil
}

// OnAfterRender records the call and appends a marker to the page, which is the
// post-processing the hook exists for.
func (p *hookPlugin) OnAfterRender(_ context.Context, ev *AfterRenderEvent) error {
	p.record("OnAfterRender")
	ev.HTML = append(ev.HTML, []byte("<!--plugin-->")...)
	return nil
}

// OnCacheWrite records the call.
func (p *hookPlugin) OnCacheWrite(_ context.Context, _ *CacheWriteEvent) error {
	p.record("OnCacheWrite")
	return nil
}

// OnCacheInvalidate records the call.
func (p *hookPlugin) OnCacheInvalidate(_ context.Context, _ *CacheInvalidateEvent) error {
	p.record("OnCacheInvalidate")
	return nil
}

// OnError records the call.
func (p *hookPlugin) OnError(_ context.Context, _ *ErrorEvent) error {
	p.record("OnError")
	return nil
}

// record counts one call to the named hook.
func (p *hookPlugin) record(hook string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fired == nil {
		p.fired = make(map[string]int)
	}
	p.fired[hook]++
}

// count returns how many times the named hook fired.
func (p *hookPlugin) count(hook string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fired[hook]
}

// TestPlugin_ImplementableFromThePublicPackage registers a plugin that implements
// every hook using only this package's exported types, drives one request, one miss
// and one invalidation through it, and checks each hook fired.
func TestPlugin_ImplementableFromThePublicPackage(t *testing.T) {
	app, err := New(&Config{
		Template: TemplateConfig{Root: templateRoot(t)},
		Cache:    CacheConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hooks := &hookPlugin{}
	if err := app.RegisterPlugin(hooks); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	page := NewPage("home").
		WithLayout(NewFragment("layout", "layouts/default.html").WithSlot("content", true, false).Build()).
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/").
		Static().
		WithDependency("homepage").
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	handler := app.Handler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(recorder.Body.String(), "<!--plugin-->") {
		t.Fatalf("body = %q, want the marker OnAfterRender appended", recorder.Body.String())
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))

	if err := app.InvalidateTags(t.Context(), "homepage"); err != nil {
		t.Fatalf("InvalidateTags: %v", err)
	}
	if err := app.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	for _, hook := range []string{
		"Init", "OnPageResolved", "OnBeforeRender", "OnAfterRender",
		"OnCacheWrite", "OnCacheInvalidate", "OnError", "Shutdown",
	} {
		if got := hooks.count(hook); got == 0 {
			t.Errorf("%s never fired", hook)
		}
	}

	if commands := app.Commands(); len(commands) != 1 || commands[0].Name != "hooks" {
		t.Fatalf("Commands = %v, want the one the plugin registered", commands)
	}
}

// countingMetrics is a Metrics implementation written with nothing but this
// package's exported types, which is what a user bridging the framework into a
// metrics backend has to be able to do.
type countingMetrics struct {
	mu     sync.Mutex
	events int
}

var _ Metrics = (*countingMetrics)(nil)

// RenderDuration records the call.
func (m *countingMetrics) RenderDuration(context.Context, string, time.Duration, bool) { m.bump() }

// FragmentDuration records the call.
func (m *countingMetrics) FragmentDuration(context.Context, string, string, time.Duration, error) {
	m.bump()
}

// CacheEvent records the call.
func (m *countingMetrics) CacheEvent(_ context.Context, event CacheEvent, _ string) {
	if event == CacheHit || event == CacheMiss || event == CacheSet || event == CacheInvalidate {
		m.bump()
	}
}

// HTTPResponse records the call.
func (m *countingMetrics) HTTPResponse(context.Context, int, string, time.Duration) { m.bump() }

// Invalidation records the call.
func (m *countingMetrics) Invalidation(context.Context, []string, int) { m.bump() }

// bump counts one reported event.
func (m *countingMetrics) bump() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events++
}

// total returns how many events were reported.
func (m *countingMetrics) total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events
}

// inertTracer is a Tracer implementation written with nothing but this package's
// exported types. Implementing Tracer requires Span, so it is the check that Span is
// reachable from outside too.
type inertTracer struct{}

var _ Tracer = inertTracer{}

// StartSpan returns ctx unchanged and an inert span.
func (inertTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, inertSpan{}
}

// inertSpan is a Span that does nothing.
type inertSpan struct{}

var _ Span = inertSpan{}

// SetAttribute does nothing.
func (inertSpan) SetAttribute(string, string) {}

// RecordError does nothing.
func (inertSpan) RecordError(error) {}

// End does nothing.
func (inertSpan) End() {}

// TestObservability_ImplementableFromThePublicPackage wires the two
// implementations above into an application and checks they actually receive
// events, so the aliases are not merely present but usable.
func TestObservability_ImplementableFromThePublicPackage(t *testing.T) {
	metrics := &countingMetrics{}
	app, err := New(&Config{
		Template:      TemplateConfig{Root: templateRoot(t)},
		Cache:         CacheConfig{Enabled: true},
		Observability: ObservabilityConfig{Metrics: metrics, Tracer: inertTracer{}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page := NewPage("home").
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/").
		Static().
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	httpRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(httpRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if httpRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", httpRecorder.Code, http.StatusOK)
	}
	if metrics.total() == 0 {
		t.Fatal("the Metrics implementation received nothing")
	}
}
