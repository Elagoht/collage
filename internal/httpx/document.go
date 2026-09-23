package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ErrDocumentRenderingUnsupported is reported to plugins, and served as a 500,
// when Deps.Renderer does not implement document execution. Renderer is an
// interface the embedding application may supply itself — Deps documents it as
// "composes a page's fragment tree into HTML" — and one that implements only that
// contract cannot serve a document. This guards the gap the same way New already
// guards a nil Router result or an out-of-range redirect status, rather than
// letting an unchecked type assertion turn into a panic mid-request.
var ErrDocumentRenderingUnsupported = errors.New("collage: renderer does not support document execution")

// documentRenderer is the capability serveDocument needs beyond render.Engine's
// page-only contract. It is declared locally, and reached from h.renderer through a
// type assertion, rather than added to render.Engine itself: widening render.Engine
// would force every render.Engine implementation — including a page-only test
// double — to grow a method it does not need just to keep compiling. *render.
// SlotEngine already satisfies it, via ExecuteDocument.
type documentRenderer interface {
	// ExecuteDocument runs doc's handler and returns its body, content type and
	// dependency tags. See render.SlotEngine.ExecuteDocument.
	ExecuteDocument(ctx context.Context, doc *types.Document, rc *types.RenderContext) (*render.DocumentResult, error)
}

// serveDocument handles a request that matched a document. It runs the same
// lifecycle as a page — cache lookup, ETag, conditional response, execute, cache
// write, tag tracking — minus the render hooks, which do not fire for a document
// because no render occurs: see plugin.BeforeRenderHook's doc comment. It returns
// the status it wrote.
//
// route is serveGuarded's record of the resolved route, already marked as this
// document by serve before this was called. Every failure below is built through
// it and written by serveFailure, so a document's plain-text error surface is not
// something this function remembers to reach for: it is what the resolved route
// makes serveFailure write, here and in the panic guard two frames up alike. See
// routeKind.
func (h *Handler) serveDocument(w http.ResponseWriter, r *http.Request, match *router.MatchResult, route *routeRef) int {
	doc := match.Document
	ctx := r.Context()

	// Only GET and HEAD may be served from cache, and only a cacheable strategy is
	// looked up at all — mirrors the page path's own cacheable computation.
	cacheable := h.cache != nil && doc.Strategy.Cacheable() && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	key := ""
	if cacheable {
		key = cache.Key(cache.KeyInput{
			Path:   r.URL.Path,
			Locale: match.Locale,
			Params: match.PathParams,
			Vary:   queryVary(r.URL, doc.CacheParams),
		})
		lookupStart := time.Now()
		if content, etag, found := h.cache.Get(ctx, key); found {
			h.metrics.CacheEvent(ctx, observability.CacheHit, key)
			h.metrics.RenderDuration(ctx, doc.Name, time.Since(lookupStart), true)
			return h.serveCachedDocument(w, r, doc, content, etag)
		}
		h.metrics.CacheEvent(ctx, observability.CacheMiss, key)
	}

	docRenderer, ok := h.renderer.(documentRenderer)
	if !ok {
		err := fmt.Errorf("%w: %T", ErrDocumentRenderingUnsupported, h.renderer)
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender, err))
	}

	rc := types.NewRenderContext(ctx, r, nil, match.Locale, match.PathParams)
	result, err := docRenderer.ExecuteDocument(ctx, doc, rc)
	if err != nil {
		// result is non-nil on every ExecuteDocument path, including a failure,
		// so NotFound can be read here safely; the error is checked first, as
		// ExecuteDocument's contract requires.
		status := http.StatusInternalServerError
		if result != nil && result.NotFound {
			status = http.StatusNotFound
		}
		return h.serveFailure(w, r, route.failure(status, stageRender, err))
	}

	if len(result.Body) == 0 {
		err := fmt.Errorf("%w: document %q", types.ErrEmptyDocumentBody, doc.Name)
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender, err))
	}

	// Dispatched before the ETag is computed and before anything is cached, so a
	// plugin that rewrites the body — a minifier, most obviously — is what gets
	// stored and what the ETag describes. Doing it after would serve one thing and
	// cache another, and hand clients an ETag for a body they never received.
	documentEvent := &plugin.DocumentRenderedEvent{
		Document:    doc,
		ContentType: doc.ContentType,
		Locale:      match.Locale,
		Path:        r.URL.Path,
		Body:        result.Body,
	}
	if err := h.plugins.DocumentRendered(ctx, documentEvent); err != nil {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender, err))
	}
	result.Body = documentEvent.Body
	if len(result.Body) == 0 {
		err := fmt.Errorf("%w: document %q, after plugin post-processing", types.ErrEmptyDocumentBody, doc.Name)
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender, err))
	}

	// Only a GET populates the cache; a HEAD is served from cache but never
	// populates it — it produced no body worth storing under its own request.
	etag := ""
	if cacheable && r.Method == http.MethodGet {
		etag = h.writeDocumentCache(r, key, doc, result, route)
	}
	if etag == "" {
		etag = cache.ETag(result.Body)
	}

	header := w.Header()
	header.Set("Content-Type", doc.ContentType)
	header.Set("ETag", etag)
	h.setCacheHeaders(header, doc.Strategy, doc.CacheTTL)
	if h.devMode {
		header.Set(renderTimeHeader, result.Timing.Total.String())
	}
	w.WriteHeader(http.StatusOK)
	writeBody(w, result.Body)

	return http.StatusOK
}

