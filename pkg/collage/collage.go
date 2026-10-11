package collage

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// Plugin is the minimum contract every plugin implements. A plugin opts into the
// framework's hooks by additionally implementing whichever hook interfaces it
// needs.
type Plugin = interface {
	// Name identifies the plugin. It must be non-empty and unique among the
	// application's plugins.
	Name() string
	// Version reports the plugin's own version string, for diagnostics.
	Version() string
	// Init prepares the plugin to run, using host to read startup state and
	// contribute routes and commands. Init runs once, in registration order,
	// before the first request is served. A returned error aborts startup, and the
	// plugins already initialised are shut down.
	Init(ctx context.Context, host Host) error
	// Shutdown releases whatever the plugin acquired in Init. It runs once, in
	// reverse registration order, either at normal shutdown or to roll back a
	// failed startup.
	Shutdown(ctx context.Context) error
}

// Host is the capability surface a plugin receives in Init, plus the ConfigReader
// PluginConfig reads through.
//
// The value handed to Init is deliberately not the *App. *App has most of these
// methods, but it also has Shutdown, ListenAndServe, Handler, and RenderPath, and a
// method set travels with a value through an interface — a plugin given the *App
// could assert its Host parameter to an interface naming those and recover them
// without ever referring to *App by name. What Init receives is a narrow forwarding
// value instead, so the assertion has nothing to find.
//
// No type outside collage implements Host: its ConfigReader's one method is
// unexported. A plugin's test that needs a Host embeds a real one.
type Host = interface {
	// --- The environment ---
	//
	// DevMode reports whether the application is running in development mode.
	DevMode() bool
	// BaseURL returns the site's public origin without a trailing slash — the
	// scheme and host an absolute URL is built on, from Config.BaseURL — or "" when
	// the application set none, in which case a plugin that needs one takes its own.
	BaseURL() string
	// OriginFor is host's public origin: an OriginResolver's, else Config.BaseURL,
	// which may be "". host is lower-cased, without its port. It is how a plugin
	// that writes absolute URLs outside a render — an invalidation, a command —
	// learns a host's origin.
	OriginFor(ctx context.Context, host string) string
	// MultiOrigin reports whether a plugin implementing OriginResolver is
	// registered — whether origins vary by host — so an empty Config.BaseURL is
	// not, by itself, a misconfiguration.
	MultiOrigin() bool
	// Locales returns the default locale and every supported one, the default
	// included, in the order they were configured.
	Locales() (defaultLocale string, supported []string)
	// BuildID names the build of the program serving — Config.Cache.Version, or
	// a fingerprint of the executable — for a plugin versioning what a browser
	// keeps across deploys: a service worker's caches, an asset's query string.
	BuildID() string
	// Logger returns the application's structured logger.
	Logger() *slog.Logger

	// --- Contributing routes ---
	//
	// RegisterCommand registers cmd as one of the application's CLI subcommands.
	RegisterCommand(cmd Command) error
	// RegisterDocument registers a document the plugin contributes, on the same
	// terms as App.RegisterDocument.
	RegisterDocument(doc *Document) error
	// Mount serves fsys under prefix, on the same terms as App.Mount. A plugin
	// that rewrites URLs into its own namespace — an image optimiser, say — uses
	// this to serve what it rewrote to.
	Mount(prefix string, fsys fs.FS, opts ...MountOption) error
	// Handle serves handler for every request under prefix, on the same terms as
	// App.Handle: a prefix ending in "/", refused when it shadows a registered
	// route. It is for what a file system cannot answer — an event stream, a
	// WebSocket.
	Handle(prefix string, handler http.Handler) error

	// --- Wrapping requests ---
	//
	// Use adds middleware around the handling of every request, as App.Use does,
	// after the application's: a plugin's headers, limits and cookies wrap what
	// the application's middleware produced.
	Use(middleware func(http.Handler) http.Handler) error

	// --- Reading the site map ---
	//
	// Pages returns every page registered with the application, each a defensive
	// copy: the Page and its Paths, Redirects and DependencyTags are copied, so
	// mutating them does not affect the framework's own. The copy goes one level
	// deep — writing through a value a copied container holds reaches the
	// original — and the fragment pointers are shared.
	Pages() []*Page
	// Page returns the page registered under name, and whether one was found.
	// The returned Page is a defensive copy, on the same terms as Pages.
	Page(name string) (*Page, bool)
	// PageURLs returns every URL the page registered as name answers — one per
	// locale for a fixed path, one per locale and parameter set for a pattern its
	// WithStaticParams lists; a pattern without it has none a plugin could know.
	// It is what a sitemap is made of.
	PageURLs(ctx context.Context, name string) ([]PageURL, error)
	// URL returns the path of the page or document registered as name, in
	// locale, with params filling its pattern — App.URL, with its strictness.
	URL(name, locale string, params map[string]string) (string, error)

	// --- Acting at request time ---
	//
	// RenderFragment renders one fragment a page opened at its own URL with
	// WithFragmentPath, for r, as a request to that URL would — and hands back the
	// parts rather than a response, so it can travel over a stream the plugin
	// owns. A fragment the page did not open is ErrUnknownFragmentPath: nothing is
	// reachable here that is not reachable over HTTP. The fragment's guard runs
	// for r as it would for a request to that URL, and a refusal is
	// ErrGuardRefused: send that reader nothing.
	RenderFragment(r *http.Request, req FragmentRequest) (*FragmentRender, error)
	// ServeStatus answers r with status and the page the application shows for
	// it — its not-found page for 404 and 410, its error page otherwise — for a
	// plugin answering a request itself that should look like the site.
	ServeStatus(w http.ResponseWriter, r *http.Request, status int)
	// InvalidateTags invalidates every cache entry associated with any of tags.
	InvalidateTags(ctx context.Context, tags ...string) error

	// ConfigReader lets PluginConfig read this plugin's configuration.
	ConfigReader
}

