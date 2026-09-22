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
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Elagoht/collage/internal/cache"
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

// ErrUnsupportedCache is returned by New when Config.Cache.Enabled is true and
// Config.Cache.Type names an implementation this package cannot build.
var ErrUnsupportedCache = errors.New("collage: unsupported cache type")

// ErrEmptyTemplateRoot is returned by New when Config.Template.Root is empty.
var ErrEmptyTemplateRoot = errors.New("collage: empty template root")

// defaultLocaleCookie is the cookie name locale resolution falls back to when
// Config.Locale.CookieName is empty, matching internal/router's own fallback.
const defaultLocaleCookie = "locale"

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
	// Logger is the structured logger the framework writes through and hands to
	// plugins via Host.Logger. A nil Logger means slog.Default().
	Logger *slog.Logger
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
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight
	// requests.
	ShutdownTimeout time.Duration
}

// TemplateConfig is internal/core's mirror of pkg/collage.TemplateConfig.
type TemplateConfig struct {
	// Root is the directory templates are loaded from.
	Root string
	// Extension is the file extension that identifies template files under Root.
	Extension string
	// DevMode reloads templates from disk on every render.
	DevMode bool
	// Timeout is the default DataHandler timeout used when a fragment sets none.
	Timeout time.Duration
}

// CacheConfig is internal/core's mirror of pkg/collage.CacheConfig.
type CacheConfig struct {
	// Enabled turns caching on. A disabled cache is a nil cache: every request
	// renders and nothing is ever stored.
	Enabled bool
	// Type selects the cache implementation. Only "memory" is built in.
	Type string
	// DefaultTTL is the cache entry lifetime used when a page sets none.
	DefaultTTL time.Duration
	// MaxEntries caps the number of cache entries.
	MaxEntries int
}

