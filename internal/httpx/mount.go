package httpx

import (
	"fmt"
	"net/http"

	"github.com/Elagoht/collage/internal/asset"
)

// serveMount runs mount's ServeHTTP through a status-capturing wrapper and
// dispatches ErrorHook for whatever it wrote, so a mounted asset request gets the
// same observability a page or document request gets. The timing, span, and panic
// guard are already in place by the time this is called: serve runs inside
// serveGuarded, and ServeHTTP wraps serve in a span and reports the HTTPResponse
// metric once it returns — this only needs to supply the status those two callers
// cannot get any other way, since asset.Mount is an http.Handler and cannot return
// one.
//
// It never writes a response of its own: asset.Mount owns everything it serves,
// including its plain-text 404, and this only observes what went out over the
// ResponseWriter it was given.
func (h *Handler) serveMount(w http.ResponseWriter, r *http.Request, mount *asset.Mount) int {
	capture := &statusCapturingWriter{ResponseWriter: w}
	mount.ServeHTTP(capture, r)

	status := capture.Status()
	if status >= http.StatusBadRequest {
		h.reportError(r, failure{
			status: status,
			err:    fmt.Errorf("%w: status %d", ErrAssetFailed, status),
			stage:  stageAsset,
		})
	}
	return status
}

// statusCapturingWriter wraps an http.ResponseWriter to record the status code an
// http.Handler wrote, and whether it wrote a body at all. It exists because
// asset.Mount is a plain http.Handler and so cannot report back what it served any
// other way, and the caller needs that status to time, trace, and count the
// response the same way it does for a page or a document.
type statusCapturingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	wroteBody   bool
}

var _ http.ResponseWriter = (*statusCapturingWriter)(nil)

// WriteHeader records status the first time it is called, then forwards it to the
// wrapped ResponseWriter regardless — a later, superfluous call is still the
// wrapped writer's own business to log or ignore, but only the first status
// written is ever the one this reports, matching net/http's own rule that only
// the first WriteHeader call has any effect.
func (s *statusCapturingWriter) WriteHeader(status int) {
	if !s.wroteHeader {
		s.wroteHeader = true
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

// Write records that a body was written, capturing an implicit 200 first when
// WriteHeader was never called explicitly — the same behavior net/http itself
// gives a handler that writes without calling WriteHeader — then forwards to the
// wrapped ResponseWriter.
func (s *statusCapturingWriter) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	s.wroteBody = true
	return s.ResponseWriter.Write(b)
}

// Status returns the status code the wrapped handler wrote, or http.StatusOK when
// it wrote nothing at all: a handler that returns without writing anything is,
// exactly as net/http treats it, a 200 with an empty body.
func (s *statusCapturingWriter) Status() int {
	if !s.wroteHeader {
		return http.StatusOK
	}
	return s.status
}
