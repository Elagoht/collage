package types

import (
	"context"
	"net/http"
)

// ActionHandlerFunc handles one request to an action and says what to answer with.
//
// It receives the same RenderContext a fragment's data handler does, so it reads path
// parameters, the locale and the request the same way — and writes into SharedData
// the same way, which is how a handler that re-renders a page hands that page what it
// learned. The request body is on rc.Request, already bounded; see Action.MaxBodyBytes.
//
// A nil result with a nil error answers 204 No Content, which is the honest answer for
// a handler that did something and has nothing to say about it.
type ActionHandlerFunc func(ctx context.Context, rc *RenderContext) (*ActionResult, error)

// ActionResult is what an action answers with.
//
// Exactly one of Location, Fragment, Page and Body decides the response body; they are
// checked in that order, and a result that sets none of them is a bare status. The
// zero Status means "whatever the body implies": 303 for a Location, 200 otherwise,
// and 204 for a result with no body at all.
type ActionResult struct {
	// Status is the HTTP status to write. Zero selects one from the body.
	Status int
	// Header is written onto the response before the status. It is the place for
	// Set-Cookie, or for anything else an action needs to say.
	Header http.Header
	// Location redirects. With no Status it is a 303 See Other, which is what a
	// browser needs after a form post: it turns the POST into a GET, so a reload
	// does not submit again.
	Location string
	// Fragment renders one fragment as the body. It is the answer to a form posted
	// from a page that wants only the changed part back.
	Fragment *Fragment
	// Page renders a whole page as the body — the shape a validation failure takes:
	// the handler puts what went wrong in SharedData and returns the form's own
	// page, which reads it while rendering.
	Page *Page
	// Body is written verbatim, with ContentType. It is what a webhook or a JSON
	// endpoint answers with.
	Body []byte
	// ContentType accompanies Body. Empty with a non-empty Body is
	// "application/octet-stream", because guessing from the bytes is how a text
	// response becomes a download.
	ContentType string
	// InvalidateTags are invalidated before the response is written.
	//
	// It is declarative on purpose. An action that changed something almost always
	// has to drop the cached pages built from it, and the alternative — handing
	// every handler a reference to the application so it can call InvalidateTags
	// itself — puts the whole application inside a function whose job is to handle
	// one request.
	InvalidateTags []string
}

// Action answers requests for a URL with a method other than the ones a page or a
// document answers.
//
// It is one mechanism with two uses. Attached to a page it gives that page's URL a
// POST, which is what an ordinary HTML form needs: the form's action is the page it
// is on. Registered on its own it is a URL of its own — a webhook, a JSON endpoint,
// a DELETE that a fetch() calls.
type Action struct {
	// Name identifies the action and appears in registration errors.
	Name string
	// Paths maps a locale to the URL pattern that reaches this action. A
	// page-attached action inherits the page's paths.
	Paths map[string]string
	// Methods are the HTTP methods this action answers. A request to one of its
	// paths with any other method is a 405 carrying an Allow header.
	Methods []string
	// Handler is what runs. It is required.
	Handler ActionHandlerFunc
	// MaxBodyBytes bounds the request body. Zero selects the application's
	// Server.MaxBodyBytes, and a negative value means unbounded — which is a
	// decision worth making deliberately, because an unbounded body is memory an
	// anonymous caller chooses the size of.
	MaxBodyBytes int64
	// SkipCSRF turns off cross-site request forgery checking for this action.
	//
	// It exists for the requests that cannot possibly carry a token: a payment
	// provider's webhook, an API called with a bearer token from something that is
	// not a browser. Turning it off on anything a browser submits gives away the
	// protection entirely, so it is named for what it does rather than for when it
	// is convenient.
	SkipCSRF bool
}

// SafeMethod reports whether method is one that must not change anything, and
// therefore one that needs no forgery check and may be served from cache.
func SafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}
