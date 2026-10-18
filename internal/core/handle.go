package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/httpx"
)

// ErrNilHandler reports a Handle or Use call given nothing to run.
var ErrNilHandler = errors.New("collage: nil handler")

// Handle mounts an http.Handler of the application's own under prefix: an API
// written with any router, a gRPC gateway, a webhook receiver. A prefix ending in
// "/" claims every path beneath it; one without is a single exact path —
// "/metrics", "/webhooks/stripe".
//
// Collage passes the request on unchanged — the handler sees the full path, so
// wrap it in http.StripPrefix if it expects otherwise — and does nothing to it: no
// forgery check, no body limit, no cache. What it does is what it does before any
// request is routed: the plugins' request hooks, the span, the metrics, the panic
// guard, the clean-path redirect and the encoded-slash 404, the baseline
// security headers, and draining it on shutdown.
//
// No middleware runs for it — neither what Use added nor any plugin's, a
// session, a rate limit, a basicauth, a compressor. The handler is the
// application's, and so is what wraps it: an API that wants its own auth or its
// own rate limit wraps itself in them, and is not handed the site's session
// cookie, consent banner or HTML toolbar on the way. Which requests those are is
// decided on the path as it is once the pre-routing checks have passed it, by the
// same match routing uses, so a request it lets past the middleware is one only
// this handler answers. WithMiddleware puts one mount back inside the chain, as
// every mount was before v0.67.0; a middleware that rewrites a request's path
// into prefix reaches the handler only then. A plugin's Host.Handle always runs
// inside the chain.
//
// A prefix that overlaps a page, a document, an action, a redirect or a mount is
// reported when the handler is built, whichever was registered first. It returns
// ErrAppStarted once the server has started.
func (a *App) Handle(prefix string, handler http.Handler, opts ...HandleOption) error {
	if prefix == "/" || !strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, "//") || strings.Contains(prefix, "/.") {
		return fmt.Errorf("%w: a handler prefix must begin with \"/\" and not be \"/\": %q", asset.ErrInvalidPrefix, prefix)
	}
	if handler == nil {
		return fmt.Errorf("%w: at %q", ErrNilHandler, prefix)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return fmt.Errorf("%w: cannot handle %q", ErrAppStarted, prefix)
	}
	mount := httpx.HandlerMount{Prefix: prefix, Handler: handler, Bare: true}
	for _, opt := range opts {
		// A nil option is passed over, not called: an option assembled
		// conditionally — nil when a flag is off — is not a reason to panic
		// at startup.
		if opt != nil {
			opt(&mount)
		}
	}
	a.handlers = append(a.handlers, mount)
	return nil
}

// Use adds middleware around the handling of every request, in the standard
// net/http shape. The first registered is the outermost, and a plugin's
// middleware counts as registered where the plugin was: the plugins in
// Config.Plugins are outside everything Use adds, so middleware here reads what
// they put in the request's context — a session, a flash message — and a plugin
// registered with RegisterPlugin is inside the middleware added before it.
//
// It runs before routing, so it sees every request — pages, documents, actions,
// mounts, a plugin's handlers and a handler mounted WithMiddleware — but one for
// a handler the application mounted with Handle alone, which is answered without
// the chain: that handler brings its own middleware. It runs inside the
// framework's span, metrics and panic guard, which a wrapper around app.Handler()
// does not. A middleware may answer the request itself, or pass on a request of
// its own; whatever it puts in that request's context is what data handlers read
// from rc.Context().
//
// A static build renders without a request, so no middleware runs for its
// renders. It then asks for each file's path through the handler to record its
// response headers (see CaptureResponses), and middleware runs for those
// requests: a header it sets is one the built file is deployed with.
func (a *App) Use(middleware func(http.Handler) http.Handler) error {
	if middleware == nil {
		return ErrNilHandler
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return fmt.Errorf("%w: cannot add middleware", ErrAppStarted)
	}
	a.middleware = append(a.middleware, middlewareSlot{chain: []httpx.Middleware{middleware}})
	return nil
}

// HandleOption configures a handler mounted with Handle. See WithMiddleware.
//
// It is a function over the mount rather than a struct of fields so that the set
// can grow without an application's call changing shape, as asset.Option does
// for Mount; what it sets is not the application's to read back.
type HandleOption func(*httpx.HandlerMount)

// WithMiddleware runs the mounted handler inside the middleware chain — what Use
// added and every plugin's — as a page is, in the same order: the behaviour every
// mount had before v0.67.0. Use it for a handler that relies on the site's
// middleware: a basicauth or rate limit registered to guard it, the session it
// reads, a middleware rewriting requests into its prefix.
func WithMiddleware() HandleOption {
	return func(m *httpx.HandlerMount) { m.Bare = false }
}

// middlewareSlot is one place in the middleware chain: a middleware the
// application added with Use, or the place a plugin was registered, which holds
// whatever the plugin's Init adds through Host.Use, in the order it adds them.
type middlewareSlot struct {
	plugin string // "" for the application's own
	chain  []httpx.Middleware
}

// usePlugin adds middleware to the place the plugin named name was registered.
func (a *App) usePlugin(name string, middleware httpx.Middleware) error {
	if middleware == nil {
		return ErrNilHandler
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return fmt.Errorf("%w: cannot add middleware", ErrAppStarted)
	}
	for i := range a.middleware {
		if a.middleware[i].plugin == name {
			a.middleware[i].chain = append(a.middleware[i].chain, middleware)
			return nil
		}
	}
	// A host is only made for a registered plugin, so this is not reached; were
	// it, innermost is where middleware added last belongs.
	a.middleware = append(a.middleware, middlewareSlot{plugin: name, chain: []httpx.Middleware{middleware}})
	return nil
}

// middlewareChain is every middleware in the order a request passes through it.
func (a *App) middlewareChain() []httpx.Middleware {
	var chain []httpx.Middleware
	for _, slot := range a.middleware {
		chain = append(chain, slot.chain...)
	}
	return chain
}
