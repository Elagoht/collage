package collage

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"slices"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
)

// ErrInvalidPort is returned when Config.Server.Port is outside the valid TCP port
// range 1..65535.
var ErrInvalidPort = errors.New("collage: invalid port")

// ErrEmptyTemplateRoot is returned when Config.Template names no template source:
// Root is empty and FS is nil.
var ErrEmptyTemplateRoot = errors.New("collage: empty template root")

// ErrInvalidCacheType is returned when Config.Cache.Enabled is true and
// Config.Cache.Type is not a supported cache type.
// ErrEmptyCacheDir is returned when Cache.Type is "disk" and Cache.Dir is empty.
var ErrEmptyCacheDir = cache.ErrEmptyCacheDir

// ErrEmptyCacheVersion is returned when Cache.Type is "disk" and Cache.Version is
// empty. See CacheConfig.Version for why it is required rather than defaulted.
var ErrEmptyCacheVersion = cache.ErrEmptyCacheVersion

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
//
// It is mirrored field for field by internal/core.Config. New applies the defaults
// and the validation here, then converts into that mirror and hands it to core.New,
// which is how internal/core stays free of any dependency on this package. A field
// added here must be added there, and to toCoreConfig, or it will simply be ignored.
type Config struct {
	// DevMode enables development-mode behaviour across the framework. See IsDevMode
	// for the effective value, which also considers Template.DevMode.
	DevMode bool
	// Logger is the structured logger the framework writes through, and the one
	// plugins receive from Host.Logger. A nil Logger means slog.Default().
	//
	// ApplyDefaults deliberately leaves it nil rather than filling in
	// slog.Default(): nil is already unambiguous, and resolving it at construction
	// keeps a Config comparable and free of a pointer the caller never supplied.
	Logger *slog.Logger
	// Server configures the HTTP server.
	Server ServerConfig
	// Template configures template loading and rendering.
	Template TemplateConfig
	// Plugins are registered and configured while the App is built.
	//
	// A plugin implementing Configurer must arrive here rather than through
	// RegisterPlugin: Configure runs before templates are parsed, so that a plugin
	// can contribute a template function, and RegisterPlugin is called after New
	// has already parsed them.
	Plugins []Plugin
	// PluginConfig is each plugin's own configuration, keyed by plugin name, which
	// should read like a module path — "elagoht/minimizer".
	//
	// The framework reads no file and imposes no format: the application fills this
	// however it likes. LoadPluginConfig is a convenience for the common case of a
	// JSON file. A key matching no registered plugin is a startup error
	// (ErrUnknownPluginConfig), because the alternative is an operator certain a
	// plugin was configured while it ran on defaults.
	PluginConfig map[string]json.RawMessage
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
	// FS, when non-nil, is the filesystem templates are loaded from, and Root names
	// a directory within it rather than on disk. Embedding templates this way is
	// what lets a binary run from any working directory:
	//
	//	//go:embed templates
	//	var templates embed.FS
	//
	//	collage.New(collage.Config{Template: collage.TemplateConfig{
	//		FS:   templates,
	//		Root: "templates",
	//	}})
	//
	// DevMode has no useful effect on an embedded filesystem, whose contents are
	// fixed at build time: reloading reparses identical bytes on every request.
	FS fs.FS
	// Root is the directory templates are loaded from, and is stripped from every
	// template name. It is a path on disk when FS is nil, defaulting to
	// "./templates", and a slash-separated path within FS otherwise, where an empty
	// value means the root of FS itself.
	Root string
	// Funcs adds template functions to, and may override entries of, the
	// framework's built-in function map (slot, safeHTML, safeURL, dict, default,
	// upper, lower, title, join, formatTime). It is merged over the built-ins at
	// construction, so an entry here under a built-in name replaces that built-in.
	//
	// It must be set before New: html/template resolves a function name at
	// execution time but can only call a name that was already in the map when the
	// template was parsed, and New is where parsing happens. Adding a name
	// afterwards is not possible, and a template calling an unknown name fails to
	// parse in New rather than at the first request.
	//
	// Overriding "slot" is possible but pointless: the render engine rebinds it per
	// render, so whatever is registered here is never the implementation that runs.
	Funcs template.FuncMap
	// Extension is the file extension appended to template names. Defaults to
	// ".html".
	Extension string
	// DevMode reloads templates from disk on every request instead of caching parsed
	// templates. Its disjunction with Config.DevMode is IsDevMode's effective value.
	DevMode bool
	// Timeout is the default DataHandler timeout used when a fragment sets none.
	// Defaults to 5s.
	//
	// It also bounds every *document* handler, and is the only bound on one:
	// unlike Fragment, Document has no per-route Timeout field, so this value is
	// not merely a default there but the whole budget. Raising it for a slow
	// fragment raises it for every sitemap and feed as well.
	//
	// As everywhere else in the framework, it bounds the context the handler is
	// given, not the handler itself: one that never consults ctx.Done() can still
	// run past it.
	Timeout time.Duration
}

