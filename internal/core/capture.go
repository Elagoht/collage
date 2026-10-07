package core

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// requestSpecific are headers that describe one response, never the file a
// static host serves, in canonical form. X-Collage-Render-Time is development's
// render timing: a cache miss carries it and the hit after it does not.
var requestSpecific = []string{
	"Date", "Etag", "Last-Modified", "Content-Length", "Set-Cookie", "Vary",
	"Content-Encoding", "Transfer-Encoding", "Connection", "Age",
	"X-Collage-Render-Time",
}

// captureTimeout bounds one capture request. A variable so a test can shorten it.
var captureTimeout = 10 * time.Second

// captureMaxTimeouts is how many paths in a row may time out before the capture
// gives up on the rest: an application that answers nothing would otherwise
// cost captureTimeout for every file on the site.
const captureMaxTimeouts = 5

// CaptureResponses asks the application's handler for each path twice, in
// process, as a static export would be asked for it — GET, no cookies, no
// Accept-Encoding, the host of Config.BaseURL or "localhost", over HTTPS when
// Config.BaseURL's scheme is https — and keeps the
// headers both responses agree on, as they were when the status was written. A
// header the two responses disagree on, or that only one of them carries, is
// named in Unstable instead.
//
// Each request's context is marked with types.WithCapture: it renders outside
// any reader's shared render, nothing is written to the response cache for it,
// and a plugin can tell it from a reader's. Each is bounded by captureTimeout,
// and a path not answered within it is recorded with an Err wrapping
// types.ErrCaptureTimeout; a handler that ignores its context is left running
// past that deadline, on a goroutine of its own, until it returns. After
// captureMaxTimeouts such paths in a row the rest are not asked for, and the
// error wraps types.ErrCaptureStopped and names how many were left. The bodies
// are discarded.
//
// It starts the application, as Handler does, and returns the error startup
// produced rather than capturing a 503 for every path. It returns ctx's error
// when ctx ends, with what it captured so far. The paths are asked for one at a
// time.
func (a *App) CaptureResponses(ctx context.Context, paths []string) (map[string]types.CapturedResponse, error) {
	handler, err := a.buildHandler()
	if err != nil {
		return nil, err
	}
	target := captureTarget{host: "localhost"}
	if u, err := url.Parse(a.cfg.BaseURL); err == nil {
		if u.Host != "" {
			target.host = u.Host
		}
		target.https = u.Scheme == "https"
	}
	out := make(map[string]types.CapturedResponse, len(paths))
	timeouts := 0
	for i, p := range paths {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		resp, err := captureTwice(ctx, handler, target, p)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, ctxErr
		}
		if err != nil {
			resp = types.CapturedResponse{Err: err}
		}
		out[p] = resp
		if !errors.Is(err, types.ErrCaptureTimeout) {
			timeouts = 0
			continue
		}
		if timeouts++; timeouts == captureMaxTimeouts && i+1 < len(paths) {
			return out, fmt.Errorf("%w: %d paths in a row were not answered within %s; %d path(s) left uncaptured",
				types.ErrCaptureStopped, timeouts, captureTimeout, len(paths)-i-1)
		}
	}
	return out, nil
}

// captureTarget is where a capture request is addressed: the host of
// Config.BaseURL, and whether it is served over HTTPS. Every static host serves
// HTTPS, and middleware that sends a header only over it —
// Strict-Transport-Security — has to see the request as one.
type captureTarget struct {
	host  string
	https bool
}

// captureTwice asks for urlPath twice and compares the answers.
func captureTwice(ctx context.Context, handler http.Handler, target captureTarget, urlPath string) (types.CapturedResponse, error) {
	asked := urlPath
	first, err := captureOnce(ctx, handler, target, asked)
	if err != nil {
		return types.CapturedResponse{}, err
	}
	if other, ok := slashSpelling(first, asked); ok {
		// The page is answered at its other spelling — "/about/" under
		// TrailingSlash, which is how the static host serves about/index.html
		// too — and its headers are that answer's, not the redirect's.
		asked = other
		if first, err = captureOnce(ctx, handler, target, asked); err != nil {
			return types.CapturedResponse{}, err
		}
	}
	second, err := captureOnce(ctx, handler, target, asked)
	if err != nil {
		return types.CapturedResponse{}, err
	}
	return compareResponses(first, second), nil
}