// FetchHeader marks a request a script made with fetch. An action redirecting it
// answers 204 with the destination in LocationHeader, so the script navigates once
// rather than downloading the page with fetch and then again by navigating.
const FetchHeader = httpx.FetchHeader

// LocationHeader carries an action's redirect to a request marked with FetchHeader.
const LocationHeader = httpx.LocationHeader

// FragmentRequest names a fragment a page opened at its own URL, and the locale and
// path parameters to render it in, for App.RenderFragment and Host.RenderFragment.
type FragmentRequest = plugin.FragmentRequest

// FragmentRender is one fragment rendered on its own, in parts: its markup, what it
// hoisted, the tags it depended on, and whether one render serves every reader.
type FragmentRender = plugin.FragmentRender

// HoistItem is one hoisted declaration: its area, its key and its markup.
type HoistItem = types.HoistItem

// Finding is something a plugin checking the output noticed about a page: shown
// over the page in development, listed in a build's report, and — at
// FindingError — failing the build. See AfterRenderEvent.Warn and Error.
type Finding = types.Finding

// FindingLevel is how serious a Finding is.
type FindingLevel = types.FindingLevel

const (
	// FindingWarning is worth fixing and stops nothing.
	FindingWarning = types.FindingWarning
	// FindingError fails a static build.
	FindingError = types.FindingError
)

// BuildFinishedHook is implemented by a plugin that checks a finished static build
// as a whole: two pages with one title, a link to a page nobody wrote.
type BuildFinishedHook = plugin.BuildFinishedHook

// BuildFinishedEvent describes a finished static build: every file it wrote.
type BuildFinishedEvent = plugin.BuildFinishedEvent

// BuiltFile is one file a static build wrote.
type BuiltFile = plugin.BuiltFile

// BuiltRedirect is one redirect a static export carries; see RedirectSource.
type BuiltRedirect = plugin.BuiltRedirect

