package plugin

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// PageResolvedHook is implemented by a plugin that wants to observe a request
// having been resolved to a page, before rendering begins.
type PageResolvedHook interface {
	// OnPageResolved is called once per request, immediately after the router
	// resolves it to a page.
	OnPageResolved(ctx context.Context, ev *PageResolvedEvent) error
}

// BeforeRenderHook is implemented by a plugin that wants to observe a render about
// to start.
type BeforeRenderHook interface {
	// OnBeforeRender is called immediately before the render engine runs for a
	// page. Unlike OnPageResolved, it does not fire when a cached render is served
	// instead of a fresh one.
	OnBeforeRender(ctx context.Context, ev *BeforeRenderEvent) error
}

// AfterRenderHook is implemented by a plugin that wants to observe, or post-process,
// a render that just completed.
type AfterRenderHook interface {
	// OnAfterRender is called after the render engine produces output for a page.
	// A plugin implementing this hook may replace ev.HTML to add a post-process
	// step, such as injecting an analytics snippet.
	OnAfterRender(ctx context.Context, ev *AfterRenderEvent) error
}

// DocumentRenderedHook is implemented by a plugin that wants to observe, or
// transform, a document's output before it is served.
//
// It is AfterRenderHook's counterpart for the non-HTML half of the site. Without
// it a plugin that post-processes output — a minifier, most obviously — covers
// pages and silently skips every sitemap, feed and JSON endpoint.
type DocumentRenderedHook interface {
	// OnDocumentRendered is called after a document's handler produces its body
	// and before that body is served or cached. A plugin implementing this hook
	// may replace ev.Body.
	OnDocumentRendered(ctx context.Context, ev *DocumentRenderedEvent) error
}

// DocumentRenderedEvent describes a document that has just produced its body.
type DocumentRenderedEvent struct {
	// Document is the live *types.Document that produced Body. Not copied for
	// this event, and not defended against mutation; see PageResolvedEvent.Page.
	Document *types.Document
	// ContentType is the document's declared content type, so a hook can decide
	// whether it handles this format without parsing the body to find out.
	ContentType string
	// Locale is the locale the document was produced for. This event's own copy.
	Locale string
	// Path is the request path being served. This event's own copy.
	Path string
	// Body is the document's output. A plugin implementing DocumentRenderedHook
	// MAY replace this slice; the replacement is what the caller serves and,
	// unless a CacheWriteHook suppresses it, caches.
	Body []byte
}

// CacheWriteHook is implemented by a plugin that wants to observe, or adjust, a
// render result about to be written to the cache.
type CacheWriteHook interface {
	// OnCacheWrite is called before a render result is written to the cache. A
	// plugin implementing this hook may set ev.Skip to suppress the write, or
	// adjust ev.TTL and ev.Tags.
	OnCacheWrite(ctx context.Context, ev *CacheWriteEvent) error
}

// CacheInvalidateHook is implemented by a plugin that wants to observe a cache
// invalidation.
type CacheInvalidateHook interface {
	// OnCacheInvalidate is called after the cache entries for ev.Tags have been
	// invalidated.
	OnCacheInvalidate(ctx context.Context, ev *CacheInvalidateEvent) error
}

// RequestHook is implemented by a plugin that shapes the context a request is
// served under before collage starts on it — before its request span, before
// middleware, before routing.
//
// It is for what has to be the outermost thing about a request: a trace carried
// in from the caller, whose span must be the parent of collage's own. Middleware
// added with Host.Use runs too late for that, inside the span collage has already
// started.
type RequestHook interface {
	// OnRequest returns the context to serve r under, derived from r's, and a
	// function collage calls with the status once the response is written. The
	// function may be nil. It must not write to the response.
	OnRequest(r *http.Request) (context.Context, func(status int))
}

// StreamCloser is implemented by a plugin serving connections that never end by
// themselves — an event stream, a WebSocket.
//
// Shutdown runs after the server has stopped, because a plugin must not be torn
// out from under requests still in flight; but the server stops by waiting for
// every open request to finish, and a stream never does. CloseStreams runs first,
// before the server waits, so the plugin can end its streams and let it.
type StreamCloser interface {
	// CloseStreams ends every open stream the plugin serves. It must not block
	// on those streams finishing.
	CloseStreams()
}

