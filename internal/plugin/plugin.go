// Package plugin defines the plugin system: the Plugin contract, the narrow Host
// capability surface plugins receive at startup, and a Registry that owns
// registration, lifecycle, and hook dispatch. The specification says a plugin
// "cannot mutate core state directly"; this package enforces that structurally
// rather than by convention, by handing Init a small read-mostly Host instead of
// the whole application, and by making mutation explicit — a field like
// AfterRenderEvent.HTML or CacheWriteEvent.Skip — everywhere it is actually
// intended.
//
// This package must not import internal/render or pkg/collage: it is composed by
// the app layer on top of render, not the other way around, and internal packages
// never depend on pkg/collage.
package plugin

import (
	"context"
	"log/slog"

	"github.com/Elagoht/collage/internal/types"
)

// Plugin is the minimum contract every plugin implements. Everything beyond
// lifecycle — reacting to a render, a cache write, an error, and so on — is opt-in:
// a plugin implements whichever hook interfaces below it needs, and the Registry
// discovers them by type assertion.
type Plugin interface {
	// Name identifies the plugin. It must be non-empty and unique among the
	// plugins registered on the same Registry.
	Name() string
	// Version reports the plugin's own version string, for diagnostics.
	Version() string
	// Init prepares the plugin to run, using host to read startup state and
	// register commands. Init runs once, in registration order, before the first
	// request is served. A returned error aborts startup: see Registry.Init.
	Init(ctx context.Context, host Host) error
	// Shutdown releases whatever the plugin acquired in Init. It runs once, in
	// reverse registration order, either at normal shutdown or to roll back a
	// failed Registry.Init.
	Shutdown(ctx context.Context) error
}

// Host is the capability surface a plugin receives in Init. It exposes what
// plugins may read and the few things they may do; it deliberately offers no way
// to mutate pages, fragments, or the router after startup — that is how "plugins
// cannot mutate core state" is enforced structurally rather than by convention.
type Host interface {
	// DevMode reports whether the application is running in development mode.
	DevMode() bool
	// Pages returns every page registered with the application.
	Pages() []*types.Page
	// Page returns the page registered under name, and whether one was found.
	Page(name string) (*types.Page, bool)
	// InvalidateTags invalidates every cache entry associated with any of tags.
	InvalidateTags(ctx context.Context, tags ...string) error
	// Logger returns the application's structured logger.
	Logger() *slog.Logger
	// RegisterCommand registers cmd with the application's CLI. It is consumed by
	// the CLI (Task 13); a Host implementation that has no CLI may simply store
	// cmd or reject it, at its own discretion.
	RegisterCommand(cmd Command) error
}

// Command is a CLI subcommand a plugin contributes via Host.RegisterCommand.
type Command struct {
	// Name is the subcommand's name, as typed on the command line.
	Name string
	// Usage is a short usage string shown in help output.
	Usage string
	// Short is a one-line description of what the command does.
	Short string
	// Run executes the command with the given arguments.
	Run func(ctx context.Context, args []string) error
}
