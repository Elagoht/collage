package collage

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/observability"
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
	metrics := observability.NewRecordingMetrics()
	tracer := observability.NoopTracer{}

	cfg := &Config{
		DevMode: true,
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
