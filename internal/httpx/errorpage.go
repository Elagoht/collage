package httpx

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// failure carries everything the error path needs to know about a 4xx or 5xx the
// handler is about to serve.
type failure struct {
	// status is the HTTP status code to write.
	status int
	// err is the failure being reported, never nil: it is what plugins observe
	// through ErrorEvent and, in dev mode, what the built-in page shows.
	err error
	// page is the page being served when the failure happened, or nil when the
	// failure happened before one was resolved.
	page *types.Page
	// locale is the resolved locale, used to render the registered error page. It
	// is empty when the failure happened before locale resolution.
	locale string
	// fragment names the fragment whose failure produced err, or is empty when no
	// single fragment is responsible.
	fragment string
	// kind is the kind of route the request resolved to, and it alone decides
	// whether this failure is written as the HTML error page or as plain text.
	// It is never set by hand at a call site: every failure raised after a route
	// resolves is built through routeRef.failure, and one raised before a route
	// resolves keeps the zero value, routeKindPage. See routeKind.
	kind routeKind
	// route identifies the resolved route in a dev-mode plain-text body: a
	// document's Name, a mount's Prefix. It is empty for a page and for an
	// unresolved request, neither of which writes a plain-text body.
	route string
	// stage names where in the pipeline the failure happened.
	stage string
}

// serveFailure logs f, dispatches it to plugins, and writes the error response. It
// returns the status it wrote.
//
// It is the single writer of every error response this package produces, and the
// one place that decides what an error looks like on the wire. That decision is
// f.kind's and nothing else's: a document or a mount gets plain text, a page — or
// a request that failed before it resolved to anything — gets the registered error
// page when one resolves and renders, otherwise the built-in HTML page. No caller
// picks the writer, so no caller can pick the wrong one. See routeKind for why
// that is worth a type.
func (h *Handler) serveFailure(w http.ResponseWriter, r *http.Request, f failure) int {
	h.reportError(r, f)

	if f.kind.plainText() {
		writePlainText(w, r, f.status, h.devMode, f.route, f.err)
		return f.status
	}

	if page := h.errorPageFor(f); page != nil {
		if content, ok := h.renderErrorPage(r, page, f); ok {
			// The application's own error page says nothing about why it is
			// showing, which is right for a reader and useless for the person
			// who caused it. In development the reason goes on top.
			if h.devMode && f.status >= http.StatusInternalServerError {
				content = withDevOverlay(content, statusTitle(f.status)+", and your error page is showing",
					[]devProblem{{fragment: f.fragment, detail: errorDetail(f.err)}})
			}
			h.writeErrorResponse(w, r, f.status, content)
			return f.status
		}
	}

	h.writeErrorResponse(w, r, f.status, builtinPage(f, h.devMode))
	return f.status
}

// reportError logs f and dispatches it to every registered ErrorHook.
//
// Only a route miss is logged at debug level, and the test is the stage, not the
// status: a route miss is a routine outcome of an anonymous request and logging it
// at error level would bury the failures that are not. A 404 that came out of a
// render — a data handler reporting that the content does not exist — reaches here
// with stage "render" and stays at error level, because it is a failure inside the
// application that an operator chasing a 404 storm has to be able to see. Both
// records carry the stage and the failing fragment, so neither kind of 404 is
// logged without saying where it came from.
//
// A mounted asset gets the same routine-versus-real-failure split, but the stage
// alone cannot carry it the way it does for a page: every asset failure reaches
// here with stage "asset", 404 and 500 alike, because they share one sentinel
// (ErrAssetFailed) and this package makes no attempt to invent a family of asset
// stages to match. So stage "asset" is the one case where the level check also
// reads f.status — a 4xx is bots probing for a stale link or a browser's stray
// favicon request, the single most routine kind of noise a web server sees, and
// belongs at debug for the same reason a page's route miss does; a 5xx means
// something in the mount's own file system genuinely broke (a file that will not
// seek, a permission error) and stays at error level. Every other stage keeps
// the status out of its level decision entirely, exactly as before.
//
// A zero f.status means no response is being written for this failure — a cache
// write that failed after the page rendered — which is logged and reported like any
// other error but changes nothing the client sees.
//
// When f names a document, the log record also names it, so an operator chasing a
// failing sitemap or feed can tell which document broke. It is deliberately left
// off ErrorEvent: ErrorEvent.Page already exists for a page and stays nil here for a
// document, since the event's Path already identifies the route, and widening a
// public plugin struct just to carry a name for a log line is not worth it.
func (h *Handler) reportError(r *http.Request, f failure) {
	level := slog.LevelError
	switch {
	case f.stage == stageNotFound:
		level = slog.LevelDebug
	case f.status == http.StatusNotFound && errors.Is(f.err, types.ErrNotFound):
		// A data handler saying the content does not exist — an unknown slug —
		// is a route miss one step later: the path matched a pattern, and the
		// record behind it is not there. Every bot and every mistyped link does
		// it, so it is logged where a route miss is.
		level = slog.LevelDebug
	case f.stage == stageAsset && f.status < http.StatusInternalServerError:
		level = slog.LevelDebug
	case f.stage == stageRoute && f.status < http.StatusInternalServerError:
		// A refusal the request earned: a method the URL does not answer, a
		// form submitted without a forgery token. Nothing in the application
		// failed — it decided — and the deciding is the most routine thing a
		// site with forms does, because a bot probing them does it all day.
		// Logging that at error level buries the failures that are real, which
		// is the same reason a route miss is at debug.
		//
		// A developer whose own form is being refused is not left guessing:
		// in development the reason is written into the response itself, which
		// is where they are looking.
		level = slog.LevelDebug
	}
	if f.kind == routeKindDocument {
		h.logger.Log(r.Context(), level, "collage: request failed",
			"path", r.URL.Path,
			"stage", f.stage,
			"fragment", f.fragment,
			"document", f.route,
			"error", f.err,
		)
	} else {
		h.logger.Log(r.Context(), level, "collage: request failed",
			"path", r.URL.Path,
			"stage", f.stage,
			"fragment", f.fragment,
			"error", f.err,
		)
	}

	// Error always returns nil: the registry logs and swallows a failing ErrorHook
	// rather than handing it back, precisely so error handling cannot recurse.
	_ = h.plugins.Error(r.Context(), &plugin.ErrorEvent{
		Err:   f.err,
		Page:  f.page,
		Path:  r.URL.Path,
		Stage: f.stage,
	})
}

