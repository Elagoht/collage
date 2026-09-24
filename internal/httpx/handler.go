// Package httpx serves rendered pages over HTTP: it wires the router, the render
// engine, the cache, the dependency tracker, the plugin registry, and observability
// into one request lifecycle. The package is named httpx rather than http so that
// importing it never shadows the standard net/http package at a call site.
package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/dependency"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ErrMissingDependency is returned by New when a required Deps field is nil. It is
// a single sentinel for every required field rather than one per field: a caller
// cannot react differently to a missing router than to a missing renderer, and the
// wrapped message names the field that was left unset.
var ErrMissingDependency = errors.New("collage: missing handler dependency")

// ErrNoRoute is the error reported to plugins through ErrorHook when no route
// matched the request. It exists because ErrorEvent.Err must name the failure being
// reported, and a not-found result arrives from the router as a normal match, not as
// an error.
//
// It is deliberately NOT named ErrNotFound: types.ErrNotFound means "the content
// this route resolves to does not exist", reported by a data handler and wrapped
// deep inside a render error. Both produce a 404, and a plugin asking why needs to
// tell them apart — an unmatched URL is a routing or link problem, a missing record
// is a content one.
var ErrNoRoute = errors.New("collage: no route matched the request")

// ErrEmptyErrorPage is the error reported to plugins when a registered error page
// renders successfully but produces no markup. It is a distinct failure from a
// render error — nothing failed, there is simply nothing to serve — and the handler
// falls through to the built-in page either way.
var ErrEmptyErrorPage = errors.New("collage: error page rendered empty")

// ErrPanic is the error reported to plugins through ErrorHook, under the stage
// "panic", when a collaborator panicked while serving a request and the handler
// recovered it into a 500. It is deliberately distinct from every other failure: a
// panic is a bug in the code that raised it, not a condition the request ran into,
// and an operator triaging one needs to tell it apart at a glance. The wrapped
// message carries the panic value and the stack it was raised on.
//
// A panic inside a data handler or a template function is not this: the render
// engine recovers those itself, as render.PanicError, and they follow the ordinary
// fragment failure policy.
var ErrPanic = errors.New("collage: panic recovered while serving the request")

// ErrAssetFailed is the error reported to plugins through ErrorHook when a mounted
// asset request completes with a status of 400 or above: a missing file, a
// disallowed method, or any other 4xx or 5xx asset.Mount itself decided to write.
// It is deliberately one sentinel for every such status rather than a family of
// them — a plugin reacting to "this asset request failed" needs no finer
// distinction than that, whether the status was 404 or 405, matching this
// project's one-sentinel-per-failure-mode rule — and the wrapped message names the
// status that was actually written, since the mount, not this package, chose it.
//
// A panic recovered while serving a mount is reported as ErrPanic instead, through
// the same path any other collaborator's panic takes: this sentinel covers only a
// mount that returned normally with an error status already on the wire.
var ErrAssetFailed = errors.New("collage: asset request failed")

// Pipeline stages, as reported to plugins through ErrorEvent.Stage. The field is
// documented as caller-defined rather than an enum, so these are the names this
// handler happens to use.
const (
	stageRoute        = "route"
	stageNotFound     = "not_found"
	stagePageResolved = "page_resolved"
	stageBeforeRender = "before_render"
	stageRender       = "render"
	stageAfterRender  = "after_render"
	stageCacheWrite   = "cache_write"
	stageErrorPage    = "error_page"
	stagePanic        = "panic"
	stageAsset        = "asset"
	stageHandler      = "handler"
)

// contentTypeHTML is the Content-Type every rendered page and built-in error page
// is served with.
const contentTypeHTML = "text/html; charset=utf-8"

// renderTimeHeader carries the wall-clock duration of a fresh render. It is set in
// dev mode only, so production responses never advertise how long a page took to
// build.
const renderTimeHeader = "X-Collage-Render-Time"

// staticCacheTTL is the TTL a StrategyStatic page's cache entry is written with when
// the page sets no CacheTTL of its own: a hundred years, which is "until explicitly
// invalidated" as closely as the Cache interface can say it.
//
// It is not zero because zero already means something else. Cache.Set documents a
// ttl of zero or less as "use the cache's own configured default", and an
// application's own Cache implementation is entitled to read it that way — so
// passing zero would hand a static page whatever default that cache happens to
// carry, which is exactly the bug this constant exists to fix. A century is longer
// than any process this will run in and still an ordinary time.Duration, so no
// implementation has to special-case it.
const staticCacheTTL = 100 * 365 * 24 * time.Hour