// LocaleConfig is internal/core's mirror of pkg/collage.LocaleConfig. Every
// Disable* field keeps the same negative polarity: the zero value leaves that
// locale source enabled.
type LocaleConfig struct {
	// Default is the locale used when none can be resolved from the request.
	Default string
	// Supported lists the locales the application serves.
	Supported []string
	// DisablePathLocale turns off resolving the locale from the request path.
	DisablePathLocale bool
	// DisableHeaderLocale turns off resolving the locale from Accept-Language.
	DisableHeaderLocale bool
	// CookieName is the cookie the locale is read from. Empty means "locale".
	CookieName string
	// DisableCookieLocale turns off resolving the locale from a cookie.
	DisableCookieLocale bool
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
	// logger is the structured logger handed to the handler, the plugin registry,
	// and plugins through Host.Logger.
	logger *slog.Logger

	// tmpl is the template engine every fragment renders through. It is also what
	// RegisterPage checks fragment template paths against.
	tmpl template.Engine
	// renderer composes a page's fragment tree into HTML.
	renderer render.Engine
	// store is the render output cache, or nil when caching is disabled. It is
	// declared as the interface and only ever assigned a non-nil implementation,
	// so "no cache" is a genuinely nil interface value, not a typed nil.
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
	// vary lists the request headers a rendered page's content depends on, derived
	// from the enabled locale sources and sent as the Vary header on publicly
	// cacheable responses.
	vary []string

	// mu guards everything below it: the page registry, the commands, the started
	// and closing flags, and the running server.
	mu sync.RWMutex
	// pages holds every registered page by name, including those registered as the
	// global not-found or error page.
	pages map[string]*types.Page
	// order holds the registered page names in registration order, so Pages is
	// deterministic.
	order []string
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
	// listening is closed once the server is accepting connections. It exists so
	// the lifecycle tests can wait for a real listener instead of sleeping.
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

// *App is the plugin.Host implementation plugins receive in Init. The assertion is
// here, not in a test, so a change that drops one of Host's methods fails to build
// rather than failing to run.
var _ plugin.Host = (*App)(nil)

// New builds an App from a validated configuration. cfg is expected to have been
// defaulted and validated already — pkg/collage.New does both before converting —
// so New applies only the fallbacks it needs to construct working subsystems: an
// empty Template.Extension becomes ".html", an empty Locale.Default becomes "en",
// and an empty Locale.CookieName becomes "locale". It returns ErrEmptyTemplateRoot
// for an empty Template.Root, ErrUnsupportedCache for an enabled cache of an
// unknown type, and a wrapped template error when the template root cannot be
// loaded.
//
// Everything is constructed here, once: New never starts a goroutine and never
// touches the network, so building an App is safe in a test.
func New(cfg Config) (*App, error) {
	if cfg.Template.Root == "" {
		return nil, ErrEmptyTemplateRoot
	}
	if cfg.Template.Extension == "" {
		cfg.Template.Extension = ".html"
	}
	if cfg.Locale.Default == "" {
		cfg.Locale.Default = "en"
	}
	if cfg.Locale.CookieName == "" {
		cfg.Locale.CookieName = defaultLocaleCookie
	}

	devMode := cfg.DevMode || cfg.Template.DevMode
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	tmpl, err := template.NewHTML(template.HTMLConfig{
		Root:      cfg.Template.Root,
		Extension: cfg.Template.Extension,
		DevMode:   devMode,
	})
	if err != nil {
		return nil, fmt.Errorf("collage: template engine: %w", err)
	}

	// One instance, shared by the render engine and the HTTP handler. See the
	// metrics field's doc comment for why that sharing is load-bearing.
	metrics := observability.MetricsOrNoop(cfg.Observability.Metrics)
	tracer := observability.TracerOrNoop(cfg.Observability.Tracer)

	var store cache.Cache
	if cfg.Cache.Enabled {
		switch cfg.Cache.Type {
		case "", "memory":
			store = cache.NewMemory(cache.MemoryConfig{
				DefaultTTL: cfg.Cache.DefaultTTL,
				MaxEntries: cfg.Cache.MaxEntries,
			})
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedCache, cfg.Cache.Type)
		}
	}

	app := &App{
		cfg:     cfg,
		devMode: devMode,
		logger:  logger,
		tmpl:    tmpl,
		renderer: render.New(tmpl, render.Options{
			DefaultTimeout: cfg.Template.Timeout,
			Metrics:        metrics,
			Tracer:         tracer,
			DevMode:        devMode,
		}),
		store:   store,
		tracker: dependency.NewMemory(),
		routes: router.New(router.LocaleOptions{
			Default:             cfg.Locale.Default,
			Supported:           cfg.Locale.Supported,
			DisablePathLocale:   cfg.Locale.DisablePathLocale,
			DisableHeaderLocale: cfg.Locale.DisableHeaderLocale,
			CookieName:          cfg.Locale.CookieName,
			DisableCookieLocale: cfg.Locale.DisableCookieLocale,
		}),
		plugins:   plugin.NewRegistry(logger),
		metrics:   metrics,
		tracer:    tracer,
		vary:      varyHeaders(cfg.Locale),
		pages:     make(map[string]*types.Page),
		listening: make(chan struct{}),
	}
	return app, nil
}

// varyHeaders returns the request headers a rendered page's content depends on,
// derived from the enabled locale sources: Accept-Language when header-locale
// resolution is on, Cookie when cookie-locale resolution is. This framework's own
// cache key already carries the resolved locale, so its cache was never at risk —
// but a shared cache between the handler and the client keys on the URL alone, and
// a locale negotiated from a header or a cookie is not in the URL. Without this, a
// CDN hands one visitor's language to the next.
//
// Path-locale resolution contributes nothing: it is in the URL already, so a shared
// cache distinguishes those representations without being told to.
func varyHeaders(cfg LocaleConfig) []string {
	var vary []string
	if !cfg.DisableHeaderLocale {
		vary = append(vary, "Accept-Language")
	}
	if !cfg.DisableCookieLocale {
		vary = append(vary, "Cookie")
	}
	return vary
}

