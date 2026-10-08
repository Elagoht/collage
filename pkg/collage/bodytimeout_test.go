package collage_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// bodyServer serves an application whose one action, at /upload, reads its
// whole streamed body and answers with how many bytes it read. The server's
// ReadTimeout and WriteTimeout are both 200ms unless writeTimeout says
// otherwise. What the application logs goes to logs; its error events to rec.
func bodyServer(t *testing.T, bodyTimeout, writeTimeout time.Duration, rec *eventRecorder, logs *lockedBuffer) (*httptest.Server, <-chan error) {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Plugins:  []collage.Plugin{rec},
		Logger:   slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	action := collage.NewAction("upload").WithPath("en", "/upload").WithMethods(http.MethodPost).
		WithoutCSRF().WithStreamingBody().WithBodyTimeout(bodyTimeout).
		WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			n, err := io.Copy(io.Discard, rc.Request.Body)
			done <- err
			if err != nil {
				return nil, err
			}
			return &collage.ActionResult{Status: http.StatusOK, Body: fmt.Appendf(nil, "%d", n), ContentType: "text/plain"}, nil
		}).Build()
	if err := app.RegisterAction(action); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(app.Handler())
	srv.Config.ReadTimeout = 200 * time.Millisecond
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the application logged:\n%s", logs)
		}
	})
	return srv, done
}

// slowUpload posts 6 chunks of 1 KiB, 100ms apart: 600ms of body, three times
// the server's 200ms deadlines.
func slowUpload(srv *httptest.Server) (*http.Response, error) {
	pr, pw := io.Pipe()
	go func() {
		chunk := strings.Repeat("x", 1024)
		for range 6 {
			time.Sleep(100 * time.Millisecond)
			if _, err := io.WriteString(pw, chunk); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	return srv.Client().Post(srv.URL+"/upload", "application/octet-stream", pr)
}

// WithBodyTimeout lets an upload outlive the server's ReadTimeout and
// WriteTimeout, and the client receives the answer: the write deadline, which
// net/http starts when the headers arrive, is moved too.
func TestBodyTimeout_SlowUploadSucceeds(t *testing.T) {
	srv, _ := bodyServer(t, 2*time.Second, 200*time.Millisecond, &eventRecorder{}, &lockedBuffer{})
	res, err := slowUpload(srv)
	if err != nil {
		t.Fatalf("the upload failed: %v", err)
	}
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	if res.StatusCode != http.StatusOK || string(got) != "6144" {
		t.Fatalf("status %d, body %q; want 200, 6144", res.StatusCode, got)
	}
}

// The same upload without it runs into the server's deadlines.
func TestBodyTimeout_WithoutItTheUploadFails(t *testing.T) {
	srv, done := bodyServer(t, 0, 200*time.Millisecond, &eventRecorder{}, &lockedBuffer{})
	res, err := slowUpload(srv)
	if err == nil {
		defer res.Body.Close()
		got, readErr := io.ReadAll(res.Body)
		if readErr == nil && res.StatusCode == http.StatusOK {
			t.Fatalf("the upload succeeded (body %q) under a 200ms deadline", got)
		}
	}
	select {
	case readErr := <-done:
		if readErr == nil {
			t.Error("the handler read the whole body under a 200ms deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never finished")
	}
}

// Registration refuses a negative body timeout.
func TestBodyTimeout_NegativeRefused(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	action := collage.NewAction("upload").WithPath("en", "/upload").WithMethods(http.MethodPost).
		WithBodyTimeout(-time.Second).WithHandler(noopAction).Build()
	if err := app.RegisterAction(action); !errors.Is(err, collage.ErrNegativeBodyTimeout) {
		t.Fatalf("RegisterAction = %v, want ErrNegativeBodyTimeout", err)
	}
}

// rawUpload sends the head of an upload promising 1000 bytes, and 100 of them,
// then calls stop on the connection — and returns the answer, if one comes.
func rawUpload(t *testing.T, srv *httptest.Server, stop func(*net.TCPConn)) *http.Response {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	head := "POST /upload HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 1000\r\n\r\n"
	if _, err := io.WriteString(conn, head+strings.Repeat("x", 100)); err != nil {
		t.Fatal(err)
	}
	stop(conn.(*net.TCPConn))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	_ = res.Body.Close()
	return res
}

// assertQuiet fails t if the cut-off upload was reported as the application's
// failure: an ERROR line, or an error event.
func assertQuiet(t *testing.T, done <-chan error, rec *eventRecorder, logs *lockedBuffer) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the handler read the whole body")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never finished")
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Error("the cut-off upload was logged at ERROR")
	}
	if !strings.Contains(logs.String(), "level=DEBUG msg=\"collage: request failed\"") {
		t.Error("the cut-off upload was not logged at DEBUG")
	}
	if got := rec.at("/upload"); len(got) != 0 {
		t.Errorf("%d error events for a cut-off upload, want 0", len(got))
	}
}

// A client that goes away mid-upload is answered 400 — to nobody, as a rule —
// and is not the application's failure.
func TestBodyAbort_ClientLeftIs400(t *testing.T) {
	rec, logs := &eventRecorder{}, &lockedBuffer{}
	srv, done := bodyServer(t, 0, 0, rec, logs)
	res := rawUpload(t, srv, func(c *net.TCPConn) { _ = c.CloseWrite() })
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", res.StatusCode)
	}
	assertQuiet(t, done, rec, logs)
}

// A client too slow for the read deadline is answered 408, and is not the
// application's failure either.
func TestBodyAbort_DeadlineIs408(t *testing.T) {
	rec, logs := &eventRecorder{}, &lockedBuffer{}
	srv, done := bodyServer(t, 0, 0, rec, logs)
	res := rawUpload(t, srv, func(*net.TCPConn) {})
	if res.StatusCode != http.StatusRequestTimeout {
		t.Errorf("status %d, want 408", res.StatusCode)
	}
	assertQuiet(t, done, rec, logs)
}