// Deps are the collaborators a Handler needs. Router, Renderer, Tracker, and Logger
// are required; Cache, Metrics, Tracer, and Plugins may be left nil.
type Deps struct {
	// Router resolves a request to a page, a redirect, or a not-found result, and
	// holds the site's global not-found and error pages. Required.
	Router router.Router
	// Renderer composes a page's fragment tree into HTML. Required.
	Renderer render.Engine
	// Cache stores rendered pages. A nil Cache disables caching entirely: every
	// request renders, and nothing is ever stored or served from cache.
	Cache cache.Cache
	// Tracker records which cache keys were produced from which dependency tags.
	// Required, because it is the authority tag invalidation resolves through
	// even when the cache indexes the tags itself.
	Tracker dependency.Tracker
	// Plugins receives the lifecycle hooks. A nil *plugin.Registry is safe and
	// inert: it dispatches to no plugin.
	Plugins *plugin.Registry
	// Metrics receives per-response timings and cache events. Nil means no-op.
	Metrics observability.Metrics
	// Tracer starts one span per request. Nil means no-op.
	Tracer observability.Tracer
	// Logger receives failures the response itself cannot carry, such as an error
	// page that failed to render. Required.
	Logger *slog.Logger
	// DevMode serves diagnostic detail — the failing fragment and the full error
	// chain — on the built-in error page, and adds the render-time header. It
	// must be false in production: those diagnostics can carry credentials,
	// internal hostnames, and filesystem paths.
	DevMode bool
	// DefaultTTL is the cache TTL used for a page that sets no CacheTTL of its
	// own. Zero or less defers to the cache's own default TTL.
	DefaultTTL time.Duration
	// Mounts serves asset file systems under their own URL prefixes, checked
	// before every request is routed. A nil or empty Mounts serves no assets. See
	// Handler.serve for why checking them first is safe.
	Mounts []*asset.Mount
	// Handlers are the application's own http.Handlers, each answering every
	// request under its prefix. They are checked after Mounts and before the
	// router, and like Mounts they are guaranteed by internal/core not to
	// overlap any route.
	Handlers []HandlerMount
	// DevSources are the file systems a development page reloads itself when
	// they change — the templates as read from disk — in addition to every
	// mount's. Ignored outside development.
	DevSources []fs.FS
	// PageReady reports whether a page can render: one with a layout only once
	// registration has bound its content into it. It is how an action answering
	// with a page built on the spot is told so, rather than served a blank one.
	// Nil skips the check.
	PageReady func(*types.Page) bool
	// Middleware wraps the handling of every request, first element outermost.
	// It runs inside the span, the metrics and the panic guard, and before a
	// mount, a handler or the router sees the request.
	Middleware []Middleware
	// MaxBodyBytes bounds an action's request body when the action declares no
	// bound of its own. Zero selects the framework's default; negative means
	// unbounded, which is a decision worth making deliberately.
	MaxBodyBytes int64
	// Invalidator drops cache entries by tag, and is what backs
	// ActionResult.InvalidateTags. Nil makes that field inert.
	Invalidator Invalidator
	// CSRF verifies unsafe requests to actions and issues the tokens
	// {{csrfToken}} renders. Nil turns forgery checking off entirely, which is
	// what an application with no forms and no key gets.
	CSRF *csrf.Guard
}

// Handler serves rendered pages over HTTP. It holds no per-request state, so one
// Handler is safe for concurrent use by as many requests as the server accepts.
type Handler struct {
	router       router.Router
	renderer     render.Engine
	cache        cache.Cache
	tracker      dependency.Tracker
	plugins      *plugin.Registry
	metrics      observability.Metrics
	tracer       observability.Tracer
	logger       *slog.Logger
	devMode      bool
	defaultTTL   time.Duration
	mounts       []*asset.Mount
	handlers     []HandlerMount
	chain        http.Handler
	reload       *reloadHub
	pageReady    func(*types.Page) bool
	maxBodyBytes int64
	invalidator  Invalidator
	csrf         *csrf.Guard
	// flight coalesces concurrent renders of one cache key, so an expiring
	// popular page costs one render rather than one per request that arrives
	// while it is being re-made.
	flight *flight
}

var _ http.Handler = (*Handler)(nil)

// New returns a Handler serving through d. It returns ErrMissingDependency, wrapped
// with the field's name, when Router, Renderer, Tracker, or Logger is nil. A nil
// Cache, Metrics, Tracer, or Plugins is accepted and made inert.
func New(d Deps) (*Handler, error) {
	if d.Router == nil {
		return nil, fmt.Errorf("%w: Router", ErrMissingDependency)
	}
	if d.Renderer == nil {
		return nil, fmt.Errorf("%w: Renderer", ErrMissingDependency)
	}
	if d.Tracker == nil {
		return nil, fmt.Errorf("%w: Tracker", ErrMissingDependency)
	}
	if d.Logger == nil {
		return nil, fmt.Errorf("%w: Logger", ErrMissingDependency)
	}
	h := &Handler{
		router:     d.Router,
		renderer:   d.Renderer,
		cache:      d.Cache,
		tracker:    d.Tracker,
		plugins:    d.Plugins,
		metrics:    observability.MetricsOrNoop(d.Metrics),
		tracer:     observability.TracerOrNoop(d.Tracer),
		logger:     d.Logger,
		devMode:    d.DevMode,
		defaultTTL: d.DefaultTTL,
		// Cloned so a caller mutating d.Mounts after New returns cannot change
		// what this Handler serves.
		mounts:       slices.Clone(d.Mounts),
		handlers:     slices.Clone(d.Handlers),
		pageReady:    d.PageReady,
		maxBodyBytes: d.MaxBodyBytes,
		invalidator:  d.Invalidator,
		csrf:         d.CSRF,
		flight:       newFlight(),
	}
	// Composed once rather than per request: a middleware constructor is
	// allowed to do setup work, and doing it on every request would be a cost
	// nobody asked for.
	if len(d.Middleware) > 0 {
		h.chain = compose(slices.Clone(d.Middleware), http.HandlerFunc(h.serveChained))
	}
	if d.DevMode {
		sources := slices.Clone(d.DevSources)
		for _, mount := range d.Mounts {
			sources = append(sources, mount.FS())
		}
		h.reload = newReloadHub(sources)
	}
	return h, nil
}

