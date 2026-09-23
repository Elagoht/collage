package plugin

import (
	"context"
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
	// Page is the live *types.Page about to be rendered. Not copied for this
	// event, and not defended against mutation; see PageResolvedEvent.Page.
	Page *types.Page
	// Locale is the locale the page is about to be rendered for.
	Locale string
	// Path is the request path being served.
	Path string
}

// AfterRenderEvent describes a render that just completed.
type AfterRenderEvent struct {
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
