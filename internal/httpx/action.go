package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ErrMethodNotAllowed reports a request whose path exists but answers no such
// method.
var ErrMethodNotAllowed = errors.New("collage: method not allowed")

// ErrNoActionHandler reports an action registered without a handler.
var ErrNoActionHandler = types.ErrNoActionHandler

// ErrEmptyRender reports a page that rendered no markup at all.
//
// It is a failure rather than a 200 with nothing in it, which is what it used to be.
// The static builder has always refused to write one on the grounds that a file
// nobody can read is worse than no file; serving one is the same mistake with a
// status code on it, where the reader gets a blank page and the operator gets a
// success in the log.
//
// The usual cause is a page handed to ActionResult.Page that was built on the spot
// rather than registered. Registration is what binds a page's content into its
// layout, so an unregistered page renders its layout around an empty required slot —
// a failure the root fragment absorbs, leaving nothing.
var ErrEmptyRender = types.ErrEmptyRender

// ErrUnregisteredPage reports an action answering with a page that was never
// registered. Registration is what puts a page's content into its layout, so such a
// page renders as a layout around nothing — which is ErrEmptyRender's usual cause,
// caught before the render and named for what it is.
var ErrUnregisteredPage = errors.New("collage: action answered with a page that was never registered")

// defaultMaxBodyBytes bounds a request body when neither the action nor the
// application says otherwise.
//
// Four megabytes: comfortably more than any form a person fills in, and small
// enough that a burst of concurrent requests cannot exhaust a small server. It is a
// bound rather than a guess at what is enough, and it exists because without one the
// size of the allocation is chosen by whoever sent the request.
const defaultMaxBodyBytes int64 = 4 << 20

// serveAction runs a matched action and writes what it answers with.
func (h *Handler) serveAction(w http.ResponseWriter, r *http.Request, match *router.MatchResult, route *routeRef) int {
	action := match.Action
	if action.Handler == nil {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender,
			fmt.Errorf("%w: %q", ErrNoActionHandler, action.Name)))
	}

	// Bounded before the handler sees it, not by the handler. A limit every
	// handler has to remember is a limit the one handler that forgot does not
	// have, and that handler is the one an anonymous caller will find.
	if limit := h.bodyLimit(action); limit >= 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}

	// Checked before the handler runs, and before anything it might change.
	//
	// Unsafe methods only: a GET action changes nothing by contract, and a token
	// on it would be a token in a URL, which is a token in a log file and in a
	// Referer header.
	if h.csrf != nil && !action.SkipCSRF && !types.SafeMethod(r.Method) {
		if err := h.csrf.Verify(r); err != nil {
			// 403, not 400. The request was well formed; it was not authorised —
			// unless reading the token hit the body limit, which is a request too
			// large to be read at all, and says so.
			status := http.StatusForbidden
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			return h.serveFailure(w, r, route.failure(status, stageRoute,
				fmt.Errorf("collage: action %q: %w", action.Name, err)))
		}
	}

	ctx := r.Context()

	// One RenderContext for the whole action, handed to the handler and then to
	// whatever renders the response. That is what makes the ordinary validation
	// flow work: the handler puts what went wrong into SharedData and returns the
	// form's own page, which reads it while rendering.
	rc := types.NewRenderContext(ctx, r, nil, match.Locale, match.PathParams)
	if skipsCache(r) {
		types.SkipDataCache(rc)
	}

	result, err := action.Handler(ctx, rc)
	if err != nil {
		return h.serveFailure(w, r, route.failure(actionErrorStatus(err), stageRender,
			fmt.Errorf("collage: action %q: %w", action.Name, err)))
	}

	if result == nil {
		// A handler that did something and has nothing to say about it. 204 is the
		// honest answer, and not an error worth inventing a body for.
		result = &types.ActionResult{Status: http.StatusNoContent}
	}

	// Before the response, deliberately. An action that changed something and then
	// redirected to a page built from it must not have that page served from an
	// entry the change made wrong; invalidating afterwards leaves a window in which
	// the reader follows the redirect and is handed the old page.
	if len(result.InvalidateTags) > 0 && h.invalidator != nil {
		if err := h.invalidator(ctx, result.InvalidateTags); err != nil {
			h.logger.Error("collage: action invalidation failed",
				"action", action.Name, "tags", result.InvalidateTags, "err", err)
		}
	}

	return h.writeActionResult(w, r, rc, match, result, route)
}