// ServeHTTP implements http.Handler: it runs the request lifecycle inside one span
// and reports the completed response to Metrics.
//
// A mount's URL space is claimed before routing, inside serve, but that check no
// longer opts a mounted request out of everything below: see serve and serveMount
// for why a mount now runs through the same timing, span, panic guard, and metric
// every page and document request gets.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Ahead of everything, middleware included: it is the development tool's
	// own channel, and an auth middleware refusing it would turn live reload off
	// with nothing to say why. Outside the metrics too — a stream that stays
	// open for an hour is not a slow request.
	if h.reload != nil && r.URL.Path == devReloadPath {
		h.reload.ServeHTTP(w, r)
		return
	}

	start := time.Now()

	ctx, span := h.startRequestSpan(r)
	defer span.End()
	r = withVarySet(r.WithContext(ctx))

	status := h.serveGuarded(w, r)

	span.SetAttribute("http.status_code", strconv.Itoa(status))
	h.metrics.HTTPResponse(ctx, status, r.URL.Path, time.Since(start))
}

// startRequestSpan opens the request's span, containing a panic from an
// application's Tracer instead of letting it reach net/http.
//
// This is the one part of the tracing bracket that runs before anything has been
// written, so it is the one part where a panic costs the client its response: the
// connection closes with no status line, and the caller sees what looks like a
// network failure rather than a bug in its tracer. A tracer that cannot start a
// span is a reason to serve the page without tracing, not a reason not to serve it.
//
// SetAttribute is inside the guard because it runs here too, before the response.
// When it panics the span is abandoned rather than ended: End would be a third call
// into a Tracer that has already demonstrated it panics, and the framework has
// nothing to gain by making it.
func (h *Handler) startRequestSpan(r *http.Request) (ctx context.Context, span observability.Span) {
	ctx, span = r.Context(), observability.NoopSpan{}

	defer func() {
		if rec := recover(); rec != nil {
			h.logger.Error("collage: tracer panicked starting the request span",
				"panic", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
			ctx, span = r.Context(), observability.NoopSpan{}
		}
	}()

	ctx, span = h.tracer.StartSpan(r.Context(), "collage.http")
	span.SetAttribute("http.method", r.Method)
	span.SetAttribute("http.path", r.URL.Path)
	return ctx, span
}

// queryVary turns a request's query into the cache-key dimensions a route declares.
//
// A nil allow keeps the raw query whole, which is the default and the conservative
// reading: the framework cannot know which parameters a data handler consults, and
// merging two representations serves one visitor another's page. The cost is that
// every "?utm_source=..." variant is its own entry, so a crawler can evict a bounded
// cache without ever asking for a distinct page — which is why a route can say
// otherwise.
//
// A non-nil allow — including an empty one, which drops the query entirely — selects
// only the named parameters, and canonicalises what survives. Canonicalising is safe
// precisely here and not above: once the route has said which parameters matter,
// their order and the absence of everything else are no longer facts about the
// representation. Values are kept in their given order within a parameter, because
// repeating a parameter is how a request expresses a list.
func queryVary(u *url.URL, allow []string) []string {
	if allow == nil {
		return []string{u.RawQuery}
	}
	if len(allow) == 0 || u.RawQuery == "" {
		return nil
	}

	values := u.Query()
	selected := make(url.Values, len(allow))
	for _, name := range allow {
		if vs, ok := values[name]; ok {
			selected[name] = vs
		}
	}
	if len(selected) == 0 {
		return nil
	}
	// url.Values.Encode sorts by key, which is the canonicalisation.
	return []string{selected.Encode()}
}

// serveGuarded runs serve and turns a panic escaping it into a 500 on the normal
// error path — logged, dispatched to every ErrorHook, and counted by the metric
// ServeHTTP reports — instead of letting it unwind into net/http, which closes the
// connection with no status line at all.
//
// The render engine already recovers a panic inside a data handler, a template
// function, or a document handler, and turns it into an ordinary failure. This
// covers everywhere else *inside serve* a request touches code the framework did
// not write: a Router of the application's own, a Cache implementation, a
// dependency Tracker, a Metrics implementation called from serve, an asset mount's
// fs.FS, and a plugin hook.
//
// It does not cover the span-closing and metric calls in ServeHTTP itself —
// SetAttribute, End, and HTTPResponse after the response — and a panic in any of
// them still unwinds into net/http. That is deliberate rather than overlooked:
// those calls bracket the response instead of producing it, so by the time they run
// there is no status left to write and nothing a 500 could add, and moving them
// inside would mean reporting a failed span through the very Tracer that just
// panicked. The comment used to claim it covered all of them, which is the kind of
// promise a panic guard must not make loosely.
//
// The tracer calls that run *before* the response are a different case and are
// guarded, by startRequestSpan rather than here: a panic there costs the client its
// response entirely.
//
// The response's content type follows what the request resolved to, not what this
// frame knows: serve records the resolved route in route, so a panic in an
// application's Cache on a document route produces the document's plain text and a
// panic on a page route produces the HTML error page. See routeKind — that record
// is the whole reason this frame does not have to be told what it is serving.
//
// A panic raised after the response headers are already on the wire — from inside
// the body write — cannot be turned into a 500 any more; serveFailure will try, and
// net/http will log the superfluous WriteHeader. That is still better than dropping
// the connection, and there is nothing else left to do at that point.
func (h *Handler) serveGuarded(w http.ResponseWriter, r *http.Request) (status int) {
	var route routeRef
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		status = h.serveFailure(w, r, route.failure(
			http.StatusInternalServerError,
			stagePanic,
			fmt.Errorf("%w: %v\n%s", ErrPanic, recovered, debug.Stack()),
		))
	}()
	if h.chain == nil {
		return h.serve(w, r, &route)
	}

	// Through the middleware, which may answer the request itself — a 401, a
	// redirect — without serve ever running. The status is then whatever it
	// wrote.
	state := &chainState{route: &route}
	capture := &statusCapturingWriter{ResponseWriter: w}
	h.chain.ServeHTTP(capture, r.WithContext(context.WithValue(r.Context(), chainStateKey{}, state)))
	if state.status != 0 {
		return state.status
	}
	return capture.Status()
}

