package collage_test

// HEAD never runs an action's handler unless the action declares HEAD. A GET
// action is a state change with no forgery check by design, so a HEAD that ran
// it too was a second way to trigger it; it is answered 405 with the Allow
// header the action's methods give. A fragment path, which is a read, still
// answers HEAD.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

func countingAction(name, path string, runs *atomic.Int32, methods ...string) *collage.Action {
	return collage.NewAction(name).WithPath("en", path).WithMethods(methods...).
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			runs.Add(1)
			return nil, nil
		}).Build()
}

func TestAction_HeadDoesNotRunAGetAction(t *testing.T) {
	var runs atomic.Int32
	site := collagetest.NewSite(t, collagetest.WithRegister(
		countingAction("ping", "/ping", &runs, http.MethodGet)))

	res := site.Do(httptest.NewRequest(http.MethodHead, "/ping", nil))
	if res.Status != http.StatusMethodNotAllowed {
		t.Errorf("HEAD status = %d, want 405", res.Status)
	}
	if allow := res.Header.Get("Allow"); allow != "GET, OPTIONS" {
		t.Errorf("Allow = %q, want %q", allow, "GET, OPTIONS")
	}
	if n := runs.Load(); n != 0 {
		t.Fatalf("handler ran %d times for HEAD", n)
	}

	opts := site.Do(httptest.NewRequest(http.MethodOptions, "/ping", nil))
	if allow := opts.Header.Get("Allow"); allow != "GET, OPTIONS" {
		t.Errorf("OPTIONS Allow = %q, want %q", allow, "GET, OPTIONS")
	}

	site.Get("/ping").WantStatus(http.StatusNoContent)
	if n := runs.Load(); n != 1 {
		t.Errorf("handler ran %d times for one GET", n)
	}
}

// An action that declares HEAD itself is run for it.
func TestAction_HeadRunsAnActionDeclaringIt(t *testing.T) {
	var runs atomic.Int32
	site := collagetest.NewSite(t, collagetest.WithRegister(
		countingAction("ping", "/ping", &runs, http.MethodGet, http.MethodHead)))
	res := site.Do(httptest.NewRequest(http.MethodHead, "/ping", nil))
	if res.Status != http.StatusNoContent || runs.Load() != 1 {
		t.Errorf("HEAD status = %d, runs = %d; want 204, 1", res.Status, runs.Load())
	}
}

// A page's fragment path is a read, like the page: HEAD answers it.
func TestAction_HeadStillAnswersAFragmentPath(t *testing.T) {
	box := collage.NewFragment("box", "box.html").Build()
	page := collage.NewPage("p").WithContent(box).WithPath("en", "/p").
		WithFragmentPath("en", "/p/box", box).Build()
	site := collagetest.NewSite(t,
		collagetest.WithTemplates(map[string]string{"box.html": `<b>box</b>`}),
		collagetest.WithRegister(page))
	res := site.Do(httptest.NewRequest(http.MethodHead, "/p/box", nil))
	if res.Status != http.StatusOK {
		t.Errorf("HEAD /p/box = %d, want 200", res.Status)
	}
}

// Registering an action whose only methods are safe, in development, says it
// changes state with no forgery check.
func TestAction_GetOnlyActionIsWarnedInDevelopment(t *testing.T) {
	var runs atomic.Int32
	site := collagetest.NewSite(t,
		collagetest.WithConfig(func(cfg *collage.Config) { cfg.DevMode = true }),
		collagetest.WithRegister(countingAction("ping", "/ping", &runs, http.MethodGet)))
	if !site.Logs.Has(slog.LevelWarn, "forgery", "ping") {
		t.Errorf("no warning for a GET-only action; logs:\n%s", site.Logs)
	}

	quiet := collagetest.NewSite(t,
		collagetest.WithRegister(countingAction("ping", "/ping", &runs, http.MethodGet)))
	if quiet.Logs.Has(slog.LevelWarn, "forgery", "ping") {
		t.Errorf("warned outside development:\n%s", quiet.Logs)
	}

	fragments := collage.NewFragment("box", "box.html").Build()
	page := collage.NewPage("p").WithContent(fragments).WithPath("en", "/p").
		WithFragmentPath("en", "/p/box", fragments).Build()
	dev := collagetest.NewSite(t,
		collagetest.WithConfig(func(cfg *collage.Config) { cfg.DevMode = true }),
		collagetest.WithTemplates(map[string]string{"box.html": `<b>box</b>`}),
		collagetest.WithRegister(page))
	if dev.Logs.Has(slog.LevelWarn, "forgery") {
		t.Errorf("a fragment path was warned about:\n%s", dev.Logs)
	}
}