// errorPageFor returns the page registered to serve f, or nil when none is.
func (h *Handler) errorPageFor(f failure) *types.Page {
	if f.status == http.StatusNotFound {
		return h.resolveNotFound(f.page)
	}
	return h.resolveError(f.page)
}

// resolveNotFound returns the page to serve when a request under page resolves to
// no content: page's own NotFoundPage, else the router's registered global one,
// else nil for the built-in page.
func (h *Handler) resolveNotFound(page *types.Page) *types.Page {
	if page != nil && page.NotFoundPage != nil {
		return page.NotFoundPage
	}
	return h.router.NotFoundPage()
}

// resolveError returns the page to serve when rendering page fails: page's own
// ErrorPage, else the router's registered global one, else nil for the built-in
// page.
func (h *Handler) resolveError(page *types.Page) *types.Page {
	if page != nil && page.ErrorPage != nil {
		return page.ErrorPage
	}
	return h.router.ErrorPage()
}

// renderErrorPage renders page as the response body for f, reporting ok false when
// the render fails or produces nothing.
//
// A failure here is logged exactly once and never retried: the caller falls through
// to the built-in page rather than back into the error path. An error page whose own
// failure re-entered error handling would recurse, and a 500 that can 500 into
// itself is an outage rather than a bad response. The render's dependency tags are
// deliberately dropped and its output is never cached, for the same reason no error
// response is: a transient failure must not be frozen in front of later requests.
//
// It runs the BeforeRender and AfterRender hooks exactly as a page does. Without
// them the error page a server answers with is not the one a plugin shaped: the
// minifier never ran on it, the structured data was never emitted, the images were
// never rewritten — and the 404.html a static export writes, which goes through the
// hooks, differs from the 404 the same site serves.
func (h *Handler) renderErrorPage(r *http.Request, page *types.Page, f failure) ([]byte, bool) {
	ctx := r.Context()
	fail := func(message string, err error) ([]byte, bool) {
		h.logger.Error(message, "page", page.Name, "status", f.status, "error", err)
		h.reportErrorPageFailure(r, page, err)
		return nil, false
	}

	rc := types.NewRenderContext(ctx, r, page, f.locale, nil)
	if err := h.plugins.BeforeRender(ctx, &plugin.BeforeRenderEvent{
		Context: rc,
		Page:    page,
		Locale:  f.locale,
		Path:    r.URL.Path,
	}); err != nil {
		return fail("collage: error page before-render hook failed", err)
	}

	result, err := h.renderer.Render(ctx, rc)
	if err != nil {
		return fail("collage: error page render failed", err)
	}

	afterRender := &plugin.AfterRenderEvent{
		Page:     page,
		Locale:   f.locale,
		Degraded: result.Degraded(),
		Data:     rc.SharedData,
		HTML:     result.HTML,
	}
	if err := h.plugins.AfterRender(ctx, afterRender); err != nil {
		return fail("collage: error page after-render hook failed", err)
	}
	if len(afterRender.HTML) == 0 {
		return fail("collage: error page rendered empty", fmt.Errorf("%w: page %q", ErrEmptyErrorPage, page.Name))
	}
	return afterRender.HTML, true
}

