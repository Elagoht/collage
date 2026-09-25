// Package plugin defines the plugin system: the Plugin contract, the narrow Host
// capability surface plugins receive at startup, and a Registry that owns
// registration, lifecycle, and hook dispatch.
//
// The framework's rule is that a plugin cannot mutate core state directly. Host
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
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/Elagoht/collage/internal/asset"
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
	// the CLI; a Host implementation that has no CLI may simply store
	// cmd or reject it, at its own discretion.
	RegisterCommand(cmd Command) error
	// Config decodes this plugin's section of the application's plugin
	// configuration into v, leaving v untouched when the plugin has no section —
	// so v carries the plugin's defaults in and comes back either unchanged or
	// overlaid.
	Config(v any) error // any: restates encoding/json's own parameter type
	// RegisterPage registers a page the plugin contributes. It fails on the same
	// terms as the application's own registration — a duplicate name, a path
	// another route already claims — and for the same reason: a plugin's page
	// colliding with the application's is a startup error, not a race decided by
	// registration order.
	RegisterPage(page *types.Page) error
	// RegisterDocument registers a document the plugin contributes.
	RegisterDocument(doc *types.Document) error
	// Mount serves fsys under prefix. A plugin that rewrites URLs into its own
	// namespace — an image optimiser, say — uses this to serve what it rewrote to.
	Mount(prefix string, fsys fs.FS, opts ...asset.Option) error
	// Handle serves handler for every request under prefix, on the same terms as
	// the application's own Handle: a prefix ending in "/", refused when it shadows
	// a registered route. It is for what a file system cannot answer — an event
	// stream, a WebSocket.
	Handle(prefix string, handler http.Handler) error
	// RenderFragment renders one fragment a page opened at its own URL with
	// WithFragmentPath, for r, as a request to that URL would — and hands back the
	// parts rather than a response, so it can travel over a stream the plugin
	// owns. A fragment the page did not open is ErrUnknownFragmentPath: nothing is
	// reachable here that is not reachable over HTTP.
	RenderFragment(r *http.Request, req FragmentRequest) (*FragmentRender, error)
}

// FragmentRequest names a fragment a page opened at its own URL, and the locale
// and path parameters to render it in.
type FragmentRequest struct {
	// Page is the page's registered name.
	Page string
	// Fragment is the fragment's name, as WithFragmentPath was given it.
	Fragment string
	// Locale is the locale to render in; empty is the default one.
	Locale string
	// Params are the path parameters, as the fragment's path would have captured
	// them.
	Params map[string]string
}

// FragmentRender is one fragment rendered on its own, in parts.
type FragmentRender struct {
	// HTML is the fragment's markup, with this reader's forgery token in any form
	// it holds.
	HTML []byte
	// Head is what the fragment hoisted into an area it placed no marker for —
	// what the page's layout would have received — in the page's own order. Each
	// item's Key is the one the page's head deduplicated by.
	Head []types.HoistItem
	// DependencyTags are the tags the render depended on. A plugin pushing
	// fragments matches them against CacheInvalidateEvent.Tags.
	DependencyTags []string
	// Shared reports that the render is the same for every reader of the same
	// page, fragment, locale and parameters: the page is one the framework caches
	// for every reader, or nothing in the subtree has a data handler not declared
	// Static or a slot resolver — and the markup carries no forgery token. Only a shared render may be rendered once and sent to many
	// readers; any other may hold one reader's data.
	Shared bool
	// Cookie is the forgery cookie the forms in HTML are checked against, when r
	// carried none. It must reach the reader before one of those forms is
	// submitted, or the submission is refused. Nil when there is nothing to set.
	Cookie *http.Cookie
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
