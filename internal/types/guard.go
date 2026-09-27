package types

import (
	"context"
	"fmt"
	"net/http"
)

// GuardFunc decides whether a request may reach what it guards. It runs after
// the router has resolved the request and before anything is served or read
// from cache, so a blocked reader never touches the page's render or its cache
// entry.
//
// A nil decision with a nil error allows the request. A non-nil decision
// blocks it: a redirect — a 3xx Status with a Location — or a bare refusal
// status such as 401 or 403. An error fails the request; it is not a refusal,
// and it is not an allow.
//
// What a guard checks is not the framework's to know. The policy — whether a
// session holds a user, whether a reader is a member of the team — lives in
// the application or a plugin; see WithGuard for how a layout carries one.
type GuardFunc func(ctx context.Context, r *http.Request) (*GuardDecision, error)

// GuardDecision is a guard's answer about one request.
type GuardDecision struct {
	// Status is the HTTP status to answer with. Zero with a Location means 303
	// See Other, which turns a form's POST into the GET that follows it; zero
	// without a Location is not a decision anything can write, and Validate
	// rejects it.
	Status int
	// Location is the redirect target when Status is 3xx. It is written into
	// the Location header as-is: an absolute path, or whatever the policy
	// considers right to send this reader to.
	Location string
}

// Validate reports whether d is a decision the framework can write: a 3xx
// status (explicitly, or zero, meaning 303) paired with a non-empty Location,
// or a refusal status in 400-599 with no Location. Anything else — a success
// status, a redirect with nowhere to go, an empty decision — would arrive at
// the reader as a blank response or fall through to the very content the
// guard exists to keep from it, so it is refused rather than guessed at.
func (d *GuardDecision) Validate() error {
	if d.Location != "" {
		status := d.Status
		if status == 0 {
			status = http.StatusSeeOther
		}
		if status >= 300 && status <= 399 {
			return nil
		}
		return fmt.Errorf("%w: status %d with a location must be a redirect", ErrInvalidGuardDecision, d.Status)
	}
	if d.Status >= 400 && d.Status <= 599 {
		return nil
	}
	return fmt.Errorf("%w: status %d with no location must be a refusal in 400-599", ErrInvalidGuardDecision, d.Status)
}