// RedirectSource is a plugin whose redirects a static export carries.
type RedirectSource = plugin.RedirectSource

// PathTag is the dependency tag every cached page and document carries for the
// URL path it was rendered for: InvalidateTags(ctx, PathTag("/blog")) drops what
// is cached for that path, whatever else it depends on.
func PathTag(path string) string { return types.PathTag(path) }

// FragmentReport is how one fragment of a render went, as AfterRenderEvent.Fragments
// lists them: its time, whether it failed.
type FragmentReport = types.FragmentReport

// PageURL is one URL a page answers, as Host.PageURLs lists them.
type PageURL = plugin.PageURL

// Command is a CLI subcommand a plugin contributes through Host.RegisterCommand.
type Command = plugin.Command

// RequestHook is implemented by a plugin that shapes the context a request is
// served under before collage starts on it — before its request span and before
// middleware: a trace carried in from the caller, whose span must be the parent
// of collage's own.
type RequestHook = plugin.RequestHook

// Route is what a request resolved to: its kind, its registered name, the path
// pattern it was registered with ("/blog/{slug}", no locale prefix) and the locale.
type Route = httpx.Route

// RouteInfo reports what the request carrying ctx resolved to, pattern and locale
// included — a bounded label for a span or a metric, for pages, documents and
// actions alike. Kind is "page", "document", "action", "mount" or "handler"; Name
// is the registered name, or the prefix of a mount or handler. The zero Route
// means it resolved to nothing, or has not yet.
func RouteInfo(ctx context.Context) Route { return httpx.RouteInfo(ctx) }

// ClientIP is the address of the client r comes from. It is RemoteAddr's host,
// unless that is one of ServerConfig.TrustedProxies: then it is the first
// untrusted address in X-Forwarded-For read from the right, or the leftmost when
// every one is trusted. An entry may carry a port ("9.9.9.9:4567",
// "[2001:db8::1]:443") or brackets ("[2001:db8::1]"). An entry that is still not
// an address ("unknown") met before any untrusted one leaves the client unknown:
// the result is the zero netip.Addr, never the proxy, which would make every
// visitor one client. With no TrustedProxies, the default, X-Forwarded-For is
// never read, so a client cannot forge it. The address is unmapped
// ("::ffff:1.2.3.4" is 1.2.3.4) and has no zone; it is the zero netip.Addr when
// RemoteAddr holds none, as in a shared page render, whose request has no
// RemoteAddr. A plugin keying anything on the client — a rate limit, a ban —
// should use it rather than RemoteAddr.
func ClientIP(r *http.Request) netip.Addr { return httpx.ClientIP(r) }

// StreamCloser is implemented by a plugin serving connections that never end by
// themselves — an event stream, a WebSocket. CloseStreams runs when shutdown
// begins, before the server waits for open requests, which a stream never ends.
type StreamCloser = plugin.StreamCloser

// DrainHook is implemented by a plugin that must know the server is about to
// stop taking traffic. OnDrain is called once, when the drain starts and before
// ServerConfig.DrainDelay; it must not block.
type DrainHook = plugin.DrainHook

// ServeHook is implemented by a plugin that does work only while the
// application is serving — a scheduler, a queue worker. OnServe is called once,
// after ListenAndServe has bound its port and before it serves; ctx is
// cancelled when the drain starts. It must not block: start goroutines.
type ServeHook = plugin.ServeHook

// BeforeRenderHook is implemented by a plugin that wants to observe a render about
// to start. It does not fire when a cached render is served
// instead of a fresh one.
type BeforeRenderHook = plugin.BeforeRenderHook

// AfterRenderHook is implemented by a plugin that wants to observe, or post-process,
// a render that just completed. It may replace the event's HTML.
type AfterRenderHook = plugin.AfterRenderHook

