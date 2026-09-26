package httpx

import (
	"context"
	"errors"
	"github.com/Elagoht/collage/internal/cache"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/csrf"

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
// A form a script submitted with fetch is told where the action redirects, and
// navigates there once, instead of fetch following the redirect and downloading
// the page first.
func TestAction_FetchIsToldTheRedirect(t *testing.T) {
	create := action("create", "/posts/new", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Location: "/posts/hello"}, nil
		})
	env := actionEnv(t, []*types.Page{testPage("new", "/posts/new", types.StrategyDynamic)}, []*types.Action{create})

	req := post("/posts/new", "title=Hello")
	req.Header.Set(FetchHeader, "1")
	res := env.do(req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.Code)
	}
	if got := res.Header().Get(LocationHeader); got != "/posts/hello" {
		t.Errorf("%s = %q", LocationHeader, got)
	}
	if got := res.Header().Get("Location"); got != "" {
		t.Errorf("Location = %q: fetch would follow it", got)
	}
}

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

// A page with a form is still cached, and each reader is still handed their own
// token. The stored body carries a marker where the token goes; the response carries
// the token. Without that, a newsletter form in a site's footer would turn caching
// off for the whole site.
func TestCSRF_APageWithAFormIsStillCachedButTokensDiffer(t *testing.T) {
	option, guard := withCSRF(t)
	page := testPage("form", "/form", types.StrategyIncremental)
	marker := guard.Marker()

	// Held here rather than read from the env: an option replaces Deps.Renderer,
	// and env.engine still names the one it replaced.
	engine := newFakeEngine(fakeRender{
		html: `<form><input type="hidden" name="_csrf" value="` + marker + `"></form>`,
	})
	env := newEnv(t, []*types.Page{page}, option, func(d *Deps) { d.Renderer = engine })

	first := env.get("/form")
	second := env.get("/form")

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d, want 200", first.Code, second.Code)
	}
	// One render served both: the expensive part is shared.
	if renders := engine.pageCalls("form"); renders != 1 {
		t.Errorf("renders = %d, want 1: the body behind the token is cacheable", renders)
	}
	// And nothing on the wire still carries the marker.
	for i, rec := range []*httptest.ResponseRecorder{first, second} {
		if strings.Contains(rec.Body.String(), marker) {
			t.Errorf("response %d still carries the marker, so the form would be refused on submission", i)
		}
		if !strings.Contains(rec.Header().Get("Set-Cookie"), csrf.DefaultCookieName) {
			t.Errorf("response %d carried no token cookie", i)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("response %d Cache-Control = %q, want no-store: this body is one reader's", i, cc)
		}
	}
}

// Two readers with no cookie of their own get two different tokens, from one cached
// body. A shared token is a token anyone obtains by visiting the site.
func TestCSRF_EachReaderGetsTheirOwnToken(t *testing.T) {
	option, guard := withCSRF(t)
	page := testPage("form", "/form", types.StrategyIncremental)
	marker := guard.Marker()

	env := newEnv(t, []*types.Page{page}, option, func(d *Deps) {
		d.Renderer = newFakeEngine(fakeRender{
			html: `<input type="hidden" name="_csrf" value="` + marker + `">`,
		})
	})

	seen := make(map[string]int)
	for range 8 {
		rec := env.get("/form")
		cookie := rec.Header().Get("Set-Cookie")
		if cookie == "" {
			t.Fatal("a response carried no token cookie")
		}
		seen[cookie]++
	}
	if len(seen) != 8 {
		t.Errorf("distinct tokens = %d across 8 readers, want 8: a token was shared", len(seen))
	}
}

