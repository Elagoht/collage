package httpx

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/Elagoht/collage/internal/types"
)

// limitedBody is a request body bounded by http.MaxBytesReader that remembers
// whether a read ran into its bound. Whoever read it — a middleware, say — may
// have dropped the error; the action it reaches still knows the body it was
// sent was too large.
type limitedBody struct {
	io.ReadCloser
	limit    int64
	tooLarge atomic.Bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		b.tooLarge.Store(true)
	}
	return n, err
}

// boundBody bounds r's body at limit, or returns the bound already on it when
// it is this one.
func boundBody(w http.ResponseWriter, r *http.Request, limit int64) *limitedBody {
	if body, ok := r.Body.(*limitedBody); ok && body.limit == limit {
		return body
	}
	body := &limitedBody{ReadCloser: http.MaxBytesReader(w, r.Body, limit), limit: limit}
	r.Body = body
	return body
}

// boundBeforeMiddleware bounds r's body before the middleware runs, at the
// limit of what will answer it: the action it routes to, or the application's
// own limit for anything else.
//
// Before, not only in the action. Middleware runs first, and one that read the
// body — a method override parsing the form, a logger, a signature check —
// read it unbounded, and left it parsed for the action, whose own limit then
// had nothing left to bound. A handler mounted with Handle is left alone: its
// body is the application's own business, as it always was.
func (h *Handler) boundBeforeMiddleware(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil || r.Body == http.NoBody {
		return
	}
	for _, mount := range h.handlers {
		if mount.Matches(r.URL.Path) {
			return
		}
	}
	limit := h.appBodyLimit()
	if match, err := h.router.Match(r); err == nil && match != nil && match.Action != nil {
		limit = h.bodyLimit(match.Action)
	}
	if limit >= 0 {
		boundBody(w, r, limit)
	}
}

// actionBodyTooLarge is the refusal of a body that ran into its bound before
// the action could read it.
func actionBodyTooLarge(action *types.Action, limit int64) error {
	return errors.Join(errors.New("collage: action "+`"`+action.Name+`"`+": body read before the action ran into its limit"), &http.MaxBytesError{Limit: limit})
}
