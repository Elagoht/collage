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

// Cache stores rendered pages keyed by request identity. Implement it to replace
// the built-in in-memory cache.
type Cache = cache.Cache

// Metrics receives the framework's counters and timings. Implement it to bridge the
// framework into a metrics backend; a nil Metrics means no-op.
type Metrics = observability.Metrics

// Tracer starts one span per request, render, and fragment. Implement it to bridge
// the framework into a tracing backend; a nil Tracer means no-op.
type Tracer = observability.Tracer

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