// Hoist adds html to the page's hoist area under key, after the render — what a
// plugin that only learns what the page needs from its finished markup would
// otherwise splice in by hand: a stylesheet for the code blocks it highlighted.
//
// It lands where the layout put {{hoist area}}, after what the render declared
// there. A key the render already declared in the area is left alone, and so is a
// key an earlier Hoist added: what a page declares is more specific than what a
// plugin adds afterwards. When an earlier plugin has replaced the HTML, the place
// the layout chose can no longer be found; "head" then lands before </head>, and
// any other area reports false. It reports whether html was added.
func (ev *AfterRenderEvent) Hoist(area, key string, html template.HTML) bool {
	if area == "" || key == "" {
		return false
	}
	if ev.hoisted == nil {
		ev.hoisted = make(map[string]bool)
	}
	id := area + "\x00" + key
	if ev.declared[id] || ev.hoisted[id] {
		return false
	}
	at, ok := ev.hoistEnds[area]
	if !ok || !sameSlice(ev.HTML, ev.hoistBase) {
		if area != "head" {
			return false
		}
		at = bytes.Index(bytes.ToLower(ev.HTML), []byte("</head>"))
		if at < 0 {
			return false
		}
		ev.hoistEnds = nil
	}
	out := make([]byte, 0, len(ev.HTML)+len(html))
	out = append(out, ev.HTML[:at]...)
	out = append(out, html...)
	out = append(out, ev.HTML[at:]...)
	for a, end := range ev.hoistEnds {
		if end >= at {
			ev.hoistEnds[a] = end + len(html)
		}
	}
	ev.HTML = out
	ev.hoistBase = out
	ev.hoisted[id] = true
	return true
}

func sameSlice(a, b []byte) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// PrepareHoist gives ev what Hoist needs: where each area ends in ev.HTML, and the
// keys the render declared in each. The framework calls it before dispatching.
func PrepareHoist(ev *AfterRenderEvent, ends map[string]int, hoisted *types.Hoisted) {
	ev.hoistEnds = make(map[string]int, len(ends))
	for a, end := range ends {
		ev.hoistEnds[a] = end
	}
	ev.hoistBase = ev.HTML
	ev.declared = make(map[string]bool)
	for _, area := range hoisted.Areas() {
		for _, item := range hoisted.Items(area) {
			ev.declared[area+"\x00"+item.Key] = true
		}
	}
}

// Warn reports a warning-level finding about this render: shown over the page in
// development, listed in a build's report.
func (ev *AfterRenderEvent) Warn(rule, message string) {
	ev.Findings = append(ev.Findings, types.Finding{Level: types.FindingWarning, Rule: rule, Message: message})
}

// Error reports an error-level finding about this render: shown over the page in
// development, and it fails a static build. The page is still served.
func (ev *AfterRenderEvent) Error(rule, message string) {
	ev.Findings = append(ev.Findings, types.Finding{Level: types.FindingError, Rule: rule, Message: message})
}

// BuildFinishedHook is implemented by a plugin that checks a static build as a
// whole — what no single render can tell: two pages with one title, a link to a
// page the build did not write.
type BuildFinishedHook interface {
	// OnBuildFinished is called once every page, document and asset has been
	// written. An error returned from it fails the build; a finding is reported
	// with ev.Warn or ev.Error.
	OnBuildFinished(ctx context.Context, ev *BuildFinishedEvent) error
}

// BuildFinishedEvent describes a finished static build.
type BuildFinishedEvent struct {
	// OutDir is the directory the build wrote into.
	OutDir string
	// Files are every file the build wrote, in no particular order.
	Files []BuiltFile
	// Findings are what the plugins that ran so far reported.
	Findings []types.Finding
}

