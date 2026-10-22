package collage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// spillSize is over the 32 MiB a FormValue parse keeps in memory, so the file
// part of a multipart body this size is spilled to a temporary file.
const spillSize = 40 << 20

// spillApp is an application whose /panel page and /up action both have a
// guard that reads a form field and refuses, with the body limit maxBody.
// What the guards' parse failed with goes to parseErr.
func spillApp(t *testing.T, maxBody int64, middleware bool, parseErr *error) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000, MaxBodyBytes: maxBody},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/pages/p.html": {Data: []byte(`<p>p</p>`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if middleware {
		if err := app.Use(func(next http.Handler) http.Handler { return next }); err != nil {
			t.Fatal(err)
		}
	}
	refuse := func(_ context.Context, r *http.Request) (*collage.GuardDecision, error) {
		*parseErr = r.ParseMultipartForm(32 << 20)
		return &collage.GuardDecision{Status: http.StatusForbidden}, nil
	}
	content := collage.NewFragment("p", "pages/p.html").WithGuard(refuse).Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(content).WithPath("en", "/panel").Build()); err != nil {
		t.Fatal(err)
	}
	action := collage.NewAction("up").WithPath("en", "/up").WithMethods(http.MethodPost).WithGuard(refuse).
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) { return nil, nil }).Build()
	if err := app.RegisterAction(action); err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

// sendSpill sends a multipart body of size bytes to path and returns the status
// and how many temporary files were left once the server had answered.
func sendSpill(t *testing.T, h http.Handler, method, path string, size int) (int, int) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, _ := form.CreateFormFile("f", "a.bin")
	_, _ = file.Write(bytes.Repeat([]byte("A"), size))
	_ = form.Close()

	srv := httptest.NewServer(h)
	req, _ := http.NewRequest(method, srv.URL+path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	srv.Close()

	left, _ := os.ReadDir(tmp)
	return res.StatusCode, len(left)
}

// A guard that reads the form reads it bounded, with or without middleware:
// without any, nothing bounded the body before the guards ran, and the guard
// read 40 MiB past a 4 MiB limit.
func TestBodySpill_GuardReadsBounded(t *testing.T) {
	for _, middleware := range []bool{false, true} {
		for _, target := range []struct{ method, path string }{{http.MethodGet, "/panel"}, {http.MethodPost, "/up"}} {
			var parseErr error
			status, _ := sendSpill(t, spillApp(t, 0, middleware, &parseErr), target.method, target.path, spillSize)
			var tooLarge *http.MaxBytesError
			if status != http.StatusForbidden || !errors.As(parseErr, &tooLarge) {
				t.Errorf("middleware=%v %s %s: status %d, guard's parse error %v; want 403 and the body limit",
					middleware, target.method, target.path, status, parseErr)
			}
		}
	}
}

// The files a form parsed on a request spilled to disk are removed, whoever
// parsed it and whatever answered: a refusing page guard, a refusing action
// guard, with or without middleware. net/http removes them only for the
// request it made, and every request collage hands on is a copy.
func TestBodySpill_TemporaryFilesRemoved(t *testing.T) {
	for _, middleware := range []bool{false, true} {
		for _, target := range []struct{ method, path string }{{http.MethodGet, "/panel"}, {http.MethodPost, "/up"}} {
			var parseErr error
			status, left := sendSpill(t, spillApp(t, 64<<20, middleware, &parseErr), target.method, target.path, spillSize)
			if parseErr != nil || status != http.StatusForbidden {
				t.Fatalf("middleware=%v %s %s: status %d, parse error %v; want 403 and a parsed form",
					middleware, target.method, target.path, status, parseErr)
			}
			if left != 0 {
				t.Errorf("middleware=%v %s %s: %d temporary files left behind", middleware, target.method, target.path, left)
			}
		}
	}
}

// A data handler that reads a form on a page with no middleware leaves no
// files behind either.
func TestBodySpill_DataHandlerFormRemoved(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000, MaxBodyBytes: 64 << 20},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/pages/s.html": {Data: []byte(`<p>{{.}}</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := collage.NewFragment("s", "pages/s.html").WithData(collage.Load(func(_ context.Context, rc *collage.RenderContext) (string, error) {
		return rc.Request.FormValue("q"), nil
	})).Build()
	if err := app.RegisterPage(collage.NewPage("s").WithContent(content).WithPath("en", "/search").Build()); err != nil {
		t.Fatal(err)
	}
	if status, left := sendSpill(t, app.Handler(), http.MethodGet, "/search?q=x", spillSize); status != http.StatusOK || left != 0 {
		t.Errorf("status %d, %d temporary files left; want 200 and none", status, left)
	}
}

// formApp is an application whose /form action, with no forgery check and a
// 1 KiB limit, answers with the a and b it read, and counts its runs in ran. A
// middleware, when given, runs before it.
func formApp(t *testing.T, mw func(http.Handler) http.Handler, ran *atomic.Int64) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000, MaxBodyBytes: 1024},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mw != nil {
		if err := app.Use(mw); err != nil {
			t.Fatal(err)
		}
	}
	action := collage.NewAction("form").WithPath("en", "/form").WithMethods(http.MethodPost).WithoutCSRF().
		WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			ran.Add(1)
			body := "a=" + rc.Request.FormValue("a") + " b=" + rc.Request.FormValue("b")
			return &collage.ActionResult{Status: http.StatusOK, Body: []byte(body), ContentType: "text/plain"}, nil
		}).Build()
	if err := app.RegisterAction(action); err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

// postLongForm posts a form whose a is longer than formApp's limit, with b
// after it and a b in the query to stand in for the one cut off. Chunked, the
// body does not say its length.
func postLongForm(h http.Handler, chunked bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/form?b=fromquery", strings.NewReader("a="+strings.Repeat("x", 4096)+"&b=tail"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if chunked {
		req.ContentLength = -1
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// An action with no forgery check is not run on a form cut down to its limit:
// a body that says it is too large is refused before the handler, and one that
// does not (chunked) is refused after it rather than answered as a success.
func TestBodyLimit_TruncatedFormRefused(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		var ran atomic.Int64
		rec := postLongForm(formApp(t, nil, &ran), chunked)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("chunked=%v: status %d, body %q; want 413", chunked, rec.Code, rec.Body)
		}
		if want := map[bool]int64{false: 0, true: 1}[chunked]; ran.Load() != want {
			t.Errorf("chunked=%v: the handler ran %d times; want %d", chunked, ran.Load(), want)
		}
	}
}

// A middleware that buffers the body and replaces it with what it read — the
// common io.ReadAll, io.NopCloser idiom, error dropped — leaves the action a
// body that never ran into its limit. The action is refused all the same.
func TestBodyLimit_BufferingMiddlewareRefused(t *testing.T) {
	buffering := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			read, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(read))
			next.ServeHTTP(w, r)
		})
	}
	var ran atomic.Int64
	if rec := postLongForm(formApp(t, buffering, &ran), true); rec.Code != http.StatusRequestEntityTooLarge || ran.Load() != 0 {
		t.Errorf("status %d, body %q, handler ran %d times; want 413 and none", rec.Code, rec.Body, ran.Load())
	}
}
