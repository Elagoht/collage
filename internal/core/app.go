// Package core is the application orchestrator: it owns one instance of every
// subsystem — the template engine, the render engine, the cache, the dependency
// tracker, the router, the plugin registry, and observability — and exposes them
// through a single App with a server lifecycle.
//
// This package must never import pkg/collage. The dependency points the other way:
// pkg/collage converts its public Config into this package's Config and calls New,
// then aliases App so users call these methods directly. Config below is the
// internal mirror of pkg/collage.Config and the two must be kept field-aligned.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/datacache"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// ErrAppStarted is returned by every registration method once the application has
// started — once Handler has built the HTTP handler, which is also what
// ListenAndServe does first. Registration is a startup-time activity: plugin Init
// has already run and already seen the registered pages, so a page, plugin, or
// error page added afterwards would be invisible to every plugin that asked.
//
// RegisterCommand is deliberately exempt: plugins register their commands from
// inside Init, which by definition runs after the application has started.
var ErrAppStarted = errors.New("collage: application already started")

// ErrConfigurerRegisteredLate is returned by RegisterPlugin for a plugin
// implementing plugin.Configurer. Configure runs inside New, before templates are
// parsed, and RegisterPlugin is called afterwards — so such a plugin belongs in
// Config.Plugins. Skipping its Configure silently would leave a plugin that
// registered a template function wondering why no template can call it.
var ErrConfigurerRegisteredLate = errors.New("collage: plugin needs Configure and must be supplied in Config.Plugins")

// ErrNilPage is returned by the page registration methods when passed a nil page.
var ErrNilPage = errors.New("collage: nil page")

// ErrDuplicatePage is returned when a page is registered under a name another
// page already holds.
var ErrDuplicatePage = errors.New("collage: duplicate page name")

// ErrTemplateNotFound is returned by the page registration methods when one of a
// page's fragments names a template the engine has not loaded. It is deliberately
// the same sentinel the template engine reports at render time, not a second one
// for the same condition: "this template does not exist" is one failure mode, and
// registering it here only moves the moment it is detected from the first request
// back to startup, which is the point.
var ErrTemplateNotFound = template.ErrTemplateNotFound

// ErrUnregisteredErrorPage is returned when the application starts and a registered
// page references a NotFoundPage or ErrorPage that was never registered itself. See
// App.checkErrorPagesRegistered for why that has to be a startup failure.
var ErrUnregisteredErrorPage = errors.New("collage: error page not registered")

// ErrPageNotFound is returned by RenderPath when the path resolves to no page —
// either nothing matched, or what matched is a redirect rather than a page. Both
// mean the same thing to the caller: there is nothing at that path to render.
var ErrPageNotFound = errors.New("collage: no page at path")

// ErrEmptyCommandName is returned by RegisterCommand when the command's Name is
// empty.
var ErrEmptyCommandName = errors.New("collage: empty command name")

// ErrDuplicateCommand is returned by RegisterCommand when a command with the same
// Name is already registered.
var ErrDuplicateCommand = errors.New("collage: duplicate command name")

// ErrUnsupportedCache is returned by New when Config.Cache.Enabled is true,
// Config.Cache.Store is nil, and Config.Cache.Type names an implementation this
// package cannot build.
var ErrUnsupportedCache = errors.New("collage: unsupported cache type")

// ErrEmptyTemplateRoot is returned by New when Config.Template.Root is empty.
var ErrEmptyTemplateRoot = errors.New("collage: empty template root")

// defaultMaxKeysPerTag is the per-tag cache-key cap the dependency tracker is given
// when Config.Cache.MaxKeysPerTag is left at zero. It matches the cache's own
// default entry cap: a tag cannot usefully resolve to more live cache entries than
// the cache holds, and an unbounded tracker in front of a bounded cache is how a
// query-string flood turns into a memory leak.
//
// pkg/collage.Config.ApplyDefaults fills the same value in, so this fallback only
// matters for a Config built here directly — but it is the tracker's only bound,
// and leaving it to the layer above would mean the bound holds by convention rather
// than by construction.
const defaultMaxKeysPerTag = 10000

// Config is internal/core's mirror of pkg/collage.Config. The two structs are kept
// field-aligned on purpose: pkg/collage owns the defaults and the validation, then
// converts its Config into this one and calls New, so that internal/core never
// imports pkg/collage. A field added to one must be added to the other.
//
// New expects an already-defaulted, already-validated Config — the state
// pkg/collage.New hands it. It applies only the few fallbacks it needs to build a
// working App at all (see New), and does not re-run pkg/collage's defaulting.
type Config struct {
	// DevMode enables development-mode behaviour across the framework. The
	// effective value is this or Template.DevMode; see App.DevMode.
	DevMode bool
	// DevWatch lists directories whose changes reload a development page.
	DevWatch []string
	// Security configures request-forgery protection.
	Security SecurityConfig
	// Logger is the structured logger the framework writes through and hands to
	// plugins via Host.Logger. A nil Logger means the framework picks one: a
	// terminal-friendly handler when the output is a terminal and nothing has
	// replaced slog's own default, and slog.Default() otherwise. See
	// defaultLogger.
	Logger *slog.Logger
	// Server configures the HTTP server.
	Server ServerConfig
	// Template configures template loading and rendering.
	Template TemplateConfig
	// Plugins are registered and configured while the App is built.
	//
	// A plugin implementing plugin.Configurer must arrive here rather than through
	// RegisterPlugin: Configure runs before templates are parsed, and RegisterPlugin
	// is called after New has already parsed them. RegisterPlugin refuses such a
	// plugin by name rather than silently skipping its Configure.
	Plugins []plugin.Plugin
	// PluginConfig is each plugin's own configuration, keyed by plugin name.
	//
	// The framework does not read a file: the application loads this however it
	// likes — JSON, YAML, environment — so no format is imposed on it. A key
	// matching no registered plugin is a startup error, because the alternative is
	// an operator certain a plugin was configured while it ran on defaults.
	PluginConfig map[string]json.RawMessage
	// Cache configures the render output cache.
	Cache CacheConfig
	// Locale configures locale resolution.
	Locale LocaleConfig
	// Observability configures metrics and tracing.
	Observability ObservabilityConfig
}

