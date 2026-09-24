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

// ErrEmptyCacheDir is returned when Cache.Type is "disk" and Cache.Dir is empty.
var ErrEmptyCacheDir = cache.ErrEmptyCacheDir

// ErrEmptyCacheVersion is the disk cache's refusal to open without a version. New
// never returns it: an empty Cache.Version is derived from the running executable,
// and when that cannot be read the application falls back to an in-memory cache
// and logs why, rather than failing. It is exported so the sentinel has a name
// should that change, and it is not a check a caller needs to make today.
var ErrEmptyCacheVersion = cache.ErrEmptyCacheVersion

// ErrInvalidCacheType is returned when Config.Cache.Enabled is true, no
// Config.Cache.Store is supplied, and Config.Cache.Type is neither "memory" nor
// "disk".
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
	// DevWatch lists directories, besides the templates and the mounts, whose
	// changes reload a development page: content the application reads from disk
	// in development — Markdown pages, JSON data. Ignored outside development.
	DevWatch []string
	// Logger is the structured logger the framework writes through, and the one
	// plugins receive from Host.Logger.
	//
	// A nil Logger means the framework picks. When the output is a terminal and
	// nothing has replaced slog's own default handler, it picks one meant for a
	// person: one line per record, a coloured marker for the level, the time
	// without the date, the attributes dimmed after the message. Anywhere else —
	// a pipe, a file, a CI log — it is slog.Default(), unchanged, so nothing that
	// parses this output has to learn a new format.
	//
	// An application that called slog.SetDefault has chosen a handler, and that
	// choice is honoured: noticing a terminal is not a reason to override a
	// decision somebody made on purpose. Pass a Logger to be certain.
	//
	// ApplyDefaults deliberately leaves it nil rather than filling in
	// slog.Default(): nil is already unambiguous, and resolving it at construction
	// keeps a Config comparable and free of a pointer the caller never supplied.
	Logger *slog.Logger
	// Server configures the HTTP server.
	Server ServerConfig

	// Security configures request-forgery protection.
	Security SecurityConfig
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
	// TrailingSlash makes every page's URL end in "/": "/blog/hello/", and a
	// locale's home "/tr/". Links built by name — pageURL, pageURLIn, localeURL,
	// App.URL — come out that way, and a request for a page without the slash is
	// redirected, 301, to the address with it.
	//
	// Turn it on for a site exported to a static host. The export writes a page as
	// <path>/index.html, and a static host serves that file at "/blog/hello/" and
	// redirects "/blog/hello" to it — so without the slash every canonical link,
	// sitemap entry and internal link points at a redirect.
	//
	// Off, which is the default, a page's URL has no trailing slash and a request
	// with one is redirected to the address without it. Either way each page has
	// one address. Documents are files and keep their paths as written:
	// "/sitemap.xml" in both modes.
	TrailingSlash bool
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
	// MaxBodyBytes bounds an action's request body when the action declares no
	// bound of its own. Defaults to four megabytes; a negative value means
	// unbounded, which is a decision worth making deliberately, because an
	// unbounded body is memory an anonymous caller chooses the size of.
	MaxBodyBytes int64
}

