package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// failingBody hands out what it holds, then fails with err, as a request body
// does when the client goes away or the read deadline passes mid-upload.
type failingBody struct {
	r   io.Reader
	err error
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}

func (b *failingBody) Close() error { return nil }

// readAll is a handler that reads its whole body and returns the read's error,
// as an upload handler does.
func readAll(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
	_, err := io.Copy(io.Discard, rc.Request.Body)
	return nil, err
}

func postFailing(err error) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/upload", &failingBody{r: strings.NewReader("part of a file"), err: err})
	req.Header.Set("Content-Type", "application/octet-stream")
	return req
}

// A body cut off because the client left, or because its deadline passed, is
// the request's failure, not the application's: a 4xx, logged at debug, and no
// error hook called — as for a reader whose request context was cancelled.
func TestAction_AbortedBodyIsNotAServerError(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		limit  int64
		status int
	}{
		"client left":               {err: io.ErrUnexpectedEOF, status: http.StatusBadRequest},
		"connection reset":          {err: &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, status: http.StatusBadRequest},
		"deadline":                  {err: &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, status: http.StatusRequestTimeout},
		"client left, no limit":     {err: io.ErrUnexpectedEOF, limit: -1, status: http.StatusBadRequest},
		"deadline, no limit":        {err: &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, limit: -1, status: http.StatusRequestTimeout},
		"handler's own error, left": {err: io.ErrUnexpectedEOF, status: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := &recordingPlugin{}
			handler := readAll
			if strings.Contains(name, "own error") {
				handler = func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
					if _, err := readAll(ctx, rc); err != nil {
						return nil, errors.New("upload failed")
					}
					return nil, nil
				}
			}
			upload := action("upload", "/upload", []string{http.MethodPost}, handler)
			upload.MaxBodyBytes = tc.limit
			env := actionEnv(t, nil, []*types.Action{upload}, withPlugins(t, recorder))
			res := env.do(postFailing(tc.err))
			if res.Code != tc.status {
				t.Errorf("status %d, want %d", res.Code, tc.status)
			}
			records := env.logs.recordsFor("collage: request failed")
			if len(records) != 1 {
				t.Fatalf("%d request-failed records, want 1", len(records))
			}
			if records[0].level != slog.LevelDebug {
				t.Errorf("logged at %v, want DEBUG", records[0].level)
			}
			if errs := recorder.reportedErrors(); len(errs) != 0 {
				t.Errorf("ErrorHook received %v for a body the client cut off", errs)
			}
		})
	}
}

// A body read after it was closed is the handler's bug, not the client's: it
// stays a 500.
func TestAction_BodyReadAfterCloseIsStillAServerError(t *testing.T) {
	upload := action("upload", "/upload", []string{http.MethodPost},
		func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			_ = rc.Request.Body.Close()
			return readAll(ctx, rc)
		})
	env := actionEnv(t, nil, []*types.Action{upload})
	req := httptest.NewRequest(http.MethodPost, "/upload", &closingBody{Reader: strings.NewReader("x")})
	if res := env.do(req); res.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", res.Code)
	}
}

// closingBody fails every read after Close the way net/http's body does.
type closingBody struct {
	io.Reader
	closed bool
}

func (b *closingBody) Read(p []byte) (int, error) {
	if b.closed {
		return 0, http.ErrBodyReadAfterClose
	}
	return b.Reader.Read(p)
}

func (b *closingBody) Close() error { b.closed = true; return nil }

// A 413 a handler ran into is the client's doing: a warning, not an error, and
// still handed to the error hooks, which filter by status.
func TestAction_HandlerOverreadIsAWarning(t *testing.T) {
	recorder := &recordingPlugin{}
	upload := action("upload", "/upload", []string{http.MethodPost}, readAll)
	upload.MaxBodyBytes = 4
	env := actionEnv(t, nil, []*types.Action{upload}, withPlugins(t, recorder))
	res := env.do(post("/upload", strings.Repeat("x", 64)))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", res.Code)
	}
	records := env.logs.recordsFor("collage: request failed")
	if len(records) != 1 || records[0].level != slog.LevelWarn {
		t.Errorf("records %+v, want one at WARN", records)
	}
	if errs := recorder.reportedErrors(); len(errs) != 1 {
		t.Errorf("ErrorHook received %d errors, want 1", len(errs))
	}
}

// A writer that cannot take a deadline does not stop the action: it runs under
// the server's own, and the operator is told once, not on every request.
func TestAction_BodyTimeoutUnsupportedWarnsOnce(t *testing.T) {
	ran := 0
	upload := action("upload", "/upload", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			ran++
			return nil, nil
		})
	upload.BodyTimeout = time.Second
	env := actionEnv(t, nil, []*types.Action{upload})
	for range 2 {
		// httptest.ResponseRecorder supports no deadline.
		if res := env.do(post("/upload", "x")); res.Code != http.StatusNoContent {
			t.Fatalf("status %d, want 204", res.Code)
		}
	}
	if ran != 2 {
		t.Errorf("handler ran %d times, want 2", ran)
	}
	records := env.logs.recordsFor(bodyTimeoutUnsupported)
	if len(records) != 1 || records[0].level != slog.LevelWarn {
		t.Errorf("records %+v, want one at WARN", records)
	}
}

// A streaming action is given its longer deadlines only once its forgery check
// passed, which reads nothing: a request without a token is refused under the
// server's own. An ordinary action's check reads the body, so its deadlines are
// set before it. The recorder cannot take a deadline, so the warning is the
// trace of an attempt.
func TestAction_BodyTimeoutAfterTheForgeryCheckWhenStreaming(t *testing.T) {
	for name, tc := range map[string]struct {
		streaming bool
		attempted bool
	}{
		"streaming": {streaming: true, attempted: false},
		"ordinary":  {streaming: false, attempted: true},
	} {
		t.Run(name, func(t *testing.T) {
			option, _ := withCSRF(t)
			upload := action("upload", "/upload", []string{http.MethodPost}, readAll)
			upload.StreamingBody = tc.streaming
			upload.BodyTimeout = time.Minute
			env := actionEnv(t, nil, []*types.Action{upload}, option)
			if res := env.do(post("/upload", "x=1")); res.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", res.Code)
			}
			if got := len(env.logs.recordsFor(bodyTimeoutUnsupported)) == 1; got != tc.attempted {
				t.Errorf("deadline attempted = %v, want %v", got, tc.attempted)
			}
		})
	}
}

// A request built with no body at all reaches an unbounded action's handler
// with no body still, not a wrapper around nil that panics when read.
func TestAction_NilBodyUnbounded(t *testing.T) {
	upload := action("upload", "/upload", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			if rc.Request.Body != nil {
				return nil, errors.New("the nil body was wrapped")
			}
			return nil, nil
		})
	upload.MaxBodyBytes = -1
	env := actionEnv(t, nil, []*types.Action{upload})
	req := httptest.NewRequest(http.MethodPost, "/upload", nil)
	req.Body = nil
	if res := env.do(req); res.Code != http.StatusNoContent {
		t.Errorf("status %d, want 204", res.Code)
	}
}