// A reader who already holds a valid token keeps it, so the several tabs they have
// open do not invalidate one another.
func TestCSRF_AnExistingTokenIsKept(t *testing.T) {
	option, guard := withCSRF(t)
	page := testPage("form", "/form", types.StrategyIncremental)
	marker := guard.Marker()

	env := newEnv(t, []*types.Page{page}, option, func(d *Deps) {
		d.Renderer = newFakeEngine(fakeRender{
			html: `<input type="hidden" name="_csrf" value="` + marker + `">`,
		})
	})

	token, _, err := guard.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("TokenFor() = %v, want nil", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/form", nil)
	req.AddCookie(&http.Cookie{Name: csrf.DefaultCookieName, Value: token})

	rec := env.do(req)
	if !strings.Contains(rec.Body.String(), token) {
		t.Error("the reader's own token is not in the page they were served")
	}
}

// A page with no form is untouched: no cookie, no no-store, and the body that was
// cached is the body that is written.
func TestCSRF_APageWithNoFormIsUnaffected(t *testing.T) {
	option, _ := withCSRF(t)
	page := testPage("plain", "/plain", types.StrategyIncremental)

	env := newEnv(t, []*types.Page{page}, option)
	rec := env.get("/plain")

	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("Set-Cookie = %q on a page with no form, want none", got)
	}
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want the page's own", cc)
	}
}

// ---------------------------------------------------------------------------
// An empty render is never a response
// ---------------------------------------------------------------------------

// A page that rendered no markup is a failure, not a 200 with nothing in it.
//
// The static builder has always refused to write one (ErrEmptyRender) on the
// grounds that a file nobody can read is worse than no file. Serving one is the same
// mistake with a status code on it: the reader gets a blank page and the operator
// gets a success in the log.
func TestEmptyRenderIsNotServed(t *testing.T) {
	page := testPage("blank", "/blank", types.StrategyDynamic)
	env := newEnv(t, []*types.Page{page}, func(d *Deps) {
		d.Renderer = newFakeEngine(fakeRender{html: ""})
	})

	rec := env.get("/blank")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a page that rendered nothing", rec.Code)
	}
}

// The same for a page an action answered with — which is where it actually bites,
// because RenderPage is usually handed a page built on the spot, and a page that was
// never registered has no content bound into its layout.
func TestEmptyRenderFromAnActionIsNotServed(t *testing.T) {
	page := testPage("blank", "/blank", types.StrategyDynamic)
	create := action("create", "/create", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Status: http.StatusUnprocessableEntity, Page: page}, nil
		})

	env := actionEnv(t, nil, []*types.Action{create}, func(d *Deps) {
		d.Renderer = newFakeEngine(fakeRender{html: ""})
	})

	rec := env.do(post("/create", ""))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: an empty body is not a validation failure", rec.Code)
	}
}

// A refusal the request earned is not an application failure. A bot probing forms
// does this all day, and at error level it buries the failures that are real.
func TestClientRefusalsAreNotLoggedAsErrors(t *testing.T) {
	option, _ := withCSRF(t)
	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) { return nil, nil })

	env := actionEnv(t, []*types.Page{testPage("home", "/", types.StrategyDynamic)},
		[]*types.Action{create}, option)

	// A POST with no token, and a DELETE to a page that answers neither.
	env.do(post("/posts", "title=x"))
	env.do(httptest.NewRequest(http.MethodDelete, "/", nil))

	for _, rec := range env.logs.recordsFor("collage: request failed") {
		if rec.level >= slog.LevelError {
			t.Errorf("a %s failure was logged at %v, want below error level", rec.stage, rec.level)
		}
	}
	if len(env.logs.recordsFor("collage: request failed")) != 2 {
		t.Errorf("records = %d, want the two refusals recorded — quietly, not silently",
			len(env.logs.recordsFor("collage: request failed")))
	}
}

