package collage

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// Action answers requests for a URL with a method a page or a document does not.
//
// It is one mechanism with two uses. Attached to a page with
// PageBuilder.WithAction, it gives that page's own URL a POST — which is what an
// ordinary HTML form needs, since a form's action is the page it is on. Registered
// on its own with App.RegisterAction, it is a URL of its own: a webhook, a JSON
// endpoint, a DELETE a fetch() calls.
type Action = types.Action

// ActionHandlerFunc handles one request to an action and says what to answer with.
type ActionHandlerFunc = types.ActionHandlerFunc

// ActionResult is what an action answers with. At most one of Location, Fragment,
// Page and Body decides the body; a result that sets more than one is refused
// with ErrAmbiguousActionResult.
type ActionResult = types.ActionResult

// ActionBuilder builds an Action through a fluent chain of WithX calls. See the
// package doc comment for how builder errors are accumulated and why it is safe
// to ignore them until registration. An action built without WithHandler records
// ErrNoActionHandler at Build time.
type ActionBuilder struct {
	action *Action
	errs   []error
	// buildTime is what the last Build found missing; see PageBuilder.
	buildTime error
}

// NewAction starts an action named name.
func NewAction(name string) *ActionBuilder {
	return &ActionBuilder{action: &Action{Name: name, Paths: map[string]string{}}}
}

// WithPath declares the URL pattern that reaches this action in locale.
//
// locale is not a label: it names the locale tree the path is written into, and
// a request reaches that tree only when the locale is Config.Locale.Default (at
// the bare path) or listed in Locale.Supported (under its prefix, "/tr/…").
// Default is "en" when left unset, so a Turkish-only site writes
// WithPath("tr", …) and sets Locale.Default to "tr". A path in a locale neither
// names is refused at registration with ErrLocaleUnreachable.
//
// A locale the action already has a path in records ErrDuplicateLocalePath and
// keeps the first path; an empty locale records ErrInvalidLocale. Both are
// retrievable via BuildErr and refused at registration.
func (b *ActionBuilder) WithPath(locale, pattern string) *ActionBuilder {
	if err := checkPathLocale(b.action.Paths, locale, "action", b.action.Name); err != nil {
		b.errs = append(b.errs, err)
		return b
	}
	b.action.Paths[locale] = pattern
	return b
}

// WithMethods declares the HTTP methods this action answers. A request to its path
// with any other method is a 405 carrying an Allow header.
//
// OPTIONS, TRACE and CONNECT are refused at registration with
// ErrInvalidActionMethod: the router answers OPTIONS itself, and the other two
// are not something a page's URL should do.
func (b *ActionBuilder) WithMethods(methods ...string) *ActionBuilder {
	b.action.Methods = append(b.action.Methods, methods...)
	return b
}

// WithHandler sets what runs.
func (b *ActionBuilder) WithHandler(h ActionHandlerFunc) *ActionBuilder {
	b.action.Handler = h
	return b
}

// WithMaxBodyBytes bounds this action's request body, overriding the application's
// Server.MaxBodyBytes. A negative value means unbounded.
func (b *ActionBuilder) WithMaxBodyBytes(n int64) *ActionBuilder {
	b.action.MaxBodyBytes = n
	return b
}

// WithStreamingBody hands the handler the request body unread, for it to read
// as a stream: nothing before the handler parses a form out of it. The forgery
// token is then accepted only in the X-CSRF-Token header (Security.CSRFHeaderName),
// never in a form field, so a plain HTML form cannot post here; a fetch() can.
// WithMaxBodyBytes still bounds the body, and its default is small, so a large
// upload raises it. Registration refuses it on an action answering none of
// POST, PUT or PATCH, with ErrStreamingBodyMethod.
func (b *ActionBuilder) WithStreamingBody() *ActionBuilder {
	b.action.StreamingBody = true
	return b
}

