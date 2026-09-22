package collage

import (
	"errors"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
)

// ErrNilConfig is returned by New when passed a nil *Config. A nil config is a
// programming mistake rather than a request for the defaults: Config carries the
// template root, and silently inventing one would put the application's templates
// somewhere the author never chose.
var ErrNilConfig = errors.New("collage: nil config")

// App owns every subsystem and the server lifecycle. It is an alias for
// internal/core's App, not a wrapper, so the methods documented there — RegisterPage,
// RegisterNotFoundPage, RegisterErrorPage, RegisterPlugin, RegisterCommand,
// Commands, Pages, Page, DevMode, Logger, InvalidateTags, InvalidateTagsN,
// RenderPath, Handler, ListenAndServe, and Shutdown — are called directly on the
// value New returns, with no forwarding layer in between.
type App = core.App

// Plugin is the minimum contract every plugin implements: Name, Version, Init, and
// Shutdown. A plugin opts into the framework's hooks by additionally implementing
// whichever hook interfaces it needs.
type Plugin = plugin.Plugin

// Host is the capability surface a plugin receives in Init. *App implements it.
type Host = plugin.Host

// Command is a CLI subcommand a plugin contributes through Host.RegisterCommand.
type Command = plugin.Command

// PageResolvedHook is implemented by a plugin that wants to observe a request
// having been resolved to a page, before rendering begins.
type PageResolvedHook = plugin.PageResolvedHook

// BeforeRenderHook is implemented by a plugin that wants to observe a render about
// to start. Unlike PageResolvedHook it does not fire when a cached render is served
// instead of a fresh one.
type BeforeRenderHook = plugin.BeforeRenderHook

// AfterRenderHook is implemented by a plugin that wants to observe, or post-process,
// a render that just completed. It may replace the event's HTML.
type AfterRenderHook = plugin.AfterRenderHook

// CacheWriteHook is implemented by a plugin that wants to observe, or adjust, a
// render result about to be written to the cache. It may suppress the write or
// change its TTL and tags.
type CacheWriteHook = plugin.CacheWriteHook

// CacheInvalidateHook is implemented by a plugin that wants to observe a cache
// invalidation.
type CacheInvalidateHook = plugin.CacheInvalidateHook

// ErrorHook is implemented by a plugin that wants to observe a failure encountered
// while serving a request.
type ErrorHook = plugin.ErrorHook

// PageResolvedEvent describes a request having been resolved to a page.
type PageResolvedEvent = plugin.PageResolvedEvent

// BeforeRenderEvent describes a render about to start.
type BeforeRenderEvent = plugin.BeforeRenderEvent

// AfterRenderEvent describes a render that just completed. A plugin may replace its
// HTML field to post-process the page.
type AfterRenderEvent = plugin.AfterRenderEvent

// CacheWriteEvent describes a render result about to be written to the cache. A
// plugin may set its Skip field or adjust its TTL and Tags.
type CacheWriteEvent = plugin.CacheWriteEvent

// CacheInvalidateEvent describes a cache invalidation that has just happened.
type CacheInvalidateEvent = plugin.CacheInvalidateEvent

// ErrorEvent describes a failure encountered while serving a request.
type ErrorEvent = plugin.ErrorEvent

// Cache stores rendered pages keyed by request identity. Implement it to replace
// the built-in in-memory cache.
type Cache = cache.Cache

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
)

// ErrAppStarted is returned by the registration methods once the application has
// started.
var ErrAppStarted = core.ErrAppStarted

// ErrNilPage is returned by the page registration methods when passed a nil page.
var ErrNilPage = core.ErrNilPage

// ErrDuplicatePage is returned when a page is registered under a name another page
// already holds.
var ErrDuplicatePage = core.ErrDuplicatePage

// ErrTemplateNotFound is returned when a page's fragment names a template the engine
// has not loaded — at registration, and again at render time if one is somehow
// reached.
var ErrTemplateNotFound = core.ErrTemplateNotFound

// ErrUnregisteredErrorPage is returned when the application starts and a registered
// page references a NotFoundPage or ErrorPage that was never registered itself. An
// unregistered error page never has its content bound into its layout, so it would
// silently render empty at the moment it was needed.
var ErrUnregisteredErrorPage = core.ErrUnregisteredErrorPage

// ErrPageNotFound is returned by App.RenderPath when a path resolves to no page.
var ErrPageNotFound = core.ErrPageNotFound

// ErrEmptyCommandName is returned by App.RegisterCommand for a command with no name.
var ErrEmptyCommandName = core.ErrEmptyCommandName

// ErrDuplicateCommand is returned by App.RegisterCommand for a name another command
// already holds.
var ErrDuplicateCommand = core.ErrDuplicateCommand

// ErrUnsupportedCache is returned by New when Cache.Enabled is set and Cache.Type
// names an implementation the framework cannot build.
var ErrUnsupportedCache = core.ErrUnsupportedCache

// New builds an App from cfg: it applies the framework's defaults to every field
// left at its zero value, validates the result, converts it into the internal
// configuration, and constructs the application.
//
// cfg is mutated in place by the defaulting step, so the caller can read back
// exactly what the application was built with. Passing nil returns ErrNilConfig.
//
// Every failure mode is reported here rather than at the first request: an invalid
// configuration (see Config.Validate), a template root that does not exist, or a
// template that does not parse.
func New(cfg *Config) (*App, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return core.New(toCoreConfig(cfg))
}

// toCoreConfig converts the public Config into internal/core's mirror of it. The
// conversion is written out field by field on purpose: the compiler cannot tell us
// that the two structs have drifted apart, so this function is the one place where
// adding a field to one and forgetting the other becomes visible.
func toCoreConfig(cfg *Config) core.Config {
	return core.Config{
		DevMode: cfg.DevMode,
		Logger:  cfg.Logger,
		Server: core.ServerConfig{
			Host:            cfg.Server.Host,
			Port:            cfg.Server.Port,
			ReadTimeout:     cfg.Server.ReadTimeout,
			WriteTimeout:    cfg.Server.WriteTimeout,
			IdleTimeout:     cfg.Server.IdleTimeout,
			ShutdownTimeout: cfg.Server.ShutdownTimeout,
		},
		Template: core.TemplateConfig{
			Root:      cfg.Template.Root,
			Extension: cfg.Template.Extension,
			DevMode:   cfg.Template.DevMode,
			Timeout:   cfg.Template.Timeout,
		},
		Cache: core.CacheConfig{
			Enabled:    cfg.Cache.Enabled,
			Type:       cfg.Cache.Type,
			DefaultTTL: cfg.Cache.DefaultTTL,
			MaxEntries: cfg.Cache.MaxEntries,
		},
		Locale: core.LocaleConfig{
			Default:             cfg.Locale.Default,
			Supported:           cfg.Locale.Supported,
			DisablePathLocale:   cfg.Locale.DisablePathLocale,
			DisableHeaderLocale: cfg.Locale.DisableHeaderLocale,
			CookieName:          cfg.Locale.CookieName,
			DisableCookieLocale: cfg.Locale.DisableCookieLocale,
		},
		Observability: core.ObservabilityConfig{
			Metrics: cfg.Observability.Metrics,
			Tracer:  cfg.Observability.Tracer,
		},
	}
}