// serveCachedDocument writes a cache hit for a document: a 304 when the request's
// If-None-Match matches the stored ETag, otherwise a 200 carrying the stored
// content. It mirrors the page path's serveCached. It returns the status it wrote.
func (h *Handler) serveCachedDocument(w http.ResponseWriter, r *http.Request, doc *types.Document, content []byte, etag string) int {
	header := w.Header()
	header.Set("ETag", etag)
	h.setCacheHeaders(header, doc.Strategy, doc.CacheTTL)

	if cache.ETagMatch(r.Header.Get("If-None-Match"), etag) {
		// No Content-Type and no body: a 304 tells the client its copy is still
		// good, it does not re-describe the representation.
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}

	header.Set("Content-Type", doc.ContentType)
	w.WriteHeader(http.StatusOK)
	writeBody(w, content)

	return http.StatusOK
}

// writeDocumentCache stores result.Body under key for doc, after giving
// CacheWriteHook the chance to adjust the TTL and tags or to suppress the write, and
// records the key's tags with the tracker. It mirrors the page path's writeCache,
// including writing through both the tagged and untagged paths so the tracker is
// always updated. A failure here never fails the request: the document has already
// executed, and serving it uncached is strictly better than turning a cache problem
// into a 500.
//
// It returns the ETag the cache stored the entry under, or the empty string when
// nothing was written.
//
// Its failures are reported, never served: they carry a zero status because the
// document is already on its way out, so they go through reportError rather than
// serveFailure. They are still built through route, so the log record names the
// document exactly as a served failure's would.
//
// CacheWriteEvent.Page is deliberately left nil here: a document was never
// rendered from a *types.Page and there is none to carry. That nil is documented
// on the field itself, because a plugin that dereferences it unguarded panics on
// every document request and loses the cache write with it.
func (h *Handler) writeDocumentCache(r *http.Request, key string, doc *types.Document, result *render.DocumentResult, route *routeRef) string {
	ctx := r.Context()

	event := &plugin.CacheWriteEvent{
		Key:  key,
		TTL:  h.ttlFor(doc.Strategy, doc.CacheTTL),
		Tags: result.Tags,
	}
	if err := h.plugins.CacheWrite(ctx, event); err != nil {
		h.reportError(r, route.failure(0, stageCacheWrite, err))
		return ""
	}
	if event.Skip {
		return ""
	}

	var etag string
	var err error
	if tagged, ok := h.cache.(cache.TaggedCache); ok {
		etag, err = tagged.SetTagged(ctx, key, result.Body, event.TTL, event.Tags)
	} else {
		etag, err = h.cache.Set(ctx, key, result.Body, event.TTL)
	}
	if err != nil {
		h.reportError(r, route.failure(0, stageCacheWrite, fmt.Errorf("collage: cache write: %w", err)))
		return ""
	}
	h.metrics.CacheEvent(ctx, observability.CacheSet, key)

	if err := h.tracker.Track(ctx, key, event.Tags); err != nil {
		h.reportError(r, route.failure(0, stageCacheWrite, fmt.Errorf("collage: track %q: %w", key, err)))
	}
	return etag
}
