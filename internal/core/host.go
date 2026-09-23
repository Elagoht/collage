package core

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/Elagoht/collage/internal/asset"
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
	// name is the plugin this view was made for, so Config can find its section.
	name string
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

// Config decodes this plugin's section of the application's plugin configuration
// into v.
func (h *hostView) Config(v any) error { // any: restates encoding/json's own parameter type
	return plugin.DecodeConfig(h.app.cfg.PluginConfig, h.name, v)
}

// RegisterPage registers a page the plugin contributes.
//
// It goes through the application's own RegisterPage, so a plugin's page collides
// with the application's on exactly the same terms — a duplicate name, a path
// another route claims — and fails at startup rather than being decided by
// registration order.
func (h *hostView) RegisterPage(page *types.Page) error {
	return h.app.RegisterPage(page)
}

// RegisterDocument registers a document the plugin contributes.
func (h *hostView) RegisterDocument(doc *types.Document) error {
	return h.app.RegisterDocument(doc)
}

// Mount serves fsys under prefix, on the same terms as the application's own Mount:
// a prefix shadowing a registered route is refused when the handler is built.
func (h *hostView) Mount(prefix string, fsys fs.FS, opts ...asset.Option) error {
	return h.app.Mount(prefix, fsys, opts...)
}

// configHostView is the plugin.ConfigHost a plugin receives in Configure.
//
// It is a separate type from hostView rather than a subset of it because the two
// phases genuinely offer different things: at Configure time the application has
// registered nothing, so Pages would return an empty slice and InvalidateTags would
// have no cache to reach. Handing over a Host whose methods are all technically
// callable and mostly meaningless is worse than handing over a smaller interface.
type configHostView struct {
	app  *App
	name string
}

var _ plugin.ConfigHost = (*configHostView)(nil)

// DevMode reports whether the application is running in development mode.
func (h *configHostView) DevMode() bool { return h.app.cfg.DevMode || h.app.cfg.Template.DevMode }

// Logger returns the application's structured logger.
func (h *configHostView) Logger() *slog.Logger { return h.app.logger }

// Config decodes this plugin's section of the application's plugin configuration
// into v.
func (h *configHostView) Config(v any) error { // any: restates encoding/json's own parameter type
	return plugin.DecodeConfig(h.app.cfg.PluginConfig, h.name, v)
}

// AddTemplateFunc registers fn under name, for every template parsed after this
// call — which, since Configure runs before parsing, means all of them.
func (h *configHostView) AddTemplateFunc(name string, fn any) error { // any: restates html/template.FuncMap's own value type
	if _, taken := h.app.pluginFuncs[name]; taken {
		return fmt.Errorf("%w: %q", plugin.ErrDuplicateTemplateFunc, name)
	}
	h.app.pluginFuncs[name] = fn
	return nil
}

// WrapMount registers a transformation applied to every mounted filesystem.
func (h *configHostView) WrapMount(wrap func(fs.FS) fs.FS) {
	if wrap == nil {
		return
	}
	h.app.mountWrappers = append(h.app.mountWrappers, wrap)
}