// ServerConfig is internal/core's mirror of pkg/collage.ServerConfig.
type ServerConfig struct {
	// Host is the address the server listens on.
	Host string
	// Port is the TCP port the server listens on.
	Port int
	// ReadTimeout bounds how long reading a request may take.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long writing a response may take.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long a keep-alive connection may sit idle.
	IdleTimeout time.Duration
	// MaxBodyBytes bounds an action's request body when the action declares no
	// bound of its own. Zero selects the framework's default of four megabytes;
	// negative means unbounded, which is a decision worth making deliberately,
	// because an unbounded body is memory an anonymous caller chooses the size of.
	MaxBodyBytes int64
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight
	// requests.
	ShutdownTimeout time.Duration
}

// TemplateConfig is internal/core's mirror of pkg/collage.TemplateConfig.
type TemplateConfig struct {
	// FS, when non-nil, is the filesystem templates are loaded from, and Root names
	// a directory within it rather than on disk.
	FS fs.FS
	// Root is the directory templates are loaded from: a path on disk when FS is
	// nil, otherwise a path within FS, where empty means the root of FS.
	Root string
	// Extension is the file extension that identifies template files under Root.
	Extension string
	// DevMode reloads templates from disk on every render.
	DevMode bool
	// Timeout is the default DataHandler timeout used when a fragment sets none.
	// It is also the only bound on a document handler, which has no per-route
	// Timeout field of its own. See pkg/collage.TemplateConfig.Timeout.
	Timeout time.Duration
	// Funcs is merged over the template engine's built-in function map at
	// construction, so an entry under a built-in name replaces that built-in.
	Funcs htmltemplate.FuncMap
}

// CacheConfig is internal/core's mirror of pkg/collage.CacheConfig.
type CacheConfig struct {
	// Enabled turns caching on. A disabled cache is a nil cache: every request
	// renders and nothing is ever stored. It is the master switch, so a Store set
	// alongside Enabled false is not used.
	Enabled bool
	// Store is a caller-supplied cache implementation. When non-nil it is used as
	// it stands and Type is ignored.
	Store cache.Cache
	// Type selects a built-in cache implementation when Store is nil. Only
	// "memory" is built in.
	Type string
	// DefaultTTL is the cache entry lifetime used when a page sets none.
	DefaultTTL time.Duration
	// MaxEntries caps the number of cache entries.
	MaxEntries int
	// Dir is where a "disk" cache stores its entries.
	Dir string
	// Version identifies the build whose output a "disk" cache holds. Entries live
	// under a subdirectory named for it, so a new build reads a fresh cache rather
	// than the previous binary's HTML.
	Version string
	// MaxKeysPerTag caps how many cache keys the dependency tracker records under
	// any one tag. Zero means "use the default" (defaultMaxKeysPerTag); a negative
	// value means unlimited. See pkg/collage.CacheConfig.MaxKeysPerTag for what
	// dropping a key means.
	MaxKeysPerTag int
}

// LocaleConfig is internal/core's mirror of pkg/collage.LocaleConfig.
type LocaleConfig struct {
	// Default is the locale of a URL with no locale prefix.
	Default string
	// Supported lists the locales the application serves.
	Supported []string
	// DisablePathLocale turns off resolving the locale from the request path,
	// which leaves every request in Default.
	DisablePathLocale bool
}

// ObservabilityConfig is internal/core's mirror of pkg/collage.ObservabilityConfig.
type ObservabilityConfig struct {
	// Metrics receives framework counters and timings. nil means no-op.
	Metrics observability.Metrics
	// Tracer starts spans around framework operations. nil means no-op.
	Tracer observability.Tracer
}

