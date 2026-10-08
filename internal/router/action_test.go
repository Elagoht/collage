package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func testAction(name, pattern string, methods ...string) *types.Action {
	return &types.Action{
		Name:    name,
		Paths:   map[string]string{"en": pattern},
		Methods: methods,
		Handler: func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) { return nil, nil },
	}
}

// An action on a page's own URL must carry the page: the page is what a guard
// applies to, and the handler has no other way to reach it.
func TestResolveReturnsOwningPageWithAction(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	page := &types.Page{
		Name:            "p",
		Paths:           map[string]string{"en": "/p"},
		ContentFragment: &types.Fragment{Name: "c", TemplatePath: "c.html"},
	}
	if err := rt.Register(page); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := rt.RegisterAction(testAction("save", "/p", http.MethodPost)); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}
	match, err := rt.Match(httptest.NewRequest(http.MethodPost, "/p", nil))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if match.Action == nil || match.Page != page {
		t.Fatalf("match = action %v page %v, want the action and its owning page", match.Action, match.Page)
	}
	// A standalone action still matches alone: no page, no spine.
	if err := rt.RegisterAction(testAction("hook", "/hook", http.MethodPost)); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}
	match, err = rt.Match(httptest.NewRequest(http.MethodPost, "/hook", nil))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if match.Page != nil {
		t.Fatalf("standalone match = page %v, want nil", match.Page)
	}
}

func TestAction_MatchesItsMethod(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	action := testAction("create", "/posts", http.MethodPost)
	if err := rt.RegisterAction(action); err != nil {
		t.Fatalf("RegisterAction() = %v, want nil", err)
	}

	match, err := rt.Match(httptest.NewRequest(http.MethodPost, "/posts", nil))
	if err != nil {
		t.Fatalf("Match() = %v, want nil", err)
	}
	if match.Action != action {
		t.Fatalf("Action = %v, want the registered action", match.Action)
	}
}

// A path that answers POST and nothing else must say so, with the header that tells
// a caller what it could have asked for.
func TestAction_WrongMethodIsNotAllowed(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	if err := rt.RegisterAction(testAction("create", "/posts", http.MethodPost)); err != nil {
		t.Fatalf("RegisterAction() = %v, want nil", err)
	}

	match, err := rt.Match(httptest.NewRequest(http.MethodDelete, "/posts", nil))
	if err != nil {
		t.Fatalf("Match() = %v, want nil", err)
	}
	if !match.MethodNotAllowed {
		t.Fatal("MethodNotAllowed = false, want true")
	}
	if match.IsNotFound {
		t.Error("IsNotFound = true: the path exists, the method does not")
	}
	if !slices.Contains(match.Allowed, http.MethodPost) {
		t.Errorf("Allowed = %v, want it to contain POST", match.Allowed)
	}
	if !slices.Contains(match.Allowed, http.MethodOptions) {
		t.Errorf("Allowed = %v, want OPTIONS: every path answers it", match.Allowed)
	}
}

// A page and an action can share one URL: that is what an HTML form needs, since a
// form's action is the page it is on.
func TestAction_SharesAPathWithItsPage(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	page := &types.Page{Name: "new-post", Paths: map[string]string{"en": "/posts/new"}}
	if err := rt.Register(page); err != nil {
		t.Fatalf("Register() = %v, want nil", err)
	}
	action := testAction("create", "/posts/new", http.MethodPost)
	if err := rt.RegisterAction(action); err != nil {
		t.Fatalf("RegisterAction() = %v, want nil", err)
	}

	get, err := rt.Match(httptest.NewRequest(http.MethodGet, "/posts/new", nil))
	if err != nil {
		t.Fatalf("Match(GET) = %v, want nil", err)
	}
	if get.Page != page {
		t.Error("GET did not reach the page")
	}
	if get.Action != nil {
		t.Error("GET reached the action; the action declares only POST")
	}

	post, err := rt.Match(httptest.NewRequest(http.MethodPost, "/posts/new", nil))
	if err != nil {
		t.Fatalf("Match(POST) = %v, want nil", err)
	}
	if post.Action != action {
		t.Error("POST did not reach the action")
	}
	if post.Page != page {
		t.Error("POST did not carry the owning page; a guard has no other way to reach it")
	}

	allowed, err := rt.Match(httptest.NewRequest(http.MethodOptions, "/posts/new", nil))
	if err != nil {
		t.Fatalf("Match(OPTIONS) = %v, want nil", err)
	}
	for _, want := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions} {
		if !slices.Contains(allowed.Allowed, want) {
			t.Errorf("Allowed = %v, want it to contain %s", allowed.Allowed, want)
		}
	}
}

