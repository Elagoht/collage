package httpx

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/types"
)

// Properties Next.js pins for server actions, form posts and request bodies,
// checked here against collage's actions.

// multipartBody returns a multipart form with one field of n bytes, and its
// content type.
func multipartBody(t *testing.T, n int) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("payload", strings.Repeat("x", n)); err != nil {
		t.Fatalf("WriteField() = %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// tokenPair returns a token guard issued, for a request to carry in both its
// cookie and its form.
func tokenPair(t *testing.T, guard *csrf.Guard) string {
	t.Helper()
	token, _, err := guard.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("TokenFor() = %v", err)
	}
	return token
}

// Next.js: test/e2e/client-max-body-size ("should accept request body at exactly
// 10MB") and test/e2e/middleware-fetches-with-body ("body size equal to 5kb").
// The limit is a size the body may have, not one it must stay under: a body of
// exactly MaxBodyBytes is read whole, and one byte more is a 413.
func TestNextjs_ABodyAtTheLimitIsAcceptedAndOneByteMoreIsNot(t *testing.T) {
	body := "title=" + strings.Repeat("x", 58) // 64 bytes
	for _, tc := range []struct {
		limit int64
		want  int
	}{
		{int64(len(body)), http.StatusOK},
		{int64(len(body)) - 1, http.StatusRequestEntityTooLarge},
	} {
		var got string
		create := action("create", "/posts", []string{http.MethodPost},
			func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
				if err := rc.Request.ParseForm(); err != nil {
					return nil, err
				}
				got = rc.Request.PostFormValue("title")
				return &types.ActionResult{Status: http.StatusOK, Body: []byte("ok"), ContentType: "text/plain"}, nil
			})
		create.MaxBodyBytes = tc.limit

		env := actionEnv(t, nil, []*types.Action{create})
		res := env.do(post("/posts", body))
		if res.Code != tc.want {
			t.Errorf("limit %d, body %d: status = %d, want %d", tc.limit, len(body), res.Code, tc.want)
		}
		if tc.want == http.StatusOK && len(got) != 58 {
			t.Errorf("limit %d: the handler read %d bytes of the title, want 58", tc.limit, len(got))
		}
	}
}

// Next.js: test/e2e/app-dir/actions/app-action-size-limit-invalid.test.ts
// ("should respect the size set in serverActions.bodySizeLimit for multipart
// fetch actions": below, at, and above the limit). Multipart is what
// fetch(url, {body: new FormData(form)}) sends, and it is bounded like a form.
func TestNextjs_AMultipartBodyIsBoundedAtTheLimit(t *testing.T) {
	body, contentType := multipartBody(t, 256)
	for _, tc := range []struct {
		limit int64
		want  int
	}{
		{int64(len(body)) + 1024, http.StatusOK},
		{int64(len(body)), http.StatusOK},
		{int64(len(body)) - 1, http.StatusRequestEntityTooLarge},
	} {
		upload := action("upload", "/upload", []string{http.MethodPost},
			func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
				if err := rc.Request.ParseMultipartForm(1 << 20); err != nil {
					return nil, err
				}
				return &types.ActionResult{Status: http.StatusOK, Body: []byte("ok"), ContentType: "text/plain"}, nil
			})
		upload.MaxBodyBytes = tc.limit

		env := actionEnv(t, nil, []*types.Action{upload})
		req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		if res := env.do(req); res.Code != tc.want {
			t.Errorf("limit %d, body %d: status = %d, want %d", tc.limit, len(body), res.Code, tc.want)
		}
	}
}

// Next.js: test/e2e/app-dir/actions/app-action-size-limit-invalid.test.ts
// ("... for plaintext fetch actions"). A handler that reads the raw body itself —
// a webhook, a JSON endpoint — is bounded too, and its overrun is a 413 rather
// than the 500 of a handler that failed.
func TestNextjs_ARawBodyReadByTheHandlerIsBounded(t *testing.T) {
	hook := action("hook", "/hooks/pay", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			if _, err := io.ReadAll(rc.Request.Body); err != nil {
				return nil, err
			}
			return &types.ActionResult{Status: http.StatusOK, Body: []byte("ok"), ContentType: "text/plain"}, nil
		})
	hook.SkipCSRF = true
	hook.MaxBodyBytes = 1024

	option, _ := withCSRF(t)
	env := actionEnv(t, nil, []*types.Action{hook}, option)
	req := httptest.NewRequest(http.MethodPost, "/hooks/pay", strings.NewReader(strings.Repeat("x", 4096)))
	req.Header.Set("Content-Type", "text/plain")
	if res := env.do(req); res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.Code)
	}
}

// Next.js: test/e2e/app-dir/actions/app-action.test.ts ("should correctly decode
// multi-byte characters in the request body"). 100000 あ arrive as 100000 あ:
// the body is not cut or decoded somewhere a character can straddle.
func TestNextjs_MultiByteCharactersInABodyAreDecodedWhole(t *testing.T) {
	var got string
	echo := action("echo", "/echo", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			if err := rc.Request.ParseForm(); err != nil {
				return nil, err
			}
			got = rc.Request.PostFormValue("text")
			return nil, nil
		})

	env := actionEnv(t, nil, []*types.Action{echo})
	want := strings.Repeat("あ", 100000)
	res := env.do(post("/echo", url.Values{"text": {want}}.Encode()))
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.Code)
	}
	if got != want {
		t.Errorf("read %d characters, %d of them U+FFFD; want 100000 あ",
			utf8.RuneCountInString(got), strings.Count(got, "�"))
	}
}