// App owns every subsystem and the server lifecycle. It is the type users hold:
// pkg/collage aliases it, so the methods here are the framework's public surface.
//
// An App is safe for concurrent use. Registration is expected at startup, from one
// goroutine, but the Host methods a plugin may call at any time — Pages, Page,
// InvalidateTags, Logger, RegisterCommand — are all safe to call while requests are
// being served.
type App struct {
	// cfg is the configuration the App was built from. It is read-only after New.
	cfg Config
	// devMode is the effective development-mode flag: cfg.DevMode or
	// cfg.Template.DevMode, resolved once so the two cannot drift apart.
	devMode bool
	// pluginFuncs are template functions contributed by plugins during Configure.
	// They are merged under the application's own Template.Funcs, so an
	// application always wins a name a plugin also claims: the application is the
	// party that can see both and decide.
	pluginFuncs map[string]any // any: html/template.FuncMap's own value type
	// mountWrappers transform every mounted filesystem, in the order plugins
	// registered them during Configure.
	mountWrappers []func(fs.FS) fs.FS
	// logger is the structured logger handed to the handler, the plugin registry,
	// and plugins through Host.Logger.
	logger *slog.Logger

	// tmpl is the template engine every fragment renders through. It is also what
	// RegisterPage checks fragment template paths against.
	tmpl template.Engine
	// renderer composes a page's fragment tree into HTML.
	renderer render.Engine
	// store is the render output cache, or nil when caching is disabled. It is
	// declared as the interface and only ever assigned a non-nil implementation —
	// either the built-in memory cache or Config.Cache.Store — so "no cache" is a
	// genuinely nil interface value. A caller that assigns a nil pointer of a
	// concrete type to Config.Cache.Store defeats that, and this package cannot
	// tell the difference without reflection; see that field's own documentation.
	store cache.Cache
	// tracker resolves dependency tags back to the cache keys built from them.
	tracker dependency.Tracker
	// routes resolves a request to a page, a redirect, or a not-found result.
	routes router.Router
	// plugins owns plugin registration, lifecycle, and hook dispatch.
	plugins *plugin.Registry
	// metrics is the single Metrics instance the whole application reports
	// through. The HTTP handler reports RenderDuration for cache hits and the
	// render engine reports it for fresh renders; the two are complementary and
	// non-double-counting only because they share this one instance.
	metrics observability.Metrics
	// tracer starts one span per request and per render.
	tracer observability.Tracer

	// mu guards everything below it: the page registry, the document registry,
	// the mount registry, the commands, the started and closing flags, and the
	// running server.
	mu sync.RWMutex
	// pages holds every registered page by name, including those registered as the
	// global not-found or error page.
	pages map[string]*types.Page
	// order holds the registered page names in registration order, so Pages is
	// deterministic.
	order []string
	// bound holds the pages whose LayoutFragment this App has already replaced with
	// a copy of their own. It is what makes the layout binding happen exactly once
	// per page; see bindContent for why the slot's contents cannot answer that.
	bound map[*types.Page]bool
	// documents holds every registered document by name. A document has no layout
	// to bind and no template to check, so unlike bound there is no analogous
	// "already prepared" state to track for it.
	documents map[string]*types.Document
	// docOrder holds the registered document names in registration order, so
	// Documents is deterministic.
	docOrder []string
	// actions holds every registered action by name, and actionOrder the same in
	// registration order. An action shares a tree node with whatever else claims
	// its path, so unlike pages and documents it is not the node's sole occupant;
	// see internal/router's RegisterAction.
	actions     map[string]*types.Action
	actionOrder []*types.Action
	// csrf issues and verifies request-forgery tokens, or is nil when the
	// application turned the protection off.
	csrf *csrf.Guard
	// mounts holds every mounted asset file system, in registration order. See
	// mount.go for Mount, Mounts, and checkMountsDoNotShadow, the close-out check
	// that keeps a mount from silently swallowing a page's or a document's route
	// regardless of which was registered first.
	mounts []*asset.Mount
	// handlers holds every http.Handler mounted with Handle, and middleware
	// every wrapper registered with Use, both in registration order. See
	// handle.go.
	handlers []httpx.HandlerMount
	// data keeps what collage.Cached fetches across renders. Nil when the cache
	// is off or in development, where Cached shares within one render only.
	data *datacache.Store
	// devTemplateDir is the directory templates are read from in development,
	// empty when they are not read from disk.
	devTemplateDir string
	middleware     []httpx.Middleware
	// commands holds the CLI subcommands plugins contributed through
	// RegisterCommand.
	commands []plugin.Command
	// started reports whether the HTTP handler has been built, which closes
	// registration.
	started bool
	// closing reports whether Shutdown has run, so a ListenAndServe that starts
	// afterwards does not serve forever with nothing left to stop it.
	closing bool
	// server is the running http.Server, or nil before ListenAndServe has started
	// one.
	server *http.Server
	// listening is closed once ListenAndServe has finished deciding whether to
	// serve — either because the server is accepting connections, or because the
	// App was already shut down and it will not serve at all. Both paths close it,
	// so a waiter never blocks forever. It exists so the lifecycle tests can wait
	// for a real listener instead of sleeping.
	listening chan struct{}
	// listenOnce guards closing listening. ListenAndServe is meant to be called
	// once; the Once is what keeps a second call from panicking on a closed
	// channel rather than merely misbehaving.
	listenOnce sync.Once

	// buildMu guards the memoised handler. It is deliberately separate from mu:
	// building the handler runs plugin Init, and a plugin's Init calls back into
	// this App's Host methods, which take mu.
	buildMu sync.Mutex
	// handlerBuilt reports whether the one-time handler build has been attempted.
	handlerBuilt bool
	// handler is the memoised HTTP handler, nil when the build failed.
	handler http.Handler
	// handlerErr is the memoised build failure, nil when the build succeeded.
	handlerErr error

	// shutdownOnce makes Shutdown idempotent: the work runs once and every caller
	// observes the same outcome.
	shutdownOnce sync.Once
	// shutdownErr is the result of the one shutdown, returned to every caller.
	shutdownErr error
}

// App deliberately carries no plugin.Host assertion of its own. Plugins receive a
// hostView, which forwards to the App and exposes nothing else; see host.go for why
// handing over the App itself made Host's narrowing notional. App still has every
// method Host names — they are its public API — but it is never the value a plugin
// holds.

