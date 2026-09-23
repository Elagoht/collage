package main

import (
	"context"
	"net/mail"
	"strings"
	"sync"

	"github.com/Elagoht/collage/pkg/collage"
)

// subscribers is where the newsletter form's submissions land.
//
// A map in memory, because this example exists to show what the framework does with
// a form rather than to be a mailing list. What it is not is a stand-in for the
// framework storing anything: collage holds no state of its own, and an application
// that wants submissions kept puts them somewhere it chose.
type subscribers struct {
	mu    sync.Mutex
	saved map[string]struct{}
}

func newSubscribers() *subscribers {
	return &subscribers{saved: make(map[string]struct{})}
}

func (s *subscribers) add(address string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, already := s.saved[address]; already {
		return false
	}
	s.saved[address] = struct{}{}
	return true
}

func (s *subscribers) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

// subscribe handles the newsletter form.
//
// It is the ordinary shape of a form in a server-rendered application, and the two
// outcomes are deliberately different in kind.
//
// A valid address redirects. The redirect is what stops a reload resubmitting the
// form — the browser's second request is a GET of somewhere else — and the framework
// answers a Location with 303 for exactly that reason.
//
// An invalid one re-renders the page the form is on, with 422 rather than 200, and
// tells that page what went wrong through the render context both halves share. No
// session, no flash storage, no redirect carrying state in a query string: the
// handler and the render are one request.
func (d *deps) subscribe(ctx context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
	if err := rc.Request.ParseForm(); err != nil {
		return nil, err
	}
	address := strings.TrimSpace(rc.Request.PostFormValue("email"))

	if reason := invalidAddress(address); reason != "" {
		rc.Set("newsletter:error", reason)
		rc.Set("newsletter:email", address)
		result := collage.RenderPage(d.pages.newsletter)
		result.Status = 422
		return result, nil
	}

	d.subscribers.add(address)

	// Where the reader lands, not where they were. A GET of the home page with a
	// marker the page reads, so the reload that follows is a plain page view.
	return collage.SeeOther("/newsletter?subscribed=1"), nil
}

// invalidAddress returns why address cannot be subscribed, or the empty string.
func invalidAddress(address string) string {
	switch {
	case address == "":
		return "An email address is required."
	case len(address) > 254:
		return "That address is too long."
	}
	if _, err := mail.ParseAddress(address); err != nil {
		return "That does not look like an email address."
	}
	return ""
}
