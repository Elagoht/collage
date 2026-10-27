package httpx

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// routeKind names what kind of route a request resolved to. It exists for exactly
// one reason: the content type of an error response follows the route kind, never
// the request. A crawler that asked for /sitemap.xml must not be handed an HTML
// error page, and a browser that asked for /about must not be handed a plain-text
// one — and which of the two is correct is decided by what the URL resolved to,
// not by what the client sent.
//
// Before this type existed that rule had no owner. Every frame that knew it was
// serving a document or a mount had to remember to reach for writePlainText
// instead of serveFailure, and the one frame that could not remember — the panic
// guard, which never learned what the request had resolved to — silently defaulted
// to HTML. The rule was re-litigated at three call sites and still had a hole. Now
// serve records the kind into a routeRef as soon as it knows it, serveFailure is
// the single writer, and it branches on the kind: a call site can no longer get it
// wrong by forgetting, because no call site chooses the writer any more.
type routeKind int

const (
	// routeKindPage is the zero value, and covers both a request that resolved
	// to a page and a request that has not resolved to anything yet: a router
	// failure, a malformed match result, a route miss. The page surface is the
	// site's HTML surface, so its failures render the HTML error page. It is the
	// zero value deliberately — a failure raised before anything is known is a
	// failure on the site's own HTML surface, which is the only answer available
	// when there is no route to follow.
	routeKindPage routeKind = iota
	// routeKindDocument is a request that resolved to a registered document: a
	// sitemap, a feed, a robots.txt. Its failures are plain text.
	routeKindDocument
	// routeKindMount is a request claimed by a mounted asset file system. Its
	// failures are plain text: a mount is never an HTML route.
	routeKindMount
	// routeKindAction is a request answered by an action. Its failures are plain
	// text, which is the safe direction: an action is as likely to be a webhook or
	// a fetch() as a form post, and an HTML error page handed to something parsing
	// JSON is a parse error on top of the real one. A handler that wants an error
	// page for the cases it expects returns one — that is what ActionResult.Page
	// is for.
	routeKindAction
	// routeKindHandler is a request answered by an http.Handler the
	// application mounted. Its failures are plain text: collage knows nothing
	// about what it serves, and an HTML page is the one answer it can be sure is
	// wrong for an API.
	routeKindHandler
	// routeKindRejected is a request refused by path normalisation before it
	// could reach a route: a dot segment or a doubled slash redirected to the
	// clean spelling, an encoded slash or a NUL answered not found. It is
	// recorded after the answer is written, which is the site's own HTML
	// not-found page or a redirect, so it decides only how the request is
	// reported: under a bounded label, never the scanner's path.
	routeKindRejected
)

// plainText reports whether a failure on a route of this kind must be written as
// plain text rather than as the HTML error page. It is the single expression of
// the rule routeKind exists for.
func (k routeKind) plainText() bool {
	return k != routeKindPage && k != routeKindRejected
}

// routeRef is the one record of what the request in flight resolved to. serve
// fills it in as soon as it knows, and it is reachable from the panic guard that
// wraps serve, which is what lets a panic raised anywhere below — in an
// application's Cache, its Tracker, its Metrics, a plugin hook — be answered in
// the content type the resolved route requires rather than in whatever the frame
// that caught it happened to default to.
//
// It is one value per request, created by serveGuarded and passed down by
// pointer. It is never shared across requests and never read concurrently with
// being written: serve, and everything it calls, runs on the one goroutine
// net/http gave the request.
type routeRef struct {
	// kind is what the request resolved to. It starts at routeKindPage, which is
	// also what an unresolved request stays at.
	kind routeKind
	// name identifies the resolved route for a dev-mode error body: a document's
	// Name, a mount's Prefix. It is empty while the request is unresolved.
	name string
	// label is the resolved route as RouteInfo reports it — a page's name too,
	// which name leaves empty — for a metric or a trace that must not be labelled
	// with raw paths.
	label string
	// pattern is the path pattern the route was registered with, as registered —
	// "/blog/{slug}", without a locale prefix — or a mount's or handler's prefix;
	// locale is the locale the request resolved to.
	pattern, locale string
	// start is when the handler took the request, which net/http's WriteTimeout
	// counts from, near enough: an action works out its write deadline from it.
	start time.Time
	// routes are the application's URL builders, which URL builds with in the
	// request's locale.
	routes *types.Routes
}