// New builds an App from a validated configuration. cfg is expected to have been
// defaulted and validated already — pkg/collage.New does both before converting —
// so New applies only the fallbacks it needs to construct working subsystems: an
// empty Template.Extension becomes ".html" and an empty Locale.Default becomes
// "en". It returns ErrEmptyTemplateRoot
// for an empty Template.Root, ErrUnsupportedCache for an enabled cache of an
// unknown type, and a wrapped template error when the template root cannot be
// loaded.
//
// Everything is constructed here, once: New never starts a goroutine and never
// touches the network, so building an App is safe in a test.
func New(cfg Config) (*App, error) {
	if cfg.Template.Root == "" && cfg.Template.FS == nil {
		return nil, ErrEmptyTemplateRoot
	}
	if cfg.Template.Extension == "" {
		cfg.Template.Extension = ".html"
	}
	if cfg.Locale.Default == "" {
		cfg.Locale.Default = "en"
	}
	if cfg.Cache.MaxKeysPerTag == 0 {
		cfg.Cache.MaxKeysPerTag = defaultMaxKeysPerTag
	}

	devMode := cfg.DevMode || cfg.Template.DevMode
	logger := cfg.Logger
	if logger == nil {
		logger = defaultLogger()
	}

	// The App exists before the template engine because plugins get to influence
	// it. Only the fields Configure can reach are filled in here; the rest are set
	// below, once there is an engine to build a renderer from.
	app := &App{
		cfg:         cfg,
		devMode:     devMode,
		logger:      logger,
		plugins:     plugin.NewRegistry(logger),
		pluginFuncs: make(map[string]any), // any: html/template.FuncMap's own value type
	}

	for _, p := range cfg.Plugins {
		if err := app.plugins.Register(p); err != nil {
			return nil, fmt.Errorf("collage: register plugin: %w", err)
		}
	}
	if err := app.plugins.Configure(context.Background(), func(name string) plugin.ConfigHost {
		return &configHostView{app: app, name: name}
	}); err != nil {
		return nil, err
	}

	// Plugin functions go in first and the application's own on top, so an
	// application always wins a name a plugin also claims: it is the party that can
	// see both and decide, and a plugin silently shadowing a function the templates
	// were written against is not a failure anyone would trace back here.
	funcs := make(map[string]any, len(app.pluginFuncs)+len(cfg.Template.Funcs)) // any: html/template.FuncMap's own value type
	maps.Copy(funcs, app.pluginFuncs)
	maps.Copy(funcs, cfg.Template.Funcs)

	// Not cfg.Template.FS directly: in development an embedded template set
	// cannot reload, so the copy on disk is preferred when there is one. See
	// templateSource.
	templates := templateSource(cfg.Template.FS, cfg.Template.Root, devMode, logger)
	if devMode && templates == nil {
		// Read from disk, so a development page reloads itself when it changes.
		app.devTemplateDir = cfg.Template.Root
	}

	tmpl, err := template.NewHTML(template.HTMLConfig{
		FS:        templates,
		Root:      cfg.Template.Root,
		Extension: cfg.Template.Extension,
		DevMode:   devMode,
		Funcs:     funcs,
	})
	if err != nil {
		return nil, fmt.Errorf("collage: template engine: %w", err)
	}

	// One instance, shared by the render engine and the HTTP handler. See the
	// metrics field's doc comment for why that sharing is load-bearing.
	metrics := observability.MetricsOrNoop(cfg.Observability.Metrics)
	tracer := observability.TracerOrNoop(cfg.Observability.Tracer)

	// Before the cache, deliberately: a stored body carries the forgery marker,
	// which is derived from the key, so the key is part of what makes a cached
	// body still correct. buildCache mixes it into the namespace.
	guard, err := buildCSRF(cfg.Security, logger)
	if err != nil {
		return nil, fmt.Errorf("collage: csrf: %w", err)
	}
	app.csrf = guard

	// A caller-supplied Store wins over Type, and is taken exactly as given: this
	// is the one cache the application will use, so nothing here wraps, copies, or
	// second-guesses it. Enabled remains the master switch — a Store on a disabled
	// cache is not silently turned on, because "caching is off" must mean off.
	var store cache.Cache
	if cfg.Cache.Enabled {
		built, err := buildCache(cfg, devMode, logger)
		if err != nil {
			return nil, err
		}
		store = built
	}

	// Bounded before it is ever written to: the tracker is written on every cache
	// write and nothing prunes it on eviction or expiry, so the cap is what keeps
	// a bounded cache from sitting behind an unbounded index. A negative
	// MaxKeysPerTag arrives here as the caller's explicit request for unlimited,
	// which is MemoryTracker's own meaning for a non-positive value.
	tracker := dependency.NewMemory()
	tracker.MaxKeysPerTag = cfg.Cache.MaxKeysPerTag

	app.tmpl = tmpl
	// On when the page cache is, and never in development: there the page cache
	// is not read either, because what a developer is changing must show on the
	// next request.
	if cfg.Cache.Enabled && !devMode {
		app.data = datacache.New(cfg.Cache.MaxEntries)
	}

	app.renderer = render.New(tmpl, render.Options{
		DefaultTimeout: cfg.Template.Timeout,
		Metrics:        metrics,
		Tracer:         tracer,
		DevMode:        devMode,
		AssetURL:       app.assetURL,
		CSRFMarker:     app.csrfMarker,
		// The field the verifier reads, so a renamed field is renamed in the
		// forms too — it used to be configurable on one side only.
		CSRFField:     cfg.Security.CSRFFieldName,
		URL:           app.URL,
		DefaultLocale: cfg.Locale.Default,
		DataCache:     app.dataCacheFor(),
	})
	app.store = store
	app.tracker = tracker
	app.routes = router.New(router.LocaleOptions{
		Default:           cfg.Locale.Default,
		Supported:         cfg.Locale.Supported,
		DisablePathLocale: cfg.Locale.DisablePathLocale,
	})
	app.metrics = metrics
	app.tracer = tracer
	app.pages = make(map[string]*types.Page)
	app.bound = make(map[*types.Page]bool)
	app.documents = make(map[string]*types.Document)
	app.actions = make(map[string]*types.Action)
	app.listening = make(chan struct{})

	return app, nil
}

// DevMode reports whether the application is running in development mode: the
// disjunction of Config.DevMode and Config.Template.DevMode, resolved at New.
func (a *App) DevMode() bool {
	return a.devMode
}

// DefaultLocale returns the locale served without a path prefix. A static build
// reads it to decide which locale occupies the bare output path and which get a
// directory of their own; see internal/build.
func (a *App) DefaultLocale() string {
	return a.cfg.Locale.Default
}

// Logger returns the application's structured logger.
func (a *App) Logger() *slog.Logger {
	return a.logger
}

