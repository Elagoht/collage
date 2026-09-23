package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ErrMethodNotAllowed reports a request whose path exists but answers no such
// method.
var ErrMethodNotAllowed = errors.New("collage: method not allowed")

// ErrNoActionHandler reports an action registered without a handler.
var ErrNoActionHandler = errors.New("collage: action has no handler")

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
			// 403, not 400. The request was well formed; it was not authorised.
			return h.serveFailure(w, r, route.failure(http.StatusForbidden, stageRoute,
				fmt.Errorf("collage: action %q: %w", action.Name, err)))
		}
	}

	ctx := r.Context()

	// One RenderContext for the whole action, handed to the handler and then to
	// whatever renders the response. That is what makes the ordinary validation
	// flow work: the handler puts what went wrong into SharedData and returns the
	// form's own page, which reads it while rendering.
	rc := types.NewRenderContext(ctx, r, nil, match.Locale, match.PathParams)

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
	if header.Get("Cache-Control") == "" {
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
		header.Set("Location", result.Location)
		w.WriteHeader(status)
		return status

	case result.Fragment != nil:
		html, err := h.renderer.RenderFragment(r.Context(), rc, result.Fragment)
		if err != nil {
			return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stageRender, err))
		}
		return writeHTML(w, statusOr(result.Status, http.StatusOK), html)

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
	return writeHTML(w, statusOr(result.Status, http.StatusOK), rendered.HTML)
}

// writeHTML writes an HTML body with status. Cache-Control is already set by the
// caller: an action's body is one submission's, and must not be stored anywhere.
func writeHTML(w http.ResponseWriter, status int, html []byte) int {
	w.Header().Set("Content-Type", contentTypeHTML)
	w.WriteHeader(status)
	writeBody(w, html)
	return status
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