// CacheConfig configures the render output cache.
type CacheConfig struct {
	// Enabled turns caching on. Caching is off by default, and it is the master
	// switch: with Enabled false nothing is cached even if Store is set.
	Enabled bool
	// Store is the cache implementation the framework reads and writes through.
	// A nil Store selects a built-in implementation by Type; a non-nil Store is
	// used as-is and Type is ignored entirely, including by validation.
	//
	// A Store that also implements TaggedCache has its SetTagged called instead of
	// Set, so the entry's dependency tags are indexed by the cache itself as well
	// as by the framework's tracker.
	//
	// Assigning a nil pointer of a concrete type to this field produces a non-nil
	// interface holding nil, which the framework cannot distinguish from a real
	// implementation without reflection, and which will panic on the first lookup.
	// Leave the field unset instead.
	Store Cache
	// Type selects a built-in cache implementation when Store is nil. The only
	// built-in is "memory", which is also what an empty Type defaults to when
	// Enabled is true and Store is nil.
	Type string
	// DefaultTTL is the cache entry lifetime used when a page does not set its own.
	// Defaults to 5m.
	DefaultTTL time.Duration
	// MaxEntries caps the number of cache entries. Zero means "use the default"
	// (ApplyDefaults sets it to 10000); a negative value means unlimited, and is left
	// untouched by ApplyDefaults.
	MaxEntries int
	// Dir is where a "disk" cache stores its entries. Required for that type.
	Dir string
	// Version identifies the build whose rendered output a "disk" cache holds, and
	// is required for that type. Anything that changes when the output could: a
	// git commit, a release tag, a build timestamp.
	//
	// It exists because a disk cache outlives the process that filled it. Without
	// it a new binary serves HTML the old one rendered — a changed template, a
	// changed data handler, and a page nobody can explain. Entries live under a
	// subdirectory named for a hash of this, so a different version reads a
	// different directory and finds nothing; there is no check to forget.
	//
	// A disk cache is never used in development. Config.DevMode or
	// Template.DevMode substitutes an in-memory one and says so, because that is
	// where the output changes between runs and nobody bumps a version to save a
	// file.
	Version string
	// MaxKeysPerTag caps how many cache keys the framework's dependency tracker
	// records under any one tag. Zero means "use the default" (ApplyDefaults sets
	// it to 10000); a negative value means unlimited, and is left untouched by
	// ApplyDefaults.
	//
	// The tracker is what resolves a dependency tag back to the cache keys built
	// from it, and it is written on every cache write — but nothing removes from
	// it when the cache evicts or expires an entry, so a key stays recorded until
	// the tag it was recorded under is invalidated. The cache key carries the
	// request's query string, so any client can mint unlimited distinct keys for
	// one page; without a cap the tracker grows without bound while the cache
	// itself stays at MaxEntries. This is that cap.
	//
	// When a tag is at the cap, recording a new key under it drops the oldest key
	// recorded under that tag. Dropping removes the key from the index only, not
	// from the cache: the entry keeps being served until it expires, but
	// InvalidateTags no longer reaches it. Set this above the number of live cache
	// entries any one tag can plausibly cover, or negative to accept unbounded
	// tracker growth in exchange for never dropping.
	MaxKeysPerTag int
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

	// Only for the disk mode. "./templates" is relative to the working directory,
	// so defaulting it into an FS-backed config would send the loader looking for a
	// "templates" subdirectory inside the embedded filesystem that the caller never
	// asked for.
	if c.Template.Root == "" && c.Template.FS == nil {
		c.Template.Root = "./templates"
	}
	if c.Template.Extension == "" {
		c.Template.Extension = ".html"
	}
	if c.Template.Timeout == 0 {
		c.Template.Timeout = 5 * time.Second
	}

	if c.Cache.Enabled && c.Cache.Store == nil && c.Cache.Type == "" {
		c.Cache.Type = "memory"
	}
	if c.Cache.DefaultTTL == 0 {
		c.Cache.DefaultTTL = 5 * time.Minute
	}
	if c.Cache.MaxEntries == 0 {
		c.Cache.MaxEntries = 10000
	}
	if c.Cache.MaxKeysPerTag == 0 {
		c.Cache.MaxKeysPerTag = 10000
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
// "memory" when Cache.Enabled and Cache.Store is nil (ErrInvalidCacheType; a
// caller-supplied Store makes Type irrelevant), Locale.Default is non-empty
// (ErrEmptyLocaleDefault) and present in Locale.Supported
// (ErrLocaleDefaultNotSupported), and every duration field is not negative
// (ErrNegativeDuration). It returns nil when c is well-formed.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidPort, c.Server.Port)
	}
	if c.Template.Root == "" && c.Template.FS == nil {
		return ErrEmptyTemplateRoot
	}
	if c.Cache.Enabled && c.Cache.Store == nil {
		switch c.Cache.Type {
		case "memory":
		case "disk":
			if c.Cache.Dir == "" {
				return ErrEmptyCacheDir
			}
			if c.Cache.Version == "" {
				return ErrEmptyCacheVersion
			}
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