// Handler returns the application's http.Handler, building it — and running every
// registered plugin's Init — on the first call and memoising it thereafter. It is
// usable without a server, which is what makes an App testable through
// httptest.NewServer or a bare ServeHTTP call.
//
// The first call closes registration: RegisterPage, RegisterNotFoundPage,
// RegisterErrorPage, and RegisterPlugin all return ErrAppStarted afterwards,
// because plugin Init has by then already seen the pages that were registered.
//
// Handler has no error return because http.Handler is what its callers need. When
// the build fails — a plugin's Init failing, or a page referencing an unregistered
// error page — the failure is logged once, at error level, and Handler returns a
// memoised handler that answers every request with 503. ListenAndServe surfaces the
// same failure as a returned error instead, so a program that starts a server never
// loses it.
func (a *App) Handler() http.Handler {
	// The error is dropped deliberately: buildHandler has already logged it once and
	// memoised unavailableHandler in the real handler's place, so every call returns
	// the same value and none of them allocates.
	handler, _ := a.buildHandler()
	return handler
}

// Start builds the application's HTTP handler and runs every registered plugin's
// Init, returning the error that startup produced — a plugin's Init failing, or a
// page referencing an unregistered error page — or nil.
//
// It is what Handler and ListenAndServe each do first, memoised the same way: calling
// it more than once, or calling it and then either of them, runs the work exactly
// once and every caller sees the same outcome. Like them it closes registration, so
// RegisterPage, RegisterNotFoundPage, RegisterErrorPage, and RegisterPlugin return
// ErrAppStarted afterwards.
//
// It exists because plugins register their CLI commands from inside Init, so
// Commands is empty until Init has run — and Handler, which has to return an
// http.Handler, cannot hand back the reason it did not. A program that wants to
// dispatch a plugin command, or simply wants startup failures before it does anything
// else, calls this.
func (a *App) Start() error {
	_, err := a.buildHandler()
	return err
}

// unavailableHandler answers every request with 503. It stands in for the real
// handler when the build failed, so an App whose startup failed serves a clear
// status rather than panicking on a nil handler. It is an empty struct rather than a
// closure so that it costs nothing and so two of them compare equal.
type unavailableHandler struct{}

var _ http.Handler = unavailableHandler{}

// ServeHTTP writes 503 with a message naming the framework.
func (unavailableHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "collage: application failed to start", http.StatusServiceUnavailable)
}

// buildHandler builds the HTTP handler once and memoises the result, successful or
// not. It marks the application started, constructs the handler over the App's
// subsystems, and then runs plugin Init with the App itself as the Host.
//
// It holds buildMu, never mu, while calling into plugin Init: a plugin's Init calls
// back into this App's Host methods, and those take mu.
func (a *App) buildHandler() (http.Handler, error) {
	a.buildMu.Lock()
	defer a.buildMu.Unlock()

	if a.handlerBuilt {
		return a.handler, a.handlerErr
	}
	a.handlerBuilt = true

	// Checked here rather than in New, because RegisterPlugin can add a plugin
	// after New and a key naming one of those would otherwise be reported as
	// unknown. This is the first point at which the whole plugin set is known, and
	// it is still startup: nothing has been served.
	if err := a.plugins.CheckConfigKeys(a.cfg.PluginConfig); err != nil {
		return a.buildFailed(err)
	}

	// Plugin Init runs before registration closes, because a plugin contributes to
	// the application: a page, a document, a mount, all through Host. Closing
	// registration first would refuse every one of them with ErrAppStarted, which
	// is what this used to do — the capability existed and nothing could use it.
	//
	// context.Background, not a request context: Init is startup work whose
	// lifetime is the process, and every plugin's Shutdown is what ends it.
	//
	// hostView, not a, is what goes across: a plugin holding the *App could assert
	// its way back to Shutdown, ListenAndServe, Handler, and RenderPath, which is
	// exactly what plugin.Host exists to keep out of reach. See host.go.
	if err := a.plugins.Init(context.Background(), func(name string) plugin.Host {
		return &hostView{app: a, name: name}
	}); err != nil {
		return a.buildFailed(err)
	}

	a.mu.Lock()
	a.started = true
	a.mu.Unlock()

	// Run once registration is closed — which now means after plugins have had
	// their say, so a page a plugin contributed is checked on the same terms as
	// one the application registered.
	if err := a.checkErrorPagesRegistered(); err != nil {
		return a.buildFailed(err)
	}

	// Also run at close-out, beside checkErrorPagesRegistered and for the same
	// reason: a mount's prefix must not swallow a page's or a document's route
	// regardless of which of the two was registered first, which only a check
	// that runs after registration closes can guarantee. Handler.ServeHTTP relies
	// on exactly this guarantee to check every mount before consulting the router
	// at all — see checkMountsDoNotShadow's own doc comment.
	if err := a.checkMountsDoNotShadow(); err != nil {
		return a.buildFailed(err)
	}

	// Here rather than at construction: whether a generated forgery key matters
	// depends on whether anything will verify a token, which is only known once
	// registration has closed.
	a.warnAboutGeneratedKey()

	handler, err := httpx.New(httpx.Deps{
		Router:       a.routes,
		Renderer:     a.renderer,
		Cache:        a.store,
		Tracker:      a.tracker,
		Plugins:      a.plugins,
		Metrics:      a.metrics,
		Tracer:       a.tracer,
		Logger:       a.logger,
		DevMode:      a.devMode,
		DefaultTTL:   a.cfg.Cache.DefaultTTL,
		Mounts:       a.Mounts(),
		Handlers:     a.handlers,
		DevSources:   a.devSources(),
		PageReady:    a.pageReady,
		Middleware:   a.middleware,
		MaxBodyBytes: a.cfg.Server.MaxBodyBytes,
		// An action asks for invalidation declaratively, and this is what
		// carries it out. Handing every handler the whole application so it
		// could call InvalidateTags itself would put the application inside a
		// function whose job is to answer one request.
		Invalidator: func(ctx context.Context, tags []string) error {
			return a.InvalidateTags(ctx, tags...)
		},
		CSRF: a.csrf,
	})
	if err != nil {
		return a.buildFailed(err)
	}

	a.handler = handler
	return handler, nil
}