// A page answers GET and HEAD. Anything else is a 405 rather than a rendered page:
// a POST that silently renders is a form submission the application never saw.
func TestAction_PageAloneRefusesUnsafeMethods(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	if err := rt.Register(&types.Page{Name: "about", Paths: map[string]string{"en": "/about"}}); err != nil {
		t.Fatalf("Register() = %v, want nil", err)
	}

	match, err := rt.Match(httptest.NewRequest(http.MethodPost, "/about", nil))
	if err != nil {
		t.Fatalf("Match() = %v, want nil", err)
	}
	if !match.MethodNotAllowed {
		t.Fatal("POST to a page with no action was not refused")
	}
	if slices.Contains(match.Allowed, http.MethodPost) {
		t.Errorf("Allowed = %v, want it not to contain POST", match.Allowed)
	}
}

func TestAction_TwoActionsCannotClaimOneMethod(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	if err := rt.RegisterAction(testAction("first", "/posts", http.MethodPost)); err != nil {
		t.Fatalf("RegisterAction() = %v, want nil", err)
	}
	err := rt.RegisterAction(testAction("second", "/posts", http.MethodPost))
	if err == nil {
		t.Fatal("RegisterAction() = nil, want a duplicate-route error")
	}
}

// Two actions may share a path when they answer different methods: a REST-shaped
// resource is exactly that.
func TestAction_DifferentMethodsShareAPath(t *testing.T) {
	rt := New(LocaleOptions{Default: "en"})
	if err := rt.RegisterAction(testAction("update", "/posts/{slug}", http.MethodPut)); err != nil {
		t.Fatalf("RegisterAction(PUT) = %v, want nil", err)
	}
	if err := rt.RegisterAction(testAction("remove", "/posts/{slug}", http.MethodDelete)); err != nil {
		t.Fatalf("RegisterAction(DELETE) = %v, want nil", err)
	}

	match, err := rt.Match(httptest.NewRequest(http.MethodDelete, "/posts/hello", nil))
	if err != nil {
		t.Fatalf("Match() = %v, want nil", err)
	}
	if match.Action == nil || match.Action.Name != "remove" {
		t.Fatalf("Action = %v, want the DELETE action", match.Action)
	}
	if match.PathParams["slug"] != "hello" {
		t.Errorf("PathParams[slug] = %q, want %q", match.PathParams["slug"], "hello")
	}
}

// An action answering GET where a page or document is read would be matched first
// and hide it without a word. Refused in either order, for either method that reads.
func TestAction_CannotAnswerGetWhereAPageIsRead(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		occupant func(rt Router) error
	}{
		{"page GET", http.MethodGet, func(rt Router) error {
			return rt.Register(&types.Page{Name: "about", Paths: map[string]string{"en": "/about"}})
		}},
		{"page HEAD", http.MethodHead, func(rt Router) error {
			return rt.Register(&types.Page{Name: "about", Paths: map[string]string{"en": "/about"}})
		}},
		{"document GET", http.MethodGet, func(rt Router) error {
			return rt.RegisterDocument(testDocument("about", "/about"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+", occupant first", func(t *testing.T) {
			rt := New(LocaleOptions{Default: "en"})
			if err := tc.occupant(rt); err != nil {
				t.Fatalf("occupant: %v", err)
			}
			err := rt.RegisterAction(testAction("home:cpu", "/about", tc.method))
			if !errors.Is(err, ErrDuplicateRoute) {
				t.Fatalf("RegisterAction() = %v, want ErrDuplicateRoute", err)
			}
		})
		t.Run(tc.name+", action first", func(t *testing.T) {
			rt := New(LocaleOptions{Default: "en"})
			if err := rt.RegisterAction(testAction("home:cpu", "/about", tc.method)); err != nil {
				t.Fatalf("RegisterAction: %v", err)
			}
			if err := tc.occupant(rt); !errors.Is(err, ErrDuplicateRoute) {
				t.Fatalf("occupant = %v, want ErrDuplicateRoute", err)
			}
		})
	}
}

// OPTIONS, TRACE and CONNECT are the server's own: the router answers OPTIONS
// with the Allow list, and an action declaring one would be skipped by the
// forgery check, since SafeMethod counts OPTIONS as safe.
func TestRegisterAction_RefusesServerMethods(t *testing.T) {
	for _, method := range []string{http.MethodOptions, http.MethodTrace, http.MethodConnect} {
		t.Run(method, func(t *testing.T) {
			rt := New(LocaleOptions{Default: "en"})
			err := rt.RegisterAction(testAction("preflight", "/api", http.MethodPost, method))
			if !errors.Is(err, ErrInvalidActionMethod) {
				t.Fatalf("RegisterAction(%s) = %v, want ErrInvalidActionMethod", method, err)
			}
			for _, want := range []string{`"preflight"`, method} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
			// Refused before anything was written into the tree: the POST it
			// also declared is not left behind, half registered.
			if match, err := rt.Match(httptest.NewRequest(http.MethodPost, "/api", nil)); err == nil && match.Action != nil {
				t.Errorf("POST /api matched action %q after the refused registration", match.Action.Name)
			}
		})
	}
}