// serve runs the request lifecycle and returns the status code it wrote.
//
// route is serveGuarded's record of what the request resolved to, and serve is the
// only frame that can fill it in: it is the frame that reads the match. It does so
// the instant a mount claims the request or the router reports a document, before
// calling anything that could panic on that route's behalf, so every failure from
// that point on — including one caught two frames up in serveGuarded — is answered
// in the content type that route requires. See routeKind.
//
// A mount is checked before routing, exactly as before, but no longer returns
// straight to net/http: internal/core's checkMountsDoNotShadow refuses to build a
// handler at all if any mount prefix would shadow a registered page or document
// path, so by the time a Handler exists every mount prefix is guaranteed to own
// URL space no route answers to. That guarantee is what makes checking mounts
// first — rather than falling through to the router and only trying a mount on a
// miss — safe: it can never take a request away from a page or a document, it
// costs nothing but a linear scan of however many mounts exist, and it is why a
// request for the bare mount prefix gets the mount's own plain-text 404 (see
// asset.Mount.Handles) instead of the router's HTML one. Without the close-out
// check enforcing that guarantee elsewhere, checking mounts before routing would
// be a correctness hazard rather than a safe optimization.
//
// What changed is that serve, not ServeHTTP, is where the check happens now:
// serve runs inside serveGuarded, which is what turns a panic into a 500 on the
// normal error path, and ServeHTTP is what times the request and reports the
// HTTPResponse metric. A mount request goes through serveMount so it gets all of
// that instead of bypassing it.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, route *routeRef) int {
	for _, mount := range h.mounts {
		if mount.Handles(r.URL.Path) {
			route.resolved(routeKindMount, mount.Prefix())
			return h.serveMount(w, r, mount, route)
		}
	}
	for _, mount := range h.handlers {
		if strings.HasPrefix(r.URL.Path, mount.Prefix) {
			route.resolved(routeKindHandler, mount.Prefix)
			return h.serveHandler(w, r, mount, route)
		}
	}

	ctx := r.Context()

	match, err := h.router.Match(r)
	if err != nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("collage: match %q: %w", r.URL.Path, err),
			stage:  stageRoute,
		})
	}

	// Router is an interface the embedding application may implement itself, and a
	// nil result with a nil error is a contract violation that would otherwise
	// panic one request into a broken response rather than a 500.
	if match == nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("collage: match %q: router returned no result", r.URL.Path),
			stage:  stageRoute,
		})
	}

	if match.RedirectTo != "" {
		// Written by hand rather than through http.Redirect, which would add a
		// hyperlink body and a Content-Type for a GET: a redirect carries its
		// destination in the Location header, and a body only makes GET and HEAD
		// behave differently for no benefit.
		status := match.RedirectStatus
		if status < 300 || status > 399 {
			// Same reason as the nil-result guard: a Router of the application's
			// own making can leave the status unset, and WriteHeader panics on a
			// status outside the valid range.
			status = http.StatusFound
		}
		w.Header().Set("Location", match.RedirectTo)
		w.WriteHeader(status)
		return status
	}

	// Checked before the not-found fallback below, deliberately: on a successful
	// document match, Page is nil by MatchResult's own contract (exactly one of
	// Page and Document is set), and the nil-Page check below would otherwise
	// treat every matched document as a 404 before this branch ever ran.
	// Before the not-found fallback and before Page, for the same reason the
	// document branch is: a matched action leaves Page and Document nil, and the
	// nil-Page check below would read that as a 404.
	if match.Action != nil {
		route.resolved(routeKindAction, match.Action.Name)
		return h.serveAction(w, r, match, route)
	}

	// A path that exists but does not answer this method. Distinct from a 404,
	// and the distinction is the whole point: the reader is told the URL is real
	// and what it does accept, rather than that it is not a URL.
	if match.MethodNotAllowed {
		w.Header().Set("Allow", strings.Join(match.Allowed, ", "))
		return h.serveFailure(w, r, failure{
			status: http.StatusMethodNotAllowed,
			err:    fmt.Errorf("%w: %s %s", ErrMethodNotAllowed, r.Method, r.URL.Path),
			locale: match.Locale,
			stage:  stageRoute,
		})
	}

	// An OPTIONS request that no action claimed. The router already worked out
	// what the path accepts, so answering here keeps one list in one place.
	if r.Method == http.MethodOptions && len(match.Allowed) > 0 {
		header := w.Header()
		header.Set("Allow", strings.Join(match.Allowed, ", "))
		header.Set("Content-Length", "0")
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}

	if match.Document != nil {
		route.resolved(routeKindDocument, match.Document.Name)
		return h.serveDocument(w, r, match, route)
	}

	// A nil Page is treated as not-found even when IsNotFound is false: Router is
	// an interface the embedding application may implement itself, and there is
	// nothing to render either way.
	if match.IsNotFound || match.Page == nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusNotFound,
			err:    fmt.Errorf("%w: %q", ErrNoRoute, r.URL.Path),
			page:   match.Page,
			locale: match.Locale,
			stage:  stageNotFound,
		})
	}

	page := match.Page

	if err := h.plugins.PageResolved(ctx, &plugin.PageResolvedEvent{
		Page:   page,
		Locale: match.Locale,
		Path:   r.URL.Path,
	}); err != nil {
		return h.serveFailure(w, r, failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stagePageResolved,
		})
	}

	// Only GET and HEAD may be served from cache, and only a cacheable strategy
	// is looked up at all: an unsafe method's response is never a cached page.
	key := ""
	cacheable := h.cache != nil && page.Strategy.Cacheable() && (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		!requestSkipsCache(r)
	if cacheable {
		// The query is a cache dimension, not decoration: a fragment's data
		// handler receives the whole *http.Request and may legitimately render
		// from r.URL.Query(), so two queries against one path are two
		// representations. Which parts of it discriminate is the page's own
		// declaration — see queryVary and Page.CacheParams.
		key = cache.Key(cache.KeyInput{
			Path:    r.URL.Path,
			Locale:  match.Locale,
			Params:  match.PathParams,
			Vary:    queryVary(r.URL, page.CacheParams),
			Request: requestVary(r),
		})
		lookupStart := time.Now()
		// Never read from the cache in development.
		//
		// Templates reload from disk there, which is the point of dev mode — and
		// a cached page hides that reload for as long as its TTL, on exactly the
		// pages a developer is most likely to be editing. Two mechanisms, each
		// sensible alone, combining into "my edit did nothing".
		//
		// The write path below is left alone deliberately: entries are still
		// stored, tags still tracked, CacheWrite hooks still fire, so anyone
		// developing a plugin or an invalidation rule still sees it work. What
		// dev mode removes is serving a page that was rendered before the edit.
		if content, etag, found := h.cacheGet(ctx, key); found {
			h.metrics.CacheEvent(ctx, observability.CacheHit, key)
			// The only place cacheHit is ever reported true: what it times is the
			// lookup that stood in for a render, not a render that did not happen.
			h.metrics.RenderDuration(ctx, page.Name, time.Since(lookupStart), true)
			return h.serveCached(w, r, page, content, etag)
		}
		h.metrics.CacheEvent(ctx, observability.CacheMiss, key)
	}

	// One render serves every request that wants this key, and the rest wait for
	// it. Without the flight, the moment a popular page expires is the moment
	// every request in flight becomes a cache miss and renders — identical work,
	// multiplied by traffic, aimed at the upstream that has just been shown to be
	// slow. See flight.
	//
	// A page with no key is rendered directly. There is nothing to coalesce on,
	// and a page the application declared dynamic is two renders for two requests
	// by its own declaration.
	produce := func() *outcome { return h.renderPage(ctx, r, page, match, key, cacheable) }

	var out *outcome
	shared := false
	if key != "" {
		out, shared = h.flight.do(ctx, key, produce)
	} else {
		out = produce()
	}
	if shared {
		// Not a hit: nothing was in the cache when this request asked. It is the
		// other outcome that costs nothing, and it is worth being able to see
		// separately — a large coalesced count is what a too-short TTL looks
		// like from the outside.
		h.metrics.CacheEvent(ctx, observability.CacheCoalesced, key)
	}

	if out.fail != nil {
		return h.serveFailure(w, r, *out.fail)
	}

	content, etag, personal := h.personalise(w, r, out.content, out.etag)

	header := w.Header()
	header.Set("Content-Type", contentTypeHTML)
	header.Set("ETag", etag)
	h.setCacheHeaders(header, r, page.Strategy, page.CacheTTL)
	// After setCacheHeaders, so it overrides whatever the page's strategy declared.
	// The body that goes on the wire carries this reader's token, whatever the
	// shared one behind it may be cached as.
	if personal {
		header.Set("Cache-Control", "private, no-store")
	}
	if h.devMode {
		header.Set(renderTimeHeader, out.renderTime.String())
	}
	content = withDevOverlay(content, "this page rendered, but part of it failed", out.problems)
	content = h.withDevReload(r, content)
	w.WriteHeader(http.StatusOK)
	writeBody(w, content)

	return http.StatusOK
}

