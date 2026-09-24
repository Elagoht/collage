package httpx

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
)

// plainText reports whether a failure on a route of this kind must be written as
// plain text rather than as the HTML error page. It is the single expression of
// the rule routeKind exists for.
func (k routeKind) plainText() bool {
	return k != routeKindPage
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
}

// resolved records that the request resolved to a route of kind kind, named name.
func (ref *routeRef) resolved(kind routeKind, name string) {
	ref.kind = kind
	ref.name = name
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