// compareResponses keeps what first and second agree on, past the
// request-specific headers, with first's status.
func compareResponses(first, second *captureWriter) types.CapturedResponse {
	kept := http.Header{}
	var unstable []string
	for name, values := range first.sent {
		if slices.Contains(requestSpecific, name) {
			continue
		}
		if !slices.Equal(values, second.sent[name]) {
			unstable = append(unstable, name)
			continue
		}
		kept[name] = slices.Clone(values)
	}
	for name := range second.sent {
		if _, ok := first.sent[name]; ok || slices.Contains(requestSpecific, name) {
			continue
		}
		unstable = append(unstable, name)
	}
	slices.Sort(unstable)
	resp := types.CapturedResponse{Status: first.status, Headers: kept, Unstable: unstable}
	if second.status != first.status {
		resp.OtherStatus = second.status
	}
	return resp
}

// slashSpelling reports the spelling w's answer redirected urlPath to, when that
// redirect is the router's trailing-slash canonicalisation: urlPath with a "/"
// added or taken away, and nothing else.
func slashSpelling(w *captureWriter, urlPath string) (string, bool) {
	if w.status != http.StatusMovedPermanently {
		return "", false
	}
	u, err := url.Parse(w.sent.Get("Location"))
	if err != nil || u.Host != "" || u.Scheme != "" || u.RawQuery != "" {
		return "", false
	}
	if u.Path == urlPath+"/" || (urlPath != "/" && strings.HasSuffix(urlPath, "/") && u.Path == strings.TrimSuffix(urlPath, "/")) {
		return u.Path, true
	}
	return "", false
}

// captureOnce serves one GET for urlPath through handler, marked as a capture
// and bounded by captureTimeout. The request is built from a URL rather than
// parsed from a request line, so a path with a space or a non-ASCII byte in it
// is asked for as it is rather than refused. For an HTTPS target it carries a
// TLS connection state and the "https" scheme, as httptest.NewRequest gives a
// request for an https:// URL.
//
// The handler runs on a goroutine of its own so that one ignoring its context
// cannot hold the build: past the deadline it is left to finish alone, and its
// writer is never read again.
func captureOnce(ctx context.Context, handler http.Handler, target captureTarget, urlPath string) (*captureWriter, error) {
	ctx, cancel := context.WithTimeout(types.WithCapture(ctx), captureTimeout)
	defer cancel()

	u := &url.URL{Path: urlPath}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	req.URL = u
	req.RequestURI = u.RequestURI()
	req.Host = target.host
	if target.https {
		u.Scheme, u.Host = "https", target.host
		req.TLS = &tls.ConnectionState{}
	}

	w := &captureWriter{header: http.Header{}}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("collage: capture %s: panic: %v", urlPath, recovered)
			}
		}()
		handler.ServeHTTP(w, req)
		w.finish()
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			// Answered, but only once the deadline had cancelled it: what it
			// answered with is the cancellation's, not the file's.
			return nil, fmt.Errorf("%w: %s, %s", types.ErrCaptureTimeout, urlPath, captureTimeout)
		}
		return w, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %s, %s", types.ErrCaptureTimeout, urlPath, captureTimeout)
	}
}

// captureWriter is the ResponseWriter a capture is answered into. It keeps the
// status and the headers as they were when the status was written — what a
// client is sent — and discards the body, so an asset is never held in memory.
type captureWriter struct {
	header http.Header
	status int
	sent   http.Header
}

var _ http.Flusher = (*captureWriter)(nil)

// Header returns the headers the handler is still free to set.
func (w *captureWriter) Header() http.Header { return w.header }

// WriteHeader records the first final status and the headers sent with it. An
// informational status is not the answer and is ignored.
func (w *captureWriter) WriteHeader(code int) {
	if w.status != 0 || (code >= 100 && code < 200) {
		return
	}
	w.status = code
	w.sent = w.header.Clone()
}

// Write discards b, after writing a 200 the way net/http does when nothing was
// written yet — with a Content-Type sniffed from b when none is set.
func (w *captureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		if w.header.Get("Content-Type") == "" && w.header.Get("Transfer-Encoding") == "" && len(b) > 0 {
			w.header.Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	return len(b), nil
}

// Flush writes a 200 when nothing was written yet, as net/http's does.
func (w *captureWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
}

// finish is what net/http does once a handler returns without writing: a 200
// with the headers as they are.
func (w *captureWriter) finish() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
}