// DevMode reports whether the application is running in development mode: the
// disjunction of Config.DevMode and Config.Template.DevMode, resolved at New.
func (a *App) DevMode() bool {
	return a.devMode
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
// the build fails — in practice, when a plugin's Init fails — the failure is logged
// once, at error level, and Handler returns a handler that answers every request
// with 503. ListenAndServe surfaces the same failure as a returned error instead,
// so a program that starts a server never loses it.
func (a *App) Handler() http.Handler {
	handler, err := a.buildHandler()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "collage: application failed to start", http.StatusServiceUnavailable)
		})
	}
	return handler
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

	a.mu.Lock()
	a.started = true
	a.mu.Unlock()

	handler, err := httpx.New(httpx.Deps{
		Router:     a.routes,
		Renderer:   a.renderer,
		Cache:      a.store,
		Tracker:    a.tracker,
		Plugins:    a.plugins,
		Metrics:    a.metrics,
		Tracer:     a.tracer,
		Logger:     a.logger,
		DevMode:    a.devMode,
		DefaultTTL: a.cfg.Cache.DefaultTTL,
		Vary:       a.vary,
	})
	if err != nil {
		a.handlerErr = err
		a.logger.Error("collage: building the HTTP handler failed", "error", err)
		return nil, err
	}

	// context.Background, not a request context: Init is startup work whose
	// lifetime is the process, and every plugin's Shutdown is what ends it.
	if err := a.plugins.Init(context.Background(), a); err != nil {
		a.handlerErr = err
		a.logger.Error("collage: plugin initialisation failed", "error", err)
		return nil, err
	}

	a.handler = handler
	return handler, nil
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
		// for rather than blocking forever.
		a.mu.Unlock()
		return listener.Close()
	}
	a.server = server
	a.mu.Unlock()
	a.listenOnce.Do(func() { close(a.listening) })

	served := make(chan error, 1)
	go func() {
		served <- server.Serve(listener)
	}()

	select {
	case err := <-served:
		return cleanStop(err)
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
		defer cancel()
		shutdownErr := a.Shutdown(ctx)
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
// count is reported to Metrics. An empty tags invalidates nothing and is not an
// error.
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
// so a path reaches exactly the page it would reach over HTTP. When locale is
// non-empty it is the locale the page renders for, and it is also offered to the
// router through the Accept-Language header and the locale cookie — whichever of
// those sources the configuration leaves enabled — so a path carrying no locale
// prefix still resolves to the requested locale. An empty locale renders for
// whatever locale the router resolves. With both of those sources disabled, a path
// must carry its own locale prefix to reach a page registered only under that
// locale — which is the only URL that reaches it over HTTP either way.
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

	resolved := locale
	if resolved == "" {
		resolved = match.Locale
	}

	merged := make(map[string]string, len(match.PathParams)+len(params))
	maps.Copy(merged, match.PathParams)
	maps.Copy(merged, params)

	return a.renderer.Render(ctx, types.NewRenderContext(ctx, req, match.Page, resolved, merged))
}

// syntheticRequest builds the GET request RenderPath resolves and renders through.
// It is assembled by hand rather than parsed from a URL string: path is already a
// path, there is no host to invent, and a hand-built request has no error path to
// swallow. Fragments receive this request in their RenderContext, so it carries a
// realistic protocol and a usable header map.
func (a *App) syntheticRequest(ctx context.Context, path, locale string) *http.Request {
	if path == "" || path[0] != '/' {
		path = "/" + path
	}

	req := (&http.Request{
		Method:     http.MethodGet,
		URL:        &url.URL{Path: path},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Host:       a.cfg.Server.Host,
	}).WithContext(ctx)

	if locale == "" {
		return req
	}
	if !a.cfg.Locale.DisableHeaderLocale {
		req.Header.Set("Accept-Language", locale)
	}
	if !a.cfg.Locale.DisableCookieLocale {
		req.AddCookie(&http.Cookie{Name: a.localeCookieName(), Value: locale})
	}
	return req
}

// localeCookieName returns the configured locale cookie name, falling back to the
// same default internal/router uses for an empty one.
func (a *App) localeCookieName() string {
	if a.cfg.Locale.CookieName == "" {
		return defaultLocaleCookie
	}
	return a.cfg.Locale.CookieName
}
