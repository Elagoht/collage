package core

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// hostView is the plugin.Host implementation a plugin receives in Init. It holds the
// App and forwards each of Host's methods to it, and it has no other method of
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
	// configSource is this plugin's name and its own section of the plugin
	// configuration, which collage.PluginConfig reads. Embedded under an
	// unexported name, and holding only unexported fields, so the value has no
	// exported field a plugin could read another plugin's section through; its
	// one method is unexported, so embedding it promotes nothing a plugin could
	// call.
	configSource
	app *App
	// name is the plugin this view was made for, so Use knows whose middleware
	// it is adding.
	name string
}

// configSource is plugin.ConfigSource under a name of this package's own: an
// embedded field is named after its type, and an alias names it unexported.
type configSource = plugin.ConfigSource

// hostView, not *App, is what plugin.Init receives, so it is what must satisfy
// plugin.Host. The assertion is here rather than in a test so that dropping one of
// Host's methods fails to build rather than failing to run.
var _ plugin.Host = (*hostView)(nil)

// OriginFor returns host's public origin. See App.OriginFor.
func (h *hostView) OriginFor(ctx context.Context, host string) string {
	return h.app.OriginFor(ctx, host)
}

// MultiOrigin reports whether a plugin implementing OriginResolver is registered.
func (h *hostView) MultiOrigin() bool { return h.app.plugins.HasOriginResolver() }

// DevMode reports whether the application is running in development mode.
func (h *hostView) DevMode() bool {
	return h.app.DevMode()
}