// Warn reports a warning-level finding about the build, about the page at path
// when it concerns one.
func (ev *BuildFinishedEvent) Warn(path, rule, message string) {
	ev.Findings = append(ev.Findings, types.Finding{Level: types.FindingWarning, Rule: rule, Message: message, Path: path})
}

// Error reports an error-level finding about the build; it fails the build.
func (ev *BuildFinishedEvent) Error(path, rule, message string) {
	ev.Findings = append(ev.Findings, types.Finding{Level: types.FindingError, Rule: rule, Message: message, Path: path})
}

// BuiltFile is one file a static build wrote.
type BuiltFile struct {
	// Kind is "page", "document" or "asset".
	Kind string
	// Name is the page's or document's registered name; empty for an asset.
	Name string
	// Locale is the locale a page or document was rendered in.
	Locale string
	// Path is the URL path the file answers, as a link on the site would name it.
	Path string
	// File is the file's absolute path on disk. Read it with os.ReadFile when
	// the content is needed: holding every page of a large site in memory to
	// hand over here would cost what few checks need.
	File string
}

// ErrorHook is implemented by a plugin that wants to observe a failure encountered
// while serving a request.
type ErrorHook interface {
	// OnError is called for a failure encountered while serving a request. An
	// error returned from OnError is logged and swallowed by the Registry rather
	// than propagated or re-dispatched: an error handler that itself errors must
	// not recurse into another round of error handling.
	OnError(ctx context.Context, ev *ErrorEvent) error
}

// PageResolvedEvent describes a request having been resolved to a page. Locale
// and Path are this event's own copies: a plugin may overwrite them without
// consequence beyond this dispatch. Page is not: see its own doc comment.
type PageResolvedEvent struct {
	// Page is the live *types.Page the framework resolved the request to — this
	// event does not copy it, deliberately: copying a page (and its fragment
	// tree) on every request would defeat a cache-first framework's hot path.
	// Nothing in this package stops a plugin from writing through it, but doing
	// so mutates the same Page every other request and hook sees, concurrently
	// with those requests reading it: that is a data race, which -race reports and
	// which silently corrupts a map or slice without it. Treat it as read-only by
	// convention, not by enforcement. See the package doc comment.
	Page *types.Page
	// Locale is the resolved locale for the request.
	Locale string
	// Path is the request path that resolved to Page.
	Path string
}

// BeforeRenderEvent describes a render about to start. Locale and Path are this
// event's own copies; Page is the live framework object — see its doc comment,
// and PageResolvedEvent's, for what that means. See AfterRenderEvent for the
// point at which a plugin is meant to act on the render's output.
type BeforeRenderEvent struct {
	// Context is the render about to run, before any fragment has touched it.
	//
	// It is the only hook that gets one, and the reason is hoisting: a plugin that
	// wants to contribute to the page — a structured-data block, a preload hint —
	// has to declare before the tree renders, because the markers are resolved when
	// it finishes. By AfterRender the page is assembled and the only thing left is
	// to splice, which is what having a mechanism was meant to stop.
	//
	// Declaring here puts the plugin at depth zero, so anything a fragment declares
	// under the same key wins. That is the right way round: the plugin is providing
	// a default, the page is providing the specific thing.
	Context *types.RenderContext
	// Page is the live *types.Page about to be rendered. Not copied for this
	// event, and not defended against mutation; see PageResolvedEvent.Page.
	Page *types.Page
	// Locale is the locale the page is about to be rendered for.
	Locale string
	// Path is the request path being served.
	Path string
	// Static reports that the page is being rendered for a static build rather
	// than for a request; see AfterRenderEvent.Static.
	Static bool
}