// SecurityConfig configures request-forgery protection.
type SecurityConfig struct {
	// CSRFKey signs forgery tokens. It should be at least 32 random bytes, kept
	// with the application's other secrets, and the same on every instance.
	//
	// An empty key is not an error: one is generated and the application logs that
	// it did. That is right for a first run and wrong to deploy, because a
	// generated key differs in every process — a token issued before a restart is
	// refused after it, and one issued by an instance is refused by the next.
	CSRFKey []byte
	// CSRFCookieName overrides the cookie a token is carried in.
	CSRFCookieName string
	// CSRFFieldName overrides the form field a token is submitted in — the name
	// {{csrfToken}} renders and the verifier reads. Defaults to "_csrf".
	CSRFFieldName string
	// CSRFHeaderName overrides the header a token may be submitted in, which is
	// how a fetch() sends one when there is no form to put a field in.
	CSRFHeaderName string
	// DisableCSRF turns forgery checking off for the whole application. It is for
	// an application with no browser-submitted forms at all.
	DisableCSRF bool
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
	// An embedded filesystem is fixed at build time, so in development — where
	// templates reload on every render — the copy on disk is preferred: when Root
	// also names a directory on disk (relative to the working directory), that
	// directory is read instead of FS, and edits appear without a rebuild. Run from anywhere else and
	// the embedded copy renders, unchanged until the binary is rebuilt. Outside
	// development FS is always what renders.
	FS fs.FS
	// Root is the directory templates are loaded from, and is stripped from every
	// template name. It is a path on disk when FS is nil, defaulting to
	// "./templates", and a slash-separated path within FS otherwise, where an empty
	// value means the root of FS itself.
	Root string
	// Funcs adds template functions to, and may override entries of, the
	// framework's built-in function map (safeHTML, safeURL, dict, default, upper,
	// lower, title, join, formatTime, and the per-render functions below). It is
	// merged over the built-ins, and over any functions plugins contributed, at
	// construction, so an entry here under a built-in name replaces that built-in.
	//
	// It must be set before New: html/template resolves a function name at
	// execution time but can only call a name that was already in the map when the
	// template was parsed, and New is where parsing happens. Adding a name
	// afterwards is not possible, and a template calling an unknown name fails to
	// parse in New rather than at the first request.
	//
	// Overriding a per-render function — slot, hoist, asset, stylesheet,
	// csrfToken, pageURL, pageURLIn, localeURL — is possible but pointless: each
	// needs the render it runs in, so the render engine rebinds all of them on
	// every render, and whatever is registered here is never the implementation
	// that runs.
	Funcs template.FuncMap
	// Extension selects which files under Root are templates: only a file ending in
	// it is parsed. It is not added to or stripped from names — a template's name is
	// its path under Root, extension included, as in "pages/home.html". Includes the
	// leading dot; defaults to ".html".
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
	// Type selects a built-in cache implementation when Store is nil: "memory",
	// which is also what an empty Type defaults to when Enabled is true and Store
	// is nil, or "disk", which keeps entries under Dir across restarts (see Dir and
	// Version).
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
	// Version identifies the build whose rendered output a "disk" cache holds.
	//
	// Leave it empty and it is derived from a hash of the running executable, which
	// changes exactly when the rendered output might: a changed template compiled
	// in, a changed handler, a changed dependency. Two runs of an unchanged program
	// derive the same value, and so does every machine in a fleet running the same
	// build, so they share a cache.
	//
	// Set it to override that — a git commit, a release tag — when something
	// outside the binary decides what the output looks like.
	//
	// It exists because a disk cache outlives the process that filled it. Without
	// one a new binary would serve HTML the old one rendered — a changed template,
	// a changed handler, and a page nobody can explain. Entries live under a
	// subdirectory named for a hash of it, so a different version reads a different
	// directory and finds nothing; there is no check to forget.
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
	// recorded under that tag, from the tracker only. The built-in memory and disk
	// caches index tags themselves (they implement TaggedCache), so InvalidateTags
	// still reaches every entry they hold; the count InvalidateTagsN reports is
	// then what the tracker resolved, which can be fewer. A custom Store that does
	// not implement TaggedCache relies on the tracker alone, and for it a dropped
	// key is one InvalidateTags no longer reaches until the entry expires. Set this
	// above the number of live entries any one tag can plausibly cover, or negative
	// for an unbounded tracker.
	MaxKeysPerTag int
}

// LocaleConfig configures which locales the application's URLs carry.
//
// The URL is the only thing that selects a locale: /about is in Default, and
// /tr/hakkinda is in "tr". Collage never assigns one from Accept-Language or a
// cookie, because a URL that means different things to different readers is one
// that caches, crawlers and shared links all get wrong. An application that wants
// to negotiate — redirect a Turkish browser to /tr/, or render one URL in the
// reader's language — does it in middleware registered with App.Use, and tells
// the cache what it decided with Vary.
type LocaleConfig struct {
	// Default is the locale of a URL with no locale prefix. Defaults to "en".
	Default string
	// Supported lists the locales the application serves. Defaults to a slice
	// containing only Default.
	Supported []string
	// DisablePathLocale turns off resolving the locale from the request path,
	// e.g. /tr/blog/post, which leaves every request in Default.
	DisablePathLocale bool
	// PrefixDefault gives Default's pages a prefix of their own, like every other
	// locale's: "/en/blog/post" beside "/tr/blog/post", with no language at the
	// bare address. Links built by name carry it, and a page requested without it
	// is redirected, 301, to the address with it — "/" to "/en". A static export
	// writes Default's pages under en/ too, and at its root a page that sends the
	// reader to Default's home.
	//
	// Documents are prefixed like pages — "/en/sitemap.xml" beside
	// "/tr/sitemap.xml" — except one built with DocumentBuilder.AtRoot, like
	// "/robots.txt", whose one address is the bare one: "/en/robots.txt"
	// redirects there. Ignored when DisablePathLocale is set.
	PrefixDefault bool
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
}

// Validate reports whether c is well-formed: Server.Port is in 1..65535
// (ErrInvalidPort), Template.Root is non-empty or Template.FS is set
// (ErrEmptyTemplateRoot), Cache.Type is "memory" or "disk" when Cache.Enabled and
// Cache.Store is nil (ErrInvalidCacheType; a caller-supplied Store makes Type
// irrelevant) and a "disk" cache has a Cache.Dir (ErrEmptyCacheDir),
// Locale.Default is non-empty
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