// withDevReload adds the live-reload script to a page in development.
//
// Only to the answer to a GET. Reloading the answer to a POST asks the browser to
// submit the form again, which is the last thing a page should do on its own.
func (h *Handler) withDevReload(r *http.Request, html []byte) []byte {
	if h.reload == nil || r.Method != http.MethodGet {
		return html
	}
	return withReloadScript(html)
}

// CloseDevStreams ends every development reload stream. A graceful shutdown waits
// for open requests, and these never finish by themselves.
func (h *Handler) CloseDevStreams() {
	if h.reload != nil {
		h.reload.Close()
	}
}

// personalise replaces the forgery-token marker in content with this reader's own
// token, and sends the cookie the token is checked against.
//
// This is what lets a page with a form be cached. What is stored, shared between
// readers and handed to everyone waiting on one render, is a body with a marker in
// it — a string nobody can compute without the application's key. What goes on the
// wire is that body with the reader's own token in place of the marker.
//
// A body with no marker is returned untouched, which is every page that has no form.
func (h *Handler) personalise(w http.ResponseWriter, r *http.Request, content []byte, etag string) ([]byte, string, bool) {
	if h.csrf == nil {
		return content, etag, false
	}
	marker := []byte(h.csrf.Marker())
	if !bytes.Contains(content, marker) {
		return content, etag, false
	}

	token, _, err := h.csrf.TokenFor(r)
	if err != nil {
		// Nothing to substitute with. Serving the marker would render a form that
		// is refused on submission with nothing to explain why, so this is a
		// failure rather than a body.
		h.logger.Error("collage: could not issue a forgery token", "err", err)
		return content, etag, false
	}

	personalised := bytes.ReplaceAll(content, marker, []byte(token))
	http.SetCookie(w, h.csrf.Cookie(r, token))
	// Recomputed, because this body is not the one the ETag was made from. An ETag
	// that names a body nobody was sent is how a conditional request is answered
	// 304 for content the client never had.
	return personalised, cache.ETag(personalised), true
}

