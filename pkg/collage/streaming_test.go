package collage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

type actionFunc = func(context.Context, *collage.RenderContext) (*collage.ActionResult, error)

// streamingApp is an application with a form page to mint a token from, and one
// action at /upload built by configure.
func streamingApp(t *testing.T, csrf, devMode bool, configure func(*collage.ActionBuilder) *collage.ActionBuilder, plugins ...collage.Plugin) http.Handler {
	t.Helper()
	cfg := &collage.Config{
		DevMode:  devMode,
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Plugins:  plugins,
	}
	if csrf {
		cfg.Security = collage.SecurityConfig{CSRFKey: bytes.Repeat([]byte("k"), 32)}
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	form := collage.NewInlineFragment("form", `<form method="post">{{csrfToken}}</form>`).Build()
	if err := app.RegisterPage(collage.NewPage("form").WithContent(form).WithPath("en", "/form").Dynamic().Build()); err != nil {
		t.Fatal(err)
	}
	b := collage.NewAction("upload").WithPath("en", "/upload").WithMethods(http.MethodPost)
	if !csrf {
		b = b.WithoutCSRF()
	}
	if err := app.RegisterAction(configure(b).Build()); err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

// csrfCookie loads the form page and returns the token cookie it set.
func csrfCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	page := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/form", nil)
	req.Host = "localhost:3000"
	h.ServeHTTP(page, req)
	for _, c := range page.Result().Cookies() {
		if c.Name == "collage_csrf" {
			return c
		}
	}
	t.Fatalf("the form set no token cookie (status %d): %v", page.Code, page.Header().Values("Set-Cookie"))
	return nil
}

// multipartWithToken is a multipart body carrying the token as a field, beside a file.
func multipartWithToken(t *testing.T, token string) (string, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("_csrf", token); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("file", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("file contents"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), w.FormDataContentType()
}

func postUpload(h http.Handler, body, contentType string, prepare func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	r.Host = "localhost:3000"
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if prepare != nil {
		prepare(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The handler of a streaming action is handed the body exactly as it was sent:
// nothing before it parsed a form out of it.
func TestStreamingBody_BodyUnread(t *testing.T) {
	sent, contentType := multipartWithToken(t, "not-a-token")
	var problems []string
	h := streamingApp(t, false, false, func(b *collage.ActionBuilder) *collage.ActionBuilder {
		return b.WithStreamingBody().WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			if rc.Request.MultipartForm != nil {
				problems = append(problems, "MultipartForm was parsed before the handler")
			}
			if rc.Request.Form != nil {
				problems = append(problems, "Form was parsed before the handler")
			}
			got, err := io.ReadAll(rc.Request.Body)
			if err != nil {
				return nil, err
			}
			if string(got) != sent {
				problems = append(problems, "the body read differs from the body sent")
			}
			return nil, nil
		})
	})
	if w := postUpload(h, sent, contentType, nil); w.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204: %s", w.Code, w.Body.String())
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// A token in a form field is not looked for: finding it would mean reading the
// body. The refusal names the header the token belongs in.
func TestStreamingBody_FormTokenRefused(t *testing.T) {
	called := false
	h := streamingApp(t, true, true, func(b *collage.ActionBuilder) *collage.ActionBuilder {
		return b.WithStreamingBody().WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			called = true
			return nil, nil
		})
	})
	cookie := csrfCookie(t, h)
	body, contentType := multipartWithToken(t, cookie.Value)
	w := postUpload(h, body, contentType, func(r *http.Request) { r.AddCookie(cookie) })
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "X-CSRF-Token") {
		t.Errorf("the refusal does not name the header: %s", w.Body.String())
	}
	if called {
		t.Error("the handler ran for a refused request")
	}
}

// The same request with the token in the header reaches the handler, its body
// still unread.
func TestStreamingBody_HeaderTokenPasses(t *testing.T) {
	var unread bool
	h := streamingApp(t, true, false, func(b *collage.ActionBuilder) *collage.ActionBuilder {
		return b.WithStreamingBody().WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			unread = rc.Request.Form == nil && rc.Request.MultipartForm == nil
			return nil, nil
		})
	})
	cookie := csrfCookie(t, h)
	body, contentType := multipartWithToken(t, cookie.Value)
	w := postUpload(h, body, contentType, func(r *http.Request) {
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", cookie.Value)
	})
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("status %d, want 2xx: %s", w.Code, w.Body.String())
	}
	if !unread {
		t.Error("the body was parsed before the handler")
	}
}

// A body past the action's bound is a 413, whether the handler hands back the
// error the read returned or one of its own.
func TestStreamingBody_LimitIs413(t *testing.T) {
	for name, handler := range map[string]actionFunc{
		"the read's own error": func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			_, err := io.Copy(io.Discard, rc.Request.Body)
			return nil, err
		},
		"an error of the handler's own": func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			if _, err := io.Copy(io.Discard, rc.Request.Body); err != nil {
				return nil, errors.New("upload failed")
			}
			return nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := streamingApp(t, false, false, func(b *collage.ActionBuilder) *collage.ActionBuilder {
				return b.WithStreamingBody().WithMaxBodyBytes(1024).WithHandler(handler)
			})
			w := postUpload(h, strings.Repeat("x", 4<<10), "application/octet-stream", nil)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status %d, want 413", w.Code)
			}
		})
	}
}