// at records the pattern and the locale the route matched with.
func (ref *routeRef) at(pattern, locale string) {
	ref.pattern = pattern
	ref.locale = locale
}

// resolved records that the request resolved to a route of kind kind, named name.
func (ref *routeRef) resolved(kind routeKind, name string) {
	ref.kind = kind
	ref.name = name
	ref.label = name
}

// rejected records that the request was refused by path normalisation. It has
// no pattern or locale: the label is the whole of what is reported, so a metric
// or a trace keyed by route has one value for every refused spelling.
func (ref *routeRef) rejected() {
	ref.kind = routeKindRejected
	ref.name = ""
	ref.label = types.RouteNameNormalise
	ref.pattern, ref.locale = "", ""
}

// resolvedPage records that the request resolved to the page named name.
func (ref *routeRef) resolvedPage(name string) {
	ref.kind = routeKindPage
	ref.label = name
}

// String names the kind as RouteInfo reports it.
func (k routeKind) String() string {
	switch k {
	case routeKindDocument:
		return types.RouteKindDocument
	case routeKindMount:
		return types.RouteKindMount
	case routeKindAction:
		return types.RouteKindAction
	case routeKindHandler:
		return types.RouteKindHandler
	case routeKindRejected:
		return types.RouteKindRejected
	default:
		return types.RouteKindPage
	}
}

// Route is what a request resolved to, as RouteInfo reports it.
type Route struct {
	// Kind is what the request resolved to: RouteKindPage, RouteKindDocument,
	// RouteKindAction, RouteKindMount or RouteKindHandler. The set may grow, so
	// a comparison names the kinds it handles rather than assuming it has seen
	// them all.
	Kind string
	// Name is the page's, document's or action's registered name, or the mount's
	// or handler's prefix.
	Name string
	// Pattern is the path pattern the route was registered with, as it was
	// registered — "/blog/{slug}", no locale prefix — or the mount's or handler's
	// prefix: a label with a bounded number of values, unlike the path.
	Pattern string
	// Locale is the locale the request resolved to; empty for a mount or a
	// handler, which are in none.
	Locale string
}

// RouteInfo reports what the request carrying ctx resolved to, pattern and locale
// included. The zero Route means it resolved to nothing, or has not yet.
func RouteInfo(ctx context.Context) Route {
	ref, ok := ctx.Value(routeCtxKey{}).(*routeRef)
	if !ok || ref.label == "" {
		return Route{}
	}
	return Route{Kind: ref.kind.String(), Name: ref.label, Pattern: ref.pattern, Locale: ref.locale}
}

type routeCtxKey struct{}

// URL returns the path of the page or document registered as name for the
// request carrying ctx: in the locale it resolved to, or the default locale
// before it has resolved — in a middleware that runs ahead of routing — and for
// a route with no path in it. Outside a request collage serves it is
// ErrUnknownRoute.
func URL(ctx context.Context, name string, params map[string]string) (string, error) {
	ref, ok := ctx.Value(routeCtxKey{}).(*routeRef)
	if !ok || ref.routes == nil || ref.routes.Page == nil {
		return "", fmt.Errorf("%w: %q: no request collage is serving", types.ErrUnknownRoute, name)
	}
	return ref.routes.URL(name, ref.locale, params)
}

// failure returns a failure for this route carrying its kind and name, so the
// response serveFailure writes follows the resolved route rather than whatever
// the failing frame knows. Every failure raised after a route resolves is built
// through here, which is what keeps the kind from being forgotten at a call site.
func (ref *routeRef) failure(status int, stage string, err error) failure {
	return failure{
		status: status,
		err:    err,
		stage:  stage,
		kind:   ref.kind,
		route:  ref.name,
	}
}

// failureFor is a failure for the route r resolved to, when one did — the
// writer that hit the failure does not have the routeRef at hand.
func failureFor(r *http.Request, status int, stage string, err error) failure {
	if ref, ok := r.Context().Value(routeCtxKey{}).(*routeRef); ok && ref != nil {
		return ref.failure(status, stage, err)
	}
	return failure{status: status, err: err, stage: stage}
}
