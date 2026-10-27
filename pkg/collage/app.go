package collage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"github.com/Elagoht/collage/internal/clienv"
	"github.com/Elagoht/collage/internal/core"
)

// errNilConfig is returned by New when passed a nil *Config. A nil config is a
// programming error, not a request for defaults.
var errNilConfig = errors.New("collage: nil config")

// App is a collage application: its pages, documents, actions, plugins and the
// server that answers them. New returns one; the zero value is not usable.
type App struct{ c *core.App }

// New builds an App from cfg: it applies the framework's defaults to every field
// left at its zero value, validates the result and constructs the application.
//
// cfg is mutated in place by the defaulting step, so the caller can read back
// exactly what the application was built with. Passing nil returns an error:
// a nil config is a mistake, not a request for the defaults.
//
// Every failure mode is reported here rather than at the first request: an invalid
// configuration, a template root that does not exist, or a template that does not
// parse.
func New(cfg *Config) (*App, error) {
	if cfg == nil {
		return nil, errNilConfig
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	c, err := core.New(toCoreConfig(cfg))
	if err != nil {
		return nil, err
	}
	return &App{c: c}, nil
}

// Register registers each of items in order — a page, a document or an action,
// each through its own Register method — and stops at the first one refused,
// returning its error wrapped with its kind and name. What was registered before a
// refusal stays registered, so treat a refusal as a startup failure.
func (a *App) Register(items ...Registrable) error { return a.c.Register(items...) }

// RegisterPage validates p, binds its content into its layout, checks that every
// template it names exists and registers its paths. A page that would fail when
// rendered fails here instead, with an error naming it; after the application
// has started it returns ErrAppStarted.
func (a *App) RegisterPage(p *Page) error { return a.c.RegisterPage(p) }

// RegisterNotFoundPage registers p as the page served when a request reaches no
// route. It is checked exactly as RegisterPage checks a page, but gets no paths of
// its own.
func (a *App) RegisterNotFoundPage(p *Page) error { return a.c.RegisterNotFoundPage(p) }

// RegisterErrorPage registers p as the page served when rendering fails. It is
// checked exactly as RegisterPage checks a page, but gets no paths of its own.
func (a *App) RegisterErrorPage(p *Page) error { return a.c.RegisterErrorPage(p) }

// RegisterDocument validates doc and registers its paths. A nil document, a
// repeated name or a path another route holds is refused, naming the document;
// after the application has started it returns ErrAppStarted.
func (a *App) RegisterDocument(doc *Document) error { return a.c.RegisterDocument(doc) }

// RegisterAction validates action and registers its paths. An action may share a
// path with a page, as a form posting to its own page does, but not a method with
// another action.
func (a *App) RegisterAction(action *Action) error { return a.c.RegisterAction(action) }

// RegisterPlugin adds p to the application's plugins. Plugins are initialised once,
// at startup, in registration order, so after the application has started this
// returns ErrAppStarted.
func (a *App) RegisterPlugin(p Plugin) error { return a.c.RegisterPlugin(p) }

// RegisterCommand registers cmd as a CLI subcommand DispatchCommands can run. An
// empty or repeated name, a "collage-" name (DispatchCommands' own) and one
// that is not a single shell word are refused. It is open while plugins' Init
// runs, since plugins contribute commands from there, and closed with
// ErrAppStarted once the application has started.
func (a *App) RegisterCommand(cmd Command) error { return a.c.RegisterCommand(cmd) }

// Commands returns the registered CLI subcommands in registration order, as a copy.
// A command registered with RegisterCommand is listed at once, before Start;
// plugins register theirs from Init, so those appear once the application has
// started.
func (a *App) Commands() []Command { return a.c.Commands() }

// Pages returns every registered page in registration order, each a copy, so
// changing one does not change the application.
func (a *App) Pages() []*Page { return a.c.Pages() }

// Page returns a copy of the page registered as name, and whether there is one.
func (a *App) Page(name string) (*Page, bool) { return a.c.Page(name) }

// Documents returns every registered document in registration order. The slice is
// a copy; the documents are the values that were registered.
func (a *App) Documents() []*Document { return a.c.Documents() }

// Actions returns every registered action in registration order, as a copy of the
// slice.
func (a *App) Actions() []*Action { return a.c.Actions() }

// Handle serves handler under prefix: an API, a webhook receiver, anything written
// against net/http. A prefix ending in "/" claims every path beneath it, one
// without is a single path. The request reaches handler unchanged, with no
// forgery check, body limit or cache, and with no middleware: neither what Use
// added nor any plugin's — session, ratelimit, basicauth, compress — runs for it.
// The handler is the application's, and wraps itself in the middleware it wants.
// The request hooks, span, metrics, panic guard and the clean-path redirect still
// apply.
//
// WithMiddleware runs one mount inside the middleware chain instead, as every
// mount was before v0.67.0; a middleware that rewrites a path into prefix reaches
// the handler only then. A plugin's Host.Handle always runs inside the chain.
func (a *App) Handle(prefix string, handler http.Handler, opts ...HandleOption) error {
	return a.c.Handle(prefix, handler, opts...)
}

// Use adds middleware around every request — pages, documents, actions, mounts, a
// plugin's handlers and a handler mounted WithMiddleware — inside the framework's
// tracing, metrics and panic guard. A handler mounted with Handle alone is not
// wrapped: it brings its own. The first registered is the outermost.
func (a *App) Use(middleware func(http.Handler) http.Handler) error {
	return a.c.Use(middleware)
}

// Mount serves the files in fsys under prefix, alongside the application's routes.
// prefix must begin and end with "/" and must not be "/" alone (ErrInvalidPrefix),
// and fsys must not be nil (ErrNilFS); after startup it returns ErrAppStarted.
func (a *App) Mount(prefix string, fsys fs.FS, opts ...MountOption) error {
	return a.c.Mount(prefix, fsys, opts...)
}

// URL returns the path of the page or document registered as name, in locale (the
// default one when empty), with params filling its pattern. It is strict: an
// unknown name, a locale with no path or params that do not fill the pattern
// exactly are errors, never a guess.
func (a *App) URL(name, locale string, params map[string]string) (string, error) {
	return a.c.URL(name, locale, params)
}

// ActionURL is URL for actions: the path a form's action attribute holds for the
// action registered as name. Actions have their own names, so a "login" page and a
// "login" action do not collide.
func (a *App) ActionURL(name, locale string, params map[string]string) (string, error) {
	return a.c.ActionURL(name, locale, params)
}

// FragmentURL returns the path page opened for its fragment named fragment with
// WithFragmentPath, in locale — the URL a script fetches to refresh that part of
// the page. It is as strict as URL.
func (a *App) FragmentURL(page, fragment, locale string, params map[string]string) (string, error) {
	return a.c.FragmentURL(page, fragment, locale, params)
}

// InvalidateTags invalidates every cached render that depended on any of tags:
// when the cache supports it (every built-in one does) the render is marked
// stale, so a render that fails is answered with it; any other cache removes
// it. It also marks every collage.Cached value under them stale: the next
// render is served the stale value at once while one fetch replaces it, and
// keeps being served it while the upstream is down. It is InvalidateTagsN
// without the count.
func (a *App) InvalidateTags(ctx context.Context, tags ...string) error {
	return a.c.InvalidateTags(ctx, tags...)
}

// InvalidateTagsN is InvalidateTags returning how many cached renders it
// invalidated. A failure to remove one entry does not stop the rest; the
// failures are joined into the returned error. With a SharedCache store the
// count, the plugin event and the warm cover the renders every instance cached,
// not only this one's.
func (a *App) InvalidateTagsN(ctx context.Context, tags ...string) (int, error) {
	return a.c.InvalidateTagsN(ctx, tags...)
}

// PurgeTags removes outright every cached render and every collage.Cached value
// under any of tags, from disk too. Unlike InvalidateTags nothing is left to serve
// stale: the next render waits for the upstream, and fails if it is down. For
// data that must not be shown again, once; InvalidateTags is the everyday call.
func (a *App) PurgeTags(ctx context.Context, tags ...string) error {
	return a.c.PurgeTags(ctx, tags...)
}

// RenderPath renders the page path reaches, for locale, bypassing the render
// cache, and overlays params onto the path parameters the router finds. It returns
// ErrPageNotFound when path reaches no page.
func (a *App) RenderPath(ctx context.Context, path, locale string, params map[string]string) (*Result, error) {
	return a.c.RenderPath(ctx, path, locale, params)
}

// RenderDocumentPath is RenderPath for documents: it renders the document path
// reaches, bypassing the render cache, and returns ErrDocumentNotFound when path
// reaches no document.
func (a *App) RenderDocumentPath(ctx context.Context, path, locale string, params map[string]string) (*DocumentResult, error) {
	return a.c.RenderDocumentPath(ctx, path, locale, params)
}

// RenderFragment renders, for r, one fragment a page opened at its own URL with
// WithFragmentPath, and returns its markup, what it hoisted, its tags and whether
// one render serves every reader — for pushing fragments over a stream.
func (a *App) RenderFragment(r *http.Request, req FragmentRequest) (*FragmentRender, error) {
	return a.c.RenderFragment(r, req)
}

// Handler returns the application's http.Handler, starting the application
// (plugins' Init included) on first use. A failed start is logged and the handler
// answers 503; call Start first to get the error instead.
func (a *App) Handler() http.Handler { return a.c.Handler() }

// Start starts the application — builds its handler and runs every plugin's Init —
// once, and returns what that produced. It closes registration. Handler and
// ListenAndServe start it too; call Start when you want the error first.
func (a *App) Start() error { return a.c.Start() }

// ListenAndServe starts the application and serves it on the configured host and
// port until SIGINT or SIGTERM, then drains and shuts down gracefully. A clean
// shutdown returns nil.
//
// It refuses to serve when the collage binary ran the program only to ask it
// something — collage inspect, routes and check do — since the CLI would wait on
// a server forever: main then never handed its arguments to DispatchCommands.
func (a *App) ListenAndServe() error {
	if name := os.Getenv(clienv.Command); name != "" {
		return fmt.Errorf("collage: the collage CLI ran this program to answer %q, but it started serving instead: "+
			"main must hand the words after its flags to collage.DispatchCommands, as collage.Main does", name)
	}
	return a.c.ListenAndServe()
}

// Shutdown drains and stops the running server, then shuts the plugins down, with
// ctx bounding the wait. It runs once; every caller sees the same result.
func (a *App) Shutdown(ctx context.Context) error { return a.c.Shutdown(ctx) }

// DevMode reports whether the application runs in development mode.
func (a *App) DevMode() bool { return a.c.DevMode() }

// Logger returns the application's structured logger.
func (a *App) Logger() *slog.Logger { return a.c.Logger() }

// BaseURL returns the site's public origin without a trailing slash, or "" when
// none is configured.
func (a *App) BaseURL() string { return a.c.BaseURL() }

// OriginFor returns the public origin URLs for host are made absolute against: the
// first origin-resolving plugin's that knows host, else BaseURL.
func (a *App) OriginFor(ctx context.Context, host string) string {
	return a.c.OriginFor(ctx, host)
}

// Locales returns the default locale and every supported one, the default
// included, in configured order.
func (a *App) Locales() (string, []string) { return a.c.Locales() }

// PageURLs returns every URL the page registered as name answers: one per locale
// for a fixed path, one per locale and parameter set for a pattern with
// WithStaticParams. It is what a sitemap is made of.
func (a *App) PageURLs(ctx context.Context, name string) ([]PageURL, error) {
	return a.c.PageURLs(ctx, name)
}

// ServeStatus answers r with status and the page the application shows for it:
// the not-found page for 404 and 410, the error page otherwise.
func (a *App) ServeStatus(w http.ResponseWriter, r *http.Request, status int) {
	a.c.ServeStatus(w, r, status)
}

// BuildID names the build of the running program: Config.Cache.Version when set,
// otherwise a fingerprint of the executable. Use it to version what a browser
// keeps, such as a service worker's caches.
func (a *App) BuildID() string { return a.c.BuildID() }

// Check reports, without rendering, what is wrong with the application's
// templates: links by name to routes that do not exist, to a locale they have no
// path in, or with parameters their pattern does not take.
func (a *App) Check() []Finding { return a.c.Check() }
