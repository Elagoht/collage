package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