// dataCacheFor returns the store behind collage.Cached as the render engine takes
// it: nil — an untyped nil, not a nil *Store in an interface — when there is none.
func (a *App) dataCacheFor() types.DataCache {
	if a.data == nil {
		return nil
	}
	return a.data
}

// pageReady reports whether p can render: a page with a layout renders only once
// registration has bound its content into it.
func (a *App) pageReady(p *types.Page) bool {
	if p == nil || p.LayoutFragment == nil {
		return true
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.bound[p]
}

// devSources are the file systems, besides the mounts, whose changes reload a
// development page: the templates, when they are read from disk.
func (a *App) devSources() []fs.FS {
	var sources []fs.FS
	if a.devTemplateDir != "" {
		sources = append(sources, os.DirFS(a.devTemplateDir))
	}
	for _, dir := range a.cfg.DevWatch {
		// A directory that is not there is not an error: the same configuration
		// runs from wherever the binary is started, and in production none of it
		// is read.
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			sources = append(sources, os.DirFS(dir))
		}
	}
	return sources
}

// buildFailed memoises a failed handler build: it logs err once, installs the
// unavailable handler so Handler has something to return, and hands the error back
// for ListenAndServe to surface. It must be called with buildMu held.
func (a *App) buildFailed(err error) (http.Handler, error) {
	a.handler = unavailableHandler{}
	a.handlerErr = err
	a.logger.Error("collage: starting the application failed", "error", err)
	return a.handler, err
}

// ListenAndServe builds the handler, starts an HTTP server on Config.Server's host
// and port, and blocks until the server stops. It sets all four of ServerConfig's
// timeouts — ReadTimeout (also used for ReadHeaderTimeout, so a slow-header client
// cannot hold a connection open indefinitely), WriteTimeout, IdleTimeout, and
// ShutdownTimeout, the last as the deadline for the graceful shutdown below.
//
// It traps SIGINT and SIGTERM and shuts down gracefully on either, and returns nil
// on a clean shutdown rather than http.ErrServerClosed: a server that stopped
// because it was asked to stop did not fail. A failure to bind the port, or a
// plugin Init that failed, is returned as an error.
//
// The one goroutine it starts sends to a buffered channel and exits as soon as the
// server stops, so it cannot outlive this call.
func (a *App) ListenAndServe() error {
	handler, err := a.buildHandler()
	if err != nil {
		return err
	}

	// Registered before the listener exists, so a signal arriving the instant the
	// port opens is already being trapped rather than killing the process.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	server := &http.Server{
		Addr:              net.JoinHostPort(a.cfg.Server.Host, strconv.Itoa(a.cfg.Server.Port)),
		Handler:           handler,
		ReadTimeout:       a.cfg.Server.ReadTimeout,
		ReadHeaderTimeout: a.cfg.Server.ReadTimeout,
		WriteTimeout:      a.cfg.Server.WriteTimeout,
		IdleTimeout:       a.cfg.Server.IdleTimeout,
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("collage: listen on %s: %w", server.Addr, err)
	}

	a.mu.Lock()
	if a.closing {
		// Shutdown already ran. Serving now would start something nothing is left
		// to stop, so close the listener and report the clean stop we were asked
		// for rather than blocking forever. listening is closed on this path too:
		// nothing will ever listen, and a waiter must learn that rather than wait
		// for a server that is never coming.
		a.mu.Unlock()
		a.listenOnce.Do(func() { close(a.listening) })
		return listener.Close()
	}
	a.server = server
	a.mu.Unlock()
	a.listenOnce.Do(func() { close(a.listening) })

	// Logged here rather than by the caller, because this is the first point at
	// which serving is a fact rather than an intention: the port is bound and the
	// handler is built. A caller that logs before calling ListenAndServe prints a
	// success message that a bind failure then contradicts one line later.
	//
	// The address comes from the listener, not from the configuration, so a
	// configured port of 0 reports the port the kernel actually chose.
	a.Logger().Info("collage: listening", "addr", listener.Addr().String())

	served := make(chan error, 1)
	go func() {
		served <- server.Serve(listener)
	}()

	select {
	case err := <-served:
		return cleanStop(err)
	case <-signals:
		a.Logger().Info("collage: shutting down", "timeout", a.cfg.Server.ShutdownTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
		defer cancel()
		shutdownErr := a.Shutdown(ctx)
		a.Logger().Info("collage: stopped")
		// Serve always returns once Shutdown has closed the listener, so this
		// receive cannot block the caller indefinitely and the goroutine above
		// cannot leak.
		return errors.Join(shutdownErr, cleanStop(<-served))
	}
}

// cleanStop maps http.ErrServerClosed — the error a server returns because it was
// asked to stop — to nil, and passes every other error through unchanged.
func cleanStop(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the running server, then the plugin registry, joining whatever
// errors either produced. ctx bounds how long the server waits for in-flight
// requests; ListenAndServe's own signal handler bounds it with
// ServerConfig.ShutdownTimeout.
//
// It is idempotent: the work runs exactly once and every caller — including one
// racing another — observes the same result. Calling it before ListenAndServe has
// started a server shuts the plugins down and prevents a later ListenAndServe from
// serving.
func (a *App) Shutdown(ctx context.Context) error {
	a.shutdownOnce.Do(func() {
		a.shutdownErr = a.shutdown(ctx)
	})
	return a.shutdownErr
}

// shutdown performs the one real shutdown Shutdown's sync.Once guards.
func (a *App) shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.closing = true
	server := a.server
	a.mu.Unlock()

	// buildMu, not mu, is what guards the handler.
	a.buildMu.Lock()
	current := a.handler
	a.buildMu.Unlock()

	// Before the server, which waits for open requests: a development page's
	// reload stream is one that never ends by itself.
	if handler, ok := current.(*httpx.Handler); ok {
		handler.CloseDevStreams()
	}

	var err error
	if server != nil {
		if serverErr := server.Shutdown(ctx); serverErr != nil {
			err = errors.Join(err, fmt.Errorf("collage: server shutdown: %w", serverErr))
		}
	}
	// Plugins stop after the server, not before: a plugin released while requests
	// are still in flight would be torn out from under them.
	return errors.Join(err, a.plugins.Shutdown(ctx))
}