// CacheInvalidateHook is implemented by a plugin that wants to observe a cache
// invalidation.
type CacheInvalidateHook = plugin.CacheInvalidateHook

// ErrorHook is implemented by a plugin that wants to observe a failure encountered
// while serving a request.
type ErrorHook = plugin.ErrorHook

// Configurer is implemented by a plugin that must act while the application is
// built rather than before it serves — to register a template function, or to wrap
// every mounted filesystem. Such a plugin is supplied through Config.Plugins;
// RegisterPlugin refuses it, because Configure has already run by then.
type Configurer = plugin.Configurer

// ConfigHost is the narrower capability surface Configure receives. At that point
// the application has registered nothing, so there are no pages to read and
// nothing to invalidate.
type ConfigHost = interface {
	// DevMode reports whether the application is running in development mode.
	DevMode() bool
	// BaseURL returns the site's public origin without a trailing slash, from
	// Config.BaseURL, or "" when the application set none.
	BaseURL() string
	// OriginFor is host's public origin: an OriginResolver's, else Config.BaseURL,
	// which may be "". host is lower-cased, without its port.
	OriginFor(ctx context.Context, host string) string
	// MultiOrigin reports whether a plugin implementing OriginResolver is
	// registered — among those registered so far, at Configure time.
	MultiOrigin() bool
	// Logger returns the application's structured logger.
	Logger() *slog.Logger
	// AddTemplateFunc registers fn as a template function under name. It returns
	// ErrDuplicateTemplateFunc when another plugin already registered that name.
	// It must be called from Configure: templates are parsed when construction
	// finishes, and a function added after that is one no template can call.
	AddTemplateFunc(name string, fn any) error // any: restates html/template.FuncMap's own value type
	// WrapMount registers a transformation applied to every mounted filesystem, in
	// the order the wrappers were registered. A minifier returns an fs.FS whose
	// files are already minified; Range requests keep working on it.
	WrapMount(wrap func(fs.FS) fs.FS)
	// AddRenderFunc registers a template function made anew for each render:
	// factory is called with the render's context and returns the function the
	// templates call, which can then read what that render holds — a nonce set in
	// BeforeRender, the render's locale. Like AddTemplateFunc it must be called
	// from Configure, and a name already registered is ErrDuplicateTemplateFunc.
	AddRenderFunc(name string, factory func(rc *RenderContext) any) error // any: html/template.FuncMap's own value type
	// ConfigReader lets PluginConfig read this plugin's configuration.
	ConfigReader
}

// ConfigReader is a host PluginConfig reads a plugin's configuration through:
// the Host given to Init and the ConfigHost given to Configure.
type ConfigReader = plugin.ConfigReader

// PluginConfig returns the plugin's section of the application's plugin
// configuration decoded over defaults:
//
//	cfg, err := collage.PluginConfig(host, Config{Limit: 10, Window: time.Minute})
//
// No section leaves defaults as they are; a malformed one is an error.
func PluginConfig[T any](host ConfigReader, defaults T) (T, error) {
	return plugin.PluginConfig(host, defaults)
}

// DocumentRenderedHook is implemented by a plugin that wants to transform a
// document's bytes before they are served — the non-HTML counterpart to
// AfterRenderHook.
type DocumentRenderedHook = plugin.DocumentRenderedHook

// DocumentRenderedEvent describes a document that has just produced its body.
type DocumentRenderedEvent = plugin.DocumentRenderedEvent

// BeforeRenderEvent describes a render about to start.
type BeforeRenderEvent = plugin.BeforeRenderEvent

// BeforeActionHook is implemented by a plugin that checks a submission before an
// action's handler sees it: after the page's guards, the action's body limit and
// the forgery check.
type BeforeActionHook = plugin.BeforeActionHook

// BeforeActionEvent describes an action request about to reach its handler.
type BeforeActionEvent = plugin.BeforeActionEvent