// renderPage renders one page and returns what to write, or why nothing can be.
//
// It writes nothing itself, and that is the point: the work may be being done on
// behalf of several requests at once, and each of them writes its own response from
// the value this returns.
func (h *Handler) renderPage(
	ctx context.Context,
	r *http.Request,
	page *types.Page,
	match *router.MatchResult,
	key string,
	cacheable bool,
) *outcome {
	// Deliberately after the cache lookup: BeforeRenderHook documents that it
	// does not fire when a cached render is served instead of a fresh one, which
	// is what distinguishes it from PageResolvedHook.
	// Built before the hook, not after: a plugin contributing to the page hoists
	// through this, and hoisting only works before the tree renders.
	rc := types.NewRenderContext(ctx, r, page, match.Locale, match.PathParams)
	if skipsCache(r) {
		// A preview skips the data cache as well as the page cache: an editor
		// shown the draft page around a cached copy of the published data has
		// not been shown the draft.
		types.SkipDataCache(rc)
	}

	if err := h.plugins.BeforeRender(ctx, &plugin.BeforeRenderEvent{
		Context: rc,
		Page:    page,
		Locale:  match.Locale,
		Path:    r.URL.Path,
	}); err != nil {
		return &outcome{fail: &failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stageBeforeRender,
		}}
	}

	renderStart := time.Now()
	result, err := h.renderer.Render(ctx, rc)
	renderTime := time.Since(renderStart)
	if err != nil {
		// result is non-nil on every Render path, including a fatal error, so
		// NotFound can be read here safely; the error is checked first, as
		// Render's contract requires.
		status := http.StatusInternalServerError
		if result.NotFound {
			// A required fragment's data handler reported that the content
			// itself does not exist, as distinct from a failure to fetch it: the
			// request is a 404, not a 500, and resolves to page's own
			// NotFoundPage through errorPageFor exactly as a router miss does.
			status = http.StatusNotFound
		}
		return &outcome{fail: &failure{
			status:   status,
			err:      err,
			page:     page,
			locale:   match.Locale,
			fragment: failedFragment(result),
			stage:    stageRender,
		}}
	}

	// The hook may replace the HTML, and the replacement is what goes on the wire
	// and, below, into the cache.
	afterRender := &plugin.AfterRenderEvent{
		Page:     page,
		Locale:   match.Locale,
		Degraded: result.Degraded(),
		Data:     rc.SharedData,
		HTML:     result.HTML,
	}
	if err := h.plugins.AfterRender(ctx, afterRender); err != nil {
		// No fragment is named: the failure is the plugin's, and pointing the dev
		// page at a fragment that merely happened to be degraded would send a
		// developer looking in the wrong place.
		return &outcome{fail: &failure{
			status: http.StatusInternalServerError,
			err:    err,
			page:   page,
			locale: match.Locale,
			stage:  stageAfterRender,
		}}
	}
	content := afterRender.HTML
	if len(content) == 0 {
		// A blank page is not a page. See ErrEmptyRender.
		return &outcome{fail: &failure{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("%w: page %q", ErrEmptyRender, page.Name),
			page:   page,
			locale: match.Locale,
			stage:  stageRender,
		}}
	}

	// A degraded render is complete enough to serve but must never be cached:
	// caching it would pin one request's transient fragment failure in front of
	// every later request. A HEAD is served from cache but never populates it —
	// it produced no body to store.
	etag := ""
	if cacheable && r.Method == http.MethodGet && !result.Degraded() {
		etag = h.writeCache(r, key, page, content, result.DependencyTags)
	}
	if etag == "" {
		etag = cache.ETag(content)
	}

	return &outcome{content: content, etag: etag, renderTime: renderTime, problems: h.devProblems(result)}
}

