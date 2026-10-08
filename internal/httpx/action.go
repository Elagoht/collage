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

	// The page's guards, not the action's own: an action has no spine to
	// guard, but one on a page's URL is reached through that page, and a page
	// a reader may not see is a page whose form the reader may not submit.
	// Before the body limit and the CSRF check — a guard that answers never
	// reads the body, and a logged-out forged POST is better spent against the
	// guard than the forgery check.
	if page := match.Page; page != nil {
		if status, allowed := h.checkGuards(w, r, route, match.Locale, page.Guards()); !allowed {
			return status
		}
	}

	// A multipart body over the parser's memory budget spills its files to disk,
	// and net/http removes them only for the request it made — not for this one,
	// a copy, which is the one the forgery check and the handler parse. Without
	// this every large upload leaves its files behind, and so does every refused
	// one.
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	// Bounded before the handler sees it, not by the handler. A limit every
	// handler has to remember is a limit the one handler that forgot does not
	// have, and that handler is the one an anonymous caller will find.
	var body *limitedBody
	if limit := h.bodyLimit(action); limit >= 0 {
		// Already bounded before the middleware, as a rule; a middleware that
		// read the body and dropped the error leaves it read past its bound,
		// and the action answers for it. See boundBeforeMiddleware.
		body = boundBody(w, r, limit)
		if body.tooLarge.Load() {
			return h.serveFailure(w, r, route.failure(http.StatusRequestEntityTooLarge, stageRoute,
				actionBodyTooLarge(action, limit)))
		}
	}

	// Checked before the handler runs, and before anything it might change.
	//
	// Unsafe methods only: a GET action changes nothing by contract, and a token
	// on it would be a token in a URL, which is a token in a log file and in a
	// Referer header. OPTIONS, the other safe method, never reaches an action:
	// registration refuses it (router.ErrInvalidActionMethod), and the router
	// answers it itself.
	if h.csrf != nil && !action.SkipCSRF && !types.SafeMethod(r.Method) {
		// A streaming body is the handler's to read, so its token comes from
		// the header alone: looking for the form field would parse the body.
		verify := h.csrf.Verify
		if action.StreamingBody {
			verify = h.csrf.VerifyHeader
		}
		if err := verify(r); err != nil {
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
	// flow work: the handler stores what went wrong under a Key and returns the
	// form's own page, which reads it while rendering.
	//
	// Its Page is the page whose URL the action answers on, so that return is
	// Page: rc.Page. Without it a page's own action had to reach the page it
	// belongs to through a variable declared before the page and filled after,
	// because the handler is written while the page is still being built.
	rc := types.NewRenderContext(ctx, r, match.Page, match.Locale, match.PathParams)
	// So the handler builds its redirect by name, without the application
	// handed to it.
	if h.routes.Page != nil {
		types.BindRoutes(rc, h.routes)
	}
	if skipsCache(r) {
		types.SkipDataCache(rc)
	}

	// After the limit and the forgery check, so a plugin reads the body as the
	// handler would; before the handler, so a refusal changes nothing.
	before := &plugin.BeforeActionEvent{Action: action, Page: match.Page, Locale: match.Locale, Request: r}
	if err := h.plugins.BeforeAction(ctx, before); err != nil {
		return h.serveFailure(w, r, route.failure(actionErrorStatus(err), stageBeforeAction,
			fmt.Errorf("collage: action %q: %w", action.Name, err)))
	}
	if before.Result != nil {
		return h.writeActionResult(w, r, rc, match, before.Result, route)
	}

	result, err := action.Handler(ctx, rc)
	if err != nil {
		// A handler that read past the bound and failed failed because of it,
		// whatever error it chose to say so with: one that wrapped the read's
		// error is a 413 already, and one that replaced it with its own — "upload
		// failed" — is the same request too large, not a fault of the server's.
		// The handler's own error is kept: it is what the operator reads.
		if body != nil && body.tooLarge.Load() {
			return h.serveFailure(w, r, route.failure(http.StatusRequestEntityTooLarge, stageRender,
				errors.Join(fmt.Errorf("collage: action %q: the handler read past its body limit: %w", action.Name, err),
					&http.MaxBytesError{Limit: body.limit})))
		}
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
	return h.appBodyLimit()
}

// appBodyLimit is the application's limit, or the default when it set none.
func (h *Handler) appBodyLimit() int64 {
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
		return writeRedirect(w, r, status, result.Location)

	case result.Fragment != nil:
		html, err := h.renderer.RenderFragment(r.Context(), rc, result.Fragment)
		if err != nil {
			// Missing and broken apart, as on the page: a required fragment whose
			// data handler wrapped ErrNotFound is a 404 here too. An optional
			// one's failure never reaches this point — the policy absorbed it —
			// so the error is read the way Render's NotFound would read it.
			status := http.StatusInternalServerError
			if errors.Is(err, types.ErrNotFound) {
				status = http.StatusNotFound
			}
			return h.serveFailure(w, r, route.failure(status, stageRender, err))
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
		h.warnBareRefusal(r, match, status)
		header.Set("Content-Length", "0")
		w.WriteHeader(status)
		return status
	}
}

// warnBareRefusal tells a developer about a form refused with nothing to show.
//
// A 422 with no body is a legitimate answer to a script, but to a browser's form
// post it is a blank page, and the usual cause is a handler that answered with
// Page: rc.Page on a URL no page is at — so rc.Page was nil and the result fell
// through to a bare status. Dev mode only, and a warning rather than a failure,
// because the response is the one the handler asked for.
func (h *Handler) warnBareRefusal(r *http.Request, match *router.MatchResult, status int) {
	if !h.devMode || status != http.StatusUnprocessableEntity || r.Header.Get(FetchHeader) != "" {
		return
	}
	name := ""
	if match.Action != nil {
		name = match.Action.Name
	}
	h.logger.Warn(bareRefusalMessage, "action", name, "path", r.URL.Path)
}

// bareRefusalMessage is what warnBareRefusal logs.
const bareRefusalMessage = "collage: action answered 422 with no body — a form post gets a blank page. " +
	"rc.Page is set only for an action on a page's URL; elsewhere answer with the registered page itself"

// writeActionPage renders a whole page as the response body: the shape a validation
// failure takes, where the handler stores what went wrong under a Key and hands back
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
		Page:           result.Page,
		Locale:         match.Locale,
		Degraded:       rendered.Degraded(),
		Fragments:      rendered.FragmentReports(),
		DependencyTags: append([]string(nil), rendered.DependencyTags...),
		Values:         types.ValuesOf(rc),
		HTML:           rendered.HTML,
	}
	plugin.PrepareHoist(afterRender, rendered.HoistEnds, rc.Hoisted())
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
	p, err := h.personalise(w, r, html, "")
	if err != nil {
		return h.serveFailure(w, r, failureFor(r, http.StatusInternalServerError, stagePlugin, err))
	}
	html = p.body
	// A hook's personal body overrides even a handler's own Cache-Control: the
	// handler declared a body it knew, not the one a plugin made of it.
	if p.hookPersonal {
		w.Header().Set("Cache-Control", "private, no-store")
	}
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
// as personal as its page may be. A handler that set Cache-Control keeps its own,
// unless a PersonaliseHook made the body personal: the handler declared a body it
// knew, not the one a plugin made of it.
func (h *Handler) writeFragmentRead(w http.ResponseWriter, r *http.Request, html []byte, cacheControlSet bool) int {
	p, err := h.personalise(w, r, html, "")
	if err != nil {
		return h.serveFailure(w, r, failureFor(r, http.StatusInternalServerError, stagePlugin, err))
	}
	html = p.body
	header := w.Header()
	if !cacheControlSet || p.hookPersonal {
		header.Set("Cache-Control", "private, no-cache")
	}
	etag := cache.ETag(html)
	header.Set("ETag", etag)
	// Not for a body a hook made personal, as for a cached page: only "*" could
	// match its new ETag, and it names nothing this reader holds.
	if !p.hookPersonal && cache.ETagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}
	header.Set("Content-Type", contentTypeHTML)
	w.WriteHeader(http.StatusOK)
	writeBody(w, html)
	return http.StatusOK
}

// writeRedirect answers with a redirect, honouring the fetch convention: a
// script marked the request with FetchHeader would rather be handed the
// destination than redirected to it, because fetch follows redirects itself
// and the script would navigate to the page and have it rendered twice.
func writeRedirect(w http.ResponseWriter, r *http.Request, status int, location string) int {
	// The answer depends on FetchHeader, so a cache keying it on the URL alone
	// would hand a script's 204 to a browser that asked to be redirected.
	addVary(w.Header(), FetchHeader)
	if r.Header.Get(FetchHeader) != "" {
		w.Header().Set(LocationHeader, location)
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}
	w.Header().Set("Location", location)
	w.WriteHeader(status)
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
