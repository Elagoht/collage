package collage

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Elagoht/collage/internal/observability"
)

// ErrInvalidPort is returned when Config.Server.Port is outside the valid TCP port
// range 1..65535.
var ErrInvalidPort = errors.New("collage: invalid port")

// ErrEmptyTemplateRoot is returned when Config.Template.Root is empty.
var ErrEmptyTemplateRoot = errors.New("collage: empty template root")

// ErrInvalidCacheType is returned when Config.Cache.Enabled is true and
// Config.Cache.Type is not a supported cache type.
var ErrInvalidCacheType = errors.New("collage: invalid cache type")

// ErrEmptyLocaleDefault is returned when Config.Locale.Default is empty.
var ErrEmptyLocaleDefault = errors.New("collage: empty default locale")

// ErrLocaleDefaultNotSupported is returned when Config.Locale.Default is not present
// in Config.Locale.Supported.
var ErrLocaleDefaultNotSupported = errors.New("collage: default locale not in supported locales")

// ErrNegativeDuration is returned when a Config duration field is negative. It is
// shared deliberately across all six duration fields Validate checks
// (Server.ReadTimeout, Server.WriteTimeout, Server.IdleTimeout,
// Server.ShutdownTimeout, Template.Timeout, Cache.DefaultTTL): unlike
// types.ErrInvalidTimeout and types.ErrInvalidTTL, which mean genuinely different
// things to a caller (a fragment's data-fetch deadline versus a page's cache
// lifetime), a negative value in any of these six fields is the same failure mode —
// "you passed a negative duration" — differing only in which field tripped. Validate
// names the offending field in the wrapped message (%w: %s); callers that need to
// distinguish which field failed should inspect that message rather than expect a
// per-field sentinel.
var ErrNegativeDuration = errors.New("collage: negative duration")

// Config is the framework's top-level configuration.
type Config struct {
	// DevMode enables development-mode behaviour across the framework. See IsDevMode
	// for the effective value, which also considers Template.DevMode.
	DevMode bool
	// Server configures the HTTP server.
	Server ServerConfig
	// Template configures template loading and rendering.
	Template TemplateConfig
	// Cache configures the render output cache.
	Cache CacheConfig
	// Locale configures locale resolution.
	Locale LocaleConfig
	// Observability configures metrics and tracing.
	Observability ObservabilityConfig
}

// ServerConfig configures the HTTP server.
type ServerConfig struct {
	// Host is the address the server listens on. Defaults to "localhost".
	Host string
	// Port is the TCP port the server listens on. Defaults to 3000.
	Port int
	// ReadTimeout bounds how long reading a request may take. Defaults to 15s.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long writing a response may take. Defaults to 30s.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long a keep-alive connection may sit idle. Defaults to
	// 60s.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight requests.
	// Defaults to 10s.
	ShutdownTimeout time.Duration
}

// TemplateConfig configures template loading and rendering.
type TemplateConfig struct {
	// Root is the directory templates are loaded from. Defaults to "./templates".
	Root string
	// Extension is the file extension appended to template names. Defaults to
	// ".html".
	Extension string
	// DevMode reloads templates from disk on every request instead of caching parsed
	// templates. Its disjunction with Config.DevMode is IsDevMode's effective value.
	DevMode bool
	// Timeout is the default DataHandler timeout used when a fragment sets none.
	// Defaults to 5s.
	Timeout time.Duration
}

// CacheConfig configures the render output cache.
type CacheConfig struct {
	// Enabled turns caching on. Caching is off by default.
	Enabled bool
	// Type selects the cache implementation. Defaults to "memory" when Enabled is
	// true and Type is left empty.
	Type string
	// DefaultTTL is the cache entry lifetime used when a page does not set its own.
	// Defaults to 5m.
	DefaultTTL time.Duration
	// MaxEntries caps the number of cache entries. Zero means "use the default"
	// (ApplyDefaults sets it to 10000); a negative value means unlimited, and is left
	// untouched by ApplyDefaults.
	MaxEntries int
}