// AfterRenderEvent describes a render that just completed. A plugin may replace its
// HTML field to post-process the page.
type AfterRenderEvent = plugin.AfterRenderEvent

// PersonaliseHook is implemented by a plugin that rewrites each HTML response for
// the reader it goes to, after the cache. See plugin.PersonaliseHook.
type PersonaliseHook = plugin.PersonaliseHook

// PersonaliseEvent is one HTML response on its way to one reader.
type PersonaliseEvent = plugin.PersonaliseEvent

// CacheInvalidateEvent describes a cache invalidation that has just happened.
type CacheInvalidateEvent = plugin.CacheInvalidateEvent

// InvalidatedEntry is one cached page or document an invalidation dropped, with
// the host it was cached for. See CacheInvalidateEvent.Entries.
type InvalidatedEntry = plugin.InvalidatedEntry

// OriginResolver is implemented by a plugin that knows the public origin of a
// host the site is served on. See collage.BaseURL.
type OriginResolver = plugin.OriginResolver

// ParseOrigin checks that raw is a bare origin, scheme://host[:port], and returns
// it without a trailing slash; anything else is ErrInvalidOrigin.
func ParseOrigin(raw string) (string, error) { return plugin.ParseOrigin(raw) }

// ErrorEvent describes a failure encountered while serving a request.
type ErrorEvent = plugin.ErrorEvent

// Result is one page's rendered output, as returned by App.RenderPath: the HTML,
// the dependency tags the render relied on, how it performed, and whether it failed
// because the content does not exist. A caller MUST check RenderPath's error before
// reading HTML.
type Result = render.Result

// Metadata describes how a render performed: the page, the locale, where the time
// went, and one entry per fragment that ran.
type Metadata = render.Metadata

// Timing records where a render spent its time. It is Metadata.Timing's type, so
// this alias is required to write a function that takes one.
type Timing = observability.Timing

// PanicError is what a panic in a data handler or a template function becomes: the
// fragment fails like any other failure instead of taking the process down. Reach
// it with errors.As on a render error to recover the panic value and its stack.
// A panic recovered while serving a request is one too, wrapped in ErrPanic.
type PanicError = render.PanicError

// Cache stores rendered pages keyed by request identity. Implement it, and set it
// on CacheConfig.Store, to replace the built-in in-memory cache.
type Cache = cache.Cache

// TaggedCache is the optional extension a Cache implements when it can associate
// dependency tags with an entry at write time. The framework type-asserts for it
// and calls SetTagged in place of Set when a cache satisfies it; a cache that does
// not is invalidated through the framework's own tag tracker instead, which stays
// the authority either way.
type TaggedCache = cache.TaggedCache

// Metrics receives the framework's counters and timings. Implement it to bridge the
// framework into a metrics backend; a nil Metrics means no-op.
type Metrics = observability.Metrics

// Tracer starts one span per request, render, and fragment. Implement it to bridge
// the framework into a tracing backend; a nil Tracer means no-op.
type Tracer = observability.Tracer

// Span is the unit of work a Tracer starts. A Tracer implementation returns one, so
// this alias is required to implement Tracer at all.
type Span = observability.Span

// CacheEvent identifies a single kind of cache operation reported to Metrics. A
// Metrics implementation receives one, so this alias is required to implement
// Metrics at all.
type CacheEvent = observability.CacheEvent

const (
	// CacheHit means a lookup found a live entry.
	CacheHit = observability.CacheHit
	// CacheMiss means a lookup found no live entry.
	CacheMiss = observability.CacheMiss
	// CacheSet means an entry was written.
	CacheSet = observability.CacheSet
	// CacheEvict means an entry was removed because it expired or was displaced.
	CacheEvict = observability.CacheEvict
	// CacheInvalidate means an entry was removed by an explicit invalidation.
	CacheInvalidate = observability.CacheInvalidate
	// CacheCoalesced means the request was served by a render of the same cache
	// key that was already running, rather than starting one of its own.
	CacheCoalesced = observability.CacheCoalesced
)

