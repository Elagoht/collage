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

// PageResolvedEvent describes a request having been resolved to a page. Every field
// is read-only: a plugin observes the resolution, it does not redirect it.
type PageResolvedEvent struct {
	// Page is the page the request resolved to.
	Page *types.Page
	// Locale is the resolved locale for the request.
	Locale string
	// Path is the request path that resolved to Page.
	Path string
}

// BeforeRenderEvent describes a render about to start. Every field is read-only: a
// plugin observes the render about to happen, it does not redirect it — see
// AfterRenderEvent for the point at which a plugin may act on the output.
type BeforeRenderEvent struct {
	// Page is the page about to be rendered.
	Page *types.Page
	// Locale is the locale the page is about to be rendered for.
	Locale string
	// Path is the request path being served.
	Path string
}

// AfterRenderEvent describes a render that just completed.
type AfterRenderEvent struct {
	// Page is the page that was rendered. Read-only.
	Page *types.Page
	// Locale is the locale the page was rendered for. Read-only.
	Locale string
	// Degraded reports whether any fragment in the render failed, whether or not a
	// fallback covered for it. Read-only.
	Degraded bool
	// HTML is the rendered output. A plugin implementing AfterRenderHook MAY
	// replace this slice to add a post-process step; the replaced value is what
	// the caller serves and, unless a CacheWriteHook suppresses it, caches.
	HTML []byte
}

// CacheWriteEvent describes a render result about to be written to the cache.
type CacheWriteEvent struct {
	// Key is the cache key the result would be stored under. Read-only.
	Key string
	// Page is the page the result was rendered from. Read-only.
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

// CacheInvalidateEvent describes a cache invalidation that has just happened. Its
// field is read-only: a plugin observes which tags were invalidated, it does not
// choose them — a plugin that wants to trigger an invalidation uses
// Host.InvalidateTags instead.
type CacheInvalidateEvent struct {
	// Tags are the dependency tags that were invalidated.
	Tags []string
}

// ErrorEvent describes a failure encountered while serving a request. Every field
// is read-only: a plugin implementing ErrorHook observes and reacts to the failure
// — logging it, reporting it, incrementing a metric — it does not change how the
// framework responds to it.
type ErrorEvent struct {
	// Err is the error that occurred.
	Err error
	// Page is the page being processed when the error occurred, or nil when the
	// error occurred before a page was resolved, for example when no route
	// matched the request.
	Page *types.Page
	// Path is the request path being processed when the error occurred.
	Path string
	// Stage names where in the request pipeline the error occurred, for example
	// "render" or "cache_write". It is caller-defined, not an exhaustive enum.
	Stage string
}
