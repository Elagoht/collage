package collage

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestConfig_ApplyDefaults(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()

	want := Config{
		Server: ServerConfig{
			Host:            "localhost",
			Port:            3000,
			ReadTimeout:     15 * time.Second,
			WriteTimeout:    30 * time.Second,
			IdleTimeout:     60 * time.Second,
			ShutdownTimeout: 10 * time.Second,
		},
		Template: TemplateConfig{
			Root:      "./templates",
			Extension: ".html",
			Timeout:   5 * time.Second,
		},
		Cache: CacheConfig{
			DefaultTTL:    5 * time.Minute,
			MaxEntries:    10000,
			MaxKeysPerTag: 10000,
		},
		Locale: LocaleConfig{
			Default:    "en",
			Supported:  []string{"en"},
			CookieName: "locale",
		},
	}

	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("ApplyDefaults() = %+v, want %+v", cfg, want)
	}
}

func TestConfig_ApplyDefaults_Idempotent(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	once := cfg

	cfg.ApplyDefaults()

	if !reflect.DeepEqual(cfg, once) {
		t.Fatalf("second ApplyDefaults() changed the config: got %+v, want %+v", cfg, once)
	}
}

func TestConfig_ApplyDefaults_EnabledCacheGetsMemoryType(t *testing.T) {
	cfg := Config{Cache: CacheConfig{Enabled: true}}
	cfg.ApplyDefaults()

	if cfg.Cache.Type != "memory" {
		t.Errorf("Cache.Type = %q, want %q", cfg.Cache.Type, "memory")
	}
}

func TestConfig_ApplyDefaults_DisabledCacheLeavesTypeEmpty(t *testing.T) {
	cfg := Config{Cache: CacheConfig{Enabled: false}}
	cfg.ApplyDefaults()

	if cfg.Cache.Type != "" {
		t.Errorf("Cache.Type = %q, want empty (caching disabled)", cfg.Cache.Type)
	}
}

func TestConfig_ApplyDefaults_PreservesExplicitValues(t *testing.T) {
	cfg := Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8080},
		Locale: LocaleConfig{Default: "tr", Supported: []string{"tr", "en"}},
	}
	cfg.ApplyDefaults()

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Server.Host = %q, want %q (explicit value preserved)", cfg.Server.Host, "0.0.0.0")
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080 (explicit value preserved)", cfg.Server.Port)
	}
	if cfg.Locale.Default != "tr" {
		t.Errorf("Locale.Default = %q, want %q (explicit value preserved)", cfg.Locale.Default, "tr")
	}
	if !reflect.DeepEqual(cfg.Locale.Supported, []string{"tr", "en"}) {
		t.Errorf("Locale.Supported = %v, want [tr en] (explicit value preserved)", cfg.Locale.Supported)
	}
}

// TestConfig_ApplyDefaults_PreservesOptOuts is the point of inverting LocaleConfig's
// booleans and redefining CacheConfig.MaxEntries: a caller who explicitly opts out of
// every locale source and asks for an unlimited cache must have that survive
// ApplyDefaults, not get silently reverted because the opted-out value happens to
// look like a zero value.
func TestConfig_ApplyDefaults_PreservesOptOuts(t *testing.T) {
	cfg := Config{
		Locale: LocaleConfig{
			Default:             "en",
			Supported:           []string{"en"},
			DisablePathLocale:   true,
			DisableHeaderLocale: true,
			DisableCookieLocale: true,
		},
		Cache: CacheConfig{MaxEntries: -1},
	}

	cfg.ApplyDefaults()

	if !cfg.Locale.DisablePathLocale {
		t.Error("ApplyDefaults cleared DisablePathLocale")
	}
	if !cfg.Locale.DisableHeaderLocale {
		t.Error("ApplyDefaults cleared DisableHeaderLocale")
	}
	if !cfg.Locale.DisableCookieLocale {
		t.Error("ApplyDefaults cleared DisableCookieLocale")
	}
	if cfg.Cache.MaxEntries != -1 {
		t.Errorf("Cache.MaxEntries = %d, want -1 (unlimited, unchanged)", cfg.Cache.MaxEntries)
	}
}