// cacheGet is the cache lookup, which never finds anything in development. See the
// call site for why.
func (h *Handler) cacheGet(ctx context.Context, key string) ([]byte, string, bool) {
	if h.devMode {
		return nil, "", false
	}
	content, etag, found := h.cache.Get(ctx, key)
	if found && h.staleMarker(content) {
		// Stored under another forgery key — before a key change, or by a process
		// that generated its own. Served, its form would carry a marker nothing
		// replaces and be refused on submission, so it is a miss, and dropped.
		_ = h.cache.InvalidateKey(ctx, key)
		return nil, "", false
	}
	return content, etag, found
}

// foreignMarker matches the forgery-token marker of any key.
var foreignMarker = regexp.MustCompile(`collage-csrf-[A-Za-z0-9_-]{32}`)

// staleMarker reports whether content carries a forgery-token marker this
// application would not replace: another key's, or any at all when forgery
// protection is off.
func (h *Handler) staleMarker(content []byte) bool {
	if !bytes.Contains(content, []byte("collage-csrf-")) {
		return false
	}
	current := ""
	if h.csrf != nil {
		current = h.csrf.Marker()
	}
	for _, found := range foreignMarker.FindAll(content, -1) {
		if string(found) != current {
			return true
		}
	}
	return false
}

// serveCached writes a cache hit: a 304 when the request's If-None-Match matches
// the stored ETag, otherwise a 200 carrying the stored content. It returns the
// status it wrote.
func (h *Handler) serveCached(w http.ResponseWriter, r *http.Request, page *types.Page, content []byte, etag string) int {
	// The stored body carries a marker where this reader's forgery token goes, so
	// what is written is not what was stored — and the ETag has to name what was
	// written, or a conditional request is answered 304 for a body the client was
	// never sent.
	content, etag, personal := h.personalise(w, r, content, etag)

	header := w.Header()
	header.Set("ETag", etag)
	h.setCacheHeaders(header, r, page.Strategy, page.CacheTTL)
	if personal {
		header.Set("Cache-Control", "private, no-store")
	}

	if cache.ETagMatch(r.Header.Get("If-None-Match"), etag) {
		// No Content-Type and no body: a 304 tells the client its copy is still
		// good, it does not re-describe the representation.
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}

	header.Set("Content-Type", contentTypeHTML)
	w.WriteHeader(http.StatusOK)
	writeBody(w, content)

	return http.StatusOK
}

