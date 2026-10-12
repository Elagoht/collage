package collage

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"slices"
	"time"

	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/plugin"
)

// Config is the framework's top-level configuration.
//
// New fills each zero-valued field of the Config it is given with its documented
// default, writing them into the caller's value, and refuses a configuration that
// does not validate.
type Config struct {
	// DevMode enables development-mode behaviour across the framework: templates
	// reload from disk on every request, and a mount re-reads its files' hashes.
	//
	// In development the server answers only a request whose Host names this
	// machine — localhost or a name under it, an IP address, a reserved name
	// (example.com, example.net, example.org and names under them; names under
	// .example, .test and .invalid), Server.Host, or a name in the
	// comma-separated COLLAGE_DEV_HOST environment variable — and refuses any
	// other with 403, so a page on another site that makes its own name resolve
	// here (DNS rebinding) cannot read the development error pages or the
	// reload stream. Under "collage dev" the name its proxy listens on is
	// allowed too. CaptureResponses is not checked.
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
	// New deliberately leaves it nil rather than filling in
	// slog.Default(): nil is already unambiguous, and resolving it at construction
	// keeps a Config comparable and free of a pointer the caller never supplied.
	Logger *slog.Logger
	// Server configures the HTTP server.
	Server ServerConfig

	// BaseURL is the site's own public origin — "https://example.com" — for the
	// absolute URLs a render cannot otherwise know: a canonical link, an og:url, a
	// sitemap's entries, a feed's self link. It is distinct from Server.Host, which
	// is the address the process listens on and is often a loopback or a container
	// name behind a proxy, not the origin a reader typed.
	//
	// It is a bare origin: a scheme (http or https) and a host, with an optional
	// port, and no path, query or fragment. An empty BaseURL is not an error — a
	// site that builds no absolute URLs needs none, and a plugin that does may take
	// its own — and Host.BaseURL reports it, without a trailing slash, to a plugin
	// that asks. New rejects a value that is not a bare origin.
	BaseURL string

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
	// Port is the TCP port the server listens on. Defaults to 6060.
	Port int
	// ReadTimeout bounds how long reading a request may take. Defaults to 15s.
	ReadTimeout time.Duration
	// ReadHeaderTimeout bounds how long reading a request's headers may take,
	// which is what holds a connection open against a client that sends them a
	// byte at a time. Zero, the default, uses ReadTimeout, as the server always
	// has. Set it shorter than ReadTimeout to drop slow-header clients early
	// while still giving a large upload the whole ReadTimeout for its body.
	//
	// It is the application's own server. "collage dev"'s proxy has its own
	// fixed 10s header timeout in front of it.
	ReadHeaderTimeout time.Duration
	// WriteTimeout bounds how long writing a response may take. Defaults to 30s.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long a keep-alive connection may sit idle. Defaults to
	// 60s.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight requests.
	// Defaults to 10s.
	ShutdownTimeout time.Duration
	// DrainDelay is how long the server keeps serving after a shutdown starts,
	// so a load balancer can see readiness fail and stop sending traffic before
	// the port closes. Plugins implementing DrainHook are told when it starts.
	// It comes before ShutdownTimeout, which then bounds the in-flight requests,
	// so a stop can take DrainDelay + ShutdownTimeout. Zero, the default, does
	// not wait. It is ignored in development mode.
	DrainDelay time.Duration
	// MaxBodyBytes bounds an action's request body when the action declares no
	// bound of its own. Defaults to four megabytes; a negative value means
	// unbounded, which is a decision worth making deliberately, because an
	// unbounded body is memory an anonymous caller chooses the size of.
	MaxBodyBytes int64
	// TrustedProxies are the addresses and CIDR ranges of the proxies in front
	// of the server ("10.0.0.0/8", "127.0.0.1"). A request from one of them may
	// name its client in X-Forwarded-For; see ClientIP. Empty, the default,
	// trusts no header: the client is always RemoteAddr. List every hop between
	// the client and the server, a CDN's published ranges included. A range
	// with zero bits ("0.0.0.0/0", "::/0") is accepted but logged at Warn by
	// New: it lets any client name any address.
	//
	// It also decides whose X-Forwarded-Proto is believed when the forgery
	// cookie's Secure flag is set. Set, only a request from one of these
	// proxies can say it arrived over TLS. Empty, the header is believed from
	// anyone — right for a server only a proxy can reach, and what an
	// application that never set this has always had.
	TrustedProxies []string
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
	// CSRFTrustedOrigins are other origins whose forms may post to this
	// application, each spelled "scheme://host[:port]" — an admin panel on its own
	// subdomain, say. Every other origin is refused even with a valid token: the
	// browser's Sec-Fetch-Site, or Origin from an older one, says where a request
	// came from, and a sibling subdomain is exactly who can plant a token cookie.
	CSRFTrustedOrigins []string
	// DisableCSRF turns forgery checking off for the whole application. It is for
	// an application with no browser-submitted forms at all.
	DisableCSRF bool
	// CSRFTokenTTL is how long a forgery token stays valid after it is issued. The
	// issue time is signed into the token, so a token past this age is refused and
	// a fresh one is minted for the next form. Zero selects a sensible default (12
	// hours); a negative value keeps a token valid for as long as its signature
	// verifies, which is the behaviour of a token with no expiry at all.
	CSRFTokenTTL time.Duration
	// FrameOptions is the X-Frame-Options header sent on every response, which
	// keeps the site's pages — and so its forms — from being framed by another
	// origin and clicked through. It defaults to "SAMEORIGIN"; "-" sends none, and
	// any other value is sent verbatim. This is the baseline every site should
	// have; a full Content-Security-Policy with frame-ancestors, HSTS and the rest
	// is what the elagoht/secure plugin adds, and its headers override these.
	FrameOptions string
	// DisableNoSniff stops the X-Content-Type-Options: nosniff header that is
	// otherwise sent on every response, so a browser honours the declared
	// Content-Type instead of guessing one and running a response as a type it was
	// never meant to be. It is sent by default because it is never wrong for a
	// site that declares its types, which collage does.
	DisableNoSniff bool
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
	// csrfToken, pageURL, pageURLIn, localeURL, actionURL, fragmentURL,
	// fragmentURLIn — is possible but pointless: each needs the render it runs
	// in, so the render engine rebinds all of them on
	// every render, and whatever is registered here is never the implementation
	// that runs.
	Funcs template.FuncMap
	// Extension selects which files under Root are templates: only a file ending in
	// it is parsed. It is not added to or stripped from names — a template's name is
	// its path under Root, extension included, as in "pages/home.html". Includes the
	// leading dot; defaults to ".html".
	Extension string
	// Timeout is the default data handler timeout used when a fragment sets none.
	// Defaults to 5s.
	//
	// It is also the default for every *document* handler that sets no Timeout
	// of its own: a slow feed raises its own budget with
	// DocumentBuilder.WithTimeout rather than this one for every fragment and
	// document.
	//
	// As everywhere else in the framework, it bounds the context the handler is
	// given, not the handler itself: one that never consults ctx.Done() can still
	// run past it.
	Timeout time.Duration
}

