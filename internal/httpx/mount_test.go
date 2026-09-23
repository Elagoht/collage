package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readerFromWriter is a ResponseWriter that also implements io.ReaderFrom and
// http.Flusher, and records that each was used. net/http's own response writer
// implements both; httptest.ResponseRecorder implements neither, which is why the
// regression this pins was invisible to every existing test.
type readerFromWriter struct {
	*httptest.ResponseRecorder
	readFroms int
	flushes   int
}

var (
	_ http.ResponseWriter = (*readerFromWriter)(nil)
	_ io.ReaderFrom       = (*readerFromWriter)(nil)
	_ http.Flusher        = (*readerFromWriter)(nil)
)

// ReadFrom records the call and copies src into the recorder.
func (w *readerFromWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readFroms++
	return io.Copy(w.ResponseRecorder.Body, src)
}

// Flush records the call.
func (w *readerFromWriter) Flush() { w.flushes++ }

// TestStatusCapturingWriter_KeepsTheSendfilePath is the regression for M3. The
// observability fix wrapped every mounted asset response in a
// statusCapturingWriter that implemented only http.ResponseWriter.
// http.ServeContent — which asset.Mount serves every file through — type-asserts
// the writer it is handed for io.ReaderFrom to let net/http hand the copy to the
// kernel, and that assertion started failing, dropping every mounted audio file,
// video and archive into a 32 KiB user-space copy loop. Wrapping for
// observability must not cost throughput.
func TestStatusCapturingWriter_KeepsTheSendfilePath(t *testing.T) {
	inner := &readerFromWriter{ResponseRecorder: httptest.NewRecorder()}
	capture := &statusCapturingWriter{ResponseWriter: inner}

	// The assertion http.ServeContent itself makes.
	rf, ok := any(capture).(io.ReaderFrom)
	if !ok {
		t.Fatal("statusCapturingWriter is not an io.ReaderFrom; http.ServeContent will fall back to a 32 KiB copy loop")
	}

	n, err := rf.ReadFrom(strings.NewReader("mounted body"))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len("mounted body")) {
		t.Fatalf("ReadFrom copied %d bytes, want %d", n, len("mounted body"))
	}
	if inner.readFroms != 1 {
		t.Fatalf("the wrapped writer's ReadFrom ran %d times, want 1 — the copy must reach it, not go through Write", inner.readFroms)
	}
	if got := inner.Body.String(); got != "mounted body" {
		t.Fatalf("body = %q, want %q", got, "mounted body")
	}

	// Status capture, the reason the wrapper exists at all, still works through
	// this path: a body written with no explicit WriteHeader is an implicit 200,
	// exactly as it is through Write.
	if capture.Status() != http.StatusOK {
		t.Fatalf("Status() = %d, want 200", capture.Status())
	}
	if !capture.wroteBody {
		t.Fatal("wroteBody = false after ReadFrom, want true")
	}

	capture.Flush()
	if inner.flushes != 1 {
		t.Fatalf("the wrapped writer's Flush ran %d times, want 1", inner.flushes)
	}
}

// TestStatusCapturingWriter_ReadFromFallsBackWhenUnsupported covers the other
// side: a ResponseWriter that is not an io.ReaderFrom — a recorder, or an
// application's own middleware — must still receive a correct copy, through the
// Write path it would have got anyway.
func TestStatusCapturingWriter_ReadFromFallsBackWhenUnsupported(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &statusCapturingWriter{ResponseWriter: rec}

	if _, err := capture.ReadFrom(strings.NewReader("fallback body")); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	if got := rec.Body.String(); got != "fallback body" {
		t.Fatalf("body = %q, want %q", got, "fallback body")
	}
	if capture.Status() != http.StatusOK {
		t.Fatalf("Status() = %d, want 200", capture.Status())
	}

	// Flushing a writer that cannot flush is a no-op, not a panic.
	capture.Flush()
}

// TestStatusCapturingWriter_ReadFromKeepsAnExplicitStatus proves the implicit-200
// rule does not overwrite a status the handler already chose — a 206 from a Range
// request being the case that matters for a mount.
func TestStatusCapturingWriter_ReadFromKeepsAnExplicitStatus(t *testing.T) {
	inner := &readerFromWriter{ResponseRecorder: httptest.NewRecorder()}
	capture := &statusCapturingWriter{ResponseWriter: inner}

	capture.WriteHeader(http.StatusPartialContent)
	if _, err := capture.ReadFrom(strings.NewReader("partial")); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	if capture.Status() != http.StatusPartialContent {
		t.Fatalf("Status() = %d, want 206", capture.Status())
	}
}
