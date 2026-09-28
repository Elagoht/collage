package httpx

import (
	"context"
	"net/http"

	"github.com/Elagoht/collage/internal/types"
)

// runGuards asks each guard, in order, whether the request may pass, and stops
// at the first that answers: a decision is the answer, an error is a failure
// that means no later guard runs. nil throughout allows the request.
func runGuards(ctx context.Context, r *http.Request, guards []types.GuardFunc) (*types.GuardDecision, error) {
	for _, guard := range guards {
		decision, err := guard(ctx, r)
		if err != nil {
			return nil, err
		}
		if decision != nil {
			return decision, nil
		}
	}
	return nil, nil
}

// checkGuards runs guards and writes what they answer. It is the guard half of
// every guarded dispatch — a page render, an action on a page's own URL — so
// the two cannot drift apart. A nil guards slice allows immediately.
func (h *Handler) checkGuards(w http.ResponseWriter, r *http.Request, route *routeRef, locale string, guards []types.GuardFunc) (int, bool) {
	if len(guards) == 0 {
		return 0, true
	}
	decision, err := runGuards(r.Context(), r, guards)
	if err != nil {
		f := route.failure(http.StatusInternalServerError, stageGuard, err)
		f.locale = locale
		return h.serveFailure(w, r, f), false
	}
	if decision == nil {
		return 0, true
	}
	if err := decision.Validate(); err != nil {
		f := route.failure(http.StatusInternalServerError, stageGuard, err)
		f.locale = locale
		return h.serveFailure(w, r, f), false
	}
	// A decision about this reader — sent to log in, refused — which a cache
	// between the server and the next reader must not keep. A CDN that stores
	// a 404 or a 308 by default would otherwise hand a logged-out reader's
	// answer to everyone who comes after.
	w.Header().Set("Cache-Control", "no-store")
	if decision.Location != "" {
		status := decision.Status
		if status == 0 {
			status = http.StatusSeeOther
		}
		return writeRedirect(w, r, status, decision.Location), false
	}
	w.WriteHeader(decision.Status)
	return decision.Status, false
}

// guardedCacheControl keeps a guarded page out of shared caches. The server's own
// cache stays shared by every allowed reader, because the guard runs before it is
// read; a CDN or a proxy in front of the server runs no guard, and a public entry
// there would hand an allowed reader's page to every blocked one. "private,
// no-cache" still lets the reader's own browser keep the page and revalidate it
// with its ETag — a revalidation is a request, and a request meets the guard.
func guardedCacheControl(header http.Header, page *types.Page) {
	if page.Guarded() {
		header.Set("Cache-Control", "private, no-cache")
	}
}