// LocaleConfig configures locale resolution. Each locale source is enabled by
// default (the zero value keeps it on); set the matching Disable* field to turn a
// source off. The fields are inverted this way deliberately: a bool documented as
// "default true" can never be turned off through ApplyDefaults, because the zero
// value (false) is indistinguishable from "caller left it unset" — ApplyDefaults
// would flip it back to true every time. Making the zero value the enabled state
// avoids that trap.
type LocaleConfig struct {
	// Default is the locale used when none can be resolved from the request.
	// Defaults to "en".
	Default string
	// Supported lists the locales the application serves. Defaults to a slice
	// containing only Default.
	Supported []string
	// DisablePathLocale turns off resolving the locale from the request path, e.g.
	// /tr/blog/post. The zero value keeps this source enabled.
	DisablePathLocale bool
	// DisableHeaderLocale turns off resolving the locale from the Accept-Language
	// header. The zero value keeps this source enabled.
	DisableHeaderLocale bool
	// CookieName is the name of the cookie the locale is read from. Empty means
	// "locale" (ApplyDefaults fills this in); set DisableCookieLocale to turn this
	// source off entirely regardless of CookieName.
	CookieName string
	// DisableCookieLocale turns off resolving the locale from a cookie. The zero
	// value keeps this source enabled.
	DisableCookieLocale bool
}

// ObservabilityConfig configures metrics and tracing. Both fields are optional: a
// nil Metrics or Tracer is valid and means "use the no-op implementation" — see
// observability.MetricsOrNoop and observability.TracerOrNoop, which consumers use
// so they never have to nil-check these fields themselves.
type ObservabilityConfig struct {
	// Metrics receives framework counters and timings. nil means no-op.
	Metrics observability.Metrics
	// Tracer starts spans around framework operations. nil means no-op.
	Tracer observability.Tracer
}

// ApplyDefaults fills every zero-valued field of c with the framework's default. It
// is idempotent: applying it to an already-defaulted Config changes nothing.
func (c *Config) ApplyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "localhost"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 3000
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 15 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 30 * time.Second
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 60 * time.Second
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 10 * time.Second
	}

	if c.Template.Root == "" {
		c.Template.Root = "./templates"
	}
	if c.Template.Extension == "" {
		c.Template.Extension = ".html"
	}
	if c.Template.Timeout == 0 {
		c.Template.Timeout = 5 * time.Second
	}

	if c.Cache.Enabled && c.Cache.Type == "" {
		c.Cache.Type = "memory"
	}
	if c.Cache.DefaultTTL == 0 {
		c.Cache.DefaultTTL = 5 * time.Minute
	}
	if c.Cache.MaxEntries == 0 {
		c.Cache.MaxEntries = 10000
	}

	if c.Locale.Default == "" {
		c.Locale.Default = "en"
	}
	if len(c.Locale.Supported) == 0 {
		c.Locale.Supported = []string{c.Locale.Default}
	}
	if c.Locale.CookieName == "" {
		c.Locale.CookieName = "locale"
	}
}

// Validate reports whether c is well-formed: Server.Port is in 1..65535
// (ErrInvalidPort), Template.Root is non-empty (ErrEmptyTemplateRoot), Cache.Type is
// "memory" when Cache.Enabled (ErrInvalidCacheType), Locale.Default is non-empty
// (ErrEmptyLocaleDefault) and present in Locale.Supported
// (ErrLocaleDefaultNotSupported), and every duration field is not negative
// (ErrNegativeDuration). It returns nil when c is well-formed.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidPort, c.Server.Port)
	}
	if c.Template.Root == "" {
		return ErrEmptyTemplateRoot
	}
	if c.Cache.Enabled {
		switch c.Cache.Type {
		case "memory":
		default:
			return fmt.Errorf("%w: %q", ErrInvalidCacheType, c.Cache.Type)
		}
	}
	if c.Locale.Default == "" {
		return ErrEmptyLocaleDefault
	}
	if !slices.Contains(c.Locale.Supported, c.Locale.Default) {
		return fmt.Errorf("%w: %q", ErrLocaleDefaultNotSupported, c.Locale.Default)
	}

	durations := []struct {
		name string
		d    time.Duration
	}{
		{"server.read_timeout", c.Server.ReadTimeout},
		{"server.write_timeout", c.Server.WriteTimeout},
		{"server.idle_timeout", c.Server.IdleTimeout},
		{"server.shutdown_timeout", c.Server.ShutdownTimeout},
		{"template.timeout", c.Template.Timeout},
		{"cache.default_ttl", c.Cache.DefaultTTL},
	}
	for _, entry := range durations {
		if entry.d < 0 {
			return fmt.Errorf("%w: %s", ErrNegativeDuration, entry.name)
		}
	}

	return nil
}

// IsDevMode reports the config's effective development-mode flag: c.DevMode or
// c.Template.DevMode. Use this method everywhere development mode is checked, rather
// than reading either field directly, so the two flags cannot drift apart.
func (c *Config) IsDevMode() bool {
	return c.DevMode || c.Template.DevMode
}