// CacheType names a built-in cache implementation.
type CacheType string

const (
	// CacheMemory keeps cache entries in the process's memory.
	CacheMemory CacheType = "memory"
	// CacheDisk keeps cache entries on disk, under CacheConfig.Dir, across restarts.
	CacheDisk CacheType = "disk"
)

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
	// Type selects a built-in cache implementation when Store is nil: CacheMemory,
	// which is also what an empty Type defaults to when Enabled is true and Store
	// is nil, or CacheDisk, which keeps entries under Dir across restarts (see Dir
	// and Version).
	Type CacheType
	// DefaultTTL is the cache entry lifetime used when a page does not set its own.
	// Defaults to 5m.
	DefaultTTL time.Duration
	// MaxEntries caps the number of cache entries. Zero means "use the default"
	// (the default is 10000); a negative value means unlimited, and is left
	// untouched by the defaults.
	//
	// It also caps how many keys the framework tracks under one dependency tag,
	// so a small MaxEntries weakens tag invalidation for a custom Store that is
	// not a TaggedCache. A negative value keeps that per-tag cap at 10000.
	MaxEntries int
	// MaxBytes caps the total size of what a built-in cache stores, in bytes.
	// Zero means the cache type's default: 256 MiB for "memory", 1 GiB for
	// "disk". A negative value means unlimited. Past it the oldest entries are
	// evicted, as at MaxEntries, and a page larger than the whole cap is served
	// but not stored. A custom Store bounds itself and ignores this.
	//
	// MaxEntries counts entries, not what they weigh. The cache key holds the
	// query by default, so a client asking for one large page under thousands
	// of invented queries fills the cache with copies of it: ten thousand
	// entries of a 500 KB page is 5 GB. This is the bound that holds whatever
	// the pages weigh.
	MaxBytes int64
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
	// A disk cache is never used in development. Config.DevMode substitutes an
	// in-memory one and says so, because that is where the output changes between
	// runs and nobody bumps a version to save a file.
	Version string
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
	// redirects there.
	PrefixDefault bool
}