// InvalidateTags invalidates every cache entry associated with any of tags. It is
// InvalidateTagsN with the count discarded, and is the form plugin.Host requires.
func (a *App) InvalidateTags(ctx context.Context, tags ...string) error {
	_, err := a.InvalidateTagsN(ctx, tags...)
	return err
}

// InvalidateTagsN invalidates every cache entry associated with any of tags and
// returns how many were removed.
//
// The dependency tracker is the authority: it resolves tags to the cache keys built
// from them, each key is dropped from the cache, plugins are told through
// OnCacheInvalidate, the keys are forgotten so they cannot resolve again, and the
// count is reported to Metrics.Invalidation. An empty tags invalidates nothing and
// is not an error.
//
// When the store also implements cache.TaggedCache, Cache.Invalidate is called with
// the tags as well. That is not redundant with the tracker walk, and it is the only
// thing that makes a shared store invalidatable at all. The tracker is per process
// and in memory: it knows the keys this instance wrote and nothing else, so with a
// store shared between instances — a Redis, say — instance A's tracker cannot name
// the keys instance B wrote, and a restart leaves every key written before it
// permanently unreachable by tag. A store that indexes tags itself, which is exactly
// what TaggedCache declares, can resolve them for the keys the tracker never saw.
// SetTagged already hands it those tags on every write; this is the call that
// finally reads them back.
//
// The count is the number of keys the tracker resolved from tags and the cache
// accepted an invalidation for — not a count of entries that were live at the time,
// and not a count of what the store's own tag index dropped, since Cache.Invalidate
// reports no count and inventing one would make the number mean different things for
// different caches. Cache.InvalidateKey is documented to succeed on a key that holds
// no entry, and reports no distinction either way, so a key whose entry had already
// expired is counted like any other. The count is therefore exactly what the tag
// reached according to the authority on that question, and an upper bound on live
// entries removed.
//
// A key that the cache fails to drop is not counted and does not stop the rest:
// every other key is still invalidated and the failures are joined into the
// returned error, because a partial invalidation that reports success is how stale
// pages survive a deploy.
func (a *App) InvalidateTagsN(ctx context.Context, tags ...string) (int, error) {
	if len(tags) == 0 {
		return 0, nil
	}

	keys, err := a.tracker.Resolve(ctx, tags)
	if err != nil {
		return 0, fmt.Errorf("collage: resolve tags: %w", err)
	}

	// The data first, so a page re-rendered the moment its entry is dropped
	// below cannot be built from the value being replaced.
	if a.data != nil {
		a.data.Invalidate(tags)
	}

	var failures error
	invalidated := 0
	for _, key := range keys {
		if a.store != nil {
			if err := a.store.InvalidateKey(ctx, key); err != nil {
				failures = errors.Join(failures, fmt.Errorf("collage: invalidate %q: %w", key, err))
				continue
			}
		}
		a.metrics.CacheEvent(ctx, observability.CacheInvalidate, key)
		invalidated++
	}

	// After the per-key walk, so a store that indexes tags itself and a tracker
	// that indexes them separately cannot disagree about a key both of them know:
	// the key-level invalidation has already run by the time the tag-level one
	// does, and both are idempotent.
	if tagged, ok := a.store.(cache.TaggedCache); ok {
		if err := tagged.Invalidate(ctx, tags); err != nil {
			failures = errors.Join(failures, fmt.Errorf("collage: invalidate tags %v: %w", tags, err))
		}
	}

	// The event carries its own copy of tags: CacheInvalidateEvent documents Tags
	// as the event's own, and a plugin must not be able to reach back into the
	// caller's slice through it.
	if err := a.plugins.CacheInvalidate(ctx, &plugin.CacheInvalidateEvent{
		Tags: append([]string(nil), tags...),
	}); err != nil {
		failures = errors.Join(failures, err)
	}

	for _, key := range keys {
		if err := a.tracker.Forget(ctx, key); err != nil {
			failures = errors.Join(failures, fmt.Errorf("collage: forget %q: %w", key, err))
		}
	}

	a.metrics.Invalidation(ctx, tags, invalidated)
	return invalidated, failures
}

// RenderPath renders the page registered at path, outside the HTTP request path and
// bypassing the cache entirely, and returns the raw render result. It is the seam
// the static site builder consumes through its own narrow interface.
//
// The page is resolved through the router, from a synthetic GET request for path,
// so a path reaches exactly the page it would reach over HTTP. A non-empty locale is
// a *request* for that locale: a path carrying no locale prefix is given the
// locale's own prefix, which is the URL that reaches it over HTTP.
//
// The locale the page actually renders for is whatever the router resolved, not the
// argument, and the two are the same thing whenever the argument had any effect. It
// matters when they differ: "/tr/blog" resolves to the tr page whatever the caller
// asked for, and rendering it with a RenderContext.Locale of "en" would put every
// locale-dependent fragment on that page into the wrong language. The router picked
// the page; the page's locale is the router's to report.
//
// params overlay the path parameters the router captured, so a caller that already
// knows the concrete values (a static build enumerating slugs) does not depend on
// the pattern re-capturing them.
//
// It returns ErrPageNotFound when path resolves to no page, including when it
// resolves to a redirect, and the render engine's own error when the render fails.
func (a *App) RenderPath(ctx context.Context, path, locale string, params map[string]string) (*render.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Startup runs first, memoised, so a render here happens in the state a served
	// render would happen in. Plugin Init is part of that, and skipping it meant a
	// plugin reading its configuration there ran on defaults during a static build
	// while running configured on the server — two different sites from one source,
	// with nothing saying so.
	if _, err := a.buildHandler(); err != nil {
		return nil, err
	}

	req := a.syntheticRequest(ctx, path, locale)

	match, err := a.routes.Match(req)
	if err != nil {
		return nil, fmt.Errorf("collage: match %q: %w", path, err)
	}
	if match == nil || match.Page == nil {
		if match != nil && match.RedirectTo != "" {
			return nil, fmt.Errorf("%w: %q redirects to %q", ErrPageNotFound, path, match.RedirectTo)
		}
		return nil, fmt.Errorf("%w: %q", ErrPageNotFound, path)
	}

	merged := make(map[string]string, len(match.PathParams)+len(params))
	maps.Copy(merged, match.PathParams)
	maps.Copy(merged, params)

	// match.Locale, not the locale argument: the router resolved a specific page for
	// a specific locale, and the page it chose is the page being rendered. Taking
	// the argument instead let the two disagree — RenderPath(ctx, "/tr/blog", "en",
	// nil) served the tr page with a RenderContext.Locale of "en", so every
	// locale-dependent fragment on it rendered in the wrong language.
	return a.renderResolved(ctx, req, match.Page, match.Locale, merged, path)
}

