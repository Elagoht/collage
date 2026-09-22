package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// errBoom is the generic render failure the error-path tests fail with.
var errBoom = errors.New("collage: boom")

// leakyError carries everything an error message must never reach a production
// client with: a credential, an internal hostname, and a filesystem path.
var leakyError = errors.New(`collage: fragment "secret-fragment" data: dial tcp db.internal:5432: dsn=postgres://admin:hunter2@db.internal:5432/prod, opened at /srv/app/internal/render/fragment.go:120`)

// ---------------------------------------------------------------------------
// Not found
// ---------------------------------------------------------------------------

func TestNotFoundUsesGlobalPage(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	global := errorOnlyPage("global-404")
	env := newEnv(t, []*types.Page{home})
	if err := env.router.RegisterNotFound(global); err != nil {
		t.Fatalf("RegisterNotFound() = %v, want nil", err)
	}
	env.engine.set("global-404", fakeRender{html: "<html>global not found</html>"})

	res := env.get("/missing")

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if got := res.Body.String(); got != "<html>global not found</html>" {
		t.Errorf("body = %q, want the registered not-found page", got)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want \"no-store\"", got)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries = %d, want 0: a 404 is never cached", entries)
	}
}

func TestNotFoundPrefersPageOverrideOverGlobal(t *testing.T) {
	override := errorOnlyPage("page-404")
	matched := testPage("section", "/section", types.StrategyStatic)
	matched.NotFoundPage = override

	stub := &stubRouter{result: &router.MatchResult{IsNotFound: true, Locale: "en", Page: matched}}
	if err := stub.RegisterNotFound(errorOnlyPage("global-404")); err != nil {
		t.Fatalf("RegisterNotFound() = %v, want nil", err)
	}

	env := newEnv(t, nil, withRouter(stub))
	env.engine.set("page-404", fakeRender{html: "<html>page not found</html>"})
	env.engine.set("global-404", fakeRender{html: "<html>global not found</html>"})

	res := env.get("/section/missing")

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if got := res.Body.String(); got != "<html>page not found</html>" {
		t.Errorf("body = %q, want the matched page's own NotFoundPage", got)
	}
}

func TestNotFoundFallsBackToBuiltinPage(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home})

	res := env.get("/missing")
	body := res.Body.String()

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if !strings.Contains(body, "404 Not Found") {
		t.Errorf("body = %q, want the built-in 404 page", body)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want \"no-store\"", got)
	}
	if strings.Contains(body, "/missing") {
		t.Errorf("body = %q, want no request path echoed back", body)
	}
}

// ---------------------------------------------------------------------------
// Server errors
// ---------------------------------------------------------------------------

func TestServerErrorUsesPageErrorPage(t *testing.T) {
	custom := errorOnlyPage("page-500")
	home := testPage("home", "/", types.StrategyStatic)
	home.ErrorPage = custom

	env := newEnv(t, []*types.Page{home})
	if err := env.router.RegisterError(errorOnlyPage("global-500")); err != nil {
		t.Fatalf("RegisterError() = %v, want nil", err)
	}
	env.engine.set("home", fakeRender{err: errBoom})
	env.engine.set("page-500", fakeRender{html: "<html>page error</html>"})
	env.engine.set("global-500", fakeRender{html: "<html>global error</html>"})

	res := env.get("/")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if got := res.Body.String(); got != "<html>page error</html>" {
		t.Errorf("body = %q, want the page's own ErrorPage", got)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want \"no-store\"", got)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries = %d, want 0: a 500 is never cached", entries)
	}
}

func TestServerErrorUsesGlobalErrorPage(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home})
	if err := env.router.RegisterError(errorOnlyPage("global-500")); err != nil {
		t.Fatalf("RegisterError() = %v, want nil", err)
	}
	env.engine.set("home", fakeRender{err: errBoom})
	env.engine.set("global-500", fakeRender{html: "<html>global error</html>"})

	res := env.get("/")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if got := res.Body.String(); got != "<html>global error</html>" {
		t.Errorf("body = %q, want the router's registered error page", got)
	}
}

func TestServerErrorFallsBackToBuiltinPageAndCachesNothing(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home})
	env.engine.set("home", fakeRender{err: errBoom})

	res := env.get("/")
	body := res.Body.String()

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(body, "500 Internal Server Error") {
		t.Errorf("body = %q, want the built-in 500 page", body)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want \"no-store\"", got)
	}
	if got := res.Header().Get("ETag"); got != "" {
		t.Errorf("ETag = %q, want none on an error response", got)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Fatalf("cache entries = %d, want 0 after a 500", entries)
	}
}

func TestRouterMatchErrorProduces500(t *testing.T) {
	stub := &stubRouter{err: errBoom}
	env := newEnv(t, nil, withRouter(stub))

	res := env.get("/")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if env.engine.totalCalls() != 0 {
		t.Errorf("render calls = %d, want 0 when routing itself failed", env.engine.totalCalls())
	}
}

