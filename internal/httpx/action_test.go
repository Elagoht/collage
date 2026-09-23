package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/render"

	"github.com/Elagoht/collage/internal/types"
)

// actionEnv builds a handler over a real router with pages and actions registered.
func actionEnv(t *testing.T, pages []*types.Page, actions []*types.Action, opts ...envOption) *testEnv {
	t.Helper()
	env := newEnv(t, pages, opts...)
	for _, action := range actions {
		if err := env.router.RegisterAction(action); err != nil {
			t.Fatalf("RegisterAction(%q) = %v, want nil", action.Name, err)
		}
	}
	return env
}

func action(name, pattern string, methods []string, h types.ActionHandlerFunc) *types.Action {
	return &types.Action{
		Name:    name,
		Paths:   map[string]string{"en": pattern},
		Methods: methods,
		Handler: h,
	}
}

func post(target string, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// The ordinary successful form post: the handler does its work and redirects, and
// the redirect is a 303 so that reloading the destination does not submit again.
func TestAction_FormPostRedirects(t *testing.T) {
	var got string
	create := action("create", "/posts/new", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			if err := rc.Request.ParseForm(); err != nil {
				return nil, err
			}
			got = rc.Request.PostFormValue("title")
			return &types.ActionResult{Location: "/posts/hello"}, nil
		})

	env := actionEnv(t, []*types.Page{testPage("new", "/posts/new", types.StrategyDynamic)},
		[]*types.Action{create})

	res := env.do(post("/posts/new", "title=Hello"))

	if res.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.Code)
	}
	if loc := res.Header().Get("Location"); loc != "/posts/hello" {
		t.Errorf("Location = %q, want %q", loc, "/posts/hello")
	}
	if got != "Hello" {
		t.Errorf("the handler read title = %q, want %q", got, "Hello")
	}
	// The page at this URL must not have rendered: a POST is the action's.
	if env.engine.pageCalls("new") != 0 {
		t.Errorf("page renders = %d, want 0", env.engine.pageCalls("new"))
	}
}

// A validation failure re-renders the form's own page, with a status that says so,
// and what the handler learned reaches the page through the shared data they both
// hold.
func TestAction_ValidationFailureRendersThePageAgain(t *testing.T) {
	page := testPage("new", "/posts/new", types.StrategyDynamic)
	create := action("create", "/posts/new", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			rc.Set("error", "a title is required")
			return &types.ActionResult{Status: http.StatusUnprocessableEntity, Page: page}, nil
		})

	env := actionEnv(t, []*types.Page{page}, []*types.Action{create})
	res := env.do(post("/posts/new", ""))

	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}
	if env.engine.pageCalls("new") != 1 {
		t.Errorf("page renders = %d, want 1", env.engine.pageCalls("new"))
	}
	if cc := res.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store: this body is one submission's", cc)
	}
}

// An action can answer with one fragment, which is what a page that wants only its
// changed part back asks for.
func TestAction_AnswersWithAFragment(t *testing.T) {
	results := &types.Fragment{Name: "results", TemplatePath: "results.html"}
	search := action("search", "/search/results", []string{http.MethodGet},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Fragment: results}, nil
		})

	env := actionEnv(t, nil, []*types.Action{search})
	res := env.do(httptest.NewRequest(http.MethodGet, "/search/results?q=grid", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if body := res.Body.String(); !strings.Contains(body, "results") {
		t.Errorf("body = %q, want the fragment's markup", body)
	}
	if ct := res.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// A handler that changed something says which tags it invalidated, and that happens
// before the response — so a reader following the redirect cannot be handed a page
// the change already made wrong.
func TestAction_InvalidatesBeforeResponding(t *testing.T) {
	var invalidated []string
	responded := false

	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Location: "/posts", InvalidateTags: []string{"posts"}}, nil
		})

	env := actionEnv(t, nil, []*types.Action{create}, func(d *Deps) {
		d.Invalidator = func(_ context.Context, tags []string) error {
			if responded {
				t.Error("invalidation ran after the response was written")
			}
			invalidated = append(invalidated, tags...)
			return nil
		}
	})

	res := env.do(post("/posts", ""))
	responded = true

	if res.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.Code)
	}
	if len(invalidated) != 1 || invalidated[0] != "posts" {
		t.Errorf("invalidated = %v, want [posts]", invalidated)
	}
}

