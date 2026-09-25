package collage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// strategyApp is an application over a layout that hoists its head, and a page
// template that prints its data, with a cache on so a static page is visibly
// cached.
func strategyApp(t *testing.T) *App {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"layouts/default.html": `<head>{{hoist "head"}}</head><main>{{slot "content"}}</main><aside>{{slot "aside"}}</aside>`,
		"pages/data.html":      `<h1>{{.}}</h1>`,
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	app, err := New(&Config{
		Template: TemplateConfig{Root: root},
		Cache:    CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func strategyLayout() *FragmentBuilder {
	return NewFragment("layout", "layouts/default.html").WithSlot("content", true, false)
}

// TestAutoStrategy_FixedPageIsStatic: a page that declares no strategy and renders
// only fixed values — WithData, WithTitle — resolves to static, is served with a
// static page's Cache-Control, and renders what it was given.
func TestAutoStrategy_FixedPageIsStatic(t *testing.T) {
	app := strategyApp(t)
	page := NewPage("fixed").
		WithLayout(strategyLayout().WithTitle("Site").Build()).
		WithContent(NewFragment("content", "pages/data.html").WithData("hello").Build()).
		WithPath("en", "/").
		Build()
	if page.Strategy != StrategyAuto {
		t.Fatalf("built strategy = %v, want auto until registration", page.Strategy)
	}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if page.Strategy != StrategyStatic {
		t.Fatalf("registered strategy = %v, want static", page.Strategy)
	}

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "<title>Site</title>") || !strings.Contains(body, "<h1>hello</h1>") {
		t.Fatalf("body = %q, want the fixed title and data", body)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=0, must-revalidate" {
		t.Errorf("Cache-Control = %q, want a static page's", got)
	}
}

// TestAutoStrategy_HandlerMakesDynamic: a handler anywhere the page renders — the
// layout, the content, something bound into a slot — may read the request, so a page
// that declared nothing is dynamic. A resolver is a handler for the same purpose.
func TestAutoStrategy_HandlerMakesDynamic(t *testing.T) {
	effect := Effect(func(context.Context, *RenderContext) error { return nil })
	tests := map[string]func() *Page{
		"handler in the content": func() *Page {
			return NewPage("p").WithLayout(strategyLayout().Build()).
				WithContent(NewFragment("content", "pages/data.html").WithDataHandler(effect).Build()).
				WithPath("en", "/").Build()
		},
		"handler in the layout": func() *Page {
			return NewPage("p").WithLayout(strategyLayout().WithDataHandler(effect).Build()).
				WithContent(NewFragment("content", "pages/data.html").Build()).
				WithPath("en", "/").Build()
		},
		"handler in a bound child": func() *Page {
			child := NewFragment("child", "pages/data.html").WithDataHandler(effect).Build()
			layout := strategyLayout().WithSlot("aside", false, false).WithSlotFragment("aside", child).Build()
			return NewPage("p").WithLayout(layout).
				WithContent(NewFragment("content", "pages/data.html").Build()).
				WithPath("en", "/").Build()
		},
		"slot resolver": func() *Page {
			layout := strategyLayout().WithSlot("aside", false, true).
				WithSlotResolver("aside", func(*RenderContext) ([]*Fragment, error) { return nil, nil }).
				Build()
			return NewPage("p").WithLayout(layout).
				WithContent(NewFragment("content", "pages/data.html").Build()).
				WithPath("en", "/").Build()
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			app := strategyApp(t)
			page := build()
			if err := app.RegisterPage(page); err != nil {
				t.Fatalf("RegisterPage: %v", err)
			}
			if page.Strategy != StrategyDynamic {
				t.Errorf("registered strategy = %v, want dynamic", page.Strategy)
			}
		})
	}
}

// TestAutoStrategy_DeclaredStrategyIsKept: a strategy the page declared is never
// second-guessed, in either direction.
func TestAutoStrategy_DeclaredStrategyIsKept(t *testing.T) {
	app := strategyApp(t)
	fixed := NewPage("fixed").WithLayout(strategyLayout().Build()).
		WithContent(NewFragment("content", "pages/data.html").Build()).
		WithPath("en", "/fixed").Dynamic().Build()
	handled := NewPage("handled").WithLayout(strategyLayout().Build()).
		WithContent(NewFragment("content", "pages/data.html").
			WithDataHandler(Load(func(context.Context, *RenderContext) (string, error) { return "x", nil })).Build()).
		WithPath("en", "/handled").Static().Build()
	for _, page := range []*Page{fixed, handled} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage(%s): %v", page.Name, err)
		}
	}
	if fixed.Strategy != StrategyDynamic || handled.Strategy != StrategyStatic {
		t.Errorf("strategies = %v, %v; want dynamic, static as declared", fixed.Strategy, handled.Strategy)
	}
}

// TestFragment_DataAndHandlerConflict: one of the two would be ignored, so setting
// both is refused at registration rather than resolved by a rule nobody reads.
func TestFragment_DataAndHandlerConflict(t *testing.T) {
	app := strategyApp(t)
	content := NewFragment("content", "pages/data.html").
		WithData("fixed").
		WithDataHandler(Load(func(context.Context, *RenderContext) (string, error) { return "fetched", nil })).
		Build()
	page := NewPage("p").WithLayout(strategyLayout().Build()).WithContent(content).WithPath("en", "/").Build()
	if err := app.RegisterPage(page); !errors.Is(err, ErrConflictingData) {
		t.Fatalf("RegisterPage = %v, want ErrConflictingData", err)
	}
}