// reportErrorPageFailure tells plugins that the error page itself is broken, under
// its own stage so a reporting plugin can tell "the request failed" from "the page
// that reports failures failed" — the second is the one nobody finds out about
// otherwise, because the client still receives a plausible-looking error page.
//
// It dispatches without logging, since the caller has already logged the specific
// failure, and it cannot recurse: Registry.Error logs and swallows a failing
// ErrorHook by contract rather than handing it back to be handled again.
func (h *Handler) reportErrorPageFailure(r *http.Request, page *types.Page, err error) {
	_ = h.plugins.Error(r.Context(), &plugin.ErrorEvent{
		Err:   err,
		Page:  page,
		Path:  r.URL.Path,
		Stage: stageErrorPage,
	})
}

// writeErrorResponse writes content as an error response of the given status. No
// ETag is set, and Cache-Control is no-store: an error response is never cached, by
// this handler or by anything between it and the client.
func (h *Handler) writeErrorResponse(w http.ResponseWriter, r *http.Request, status int, content []byte) {
	header := w.Header()
	header.Set("Content-Type", contentTypeHTML)
	header.Set("Cache-Control", "no-store")
	// An error page reloads too: the page that failed on a broken template is
	// the one most worth seeing again once the template is fixed.
	content = h.withDevReload(r, content)
	w.WriteHeader(status)
	writeBody(w, content)
}

// failedFragment returns the name of the first fragment in result that failed, or
// the empty string when none did or when there is no metadata to read. It is
// nil-safe: a fatal render returns a Result too, and that is exactly the Result this
// is asked about.
func failedFragment(result *render.Result) string {
	if result == nil || result.Metadata == nil {
		return ""
	}
	fragments := result.Metadata.Fragments
	// A fatal failure is recorded against every fragment it passes through on its
	// way up, outermost first, so the first failed fragment is usually the layout
	// that merely carried it. The one to name is where it started: a failed
	// fragment whose error does not wrap another failed fragment's.
	first := ""
	for i := range fragments {
		if !fragments[i].Failed {
			continue
		}
		if first == "" {
			first = fragments[i].Name
		}
		carried := false
		for j := range fragments {
			if j != i && fragments[j].Failed && fragments[j].Err != nil && fragments[i].Err != fragments[j].Err &&
				errors.Is(fragments[i].Err, fragments[j].Err) {
				carried = true
				break
			}
		}
		if !carried {
			return fragments[i].Name
		}
	}
	return first
}

// builtinPage returns the framework's own error page for f: one self-contained HTML
// document, with no external stylesheet, script, image, or font, so it renders
// identically on a deployment whose assets are exactly what has just broken.
//
// In dev mode it carries the diagnostics a developer needs: the failing fragment's
// name and the full error chain, including a panic's stack. In production it carries
// a generic sentence and nothing else — no error text, no fragment name, no path.
// That is a security property, not a matter of taste: an error message routinely
// carries a database DSN, an internal hostname, or a filesystem path that maps the
// deployment, and an error page is the one response most likely to hand all three to
// an anonymous client.
func builtinPage(f failure, devMode bool) []byte {
	title := statusTitle(f.status)

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	b.WriteString(title)
	b.WriteString("</title>\n</head>\n<body>\n<h1>")
	b.WriteString(title)
	b.WriteString("</h1>\n<p>")
	b.WriteString(statusMessage(f.status))
	b.WriteString("</p>\n")

	if devMode {
		if f.fragment != "" {
			b.WriteString("<p>Fragment: <code>")
			b.WriteString(html.EscapeString(f.fragment))
			b.WriteString("</code></p>\n")
		}
		if detail := errorDetail(f.err); detail != "" {
			b.WriteString("<pre>")
			b.WriteString(html.EscapeString(detail))
			b.WriteString("</pre>\n")
		}
	}

	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// statusTitle returns the built-in page's title for status, such as "404 Not Found".
func statusTitle(status int) string {
	text := http.StatusText(status)
	if text == "" {
		text = "Error"
	}
	return strconv.Itoa(status) + " " + text
}

// statusMessage returns the built-in page's one-sentence body for status. It names
// no page, no path, and no error: see builtinPage.
func statusMessage(status int) string {
	if status == http.StatusNotFound {
		return "The page you requested could not be found."
	}
	return "The server encountered an error and could not complete your request."
}

// errorDetail renders err for the dev-mode built-in page: its message, which already
// contains the whole wrapped chain because every layer wraps with %w, followed by the
// captured stack when a panic is anywhere in that chain.
func errorDetail(err error) string {
	if err == nil {
		return ""
	}

	detail := err.Error()

	var panicErr *render.PanicError
	if errors.As(err, &panicErr) && len(panicErr.Stack) > 0 {
		detail += "\n\n" + string(panicErr.Stack)
	}
	return detail
}
