package collage_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

var tokenField = regexp.MustCompile(`name="_csrf" value="([^"]*)"`)

// A page with a form is served with the reader's token masked anew in every
// response — so a compressed response's length gives the token away to nobody
// (BREACH) — while its ETag names the token itself, the same each time, so a
// conditional request is still answered 304. The masked token submits.
func TestCSRF_TokenMaskedPerResponse(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/form.html": {Data: []byte(`<form method="post">{{csrfToken}}</form>`)},
		}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Security: collage.SecurityConfig{CSRFKey: []byte(strings.Repeat("k", 32))},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	save := func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
		ran++
		return nil, nil
	}
	if err := app.RegisterPage(collage.NewPage("form").WithContent(collage.NewFragment("form", "form.html").Build()).
		WithPath("en", "/form").WithAction(http.MethodPost, save).Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()

	get := func(cookie *http.Cookie, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://localhost:3000/form", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := get(nil, "")
	cookies := first.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %v, want the forgery cookie", cookies)
	}
	cookie := cookies[0]
	second := get(cookie, "")
	a, b := tokenField.FindStringSubmatch(first.Body.String()), tokenField.FindStringSubmatch(second.Body.String())
	if a == nil || b == nil {
		t.Fatalf("no forgery field in %q, %q", first.Body, second.Body)
	}
	if a[1] == b[1] || strings.Contains(first.Body.String(), cookie.Value) {
		t.Errorf("served %q then %q for the token %q; want it masked differently each time", a[1], b[1], cookie.Value)
	}
	etag := second.Header().Get("ETag")
	if etag == "" || etag != get(cookie, "").Header().Get("ETag") {
		t.Errorf("ETag %q changed between responses to one reader", etag)
	}
	if rec := get(cookie, etag); rec.Code != http.StatusNotModified {
		t.Errorf("conditional request = %d, want 304", rec.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "http://localhost:3000/form",
		strings.NewReader(url.Values{"_csrf": {b[1]}}.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Sec-Fetch-Site", "same-origin")
	post.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	if ran != 1 {
		t.Errorf("submitting the masked token = %d, ran %d times; want the action run", rec.Code, ran)
	}
}
