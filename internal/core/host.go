package core

import (
	"context"
	"log/slog"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// hostView is the plugin.Host implementation a plugin receives in Init. It holds the
// App and forwards each of Host's six methods to it, and it has no other method of
// its own.
//
// It exists because handing the plugin registry the *App itself made Host's
// narrowing purely notional. plugin.Host's contract is that a plugin "has no way to
// reach the router, the cache, the render engine, the template set, or any page it
// was not explicitly handed" — but a value's method set travels with it through an
// interface, so a plugin given the *App could type-assert its Host parameter back to
// a wider interface and recover Shutdown, ListenAndServe, Handler, and RenderPath.
// That assertion is structural: it needs no import of pkg/collage and no name for
// the concrete type, so narrowing what pkg/collage exports would not have closed it
// either. Only passing a value that does not have those methods closes it, which is
// what this type is.
//
// The forwarding methods are deliberately not promoted from an embedded *App: an
// embedded field promotes the whole method set, which is the thing being prevented.
type hostView struct {
	app *App
}

// hostView, not *App, is what plugin.Init receives, so it is what must satisfy
// plugin.Host. The assertion is here rather than in a test so that dropping one of
// Host's methods fails to build rather than failing to run.
var _ plugin.Host = (*hostView)(nil)

// DevMode reports whether the application is running in development mode.
func (h *hostView) DevMode() bool {
	return h.app.DevMode()
}

// Pages returns every page registered with the application, each a defensive copy.
func (h *hostView) Pages() []*types.Page {
	return h.app.Pages()
}

// Page returns the page registered under name, as a defensive copy, and whether one
// was found.
func (h *hostView) Page(name string) (*types.Page, bool) {
	return h.app.Page(name)
}

// InvalidateTags invalidates every cache entry associated with any of tags.
func (h *hostView) InvalidateTags(ctx context.Context, tags ...string) error {
	return h.app.InvalidateTags(ctx, tags...)
}

// Logger returns the application's structured logger.
func (h *hostView) Logger() *slog.Logger {
	return h.app.Logger()
}

// RegisterCommand registers cmd as one of the application's CLI subcommands.
func (h *hostView) RegisterCommand(cmd plugin.Command) error {
	return h.app.RegisterCommand(cmd)
}