// A body larger than the limit is refused rather than read into memory. Without a
// bound, the size of the allocation is chosen by whoever sent the request.
func TestAction_BodyIsBounded(t *testing.T) {
	create := action("create", "/posts", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			return nil, rc.Request.ParseForm()
		})
	create.MaxBodyBytes = 16

	env := actionEnv(t, nil, []*types.Action{create})
	res := env.do(post("/posts", "title="+strings.Repeat("x", 4096)))

	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.Code)
	}
}

// A handler returning nothing answers 204: it did something and has nothing to say
// about it, which is not an error worth inventing a body for.
func TestAction_NilResultIsNoContent(t *testing.T) {
	remove := action("remove", "/posts/{slug}", []string{http.MethodDelete},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return nil, nil
		})

	env := actionEnv(t, nil, []*types.Action{remove})
	res := env.do(httptest.NewRequest(http.MethodDelete, "/posts/hello", nil))

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.Code)
	}
	if res.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", res.Body.String())
	}
}

// An OPTIONS request is answered from the router's own list of what the path
// accepts, so the 405's Allow header and this one cannot disagree.
func TestAction_OptionsReportsWhatThePathAccepts(t *testing.T) {
	create := action("create", "/posts/new", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) { return nil, nil })

	env := actionEnv(t, []*types.Page{testPage("new", "/posts/new", types.StrategyDynamic)},
		[]*types.Action{create})

	res := env.do(httptest.NewRequest(http.MethodOptions, "/posts/new", nil))

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.Code)
	}
	allow := res.Header().Get("Allow")
	for _, want := range []string{"GET", "HEAD", "POST", "OPTIONS"} {
		if !strings.Contains(allow, want) {
			t.Errorf("Allow = %q, want it to contain %s", allow, want)
		}
	}
}

// A handler reporting that the thing does not exist answers 404, not 500.
func TestAction_NotFoundFromAHandler(t *testing.T) {
	remove := action("remove", "/posts/{slug}", []string{http.MethodDelete},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return nil, errors.New("gone: " + types.ErrNotFound.Error())
		})
	remove.Handler = func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
		return nil, types.ErrNotFound
	}

	env := actionEnv(t, nil, []*types.Action{remove})
	res := env.do(httptest.NewRequest(http.MethodDelete, "/posts/nope", nil))

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
}

// ---------------------------------------------------------------------------
// Request forgery
// ---------------------------------------------------------------------------

func withCSRF(t *testing.T) (envOption, *csrf.Guard) {
	t.Helper()
	guard, err := csrf.New(csrf.Config{Key: []byte("a key for the tests")})
	if err != nil {
		t.Fatalf("csrf.New() = %v, want nil", err)
	}
	return func(d *Deps) { d.CSRF = guard }, guard
}

// An unsafe request with no token is refused before the handler runs, and refused as
// 403: the request was well formed, it was not authorised.
func TestCSRF_UnsafeRequestWithoutATokenIsRefused(t *testing.T) {
	option, _ := withCSRF(t)
	ran := false
	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			ran = true
			return nil, nil
		})

	env := actionEnv(t, nil, []*types.Action{create}, option)
	res := env.do(post("/posts", "title=Hello"))

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
	if ran {
		t.Error("the handler ran for a request that carried no token")
	}
}

func TestCSRF_MatchingTokenIsAccepted(t *testing.T) {
	option, guard := withCSRF(t)
	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Location: "/posts"}, nil
		})

	env := actionEnv(t, nil, []*types.Action{create}, option)

	token, _, err := guard.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("TokenFor() = %v, want nil", err)
	}
	req := post("/posts", csrf.DefaultFieldName+"="+token+"&title=Hello")
	req.AddCookie(&http.Cookie{Name: csrf.DefaultCookieName, Value: token})

	if res := env.do(req); res.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.Code)
	}
}