// WithBodyTimeout gives a request to this action d to be read and answered,
// in place of the server's ReadTimeout and WriteTimeout, which are sized for
// pages and cut a long upload off. Both deadlines are set to now plus d once the
// page's guards have let the request through, before anything reads the body;
// the write deadline covers the whole upload too, since net/http starts it when
// the headers arrive. Zero, the default, keeps the server's deadlines; a negative
// d records ErrNegativeBodyTimeout, retrievable via BuildErr and refused at
// registration.
//
// It needs a ResponseWriter that can take a deadline. One wrapped by a
// middleware that does not implement Unwrap cannot, and the request then runs
// under the server's deadlines, with a warning logged once.
func (b *ActionBuilder) WithBodyTimeout(d time.Duration) *ActionBuilder {
	if d < 0 {
		b.errs = append(b.errs, fmt.Errorf("%w: action %q declares %v", ErrNegativeBodyTimeout, b.action.Name, d))
	}
	b.action.BodyTimeout = d
	return b
}

// WithoutCSRF turns off cross-site request forgery checking for this action.
//
// It is for requests that cannot carry a token: a payment provider's webhook, an API
// called with a bearer token by something that is not a browser. On anything a
// browser submits it gives the protection away entirely.
func (b *ActionBuilder) WithoutCSRF() *ActionBuilder {
	b.action.SkipCSRF = true
	return b
}

// Build returns the Action constructed so far. It never panics and never returns
// nil for a non-nil builder, even if WithX calls recorded errors along the way, or
// if no handler was ever set (which records ErrNoActionHandler); call BuildErr to
// check whether any errors were recorded.
//
// The action returned is a copy: a WithX call after Build changes the builder and
// what a later Build returns, never an action already built.
func (b *ActionBuilder) Build() *Action {
	b.buildTime = nil
	if b.action.Handler == nil {
		b.buildTime = fmt.Errorf("%w: %q", ErrNoActionHandler, b.action.Name)
	}
	a := types.CloneAction(b.action)
	types.RecordBuildErr(a, b.BuildErr())
	return a
}

// BuildErr returns the errors accumulated by prior WithX calls and by the last
// Build, joined with errors.Join, or nil if none were recorded. Call it after Build.
func (b *ActionBuilder) BuildErr() error {
	return errors.Join(append(slices.Clone(b.errs), b.buildTime)...)
}

// SeeOther is the answer to a successful form post: a 303 to location.
//
// 303 rather than 302, and it matters. A 303 turns the follow-up request into a GET,
// so reloading the destination does not submit the form a second time — which is the
// bug the redirect-after-post pattern exists to prevent.
func SeeOther(location string) *ActionResult {
	return &ActionResult{Status: http.StatusSeeOther, Location: location}
}

// SeeOtherPage is SeeOther to the page or document registered as name, its path
// built by rc.URL in the request's locale:
//
//	return collage.SeeOtherPage(rc, "story", map[string]string{"id": id})
//
// An unknown name or params that do not fill the pattern are rc.URL's error, so
// a handler can return both values as they are.
func SeeOtherPage(rc *RenderContext, name string, params map[string]string) (*ActionResult, error) {
	location, err := rc.URL(name, params)
	if err != nil {
		return nil, err
	}
	return SeeOther(location), nil
}

// RenderFragment answers with one fragment's markup: the changed part of a page,
// rather than the page.
func RenderFragment(f *Fragment) *ActionResult {
	return &ActionResult{Fragment: f}
}

// RenderPage answers with a whole page, which is the shape a validation failure
// takes: the handler puts what went wrong into the render context's shared data and
// hands back the form's own page, which reads it while rendering. On the page's own
// URL that page is rc.Page:
//
//	return collage.RenderPage(rc.Page), nil
func RenderPage(p *Page) *ActionResult {
	return &ActionResult{Page: p}
}

// JSONBytes answers with body, already encoded, as application/json.
func JSONBytes(status int, body []byte) *ActionResult {
	return &ActionResult{Status: status, Body: body, ContentType: "application/json"}
}

// JSON answers with v marshalled as application/json:
//
//	return collage.JSON(http.StatusOK, countResponse{Count: n})
//
// It is JSONBytes with the json.Marshal and its error handled, which is every
// JSON action's first three lines otherwise.
func JSON[T any](status int, v T) (*ActionResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("collage: marshal JSON response: %w", err)
	}
	return JSONBytes(status, body), nil
}

// Status answers with a bare status and no body.
func Status(status int) *ActionResult {
	return &ActionResult{Status: status}
}