func TestErrorPageRenderFailureIsLoggedOnceAndNotRetried(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home})
	if err := env.router.RegisterError(errorOnlyPage("global-500")); err != nil {
		t.Fatalf("RegisterError() = %v, want nil", err)
	}
	env.engine.set("home", fakeRender{err: errBoom})
	env.engine.set("global-500", fakeRender{err: errors.New("collage: error page exploded")})

	res := env.get("/")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(res.Body.String(), "500 Internal Server Error") {
		t.Errorf("body = %q, want the built-in page after the error page failed", res.Body.String())
	}
	if calls := env.engine.pageCalls("global-500"); calls != 1 {
		t.Errorf("error page renders = %d, want 1: a failed error page is never retried", calls)
	}
	if count := env.logs.count("collage: error page render failed"); count != 1 {
		t.Errorf("error page failure logged %d times, want exactly 1", count)
	}
}

func TestEmptyErrorPageFallsBackToBuiltinPage(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home})
	if err := env.router.RegisterError(errorOnlyPage("global-500")); err != nil {
		t.Fatalf("RegisterError() = %v, want nil", err)
	}
	env.engine.set("home", fakeRender{err: errBoom})
	env.engine.set("global-500", fakeRender{html: ""})

	res := env.get("/")

	if !strings.Contains(res.Body.String(), "500 Internal Server Error") {
		t.Errorf("body = %q, want the built-in page instead of a blank error page", res.Body.String())
	}
	if count := env.logs.count("collage: error page rendered empty"); count != 1 {
		t.Errorf("empty error page logged %d times, want exactly 1", count)
	}
}

func TestErrorPageBodyOmittedForHead(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home})
	env.engine.set("home", fakeRender{err: errBoom})

	res := env.do(httptest.NewRequest(http.MethodHead, "/", nil))

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if res.Body.Len() != 0 {
		t.Errorf("body = %q, want empty for a HEAD", res.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The built-in page's disclosure boundary
// ---------------------------------------------------------------------------

func TestBuiltinProductionErrorPageLeaksNothing(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home})
	env.engine.set("home", fakeRender{err: leakyError, degraded: "secret-fragment"})

	res := env.get("/")
	body := res.Body.String()

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(body, "The server encountered an error") {
		t.Fatalf("body = %q, want the generic message", body)
	}

	forbidden := []struct {
		what    string
		needle  string
		because string
	}{
		{"the error text", "dial tcp", "an error message is written for an operator, not an anonymous client"},
		{"a credential", "hunter2", "error messages routinely carry a DSN"},
		{"a connection string", "postgres://", "a DSN names the database and how to reach it"},
		{"an internal hostname", "db.internal", "internal names map the private network"},
		{"the failing fragment", "secret-fragment", "fragment names describe the application's internal structure"},
		{"a file path", "/srv/app", "a filesystem path maps the deployment"},
		{"a source file", "fragment.go", "a source file name maps the deployment"},
		{"a package path", "github.com/Elagoht/collage", "the framework and its version are an attack surface"},
		{"a stack frame", "goroutine ", "a stack trace exposes the whole call path"},
	}
	for _, f := range forbidden {
		if strings.Contains(body, f.needle) {
			t.Errorf("production 500 body contains %s (%q): %s\nbody:\n%s", f.what, f.needle, f.because, body)
		}
	}
}

func TestBuiltinDevErrorPageShowsDiagnostics(t *testing.T) {
	home := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{home}, withDevMode())
	env.engine.set("home", fakeRender{err: leakyError, degraded: "secret-fragment"})

	res := env.get("/")
	body := res.Body.String()

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	for _, needle := range []string{"secret-fragment", "dial tcp", "db.internal"} {
		if !strings.Contains(body, needle) {
			t.Errorf("dev 500 body does not contain %q, want the fragment and the error chain\nbody:\n%s", needle, body)
		}
	}
}

func TestBuiltinPageIsSelfContained(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		body := string(builtinPage(failure{status: status, err: errBoom, fragment: "frag"}, true))
		for _, needle := range []string{"<link", "<script", "<img", "http://", "https://", "url("} {
			if strings.Contains(body, needle) {
				t.Errorf("status %d: built-in page references %q, want a self-contained document", status, needle)
			}
		}
		if !strings.HasPrefix(body, "<!DOCTYPE html>") {
			t.Errorf("status %d: built-in page = %q, want a complete HTML document", status, body)
		}
	}
}

func TestBuiltinPageEscapesDiagnostics(t *testing.T) {
	body := string(builtinPage(failure{
		status:   http.StatusInternalServerError,
		err:      errors.New("<script>alert(1)</script>"),
		fragment: "<b>frag</b>",
	}, true))

	if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, "<b>frag</b>") {
		t.Errorf("built-in page = %q, want the diagnostics HTML-escaped", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("built-in page = %q, want the escaped error text", body)
	}
}