// AfterRenderEvent describes a render that just completed.
//
// A plugin checking the output reports what it finds with Warn and Error; see
// types.Finding.
type AfterRenderEvent struct {
	// Findings are what the plugins that ran so far reported about this render,
	// through Warn and Error.
	Findings []types.Finding
	// Fragments reports how each fragment of the render went — its time, whether
	// it failed — and DependencyTags the tags the render depended on. What a
	// development tool shows beside the page; this event's own copies.
	Fragments      []types.FragmentReport
	DependencyTags []string
	// Hoist's bookkeeping, set by PrepareHoist.
	hoistEnds map[string]int
	hoistBase []byte
	declared  map[string]bool
	hoisted   map[string]bool
	// Static reports that the page was rendered for a static build — through
	// App.RenderPath — rather than for a request. A plugin checking the output
	// runs then, and in development, and stays out of a production server's way.
	Static bool
	// Page is the live *types.Page that was rendered. Not copied for this event,
	// and not defended against mutation; see PageResolvedEvent.Page.
	Page *types.Page
	// Locale is the locale the page was rendered for. This event's own copy.
	Locale string
	// Degraded reports whether any fragment in the render failed, whether or not
	// a fallback covered for it. This event's own copy.
	Degraded bool
	// HTML is the rendered output. A plugin implementing AfterRenderHook MAY
	// replace this slice to add a post-process step; the replaced value is what
	// the caller serves and, unless a CacheWriteHook suppresses it, caches.
	HTML []byte
	// Data is the render's SharedData: whatever the page's fragments exchanged
	// while producing HTML.
	//
	// It is how a plugin reaches what the page was built *from* rather than what
	// it was rendered *into* — a structured-data plugin wants the article, not the
	// markup it would otherwise have to parse back. What is in it is entirely the
	// application's convention; the framework puts nothing there.
	//
	// It is the live map rather than a copy, for the same reason Page is: copying
	// it on every render would cost the hot path. Writing to it from a hook races
	// nothing (the render has finished) but is pointless, and a plugin that keeps
	// a reference past the hook is holding request-scoped state.
	Data map[string]any // any: SharedData's own value type, which fragments define
}

// CacheWriteEvent describes a render result about to be written to the cache.
type CacheWriteEvent struct {
	// Key is the cache key the result would be stored under. This event's own
	// copy; changing it does not redirect where the write goes.
	Key string
	// Page is the live *types.Page the result was rendered from, or nil when the
	// result is a document's: a document renders no page, and there is none to
	// carry. A hook that reads Page MUST check it, exactly as one reading
	// ErrorEvent.Page must — dereferencing it unguarded panics on every document
	// request, and because a panicking hook abandons the cache write it was
	// dispatched for, the document is then never cached at all.
	//
	// When non-nil, it is not copied for this event and not defended against
	// mutation; see PageResolvedEvent.Page.
	Page *types.Page
	// TTL is how long the cached entry would remain valid. A plugin implementing
	// CacheWriteHook may adjust it.
	TTL time.Duration
	// Tags are the dependency tags the entry would be written with. A plugin
	// implementing CacheWriteHook may adjust them.
	Tags []string
	// Skip suppresses the cache write when set true by a plugin implementing
	// CacheWriteHook. It starts false.
	Skip bool
}

// CacheInvalidateEvent describes a cache invalidation that has just happened.
type CacheInvalidateEvent struct {
	// Tags are the dependency tags that were invalidated. This event's own copy;
	// a plugin that wants to trigger an invalidation uses Host.InvalidateTags
	// instead of trying to feed this back into one.
	Tags []string
	// Paths are the URL paths of the cached pages and documents the
	// invalidation dropped, sorted — what a CDN has to purge and a search engine
	// be told has changed. A page that is not cached is never in it: nothing of
	// it was dropped.
	Paths []string
}

// ErrorEvent describes a failure encountered while serving a request.
type ErrorEvent struct {
	// Err is the error that occurred. This event's own value.
	Err error
	// Page is the live *types.Page being processed when the error occurred, or
	// nil when the error occurred before a page was resolved, for example when
	// no route matched the request. When non-nil, it is not copied for this
	// event and not defended against mutation; see PageResolvedEvent.Page.
	Page *types.Page
	// Path is the request path being processed when the error occurred. This
	// event's own copy.
	Path string
	// Stage names where in the request pipeline the error occurred, for example
	// "render" or "cache_write". It is caller-defined, not an exhaustive enum,
	// and this event's own copy.
	Stage string
}