// actionErrorStatus maps a handler's error to a status.
func actionErrorStatus(err error) int {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, types.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// bodyLimit resolves the byte limit for one action: its own, then the
// application's, then the default. A negative value means unbounded.
func (h *Handler) bodyLimit(action *types.Action) int64 {
	if action.MaxBodyBytes != 0 {
		return action.MaxBodyBytes
	}
	if h.maxBodyBytes != 0 {
		return h.maxBodyBytes
	}
	return defaultMaxBodyBytes
}

// FetchHeader marks a request a script made with fetch, which would rather be told
// where an action redirects than be redirected: an action answering it with a
// Location answers 204 with LocationHeader instead.
const FetchHeader = "Collage-Fetch"

// LocationHeader carries an action's redirect to a request marked with FetchHeader.
const LocationHeader = "Collage-Location"

// writeActionResult writes the response an action asked for.
//
// Exactly one of Location, Fragment, Page and Body decides the body, checked in that
// order. A result that sets none of them is a bare status, which is what a DELETE
// usually wants.
func (h *Handler) writeActionResult(
	w http.ResponseWriter,
	r *http.Request,
	rc *types.RenderContext,
	match *router.MatchResult,
	result *types.ActionResult,
	route *routeRef,
) int {
	header := w.Header()
	for name, values := range result.Header {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	// An action's response is what one submission produced. Nothing between here
	// and the reader should keep it, and a handler that knows better says so by
	// setting the header itself.
	cacheControlSet := header.Get("Cache-Control") != ""
	if !cacheControlSet {
		header.Set("Cache-Control", "no-store")
	}

	switch {
	case result.Location != "":
		status := result.Status
		if status == 0 {
			// 303, not 302. After a form post it is what turns the follow-up into
			// a GET, so a reload does not submit the form a second time — which is
			// the bug the pattern exists to prevent.
			status = http.StatusSeeOther
		}
		// A script submitting the form with fetch says so, and is handed the
		// destination instead of being redirected to it: fetch would follow the
		// redirect and download the page, and the script would then navigate to
		// it and have it rendered a second time. It navigates once, with this.
		if r.Header.Get(FetchHeader) != "" {
			header.Set(LocationHeader, result.Location)
			w.WriteHeader(http.StatusNoContent)
			return http.StatusNoContent
		}
		header.Set("Location", result.Location)
		w.WriteHeader(status)
		return status

	case result.Fragment != nil:
		html, err := h.renderer.RenderFragment(r.Context(), rc, result.Fragment)
		if err != nil {
			return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender, err))
		}
		status := statusOr(result.Status, http.StatusOK)
		if status == http.StatusOK && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			return h.writeFragmentRead(w, r, html, cacheControlSet)
		}
		return h.writeActionHTML(w, r, status, html)

	case result.Page != nil:
		return h.writeActionPage(w, r, rc, match, result, route)

	case len(result.Body) > 0:
		contentType := result.ContentType
		if contentType == "" {
			// Never sniffed. Guessing a type from bytes is how a text response
			// becomes a download, and how an uploaded file becomes a script.
			contentType = "application/octet-stream"
		}
		header.Set("Content-Type", contentType)
		status := statusOr(result.Status, http.StatusOK)
		w.WriteHeader(status)
		writeBody(w, result.Body)
		return status

	default:
		status := statusOr(result.Status, http.StatusNoContent)
		header.Set("Content-Length", "0")
		w.WriteHeader(status)
		return status
	}
}