// renderResolved renders page for locale and returns what the hooks left behind.
// It is the half of RenderPath after the router has decided, shared with
// RenderNotFound — which has no router decision to make, because a not-found page
// is reached by failing to match rather than by matching.
func (a *App) renderResolved(
	ctx context.Context,
	req *http.Request,
	page *types.Page,
	locale string,
	params map[string]string,
	path string,
) (*render.Result, error) {
	rc := types.NewRenderContext(ctx, req, page, locale, params)

	// The render hooks fire here as well as in the HTTP handler, and that is the
	// point rather than a convenience.
	//
	// A static build renders through this method. Skipping the hooks meant a built
	// site was not what the server served: unminified where the server minified,
	// unannotated where it annotated, with image URLs the server had rewritten left
	// pointing at the origin — and nothing anywhere said so.
	//
	// PageResolved is deliberately not dispatched. Its contract is "once per
	// request, immediately after the router resolves it", and a build is not a
	// request; firing it would make every plugin counting requests count renders
	// that nobody asked for. BeforeRender and AfterRender are about a render, which
	// this unambiguously is, and they fire as a pair so a plugin that sets something
	// up in one and uses it in the other is not handed half of each.
	if err := a.plugins.BeforeRender(ctx, &plugin.BeforeRenderEvent{
		Context: rc,
		Page:    page,
		Locale:  locale,
		Path:    path,
	}); err != nil {
		return nil, err
	}

	result, err := a.renderer.Render(ctx, rc)
	if err != nil {
		return result, err
	}

	event := &plugin.AfterRenderEvent{
		Page:     page,
		Locale:   locale,
		Degraded: result.Degraded(),
		HTML:     result.HTML,
		Data:     rc.SharedData,
	}
	if err := a.plugins.AfterRender(ctx, event); err != nil {
		return nil, err
	}
	// What the hooks produced is what the caller gets, exactly as on the serving
	// path — otherwise the dispatch would be observation dressed as transformation.
	result.HTML = event.HTML
	return result, nil
}

// syntheticRequest builds the GET request RenderPath resolves and renders through.
// It is assembled by hand rather than parsed from a URL string: path is already a
// path, there is no host to invent, and a hand-built request has no error path to
// swallow. Fragments receive this request in their RenderContext, so it carries a
// realistic protocol and a usable header map.
//
// locale is reached the way a reader reaches it: through the URL. A path with no
// locale prefix is given the one it needs — see localePath — because the URL is
// the only thing that selects a locale.
func (a *App) syntheticRequest(ctx context.Context, path, locale string) *http.Request {
	path = a.localePath(path, locale)

	req := (&http.Request{
		Method:     http.MethodGet,
		URL:        &url.URL{Path: path},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Host:       a.cfg.Server.Host,
	}).WithContext(ctx)

	return req
}

// localePath returns the URL path that reaches path in locale over HTTP: path
// itself for the default locale, and path under the locale's prefix for any
// other. A path already carrying a supported locale prefix is left alone, and so
// is every path when path locales are off — the default locale is then the only
// one a request can reach.
func (a *App) localePath(path, locale string) string {
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	if locale == "" || locale == a.cfg.Locale.Default || a.cfg.Locale.DisablePathLocale {
		return path
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	for _, supported := range a.cfg.Locale.Supported {
		if strings.EqualFold(first, supported) {
			return path
		}
	}
	if path == "/" {
		return "/" + locale
	}
	return "/" + locale + path
}

// RenderNotFound renders the registered not-found page for locale, or reports that
// there is none with a nil result and a nil error.
//
// A not-found page is reached by failing to match, so it has no path and RenderPath
// cannot reach it. A static build needs it anyway: a static host answers an unknown
// URL with the site's own 404.html, and a site exported without one answers with
// whatever the host's default is.
//
// The page's render strategy is deliberately not consulted. Whether a page is worth
// caching and whether it belongs in a static export are different questions, and a
// not-found page declared Dynamic — which is the usual declaration, since it is
// never worth caching — would otherwise have no 404.html at all.
func (a *App) RenderNotFound(ctx context.Context, locale string) (*render.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := a.buildHandler(); err != nil {
		return nil, err
	}

	page := a.routes.NotFoundPage()
	if page == nil {
		return nil, nil
	}

	// A path that cannot be a route, because that is what the page is for: a
	// fragment reading rc.Request sees a request that did not match, which is the
	// truth about why it is rendering.
	const path = "/404"
	req := a.syntheticRequest(ctx, path, locale)

	return a.renderResolved(ctx, req, page, locale, map[string]string{}, path)
}
