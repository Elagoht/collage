package httpx

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/Elagoht/collage/internal/asset"
)

// serveMount runs mount's ServeHTTP through a status-capturing wrapper and
// dispatches ErrorHook for whatever it wrote, so a mounted asset request gets the
// same observability a page or document request gets. The timing and span are
// already in place by the time this is called: serve runs inside serveGuarded, and
// ServeHTTP wraps serve in a span and reports the HTTPResponse metric once it
// returns — this only needs to supply the status those two callers cannot get any
// other way, since asset.Mount is an http.Handler and cannot return one.
//
// It never writes a response of its own: asset.Mount owns everything it serves,
// including its plain-text 404, and this only observes what went out over the
// ResponseWriter it was given.
//
// It has no panic guard of its own, and deliberately so. It used to: a panic
// inside mount.ServeHTTP — most plausibly its fs.FS's Open — had to be recovered
// here because serveGuarded's outer guard rendered the framework's built-in HTML
// error page, which is the one response every other asset failure exists to avoid.
// That is no longer true. serve records the resolved route before calling this
// (route.resolved(routeKindMount, ...)), and serveFailure writes plain text for a
// mount wherever the failure is caught, so the outer guard now produces exactly
// what this local one did — with one recovery instead of two, and without a second
// copy of the rule to keep in step. See routeKind.
func (h *Handler) serveMount(w http.ResponseWriter, r *http.Request, mount *asset.Mount, route *routeRef) int {
	capture := &statusCapturingWriter{ResponseWriter: w}

	mount.ServeHTTP(capture, r)

	status := capture.Status()
	if status >= http.StatusBadRequest {
		h.reportError(r, route.failure(status, stageAsset, fmt.Errorf("%w: status %d", ErrAssetFailed, status)))
	}
	return status
}

// statusCapturingWriter wraps an http.ResponseWriter to record the status code an
// http.Handler wrote, and whether it wrote a body at all. It exists because
// asset.Mount is a plain http.Handler and so cannot report back what it served any
// other way, and the caller needs that status to time, trace, and count the
// response the same way it does for a page or a document.
//
// It forwards io.ReaderFrom and http.Flusher as well as the ResponseWriter
// contract itself. That is not optional politeness: asset.Mount serves files
// through http.ServeContent, which type-asserts the writer it is given for
// io.ReaderFrom and uses it to hand the copy to the kernel (sendfile) when the
// body is a file. A wrapper that implements only http.ResponseWriter makes that
// assertion fail, and every mounted audio file, video, or archive silently falls
// back to a 32 KiB user-space copy loop. Wrapping for observability must not cost
// throughput.
type statusCapturingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	wroteBody   bool
}

var (
	_ http.ResponseWriter = (*statusCapturingWriter)(nil)
	_ io.ReaderFrom       = (*statusCapturingWriter)(nil)
	_ http.Flusher        = (*statusCapturingWriter)(nil)
	_ http.Hijacker       = (*statusCapturingWriter)(nil)
)

// Unwrap returns the wrapped ResponseWriter, which is how http.ResponseController
// reaches what this wrapper does not declare. A mounted handler serving an event
// stream pushes its write deadline forward through it; without Unwrap that call
// fails quietly and the server's WriteTimeout cuts the stream.
func (s *statusCapturingWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Hijack hands the connection to a handler that upgrades it — a WebSocket — and
// records 101, the status an upgrade answers with, since nothing is written
// through this wrapper afterwards.
func (s *statusCapturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil && !s.wroteHeader {
		s.wroteHeader = true
		s.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

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

// ReadFrom copies src into the wrapped ResponseWriter through its own ReadFrom
// when it has one, which is how net/http reaches sendfile for a file body, and
// falls back to io.Copy over Write when it does not. It records the same implicit
// 200 and body flag Write does, since a body written this way is just as much a
// response as one written a byte slice at a time.
//
// The fallback is what makes this safe to declare unconditionally: a
// ResponseWriter that is not an io.ReaderFrom — an httptest.ResponseRecorder, a
// middleware wrapper of the application's own — still gets a correct copy,
// performed through the same Write path it would have received anyway.
func (s *statusCapturingWriter) ReadFrom(src io.Reader) (int64, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	s.wroteBody = true

	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	// s.ResponseWriter, not s: copying through s would re-enter this method's own
	// bookkeeping for no reason, and the flags above are already set.
	return io.Copy(s.ResponseWriter, src)
}

// Flush forwards to the wrapped ResponseWriter's Flush when it has one, and does
// nothing when it does not. Without it, wrapping a flushable writer takes
// flushing away from everything below, which for a large file body means the
// response sits in net/http's buffer instead of moving.
func (s *statusCapturingWriter) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
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
