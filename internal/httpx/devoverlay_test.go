package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// degradedEngine renders every page, with one fragment failed and covered.
type degradedEngine struct{}

func (degradedEngine) Render(_ context.Context, rc *types.RenderContext) (*render.Result, error) {
	return &render.Result{
		HTML: []byte("<html><body><p>page</p></body></html>"),
		Metadata: &render.Metadata{Page: rc.Page.Name, Fragments: []render.FragmentMetadata{{
			Name: "sidebar", Failed: true, UsedFallback: true,
			Err: errors.New(`template: sidebar.html:12:3: executing "sidebar.html" <.Missing>: can't evaluate field Missing`),
		}}},
	}, nil
}

func (degradedEngine) RenderFragment(context.Context, *types.RenderContext, *types.Fragment) ([]byte, error) {
	return nil, nil
}

// A page that rendered with a broken part says so, in development, on top of
// itself: the fragment, and the error with the template's file and line.
func TestDevOverlay_OnADegradedPage(t *testing.T) {
	page := testPage("home", "/", types.StrategyDynamic)
	engine := func(d *Deps) { d.Renderer = degradedEngine{} }

	body := newEnv(t, []*types.Page{page}, withDevMode(), engine).get("/").Body.String()
	for _, want := range []string{"collage-dev-overlay", "sidebar", "sidebar.html:12:3", "its fallback rendered"} {
		if !strings.Contains(body, want) {
			t.Errorf("development page does not contain %q:\n%s", want, body)
		}
	}
	if i, j := strings.Index(body, "collage-dev-overlay"), strings.Index(body, "</body>"); i > j {
		t.Errorf("overlay placed after </body>:\n%s", body)
	}

	if body := newEnv(t, []*types.Page{page}, engine).get("/").Body.String(); strings.Contains(body, "collage-dev-overlay") || strings.Contains(body, "sidebar.html") {
		t.Errorf("production page carries the overlay or the error:\n%s", body)
	}
}

// The application's own error page keeps its look, and in development gets the
// reason on top — which the built-in page shows and a custom one never did.
func TestDevOverlay_OnTheApplicationsErrorPage(t *testing.T) {
	custom := errorOnlyPage("oops")
	home := testPage("home", "/", types.StrategyStatic)
	home.ErrorPage = custom

	env := newEnv(t, []*types.Page{home}, withDevMode())
	env.engine.set("home", fakeRender{err: errLeaky, degraded: "secret-fragment"})
	env.engine.set("oops", fakeRender{html: "<html><body><h1>Something broke</h1></body></html>"})

	res := env.get("/")
	body := res.Body.String()
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	for _, want := range []string{"Something broke", "collage-dev-overlay", "secret-fragment", "dial tcp"} {
		if !strings.Contains(body, want) {
			t.Errorf("development error page does not contain %q:\n%s", want, body)
		}
	}
}

// A fatal failure is recorded against every fragment it passes through; the one
// named is where it started, not the layout that carried it up.
func TestFailedFragment_NamesWhereItStarted(t *testing.T) {
	cause := errors.New("template: recipe.html:3: can't evaluate field Missing")
	inContent := fmt.Errorf("fragment %q: %w", "recipe-content", cause)
	inLayout := fmt.Errorf("fragment %q: %w", "layout", inContent)
	result := &render.Result{Metadata: &render.Metadata{Fragments: []render.FragmentMetadata{
		{Name: "layout", Failed: true, Err: inLayout},
		{Name: "header"},
		{Name: "recipe-content", Failed: true, Err: inContent},
	}}}
	if got := failedFragment(result); got != "recipe-content" {
		t.Errorf("failedFragment = %q, want recipe-content", got)
	}
}

// The overlay opens as a dialog in the middle of the page, can be minimized to a
// button at the bottom right that counts what it holds, and closed, and is the
// last thing in the body so it sits above whatever a plugin put there first.
func TestDevOverlay_IsADialogThatMinimizes(t *testing.T) {
	page := []byte(`<html><body><p>page</p><div id="toolbar"></div></body></html>`)
	problems := []devProblem{
		{fragment: "sidebar", detail: "boom"},
		{finding: &types.Finding{Rule: "one-h1", Plugin: "test/checker"}, detail: "no h1"},
		{finding: &types.Finding{Rule: "meta", Plugin: "test/checker"}, detail: "no meta"},
	}
	body := string(withDevOverlay(page, overlayHeading(problems), problems))

	for _, want := range []string{`data-state="open"`, `role="alertdialog"`, `aria-label="Minimize"`, `aria-label="Close"`, `class="cdo-fab"`, "1 failure · 2 findings"} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay does not contain %q:\n%s", want, body)
		}
	}
	if !strings.HasSuffix(body, "</button></div></body></html>") || strings.Index(body, "toolbar") > strings.Index(body, "collage-dev-overlay") {
		t.Errorf("overlay is not the last element of the body:\n%s", body)
	}
}

func TestFabLabel(t *testing.T) {
	for _, c := range []struct {
		failures, findings int
		want               string
	}{{1, 0, "1 failure"}, {0, 1, "1 finding"}, {2, 3, "2 failures · 3 findings"}} {
		if got := fabLabel(c.failures, c.findings); got != c.want {
			t.Errorf("fabLabel(%d, %d) = %q, want %q", c.failures, c.findings, got, c.want)
		}
	}
}