// writeActionPage renders a whole page as the response body: the shape a validation
// failure takes, where the handler puts what went wrong in SharedData and hands back
// the form's own page to read it.
func (h *Handler) writeActionPage(
	w http.ResponseWriter,
	r *http.Request,
	rc *types.RenderContext,
	match *router.MatchResult,
	result *types.ActionResult,
	route *routeRef,
) int {
	if h.pageReady != nil && !h.pageReady(result.Page) {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender,
			fmt.Errorf("%w: page %q — register it with RegisterPage and answer with the same value, "+
				"rather than building a page in the handler", ErrUnregisteredPage, result.Page.Name)))
	}

	// The same context the handler used, now pointed at the page it chose, so
	// everything it shared is there to be read.
	rc.Page = result.Page

	if err := h.plugins.BeforeRender(r.Context(), &plugin.BeforeRenderEvent{
		Context: rc,
		Page:    result.Page,
		Locale:  match.Locale,
		Path:    r.URL.Path,
	}); err != nil {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageBeforeRender, err))
	}

	rendered, err := h.renderer.Render(r.Context(), rc)
	if err != nil {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender, err))
	}
	// AfterRender as for any page: a validation failure's page is a page, and the
	// minifier, the image rewriter and whatever else shapes pages shape it too.
	afterRender := &plugin.AfterRenderEvent{
		Page:     result.Page,
		Locale:   match.Locale,
		Degraded: rendered.Degraded(),
		Data:     rc.SharedData,
		HTML:     rendered.HTML,
	}
	if err := h.plugins.AfterRender(r.Context(), afterRender); err != nil {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageAfterRender, err))
	}
	if len(afterRender.HTML) == 0 {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender,
			fmt.Errorf("%w: page %q", ErrEmptyRender, result.Page.Name)))
	}
	// A page an action answers with is a page: what the checks found about it is
	// shown as on any other. Written, never cached.
	html := withDevOverlay(afterRender.HTML, overlayHeading(nil), h.devFindings(afterRender.Findings))
	return h.writeActionHTML(w, r, statusOr(result.Status, http.StatusOK), html)
}

// writeActionHTML writes an HTML body with status. Cache-Control is already set by
// the caller: an action's body is one submission's, and must not be stored anywhere.
//
// Personalised like a page is, because it was rendered like one: a form in it
// carries the marker, and a marker that reaches the reader is a form whose next
// submission is refused. That includes every fragment path, which is served as
// an action.
func (h *Handler) writeActionHTML(w http.ResponseWriter, r *http.Request, status int, html []byte) int {
	html, _, _ = h.personalise(w, r, html, "")
	w.Header().Set("Content-Type", contentTypeHTML)
	w.WriteHeader(status)
	writeBody(w, html)
	return status
}

// writeFragmentRead answers a GET for a fragment — every fragment path, and any
// action reading one — with an ETag, and a 304 with no body when the reader
// already holds that ETag.
//
// A fragment refreshed on a timer is usually unchanged, and the render happens
// either way; what the 304 saves is the body on the wire and the client's work
// comparing it. The ETag is the hash of the body as sent, after the reader's own
// forgery token went in, so it never names bytes this reader was not given.
//
// Revalidated rather than unstored: "private, no-cache" lets the reader's own
// browser keep the body and ask whether it still holds, where "no-store" would
// have it throw the body away and fetch it whole. Private, because a fragment is
// as personal as its page may be. A handler that set Cache-Control keeps its own.
func (h *Handler) writeFragmentRead(w http.ResponseWriter, r *http.Request, html []byte, cacheControlSet bool) int {
	html, _, _ = h.personalise(w, r, html, "")
	header := w.Header()
	if !cacheControlSet {
		header.Set("Cache-Control", "private, no-cache")
	}
	etag := cache.ETag(html)
	header.Set("ETag", etag)
	if cache.ETagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}
	header.Set("Content-Type", contentTypeHTML)
	w.WriteHeader(http.StatusOK)
	writeBody(w, html)
	return http.StatusOK
}

// statusOr returns declared when it is set, and fallback otherwise.
func statusOr(declared, fallback int) int {
	if declared == 0 {
		return fallback
	}
	return declared
}

// Invalidator drops every cache entry built from tags. The application supplies it;
// an action asks for invalidation declaratively, through ActionResult.InvalidateTags,
// rather than being handed the whole application to call a method on.
type Invalidator func(ctx context.Context, tags []string) error
