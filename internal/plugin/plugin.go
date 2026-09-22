// Package plugin defines the plugin system: the Plugin contract, the narrow Host
// capability surface plugins receive at startup, and a Registry that owns
// registration, lifecycle, and hook dispatch.
//
// The specification says a plugin "cannot mutate core state directly." Host
// enforces the reachability half of that structurally: Init receives Host, not
// the whole application, so a plugin has no way to obtain the router, the cache,
// the render engine, the template set, or any page it was not explicitly handed.
// Host does NOT make what it hands out immutable, and this package does not
// claim otherwise. *types.Page is a plain struct of exported fields; a plugin
// holding one — from a Host.Page/Pages call or from an event's Page field — can
// write straight through it. The per-request event types in hooks.go deliberately
// carry the framework's live *types.Page rather than a copy, since copying a
// page (and, transitively, its fragment tree) on every render would defeat a
// cache-first framework's hot path; see the Host doc comment for where a defensive
// copy is required instead (Pages and Page, both startup/CLI-path calls where the
// allocation is free). Mutating a live Page is a data race, not merely something
// this package leaves undefined: hooks run on request goroutines, so a write to a
// live Page races every concurrent request reading it. Under -race the detector
// reports it; without the detector it corrupts whichever map or slice was written
// to. This package does not defend against it.
//
// This is deliberate, not an oversight: in-process Go plugins are trusted code,
// not a sandbox boundary. The goal is to make accidental mutation hard and
// deliberate mutation obvious — not to make mutation impossible, which Go's type
// system cannot give us anyway without copying. Where mutation is actually
// intended, it is explicit: AfterRenderEvent.HTML, and CacheWriteEvent's
// Skip/TTL/Tags.
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

// Host is the capability surface a plugin receives in Init. It limits
// *reachability*: a plugin gets only what Host exposes, with no way to reach the
// router, the cache, the render engine, the template set, or any page it was not
// explicitly handed through Pages or Page — that is the structural half of
// "plugins cannot mutate core state."
//
// It does NOT make what it hands out immutable. Pages and Page return
// *types.Page, a plain struct of exported fields, so a plugin holding one can
// write through it. To keep that from corrupting the framework's own registered
// pages, implementations of Pages and Page MUST return a defensive copy: the
// Page struct itself copied, along with its Paths, Redirects, SEO, and
// DependencyTags containers (so mutating those on the returned value cannot
// reach the original). The copy's LayoutFragment, ContentFragment, NotFoundPage,
// and ErrorPage pointers are left shared with the original, not deep-copied —
// fragment mutation is a separate concern this interface does not attempt to
// guard against either, and these two calls are startup/CLI-path, so the
// shallow-copy cost is irrelevant. internal/core's hostView implements Host and is
// held to this.
//
// A copied container is copied one level deep, which is as far as this can go
// without reflection: replacing a Page.SEO entry on the copy is safe, but writing
// through a value that entry holds — SEO values are opaque to the framework, so
// one may be a map, a slice, or a pointer — reaches the original.
//
// The per-request event types in hooks.go are a deliberate exception: they carry
// the framework's live *types.Page rather than a copy, to avoid that allocation
// on every render. A plugin that mutates a Page reached that way is on its own;
// see the package doc comment.
type Host interface {
	// DevMode reports whether the application is running in development mode.
	DevMode() bool
	// Pages returns every page registered with the application, each a defensive
	// copy — see the Host doc comment for exactly what "defensive copy" means
	// here. Mutating a returned Page does not affect the framework's own, with the
	// one limit that a copied container is copied one level deep: replacing a
	// Page.SEO entry on the copy is safe, but writing through a value that entry
	// holds — a nested map, slice, or pointer — reaches the original.
	Pages() []*types.Page
	// Page returns the page registered under name, and whether one was found.
	// The returned Page is a defensive copy, on the same terms as Pages.
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
