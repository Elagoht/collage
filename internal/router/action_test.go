package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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
	if post.Page != nil {
		t.Error("POST reached the page as well; exactly one of them answers a request")
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