// TestConfig_MinimalAppExample mirrors the Config literal from
// docs/spec/usage-examples.md's "Minimal application" example verbatim, proving it
// still compiles against this package's field names and passes ApplyDefaults and
// Validate unchanged.
func TestConfig_MinimalAppExample(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			Host: "localhost",
			Port: 3000,
		},
		Template: TemplateConfig{
			Root:      "./templates",
			Extension: ".html",
			DevMode:   true,
		},
		Cache: CacheConfig{
			Enabled:    true,
			Type:       "memory",
			DefaultTTL: 5 * time.Minute,
		},
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if !cfg.IsDevMode() {
		t.Error("IsDevMode() = false, want true (Template.DevMode was set)")
	}
}

func validConfig() Config {
	cfg := Config{
		Locale: LocaleConfig{Default: "en", Supported: []string{"en"}},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr error
	}{
		{
			name:    "valid config",
			mutate:  func(c *Config) {},
			wantErr: nil,
		},
		{
			name:    "port zero",
			mutate:  func(c *Config) { c.Server.Port = 0 },
			wantErr: ErrInvalidPort,
		},
		{
			name:    "port too large",
			mutate:  func(c *Config) { c.Server.Port = 70000 },
			wantErr: ErrInvalidPort,
		},
		{
			name:    "empty template root",
			mutate:  func(c *Config) { c.Template.Root = "" },
			wantErr: ErrEmptyTemplateRoot,
		},
		{
			name:    "enabled cache with unsupported type",
			mutate:  func(c *Config) { c.Cache.Enabled = true; c.Cache.Type = "redis" },
			wantErr: ErrInvalidCacheType,
		},
		{
			name:    "empty default locale",
			mutate:  func(c *Config) { c.Locale.Default = "" },
			wantErr: ErrEmptyLocaleDefault,
		},
		{
			name:    "default locale not supported",
			mutate:  func(c *Config) { c.Locale.Supported = []string{"tr"} },
			wantErr: ErrLocaleDefaultNotSupported,
		},
		{
			name:    "negative read timeout",
			mutate:  func(c *Config) { c.Server.ReadTimeout = -1 },
			wantErr: ErrNegativeDuration,
		},
		{
			name:    "negative write timeout",
			mutate:  func(c *Config) { c.Server.WriteTimeout = -1 },
			wantErr: ErrNegativeDuration,
		},
		{
			name:    "negative idle timeout",
			mutate:  func(c *Config) { c.Server.IdleTimeout = -1 },
			wantErr: ErrNegativeDuration,
		},
		{
			name:    "negative shutdown timeout",
			mutate:  func(c *Config) { c.Server.ShutdownTimeout = -1 },
			wantErr: ErrNegativeDuration,
		},
		{
			name:    "negative template timeout",
			mutate:  func(c *Config) { c.Template.Timeout = -1 },
			wantErr: ErrNegativeDuration,
		},
		{
			name:    "negative cache default ttl",
			mutate:  func(c *Config) { c.Cache.DefaultTTL = -1 },
			wantErr: ErrNegativeDuration,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_IsDevMode(t *testing.T) {
	tests := []struct {
		name        string
		devMode     bool
		templateDev bool
		wantDevMode bool
	}{
		{"both false", false, false, false},
		{"config only", true, false, true},
		{"template only", false, true, true},
		{"both true", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{DevMode: tt.devMode, Template: TemplateConfig{DevMode: tt.templateDev}}
			if got := cfg.IsDevMode(); got != tt.wantDevMode {
				t.Errorf("IsDevMode() = %v, want %v", got, tt.wantDevMode)
			}
		})
	}
}
