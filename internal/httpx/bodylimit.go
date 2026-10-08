package httpx

import (
	"errors"
	"io"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/Elagoht/collage/internal/types"
)

// limitedBody is a request body bounded by http.MaxBytesReader that remembers
// whether a read ran into its bound. Whoever read it — a middleware, say — may
// have dropped the error; the action it reaches still knows the body it was
// sent was too large.
//
// It also remembers the first read that failed for any other reason the client
// is answerable for — the connection dropped mid-body, the read deadline
// passed — so that an action whose handler failed because of it is answered as
// the request's failure rather than the server's. An unbounded body (a negative
// limit) is wrapped for that alone, with no MaxBytesReader under it.
type limitedBody struct {
	io.ReadCloser
	limit    int64
	tooLarge atomic.Bool
	readErr  atomic.Pointer[bodyReadError]
}

// bodyReadError holds the error a body read failed with. A pointer to it is what
// limitedBody stores, since an atomic.Value refuses errors of differing types.
type bodyReadError struct{ err error }

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		b.tooLarge.Store(true)
	case errors.Is(err, http.ErrBodyReadAfterClose):
		// The handler's own mistake, not the client's: left to be a 500.
	default:
		b.readErr.CompareAndSwap(nil, &bodyReadError{err: err})
	}
	return n, err
}

// failedRead returns the first read error recorded that was neither the end of
// the body nor its bound, or nil.
func (b *limitedBody) failedRead() error {
	if recorded := b.readErr.Load(); recorded != nil {
		return recorded.err
	}
	return nil
}

// boundBody bounds r's body at limit, or returns the bound already on it when
// it is this one. A negative limit leaves the body unbounded, but still
// watched for a failed read.
func boundBody(w http.ResponseWriter, r *http.Request, limit int64) *limitedBody {
	if body, ok := r.Body.(*limitedBody); ok && body.limit == limit {
		return body
	}
	inner := r.Body
	if limit >= 0 {
		inner = http.MaxBytesReader(w, r.Body, limit)
	}
	body := &limitedBody{ReadCloser: inner, limit: limit}
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
	boundBody(w, r, limit)
}

// bodyAbortStatus is the status for a body whose read failed with err: 408 when
// its deadline passed, 400 for anything else — the client went away, or sent
// what the connection could not carry. Either way the request is at fault, not
// the server.
func bodyAbortStatus(err error) int {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return http.StatusRequestTimeout
	}
	return http.StatusBadRequest
}

// actionBodyTooLarge is the refusal of a body that ran into its bound before
// the action could read it.
func actionBodyTooLarge(action *types.Action, limit int64) error {
	return errors.Join(errors.New("collage: action "+`"`+action.Name+`"`+": body read before the action ran into its limit"), &http.MaxBytesError{Limit: limit})
}