// writeCache stores content under key, after giving CacheWriteHook the chance to
// adjust the TTL and tags or to suppress the write, and records the key's tags with
// the tracker. Both write paths track: SetTagged indexes the tags inside the cache
// and Track indexes them in the tracker, and invalidation may go through either, so
// the two indexes are kept deliberately redundant.
//
// A failure here never fails the request. The page has already rendered, and
// serving it uncached is strictly better than turning a cache problem into a 500.
//
// It returns the ETag the cache stored the entry under, or the empty string when
// nothing was written. Cache is an interface the application may implement, and its
// contract is that Set returns the content's ETag — so the stored value, not a
// locally recomputed one, is what the fresh response must advertise. A cache that
// derives ETags its own way would otherwise send one value on the fresh response and
// a different one on every hit that followed, and every conditional request would
// miss.
func (h *Handler) writeCache(r *http.Request, key string, page *types.Page, content []byte, tags []string) string {
	ctx := r.Context()

	event := &plugin.CacheWriteEvent{
		Key:  key,
		Page: page,
		TTL:  h.ttlFor(page.Strategy, page.CacheTTL),
		Tags: tags,
	}
	if err := h.plugins.CacheWrite(ctx, event); err != nil {
		h.reportError(r, failure{err: err, page: page, stage: stageCacheWrite})
		return ""
	}
	if event.Skip {
		return ""
	}

	var etag string
	var err error
	if tagged, ok := h.cache.(cache.TaggedCache); ok {
		etag, err = tagged.SetTagged(ctx, key, content, event.TTL, event.Tags)
	} else {
		etag, err = h.cache.Set(ctx, key, content, event.TTL)
	}
	if err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: cache write: %w", err), page: page, stage: stageCacheWrite})
		return ""
	}
	h.metrics.CacheEvent(ctx, observability.CacheSet, key)

	if err := h.tracker.Track(ctx, key, event.Tags); err != nil {
		h.reportError(r, failure{err: fmt.Errorf("collage: track %q: %w", key, err), page: page, stage: stageCacheWrite})
	}
	return etag
}

// ttlFor returns the cache TTL for a route with the given strategy and cacheTTL:
// cacheTTL itself when set, then staticCacheTTL for StrategyStatic, otherwise the
// handler's DefaultTTL, which may itself be zero to defer to the cache's default.
//
// It is shared by the page and document paths. They are parameterized on strategy
// and cacheTTL directly, rather than on *types.Page or *types.Document, because
// the two types share no common field accessor to feed a single implementation
// through, and this package uses neither an interface for that nor reflection —
// the values themselves are all the logic below needs.
//
// The StrategyStatic case is what makes that strategy mean what it says. "Render
// once and serve until explicitly invalidated" is its documented contract, but
// Static() sets no CacheTTL, so without this a static route fell through to
// DefaultTTL — which pkg/collage's own defaulting forces to five minutes — and every
// static route silently re-rendered on that cycle.
func (h *Handler) ttlFor(strategy types.RenderStrategy, cacheTTL time.Duration) time.Duration {
	if cacheTTL > 0 {
		return cacheTTL
	}
	if strategy == types.StrategyStatic {
		return staticCacheTTL
	}
	return h.defaultTTL
}

// setCacheHeaders writes the Cache-Control for a route with the given strategy and
// cacheTTL, and, whenever that response is publicly cacheable, the Vary header
// naming the headers the request declared through Vary. Vary belongs only on a public response: it tells a shared
// cache which request headers select between representations, and a no-store
// response has no representation to select. Shared by the page and document paths;
// see ttlFor for why it is parameterized rather than typed on either route kind.
func (h *Handler) setCacheHeaders(header http.Header, r *http.Request, strategy types.RenderStrategy, cacheTTL time.Duration) {
	control := h.cacheControl(strategy, cacheTTL)
	if skipsCache(r) {
		// A preview's page is one reader's, and must not be kept by anything
		// between the server and them either.
		control = "private, no-store"
	}
	header.Set("Cache-Control", control)

	if !strings.HasPrefix(control, "public") {
		return
	}
	if names := requestVaryHeaders(r); len(names) > 0 {
		header.Set("Vary", strings.Join(names, ", "))
	}
}

// cacheControl returns the Cache-Control value for a route with the given render
// strategy: static is cached but always revalidated, incremental is cached for its
// effective TTL, and dynamic is never stored. The incremental value is derived from
// cacheTTL, not from a TTL a CacheWriteHook adjusted, so the header a client sees
// does not change from request to request. Shared by the page and document paths;
// see ttlFor for why it is parameterized rather than typed on either route kind.
func (h *Handler) cacheControl(strategy types.RenderStrategy, cacheTTL time.Duration) string {
	switch strategy {
	case types.StrategyStatic:
		return "public, max-age=0, must-revalidate"
	case types.StrategyIncremental:
		seconds := int64(h.ttlFor(strategy, cacheTTL) / time.Second)
		if seconds < 0 {
			seconds = 0
		}
		return "public, max-age=" + strconv.FormatInt(seconds, 10)
	default:
		return "no-store"
	}
}

// writeBody writes content, including for a HEAD request.
//
// Writing on a HEAD looks wrong and is not: net/http discards a HEAD response's body
// itself, and it derives Content-Length from what the handler wrote. Returning early
// instead — which is what this did — left every HEAD response advertising
// "Content-Length: 0" for a resource whose GET reports its real size, which is
// exactly what HEAD exists to tell a client. The body is built either way, since the
// page has already rendered; what is skipped is only the copy onto the socket, and
// net/http is the one positioned to skip it.
func writeBody(w http.ResponseWriter, content []byte) {
	if len(content) == 0 {
		return
	}
	// The error is deliberately unchecked: the status line and headers are
	// already on the wire, so there is nothing left to tell the client, and a
	// client that hung up mid-body is not a server fault worth logging.
	_, _ = w.Write(content)
}
