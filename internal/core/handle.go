package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Elagoht/collage/internal/httpx"
)

// ErrInvalidHandlerPrefix reports a Handle prefix that is not a slash-delimited
// path, or is "/" alone. A handler at "/" would take every request from every
// page; wrap app.Handler() in a mux of your own if that is what you want.
var ErrInvalidHandlerPrefix = errors.New("collage: handler prefix must begin with \"/\" and not be \"/\"")

// ErrNilHandler reports a Handle or Use call given nothing to run.
var ErrNilHandler = errors.New("collage: nil handler")

// Handle mounts an http.Handler of the application's own under prefix: an API
// written with any router, a gRPC gateway, a webhook receiver. A prefix ending in
// "/" claims every path beneath it; one without is a single exact path —
// "/metrics", "/webhooks/stripe".
//
// Collage passes the request on unchanged — the handler sees the full path, so
// wrap it in http.StripPrefix if it expects otherwise — and does nothing to it: no
// forgery check, no body limit, no cache. What it does is what it does for every
// request: the span, the metrics, the panic guard, the middleware registered with
// Use, and draining it on shutdown.
//
// A prefix that overlaps a page, a document, an action, a redirect or a mount is
// reported when the handler is built, whichever was registered first. It returns
// ErrAppStarted once the server has started.
func (a *App) Handle(prefix string, handler http.Handler) error {
	if prefix == "/" || !strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, "//") || strings.Contains(prefix, "/.") {
		return fmt.Errorf("%w: %q", ErrInvalidHandlerPrefix, prefix)
	}
	if handler == nil {
		return fmt.Errorf("%w: at %q", ErrNilHandler, prefix)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return fmt.Errorf("%w: cannot handle %q", ErrAppStarted, prefix)
	}
	a.handlers = append(a.handlers, httpx.HandlerMount{Prefix: prefix, Handler: handler})
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
// mounts and handlers alike — and it runs inside the framework's span, metrics and
// panic guard, which a wrapper around app.Handler() does not. A middleware may
// answer the request itself, or pass on a request of its own; whatever it puts in
// that request's context is what data handlers read from rc.Context().
//
// A static build renders without a request, so no middleware runs for it.
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