// BaseURL returns the site's public origin without a trailing slash, or "" when
// the application set none.
func (h *hostView) BaseURL() string {
	return h.app.BaseURL()
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

// Logger returns the application's structured logger.
func (h *hostView) Logger() *slog.Logger {
	return h.app.Logger()
}

// RegisterCommand registers cmd as one of the application's CLI subcommands.
// A plugin's command may not take one of the collage binary's own names.
func (h *hostView) RegisterCommand(cmd plugin.Command) error {
	return h.app.registerCommand(cmd, true)
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

// OpenMounted opens the file a URL path under one of the application's mounts
// is served from.
func (h *hostView) OpenMounted(urlPath string) (fs.File, error) {
	return h.app.OpenMounted(urlPath)
}

// Handle serves handler under prefix, on the same terms as the application's own
// Handle but one: inside the middleware chain, always. A plugin's endpoint is
// part of the site — an event stream reading the reader's session or tenant, a
// /metrics a basicauth was registered to guard — not an application's own API
// that brings its own middleware, so it keeps what every mount had before
// v0.67.0, and the Host interface keeps its shape.
//
// Its request body is bounded at Server.MaxBodyBytes before the chain, as a
// page's is, so another plugin's middleware reading a body there reads at
// most that much; the handler reads the same bounded body.
func (h *hostView) Handle(prefix string, handler http.Handler) error {
	return h.app.Handle(prefix, handler, WithMiddleware(), func(m *httpx.HandlerMount) { m.Bounded = true })
}

// RenderFragment renders one fragment a page opened at its own URL, in parts.
func (h *hostView) RenderFragment(r *http.Request, req plugin.FragmentRequest) (*plugin.FragmentRender, error) {
	return h.app.RenderFragment(r, req)
}

// Use adds middleware where the plugin was registered: for one in
// Config.Plugins, outside the application's own.
func (h *hostView) Use(middleware func(http.Handler) http.Handler) error {
	return h.app.usePlugin(h.name, middleware)
}

// URL builds the path of a page or document by name.
func (h *hostView) URL(name, locale string, params map[string]string) (string, error) {
	return h.app.URL(name, locale, params)
}

// Locales returns the default locale and every supported one.
func (h *hostView) Locales() (string, []string) {
	return h.app.Locales()
}

// PageURLs returns every URL a page answers.
func (h *hostView) PageURLs(ctx context.Context, name string) ([]plugin.PageURL, error) {
	return h.app.PageURLs(ctx, name)
}

// BuildID names the build of the program serving.
func (h *hostView) BuildID() string { return h.app.BuildID() }

// ServeStatus answers r with status and the application's page for it.
func (h *hostView) ServeStatus(w http.ResponseWriter, r *http.Request, status int) {
	h.app.ServeStatus(w, r, status)
}

// configHostView is the plugin.ConfigHost a plugin receives in Configure.
//
// It is a separate type from hostView rather than a subset of it because the two
// phases genuinely offer different things: at Configure time the application has
// registered nothing, so Pages would return an empty slice and there would be no
// cache to reach. Handing over a Host whose methods are all technically
// callable and mostly meaningless is worse than handing over a smaller interface.
type configHostView struct {
	// configSource is what collage.PluginConfig reads; see hostView.
	configSource
	app *App
	// name is the plugin this view was made for.
	name string
	// closed is set when this plugin's Configure returns. A plugin can keep
	// its ConfigHost, and the maps AddTemplateFunc and AddRenderFunc write are
	// read by every render from then on: a write after Configure would race
	// them, and could not reach a template parsed already anyway.
	closed atomic.Bool
}

var _ plugin.ConfigHost = (*configHostView)(nil)

// ApplicationPlugins is plugin.Lister: a plugin looking for another in Configure
// finds it whether it is listed before or after. Only the ConfigHost has it;
// hostView, the Init host, does not, so no plugin reaches another's live
// instance from Init or at request time.
func (h *configHostView) ApplicationPlugins() []plugin.Plugin { return h.app.plugins.Plugins() }

// DevMode reports whether the application is running in development mode.
func (h *configHostView) DevMode() bool { return h.app.cfg.DevMode }

// Logger returns the application's structured logger.
func (h *configHostView) Logger() *slog.Logger { return h.app.logger }

// AddTemplateFunc registers fn under name, for every template parsed after this
// call — which, since Configure runs before parsing, means all of them.
func (h *configHostView) AddTemplateFunc(name string, fn any) error { // any: restates html/template.FuncMap's own value type
	if h.closed.Load() {
		return fmt.Errorf("%w: AddTemplateFunc(%q)", plugin.ErrConfigureClosed, name)
	}
	if _, taken := h.app.pluginFuncs[name]; taken {
		return fmt.Errorf("%w: %q", plugin.ErrDuplicateTemplateFunc, name)
	}
	// A plugin's function under a built-in's name would replace the built-in
	// every template was written against, without a word; AddRenderFunc refuses
	// the same names.
	if err := reservedFunc(name); err != nil {
		return err
	}
	h.app.pluginFuncs[name] = fn
	return nil
}

// reservedFunc refuses a name a plugin's template function may not take: one
// of the framework's built-ins, or one of the template language's own — "eq",
// "len", "html", "urlquery" — which would replace it in every template of the
// site, the escapers included. ErrDuplicateTemplateFunc, as for a name another
// plugin holds: two parties would be claiming one name.
func reservedFunc(name string) error {
	if _, reserved := template.DefaultFuncs()[name]; reserved {
		return fmt.Errorf("%w: %q is the framework's own", plugin.ErrDuplicateTemplateFunc, name)
	}
	if template.LanguageBuiltin(name) {
		return fmt.Errorf("%w: %q is the template language's own", plugin.ErrDuplicateTemplateFunc, name)
	}
	return nil
}

// AddRenderFunc registers a template function made anew for each render.
func (h *configHostView) AddRenderFunc(name string, factory func(rc *types.RenderContext) any) error { // any: html/template.FuncMap's own value type
	if h.closed.Load() {
		return fmt.Errorf("%w: AddRenderFunc(%q)", plugin.ErrConfigureClosed, name)
	}
	if factory == nil {
		return fmt.Errorf("collage: nil render function factory for %q", name)
	}
	if _, taken := h.app.pluginFuncs[name]; taken {
		return fmt.Errorf("%w: %q", plugin.ErrDuplicateTemplateFunc, name)
	}
	if err := reservedFunc(name); err != nil {
		return err
	}
	// A stand-in for parsing, which needs every name the templates call; the
	// render engine binds factory's function in its place on every render.
	h.app.pluginFuncs[name] = func(...any) (any, error) { // any: a stand-in whose arguments and result are the factory's to decide
		return nil, fmt.Errorf("collage: %q called outside a render", name)
	}
	h.app.renderFuncs[name] = factory
	return nil
}

// WrapMount registers a transformation applied to every mounted filesystem. A
// call after Configure returned wraps nothing — the mounts may be serving — and
// is logged, since the method has no error to return.
func (h *configHostView) WrapMount(wrap func(fs.FS) fs.FS) {
	if h.closed.Load() {
		h.app.logger.Warn("collage: WrapMount called after Configure returned; nothing was wrapped", "plugin", h.name)
		return
	}
	if wrap == nil {
		return
	}
	h.app.mountWrappers = append(h.app.mountWrappers, wrap)
}