// ETag returns the strong HTTP ETag the framework itself computes for content: the
// double-quoted hex encoding of the first 16 bytes of its SHA-256 hash. A Cache
// implementation is free to derive ETags its own way — whatever Set returns is what
// the response advertises — but this is the one to use unless there is a reason not
// to, since it is already known to satisfy the HTTP ETag grammar.
func ETag(content []byte) string {
	return cache.ETag(content)
}

// toCoreConfig converts the public Config into internal/core's mirror of it. The
// conversion is written out field by field on purpose: the compiler cannot tell us
// that the two structs have drifted apart, so this function is the one place where
// adding a field to one and forgetting the other becomes visible.
func toCoreConfig(cfg *Config) core.Config {
	baseURL := cfg.BaseURL
	if normalized, err := plugin.ParseOrigin(baseURL); err == nil {
		baseURL = normalized
	}
	// Validate has already refused an entry that does not parse.
	trustedProxies, _ := httpx.ParseTrustedProxies(cfg.Server.TrustedProxies)
	return core.Config{
		DevMode:  cfg.DevMode,
		DevWatch: cfg.DevWatch,
		BaseURL:  baseURL,
		Logger:   cfg.Logger,
		Server: core.ServerConfig{
			Host:              cfg.Server.Host,
			Port:              cfg.Server.Port,
			ReadTimeout:       cfg.Server.ReadTimeout,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			WriteTimeout:      cfg.Server.WriteTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
			ShutdownTimeout:   cfg.Server.ShutdownTimeout,
			DrainDelay:        cfg.Server.DrainDelay,
			MaxBodyBytes:      cfg.Server.MaxBodyBytes,
			TrustedProxies:    trustedProxies,
		},
		Security: core.SecurityConfig{
			CSRFKey:            cfg.Security.CSRFKey,
			CSRFCookieName:     cfg.Security.CSRFCookieName,
			CSRFFieldName:      cfg.Security.CSRFFieldName,
			CSRFHeaderName:     cfg.Security.CSRFHeaderName,
			CSRFTrustedOrigins: cfg.Security.CSRFTrustedOrigins,
			DisableCSRF:        cfg.Security.DisableCSRF,
			CSRFTokenTTL:       cfg.Security.CSRFTokenTTL,
			FrameOptions:       cfg.Security.FrameOptions,
			NoSniff:            cfg.Security.NoSniff,
		},
		Plugins:      cfg.Plugins,
		PluginConfig: cfg.PluginConfig,
		Template: core.TemplateConfig{
			FS:        cfg.Template.FS,
			Root:      cfg.Template.Root,
			Extension: cfg.Template.Extension,
			DevMode:   cfg.Template.DevMode,
			Timeout:   cfg.Template.Timeout,
			Funcs:     cfg.Template.Funcs,
		},
		Cache: core.CacheConfig{
			Enabled:       cfg.Cache.Enabled,
			Store:         cfg.Cache.Store,
			Type:          cfg.Cache.Type,
			DefaultTTL:    cfg.Cache.DefaultTTL,
			MaxEntries:    cfg.Cache.MaxEntries,
			MaxBytes:      cfg.Cache.MaxBytes,
			Dir:           cfg.Cache.Dir,
			Version:       cfg.Cache.Version,
			MaxKeysPerTag: cfg.Cache.MaxKeysPerTag,
		},
		Locale: core.LocaleConfig{
			Default:           cfg.Locale.Default,
			Supported:         cfg.Locale.Supported,
			DisablePathLocale: cfg.Locale.DisablePathLocale,
			PrefixDefault:     cfg.Locale.PrefixDefault,
		},
		TrailingSlash: cfg.TrailingSlash,
		Observability: core.ObservabilityConfig{
			Metrics: cfg.Observability.Metrics,
			Tracer:  cfg.Observability.Tracer,
		},
	}
}