func TestBuiltinPageUnknownStatusHasTitle(t *testing.T) {
	body := string(builtinPage(failure{status: 599, err: errBoom}, false))
	if !strings.Contains(body, "599 ") {
		t.Errorf("built-in page = %q, want the status in the title", body)
	}
}

// ---------------------------------------------------------------------------
// Error page resolution
// ---------------------------------------------------------------------------

func TestResolveNotFoundAndErrorPrecedence(t *testing.T) {
	pageNotFound := errorOnlyPage("page-404")
	pageError := errorOnlyPage("page-500")
	globalNotFound := errorOnlyPage("global-404")
	globalError := errorOnlyPage("global-500")

	withOverrides := testPage("home", "/", types.StrategyStatic)
	withOverrides.NotFoundPage = pageNotFound
	withOverrides.ErrorPage = pageError
	plain := testPage("plain", "/plain", types.StrategyStatic)

	tests := []struct {
		name           string
		registerGlobal bool
		page           *types.Page
		wantNotFound   *types.Page
		wantError      *types.Page
	}{
		{"page override wins", true, withOverrides, pageNotFound, pageError},
		{"global when the page has none", true, plain, globalNotFound, globalError},
		{"nil page falls back to global", true, nil, globalNotFound, globalError},
		{"nil when nothing is registered", false, plain, nil, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, []*types.Page{})
			if tc.registerGlobal {
				if err := env.router.RegisterNotFound(globalNotFound); err != nil {
					t.Fatalf("RegisterNotFound() = %v, want nil", err)
				}
				if err := env.router.RegisterError(globalError); err != nil {
					t.Fatalf("RegisterError() = %v, want nil", err)
				}
			}

			if got := env.handler.resolveNotFound(tc.page); got != tc.wantNotFound {
				t.Errorf("resolveNotFound() = %v, want %v", got, tc.wantNotFound)
			}
			if got := env.handler.resolveError(tc.page); got != tc.wantError {
				t.Errorf("resolveError() = %v, want %v", got, tc.wantError)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Diagnostics helpers
// ---------------------------------------------------------------------------

func TestFailedFragment(t *testing.T) {
	tests := []struct {
		name   string
		result *render.Result
		want   string
	}{
		{"nil result", nil, ""},
		{"nil metadata", &render.Result{}, ""},
		{"no failure", &render.Result{Metadata: &render.Metadata{
			Fragments: []render.FragmentMetadata{{Name: "ok"}},
		}}, ""},
		{"first failure wins", &render.Result{Metadata: &render.Metadata{
			Fragments: []render.FragmentMetadata{{Name: "ok"}, {Name: "bad", Failed: true}, {Name: "worse", Failed: true}},
		}}, "bad"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := failedFragment(tc.result); got != tc.want {
				t.Errorf("failedFragment() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorDetailIncludesPanicStack(t *testing.T) {
	if got := errorDetail(nil); got != "" {
		t.Errorf("errorDetail(nil) = %q, want \"\"", got)
	}

	panicErr := &render.PanicError{Value: "boom", Stack: []byte("goroutine 17 [running]:\ncollage.render()")}
	detail := errorDetail(fmt.Errorf("collage: fragment %q render: %w", "sidebar", panicErr))

	if !strings.Contains(detail, "sidebar") || !strings.Contains(detail, "boom") {
		t.Errorf("errorDetail() = %q, want the wrapped chain", detail)
	}
	if !strings.Contains(detail, "goroutine 17") {
		t.Errorf("errorDetail() = %q, want the panic stack", detail)
	}
}

func TestDevBuiltinPageIncludesPanicStack(t *testing.T) {
	panicErr := &render.PanicError{Value: "boom", Stack: []byte("goroutine 17 [running]:\ncollage.render()")}
	wrapped := fmt.Errorf("collage: fragment %q render: %w", "sidebar", panicErr)

	dev := string(builtinPage(failure{status: http.StatusInternalServerError, err: wrapped, fragment: "sidebar"}, true))
	if !strings.Contains(dev, "goroutine 17") {
		t.Errorf("dev built-in page = %q, want the panic stack", dev)
	}

	prod := string(builtinPage(failure{status: http.StatusInternalServerError, err: wrapped, fragment: "sidebar"}, false))
	if strings.Contains(prod, "goroutine 17") || strings.Contains(prod, "sidebar") {
		t.Errorf("production built-in page = %q, want no stack and no fragment name", prod)
	}
}

func TestRouterReturningNoResultProduces500(t *testing.T) {
	env := newEnv(t, nil, withRouter(&stubRouter{}))

	res := env.get("/")

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: a Router that returns neither a result nor an error must not panic the handler", res.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(res.Body.String(), "500 Internal Server Error") {
		t.Errorf("body = %q, want the built-in 500 page", res.Body.String())
	}
}