// ObservabilityConfig configures metrics and tracing. Both fields are optional: a
// nil Metrics or Tracer is valid and means "use the no-op implementation"; the
// framework substitutes it, so a consumer never has to nil-check these fields.
type ObservabilityConfig struct {
	// Metrics receives framework counters and timings. nil means no-op.
	Metrics Metrics
	// Tracer starts spans around framework operations. nil means no-op.
	Tracer Tracer
}

// applyDefaults fills every zero-valued field of c with the framework's default. It
// is idempotent: applying it to an already-defaulted Config changes nothing.
func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "localhost"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 6060
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
		c.Cache.Type = CacheMemory
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
}

// validate reports whether c is well-formed: Server.Port is in 1..65535
// (ErrInvalidPort), Cache.Type is "memory" or "disk" when Cache.Enabled and
// Cache.Store is nil (ErrInvalidCacheType; a caller-supplied Store makes Type
// irrelevant) and a "disk" cache has a Cache.Dir (ErrEmptyCacheDir), Locale.Default
// is present in Locale.Supported (ErrLocaleDefaultNotSupported), and every duration
// field is not negative (ErrNegativeDuration). It returns nil when c is
// well-formed.
func (c *Config) validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidPort, c.Server.Port)
	}
	if c.Cache.Enabled && c.Cache.Store == nil {
		switch c.Cache.Type {
		case CacheMemory:
		case CacheDisk:
			if c.Cache.Dir == "" {
				return ErrEmptyCacheDir
			}
		default:
			return fmt.Errorf("%w: %q", ErrInvalidCacheType, c.Cache.Type)
		}
	}
	if !slices.Contains(c.Locale.Supported, c.Locale.Default) {
		return fmt.Errorf("%w: %q", ErrLocaleDefaultNotSupported, c.Locale.Default)
	}

	durations := []struct {
		name string
		d    time.Duration
	}{
		{"server.read_timeout", c.Server.ReadTimeout},
		{"server.read_header_timeout", c.Server.ReadHeaderTimeout},
		{"server.write_timeout", c.Server.WriteTimeout},
		{"server.idle_timeout", c.Server.IdleTimeout},
		{"server.shutdown_timeout", c.Server.ShutdownTimeout},
		{"server.drain_delay", c.Server.DrainDelay},
		{"template.timeout", c.Template.Timeout},
		{"cache.default_ttl", c.Cache.DefaultTTL},
	}
	for _, entry := range durations {
		if entry.d < 0 {
			return fmt.Errorf("%w: %s", ErrNegativeDuration, entry.name)
		}
	}

	if c.BaseURL != "" {
		if _, err := plugin.ParseOrigin(c.BaseURL); err != nil {
			return err
		}
	}
	if _, err := httpx.ParseTrustedProxies(c.Server.TrustedProxies); err != nil {
		return err
	}

	return nil
}