// An action's response is rendered like any other, so a form inside it carries the
// marker — and it has to leave carrying a token, or the next submission from it is
// refused. Both the fragment and the page an action answers with.
func TestCSRF_AnActionsResponseCarriesAToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result func(page *types.Page) *types.ActionResult
	}{
		{"fragment", func(*types.Page) *types.ActionResult {
			return &types.ActionResult{Fragment: &types.Fragment{Name: "counter", TemplatePath: "counter.html"}}
		}},
		{"page", func(page *types.Page) *types.ActionResult {
			return &types.ActionResult{Page: page}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			option, guard := withCSRF(t)
			marker := guard.Marker()
			form := `<form><input type="hidden" name="_csrf" value="` + marker + `"></form>`
			page := testPage("counter", "/count", types.StrategyDynamic)
			count := action("count", "/count", []string{http.MethodPost},
				func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
					return tc.result(page), nil
				})

			engine := newFakeEngine(fakeRender{html: form})
			engine.set("fragment:counter", fakeRender{html: form})
			env := actionEnv(t, []*types.Page{page}, []*types.Action{count}, option, func(d *Deps) {
				d.Renderer = engine
			})

			token, _, err := guard.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
			if err != nil {
				t.Fatalf("TokenFor() = %v, want nil", err)
			}
			req := post("/count", csrf.DefaultFieldName+"="+token)
			req.AddCookie(&http.Cookie{Name: csrf.DefaultCookieName, Value: token})
			res := env.do(req)

			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.Code)
			}
			body := res.Body.String()
			if strings.Contains(body, marker) {
				t.Errorf("body = %q, still carries the marker", body)
			}
			if !strings.Contains(body, token) {
				t.Errorf("body = %q, want the reader's token %q", body, token)
			}
		})
	}
}

// A form too large to read, token and all, is a 413 — not a 403 that blames the
// token for a limit it never reached.
func TestCSRF_AnOversizedFormIsTooLargeNotForbidden(t *testing.T) {
	option, guard := withCSRF(t)
	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) { return nil, nil })
	create.MaxBodyBytes = 16

	env := actionEnv(t, nil, []*types.Action{create}, option)
	token, _, _ := guard.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	req := post("/posts", "title="+strings.Repeat("x", 64)+"&"+csrf.DefaultFieldName+"="+token)
	req.AddCookie(&http.Cookie{Name: csrf.DefaultCookieName, Value: token})

	if res := env.do(req); res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.Code)
	}
}

// A page cached under one forgery key is not served under another: its form would
// carry a marker nothing replaces. It is rendered again instead — which is what
// lets the key stay out of the cache's namespace, so a site keeps its cache across
// restarts whether or not it has forms.
func TestCSRF_APageStoredUnderAnotherKeyIsRenderedAgain(t *testing.T) {
	shared := cache.NewMemory(cache.MemoryConfig{})
	serve := func(key string) (*fakeEngine, *httptest.ResponseRecorder) {
		guard, err := csrf.New(csrf.Config{Key: []byte(key)})
		if err != nil {
			t.Fatal(err)
		}
		page := testPage("form", "/form", types.StrategyIncremental)
		page.CacheTTL = time.Hour
		engine := newFakeEngine(fakeRender{html: `<input name="_csrf" value="` + guard.Marker() + `">`})
		env := newEnv(t, []*types.Page{page}, func(d *Deps) {
			d.CSRF = guard
			d.Cache = shared
			d.Renderer = engine
		})
		return engine, env.get("/form")
	}

	first, _ := serve("the key before the restart")
	if first.pageCalls("form") != 1 {
		t.Fatalf("first render count = %d", first.pageCalls("form"))
	}
	second, res := serve("the key after the restart")
	if second.pageCalls("form") != 1 {
		t.Errorf("renders under the new key = %d, want 1: the stored page carried the old key's marker", second.pageCalls("form"))
	}
	if strings.Contains(res.Body.String(), "collage-csrf-") {
		t.Errorf("body = %q, still carries a marker", res.Body.String())
	}
}

// The page an action answers with is shaped by AfterRender like any other: a
// validation failure's page is minified and has its images rewritten too.
func TestAction_APageRunsAfterRender(t *testing.T) {
	page := testPage("form", "/form", types.StrategyDynamic)
	recorder := &recordingPlugin{replaceHTML: []byte("<p>shaped</p>")}
	submit := action("submit", "/form", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Status: http.StatusUnprocessableEntity, Page: page}, nil
		})
	env := actionEnv(t, []*types.Page{page}, []*types.Action{submit}, withPlugins(t, recorder))

	res := env.do(post("/form", ""))
	if res.Code != http.StatusUnprocessableEntity || res.Body.String() != "<p>shaped</p>" {
		t.Errorf("status %d body %q, want 422 and the hook's HTML", res.Code, res.Body.String())
	}
}
