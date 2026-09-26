package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/Elagoht/collage/internal/types"
)

// Configurer is implemented by a plugin that needs to act before the application is
// built, rather than before it serves. It is optional and discovered by type
// assertion, exactly as the hooks are, so a plugin that does not need it is
// unaffected.
//
// The lifecycle splits in two because the two phases can offer different things and
// no single ordering serves both. A template function has to be registered before
// templates are parsed — html/template resolves a function name at execution time
// but can only call one that was in the FuncMap at parse time — and parsing happens
// while the application is constructed. Pages, meanwhile, are registered by the
// application itself *after* construction, so a plugin that wants to read them, or
// add one of its own, has to run later.
//
// Configure therefore runs during construction, and Init runs before serving. A
// plugin needing both implements both.
type Configurer interface {
	// Configure prepares the plugin against the application being built. It runs
	// once, in registration order, before templates are parsed. A returned error
	// aborts construction.
	Configure(ctx context.Context, host ConfigHost) error
}

// ConfigHost is the capability surface Configure receives.
//
// It is narrower than Host, and narrower on purpose: at this point the application
// has registered nothing, so there are no pages to read and nothing to invalidate.
// Offering those here would mean offering them empty.
type ConfigHost interface {
	// DevMode reports whether the application is running in development mode.
	DevMode() bool
	// Logger returns the application's structured logger.
	Logger() *slog.Logger
	// Config decodes this plugin's section of the application's plugin
	// configuration into v, leaving v untouched when the plugin has no section.
	// That is what lets v carry the plugin's defaults in and come back either
	// unchanged or overlaid.
	Config(v any) error // any: restates encoding/json's own parameter type
	// AddTemplateFunc registers fn as a template function under name. It returns
	// ErrDuplicateTemplateFunc when another plugin already registered that name,
	// because two plugins quietly overwriting each other's functions is a bug
	// nobody would find from the rendered output.
	//
	// It must be called from Configure. Templates are parsed when construction
	// finishes, and a function added after that is one no template can call.
	AddTemplateFunc(name string, fn any) error // any: restates html/template.FuncMap's own value type
	// WrapMount registers a transformation applied to every mounted filesystem, in
	// the order the wrappers were registered.
	//
	// It wraps the filesystem rather than the response because mounts serve
	// through http.ServeContent, which brings Range, If-Range and 206 with it.
	// Transforming bytes per request shifts every offset, so a range request would
	// return the wrong slice of a file whose advertised length no longer matches.
	// A minifier returns an fs.FS whose files are already minified, and
	// ServeContent keeps working on whatever it is handed.
	WrapMount(wrap func(fs.FS) fs.FS)
	// AddRenderFunc registers a template function made anew for each render:
	// factory is called with the render's context and returns the function the
	// templates call, which can then read what that render holds — a nonce set in
	// BeforeRender, the render's locale. AddTemplateFunc's functions are fixed for
	// the life of the application and cannot.
	//
	// Like AddTemplateFunc it must be called from Configure, and a name another
	// plugin registered is ErrDuplicateTemplateFunc.
	AddRenderFunc(name string, factory func(rc *types.RenderContext) any) error // any: html/template.FuncMap's own value type
}

// ErrDuplicateTemplateFunc is returned by ConfigHost.AddTemplateFunc when a name is
// already registered by another plugin.
var ErrDuplicateTemplateFunc = fmt.Errorf("collage: duplicate plugin template function")

// ErrUnknownPluginConfig is returned when the application's plugin configuration
// holds a key matching no registered plugin.
//
// Ignoring it would leave the operator certain a plugin was configured while it ran
// on defaults, which is the failure mode a typo in a config file has — and the one
// this framework refuses everywhere else.
var ErrUnknownPluginConfig = fmt.Errorf("collage: plugin configuration names no registered plugin")

// Configure calls Configure on every registered plugin implementing Configurer, in
// registration order, stopping at the first failure.
//
// Unlike Init it does not roll back: nothing has been acquired yet, because the
// application is still being built and a plugin has nowhere to put a resource it
// would need to release. A failure here aborts construction, and the application
// never exists.
func (r *Registry) Configure(ctx context.Context, hostFor func(name string) ConfigHost) error {
	if r == nil {
		return nil
	}
	for _, p := range r.snapshot() {
		configurer, ok := p.(Configurer)
		if !ok {
			continue
		}
		host := hostFor(p.Name())
		if err := safeCall(func() error { return configurer.Configure(ctx, host) }); err != nil {
			return fmt.Errorf("collage: plugin %q configure: %w", p.Name(), err)
		}
	}
	return nil
}

// CheckConfigKeys reports a configuration key matching no registered plugin.
func (r *Registry) CheckConfigKeys(config map[string]json.RawMessage) error {
	if r == nil || len(config) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(r.snapshot()))
	for _, p := range r.snapshot() {
		known[p.Name()] = struct{}{}
	}
	for key := range config {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownPluginConfig, key)
		}
	}
	return nil
}

// DecodeConfig decodes the section named for a plugin into v.
//
// An absent section is not an error and leaves v alone, so a plugin passes its
// defaults in and gets them back either unchanged or overlaid. A present but
// malformed section is an error: the operator wrote something, and running on
// defaults instead would be the silent failure this refuses.
func DecodeConfig(config map[string]json.RawMessage, name string, v any) error { // any: restates encoding/json's own parameter type
	raw, ok := config[name]
	if !ok || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("collage: plugin %q configuration: %w", name, err)
	}
	return nil
}