// Next.js: test/e2e/app-dir/actions/app-action.test.ts ("should not log errors
// for non-action form POSTs"). A form posted to a page with no action is the
// reader's mistake, not the application's: a 405 saying what the URL does
// accept, below error level.
func TestNextjs_AFormPostedToAPageWithNoActionIsAQuiet405(t *testing.T) {
	env := actionEnv(t, []*types.Page{testPage("home", "/", types.StrategyDynamic)}, nil)

	res := env.do(post("/", "title=x"))
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.Code)
	}
	if allow := res.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) || strings.Contains(allow, http.MethodPost) {
		t.Errorf("Allow = %q, want GET and not POST", allow)
	}
	if env.engine.pageCalls("home") != 0 {
		t.Error("the page rendered for a POST it does not answer")
	}
	for _, rec := range env.logs.recordsFor("collage: request failed") {
		if rec.level >= slog.LevelError {
			t.Errorf("logged at %v, want below error level", rec.level)
		}
	}
}

// Next.js: test/e2e/app-dir/actions/app-action.test.ts ("merges cookies correctly
// when redirecting", "should support headers and cookies"). A cookie an action
// sets or clears goes out on its redirect — to a form post and to a fetch alike —
// so the page it redirects to is read with it.
func TestNextjs_CookiesSetByAnActionTravelWithItsRedirect(t *testing.T) {
	login := action("login", "/login", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			header := http.Header{}
			header.Add("Set-Cookie", (&http.Cookie{Name: "bar", Value: "2", Path: "/"}).String())
			header.Add("Set-Cookie", (&http.Cookie{Name: "foo", Value: "", Path: "/", MaxAge: -1}).String())
			return &types.ActionResult{Location: "/target", Header: header}, nil
		})

	for name, fetch := range map[string]bool{"form post": false, "fetch": true} {
		env := actionEnv(t, nil, []*types.Action{login})
		req := post("/login", "")
		if fetch {
			req.Header.Set(FetchHeader, "1")
		}
		res := env.do(req)

		cookies := map[string]*http.Cookie{}
		for _, c := range res.Result().Cookies() {
			cookies[c.Name] = c
		}
		if c := cookies["bar"]; c == nil || c.Value != "2" {
			t.Errorf("%s: bar = %v, want 2", name, c)
		}
		if c := cookies["foo"]; c == nil || c.MaxAge >= 0 {
			t.Errorf("%s: foo = %v, want it cleared", name, c)
		}
	}
}

// Next.js: test/e2e/app-dir/actions/app-action.test.ts ("should handle calls to
// redirect() with external URLs"). An action may send the reader to another
// site; the destination is written as given, and a fetch is handed it rather
// than following it itself.
func TestNextjs_AnActionMayRedirectToAnotherSite(t *testing.T) {
	const destination = "https://pay.example/checkout?order=1"
	checkout := action("checkout", "/checkout", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Location: destination}, nil
		})
	env := actionEnv(t, nil, []*types.Action{checkout})

	if res := env.do(post("/checkout", "")); res.Code != http.StatusSeeOther || res.Header().Get("Location") != destination {
		t.Errorf("form post: %d Location %q, want 303 %q", res.Code, res.Header().Get("Location"), destination)
	}
	req := post("/checkout", "")
	req.Header.Set(FetchHeader, "1")
	res := env.do(req)
	if res.Code != http.StatusNoContent || res.Header().Get(LocationHeader) != destination || res.Header().Get("Location") != "" {
		t.Errorf("fetch: %d %s %q Location %q, want 204 with only %s",
			res.Code, LocationHeader, res.Header().Get(LocationHeader), res.Header().Get("Location"), LocationHeader)
	}
}

// Next.js: test/e2e/app-dir/actions-allowed-origins (opaque-origin,
// unsafe-origins). The origin check runs in front of the action, with a valid
// token in hand: a sandboxed page's Origin "null", and an Origin a spoofed
// X-Forwarded-Host agrees with, are refused before the handler runs.
func TestNextjs_AnActionRefusesAnOriginTheHostDoesNotName(t *testing.T) {
	option, guard := withCSRF(t)
	ran := false
	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			ran = true
			return nil, nil
		})
	env := actionEnv(t, nil, []*types.Action{create}, option)
	token := tokenPair(t, guard)

	for name, headers := range map[string]map[string]string{
		"opaque":         {"Origin": "null"},
		"forwarded host": {"Origin": "https://evil.example", "X-Forwarded-Host": "evil.example"},
	} {
		req := post("/posts", csrf.DefaultFieldName+"="+token)
		req.AddCookie(&http.Cookie{Name: csrf.DefaultCookieName, Value: token})
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if res := env.do(req); res.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", name, res.Code)
		}
	}
	if ran {
		t.Error("the handler ran for a request from another origin")
	}
}

// Next.js: test/production/reading-request-body-in-middleware ("passes the body
// to the api endpoint"). Middleware that parses the form before an action does
// not leave the action an empty body: the token and the fields it read are still
// there for the forgery check and the handler.
func TestNextjs_AFormParsedByMiddlewareStillReachesTheAction(t *testing.T) {
	option, guard := withCSRF(t)
	var seen, got string
	create := action("create", "/posts", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			got = rc.Request.PostFormValue("title")
			return nil, nil
		})
	parse := func(d *Deps) {
		d.Middleware = append(d.Middleware, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				seen = r.PostFormValue("title")
				next.ServeHTTP(w, r)
			})
		})
	}
	env := actionEnv(t, nil, []*types.Action{create}, option, parse)

	token := tokenPair(t, guard)
	req := post("/posts", csrf.DefaultFieldName+"="+token+"&title=Hello")
	req.AddCookie(&http.Cookie{Name: csrf.DefaultCookieName, Value: token})
	if res := env.do(req); res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.Code)
	}
	if seen != "Hello" || got != "Hello" {
		t.Errorf("middleware read %q and the handler %q, want Hello for both", seen, got)
	}
}