// TestFragment_HandlerTitleReplacesFixedTitle: a fragment's fixed title is declared
// before its handler runs, so a handler of the same fragment hoisting a title — a
// post naming itself — replaces it.
func TestFragment_HandlerTitleReplacesFixedTitle(t *testing.T) {
	app := strategyApp(t)
	content := NewFragment("content", "pages/data.html").
		WithTitle("Fixed").
		WithDataHandler(Effect(func(_ context.Context, rc *RenderContext) error {
			rc.HoistTitle("From handler")
			return nil
		})).
		Build()
	page := NewPage("p").WithLayout(strategyLayout().WithTitle("Site").Build()).WithContent(content).WithPath("en", "/").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if body := recorder.Body.String(); !strings.Contains(body, "<title>From handler</title>") || strings.Count(body, "<title>") != 1 {
		t.Fatalf("body = %q, want the handler's title alone", body)
	}
}

// TestAutoStrategy_Documents: a document's handler is all there is to look at, so a
// handler makes it dynamic and a fixed body static.
func TestAutoStrategy_Documents(t *testing.T) {
	app := strategyApp(t)
	robots := NewDocument("robots", "text/plain").AtRoot("/robots.txt").
		WithBody([]byte("User-agent: *\n")).Build()
	health := NewDocument("health", "application/json").AtRoot("/healthz").
		WithHandler(func(context.Context, *RenderContext) ([]byte, []string, error) { return []byte("{}"), nil, nil }).
		Build()
	for _, doc := range []*Document{robots, health} {
		if err := app.RegisterDocument(doc); err != nil {
			t.Fatalf("RegisterDocument(%s): %v", doc.Name, err)
		}
	}
	if robots.Strategy != StrategyStatic || health.Strategy != StrategyDynamic {
		t.Fatalf("strategies = %v, %v; want static, dynamic", robots.Strategy, health.Strategy)
	}

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "User-agent: *\n" {
		t.Fatalf("robots.txt = %d %q, want the fixed body", recorder.Code, recorder.Body.String())
	}
}

// TestDocument_BodyAndHandlerConflict mirrors the fragment's rule.
func TestDocument_BodyAndHandlerConflict(t *testing.T) {
	app := strategyApp(t)
	doc := NewDocument("robots", "text/plain").AtRoot("/robots.txt").
		WithBody([]byte("x")).
		WithHandler(func(context.Context, *RenderContext) ([]byte, []string, error) { return []byte("y"), nil, nil }).
		Build()
	if err := app.RegisterDocument(doc); !errors.Is(err, ErrConflictingData) {
		t.Fatalf("RegisterDocument = %v, want ErrConflictingData", err)
	}
}

// TestSlots_NeedNoDeclaring: a layout renders a page's content, and a child bound
// into a slot its template calls, with no WithSlot anywhere.
func TestSlots_NeedNoDeclaring(t *testing.T) {
	app := strategyApp(t)
	layout := NewFragment("layout", "layouts/default.html").
		WithSlotFragment("aside", NewFragment("note", "pages/data.html").WithData("note").Build()).
		Build()
	page := NewPage("p").WithLayout(layout).
		WithContent(NewFragment("content", "pages/data.html").WithData("content").Build()).
		WithPath("en", "/").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if body := recorder.Body.String(); !strings.Contains(body, "<main><h1>content</h1></main><aside><h1>note</h1></aside>") {
		t.Fatalf("body = %q, want the content and the note in their slots", body)
	}
}

// TestWithSlot_ConstrainsInEitherOrder: WithSlot is what makes a slot required or
// single, and it may come after the binding it constrains.
func TestWithSlot_ConstrainsInEitherOrder(t *testing.T) {
	child := NewFragment("child", "child.html").Build()

	after := NewFragment("a", "a.html").WithSlotFragment("s", child).WithSlot("s", true, false)
	if err := after.BuildErr(); err != nil {
		t.Fatalf("WithSlot after a binding: BuildErr() = %v, want nil", err)
	}
	if slot, _ := after.Build().Slot("s"); !slot.Required || slot.AllowMultiple || len(slot.Fill) != 1 {
		t.Errorf("slot = %+v, want required, single, and still holding its fill", slot)
	}

	single := NewFragment("b", "b.html").WithSlot("s", false, false).WithSlotFragment("s", child).WithSlotFragment("s", child)
	if err := single.BuildErr(); !errors.Is(err, ErrSlotOccupied) {
		t.Errorf("second fill of a single slot: BuildErr() = %v, want ErrSlotOccupied", err)
	}

	twice := NewFragment("c", "c.html").WithSlotFragment("s", child).WithSlot("s", false, true).WithSlot("s", true, true)
	if err := twice.BuildErr(); !errors.Is(err, ErrDuplicateSlot) {
		t.Errorf("WithSlot twice: BuildErr() = %v, want ErrDuplicateSlot", err)
	}
}