// An action that does not stream is as it was: the forgery check reads the
// token from the multipart form, and the handler finds the form parsed.
func TestNonStreaming_Unchanged(t *testing.T) {
	var file string
	h := streamingApp(t, true, false, func(b *collage.ActionBuilder) *collage.ActionBuilder {
		return b.WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			if rc.Request.MultipartForm == nil {
				return nil, errors.New("MultipartForm is nil")
			}
			if files := rc.Request.MultipartForm.File["file"]; len(files) == 1 {
				file = files[0].Filename
			}
			return nil, nil
		})
	})
	cookie := csrfCookie(t, h)
	body, contentType := multipartWithToken(t, cookie.Value)
	w := postUpload(h, body, contentType, func(r *http.Request) { r.AddCookie(cookie) })
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204: %s", w.Code, w.Body.String())
	}
	if file != "a.txt" {
		t.Errorf("handler saw file %q, want a.txt", file)
	}
}

// Registration refuses a streaming action with no method that carries a body.
func TestStreamingBody_NeedsBodyMethod(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	action := collage.NewAction("feed").WithPath("en", "/feed").WithMethods(http.MethodGet).
		WithStreamingBody().WithHandler(noopAction).Build()
	if err := app.RegisterAction(action); !errors.Is(err, collage.ErrStreamingBodyMethod) {
		t.Fatalf("RegisterAction = %v, want ErrStreamingBodyMethod", err)
	}
}

// formReader is a plugin that asks for each submission's form before the
// handler, as a spam check or a validator would.
type formReader struct{ err error }

func (p *formReader) Name() string                             { return "test/form-reader" }
func (p *formReader) Version() string                          { return "0" }
func (p *formReader) Init(context.Context, collage.Host) error { return nil }
func (p *formReader) Shutdown(context.Context) error           { return nil }
func (p *formReader) OnBeforeAction(_ context.Context, ev *collage.BeforeActionEvent) error {
	_, p.err = ev.Form()
	return nil
}

// A plugin asking for a streaming action's form is told there is none, and the
// body is left whole for the handler.
func TestStreamingBody_PluginFormRefused(t *testing.T) {
	plugin := &formReader{}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Plugins:  []collage.Plugin{plugin},
	})
	if err != nil {
		t.Fatal(err)
	}
	sent, contentType := multipartWithToken(t, "a field")
	var got string
	action := collage.NewAction("upload").WithPath("en", "/upload").WithMethods(http.MethodPost).
		WithoutCSRF().WithStreamingBody().
		WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			if rc.Request.Form != nil || rc.Request.MultipartForm != nil {
				return nil, errors.New("the form was parsed before the handler")
			}
			b, err := io.ReadAll(rc.Request.Body)
			got = string(b)
			return nil, err
		}).Build()
	if err := app.RegisterAction(action); err != nil {
		t.Fatal(err)
	}
	w := postUpload(app.Handler(), sent, contentType, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204: %s", w.Code, w.Body.String())
	}
	if !errors.Is(plugin.err, collage.ErrStreamingBody) {
		t.Errorf("Form() = %v, want ErrStreamingBody", plugin.err)
	}
	if plugin.err != nil && plugin.err.Error() != "collage: the action's body is streamed; it is not parsed as a form" {
		t.Errorf("Form() error reads %q", plugin.err)
	}
	if got != sent {
		t.Errorf("the handler read %d bytes, want the %d sent", len(got), len(sent))
	}
}

// A non-streaming action whose handler reads past its bound and fails is a 413
// too, whatever error it returned — and the error it returned still reaches the
// error hooks.
func TestNonStreaming_HandlerOverreadIs413(t *testing.T) {
	events := &eventRecorder{}
	h := streamingApp(t, false, false, func(b *collage.ActionBuilder) *collage.ActionBuilder {
		return b.WithMaxBodyBytes(16).WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			_, _ = io.Copy(io.Discard, rc.Request.Body)
			return nil, errors.New("upload failed")
		})
	}, events)
	w := postUpload(h, strings.Repeat("x", 1024), "application/octet-stream", nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", w.Code)
	}
	got := events.at("/upload")
	if len(got) != 1 {
		t.Fatalf("%d error events, want 1", len(got))
	}
	ev := got[0]
	if ev.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("event status %d, want 413", ev.Status)
	}
	if !strings.Contains(ev.Err.Error(), "upload failed") {
		t.Errorf("event error %q lost the handler's error", ev.Err)
	}
	if !strings.Contains(ev.Err.Error(), "the handler read past its body limit") {
		t.Errorf("event error %q does not say the handler read past the limit", ev.Err)
	}
	var tooLarge *http.MaxBytesError
	if !errors.As(ev.Err, &tooLarge) || tooLarge.Limit != 16 {
		t.Errorf("event error %v carries no MaxBytesError for the limit", ev.Err)
	}
}

// countingBody counts the bytes read from it.
type countingBody struct {
	io.Reader
	n atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.n.Add(int64(n))
	return n, err
}

func (b *countingBody) Close() error { return nil }

// A streaming action's refused request — the forgery check on, no header — is
// refused without a byte of its body read, and no form parsed.
func TestStreamingBody_RefusalReadsNothing(t *testing.T) {
	h := streamingApp(t, true, false, func(b *collage.ActionBuilder) *collage.ActionBuilder {
		return b.WithStreamingBody().WithHandler(noopAction)
	})
	cookie := csrfCookie(t, h)
	sent, contentType := multipartWithToken(t, cookie.Value)
	body := &countingBody{Reader: strings.NewReader(sent)}
	r := httptest.NewRequest(http.MethodPost, "/upload", body)
	r.Host = "localhost:3000"
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
	if r.Form != nil || r.MultipartForm != nil {
		t.Error("the form was parsed")
	}
	if n := body.n.Load(); n != 0 {
		t.Errorf("%d bytes of the body were read, want 0", n)
	}
}
