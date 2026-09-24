package collage

import (
	"encoding/json"
	"fmt"
	"net/http"

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

// ActionResult is what an action answers with. Exactly one of Location, Fragment,
// Page and Body decides the body, checked in that order.
type ActionResult = types.ActionResult

// ActionBuilder builds an Action.
type ActionBuilder struct {
	action *Action
}

// NewAction starts an action named name.
func NewAction(name string) *ActionBuilder {
	return &ActionBuilder{action: &Action{Name: name, Paths: map[string]string{}}}
}

// WithPath declares the URL pattern that reaches this action in locale.
func (b *ActionBuilder) WithPath(locale, pattern string) *ActionBuilder {
	b.action.Paths[locale] = pattern
	return b
}

// WithMethods declares the HTTP methods this action answers. A request to its path
// with any other method is a 405 carrying an Allow header.
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

// WithoutCSRF turns off cross-site request forgery checking for this action.
//
// It is for requests that cannot carry a token: a payment provider's webhook, an API
// called with a bearer token by something that is not a browser. On anything a
// browser submits it gives the protection away entirely.
func (b *ActionBuilder) WithoutCSRF() *ActionBuilder {
	b.action.SkipCSRF = true
	return b
}

// Build returns the action.
func (b *ActionBuilder) Build() *Action { return b.action }

// SeeOther is the answer to a successful form post: a 303 to location.
//
// 303 rather than 302, and it matters. A 303 turns the follow-up request into a GET,
// so reloading the destination does not submit the form a second time — which is the
// bug the redirect-after-post pattern exists to prevent.
func SeeOther(location string) *ActionResult {
	return &ActionResult{Status: http.StatusSeeOther, Location: location}
}

// RenderFragment answers with one fragment's markup: the changed part of a page,
// rather than the page.
func RenderFragment(f *Fragment) *ActionResult {
	return &ActionResult{Fragment: f}
}

// RenderPage answers with a whole page, which is the shape a validation failure
// takes: the handler puts what went wrong into the render context's shared data and
// hands back the form's own page, which reads it while rendering.
func RenderPage(p *Page) *ActionResult {
	return &ActionResult{Page: p}
}

// JSON answers with body as application/json.
func JSON(status int, body []byte) *ActionResult {
	return &ActionResult{Status: status, Body: body, ContentType: "application/json"}
}

// JSONOf answers with v marshalled as application/json:
//
//	return collage.JSONOf(http.StatusOK, countResponse{Count: n})
//
// It is JSON with the json.Marshal and its error handled, which is every JSON
// action's first three lines otherwise.
func JSONOf[T any](status int, v T) (*ActionResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("collage: marshal JSON response: %w", err)
	}
	return JSON(status, body), nil
}

// NoContent answers with a bare status and no body.
func NoContent(status int) *ActionResult {
	return &ActionResult{Status: status}
}