// A webhook cannot carry a token, and says so.
func TestCSRF_SkippedActionIsNotChecked(t *testing.T) {
	option, _ := withCSRF(t)
	hook := action("hook", "/hooks/pay", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Status: http.StatusOK, Body: []byte("ok"), ContentType: "text/plain"}, nil
		})
	hook.SkipCSRF = true

	env := actionEnv(t, nil, []*types.Action{hook}, option)
	if res := env.do(post("/hooks/pay", "event=paid")); res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
}

// A GET action is not checked: it changes nothing by contract, and a token on it
// would be a token in a URL, which is a token in a log file and in a Referer header.
func TestCSRF_SafeMethodsAreNotChecked(t *testing.T) {
	option, _ := withCSRF(t)
	read := action("read", "/posts/list", []string{http.MethodGet},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Status: http.StatusOK, Body: []byte("[]"), ContentType: "application/json"}, nil
		})

	env := actionEnv(t, nil, []*types.Action{read}, option)
	if res := env.do(httptest.NewRequest(http.MethodGet, "/posts/list", nil)); res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
}

// A page that issued a token is never written to the cache. A cached page carrying a
// token would hand the next visitor a token that is not theirs — and hand every
// visitor the same one, which is a token that proves nothing.
func TestCSRF_APageWithATokenIsNotCached(t *testing.T) {
	option, _ := withCSRF(t)
	page := testPage("form", "/form", types.StrategyIncremental)

	env := newEnv(t, []*types.Page{page}, option, func(d *Deps) {
		d.Renderer = &tokenIssuingEngine{inner: newFakeEngine(fakeRender{html: "<form></form>"})}
	})

	first := env.get("/form")
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}
	if entries := env.cacheEntries(); entries != 0 {
		t.Errorf("cache entries = %d, want 0: a page carrying a token belongs to one visitor", entries)
	}
	if cc := first.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if !strings.Contains(first.Header().Get("Set-Cookie"), csrf.DefaultCookieName) {
		t.Errorf("Set-Cookie = %q, want the token cookie", first.Header().Get("Set-Cookie"))
	}
}

// Nor is such a render shared between concurrent requests. Coalescing is what makes
// an expiring page cost one render; it must not make two visitors share one token.
func TestCSRF_ATokenIssuingRenderIsNotCoalesced(t *testing.T) {
	option, _ := withCSRF(t)
	page := testPage("form", "/form", types.StrategyIncremental)
	issuing := &tokenIssuingEngine{inner: newFakeEngine(fakeRender{html: "<form></form>"})}

	env := newEnv(t, []*types.Page{page}, option, func(d *Deps) { d.Renderer = issuing })

	var wg sync.WaitGroup
	tokens := make([]string, 8)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := env.get("/form")
			tokens[i] = rec.Header().Get("Set-Cookie")
		}()
	}
	wg.Wait()

	seen := make(map[string]int)
	for _, cookie := range tokens {
		if cookie == "" {
			t.Fatal("a response carried no token cookie")
		}
		seen[cookie]++
	}
	if len(seen) != len(tokens) {
		t.Errorf("distinct tokens = %d across %d requests: a token was shared between visitors",
			len(seen), len(tokens))
	}
}

// tokenIssuingEngine renders a page that asks for a forgery token, which is what a
// page with a form does. The fake template engine has no template functions, so the
// issuing is done here instead.
type tokenIssuingEngine struct {
	inner *fakeEngine
	seq   atomic.Int64
}

func (e *tokenIssuingEngine) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	rc.IssueCSRF(fmt.Sprintf("token-%d.signature", e.seq.Add(1)))
	return e.inner.Render(ctx, rc)
}

func (e *tokenIssuingEngine) RenderFragment(ctx context.Context, rc *types.RenderContext, f *types.Fragment) ([]byte, error) {
	return e.inner.RenderFragment(ctx, rc, f)
}
