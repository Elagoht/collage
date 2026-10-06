package collage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
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
		"pages/slotted.html":   `<div>{{slot "x"}}</div>`,
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
		WithLayouts(strategyLayout().WithTitle("Site").Build()).
		WithContent(NewFragment("content", "pages/data.html").WithData(Value("hello")).Build()).
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
			return NewPage("p").WithLayouts(strategyLayout().Build()).
				WithContent(NewFragment("content", "pages/data.html").WithData(effect).Build()).
				WithPath("en", "/").Build()
		},
		"handler in the layout": func() *Page {
			return NewPage("p").WithLayouts(strategyLayout().WithData(effect).Build()).
				WithContent(NewFragment("content", "pages/data.html").Build()).
				WithPath("en", "/").Build()
		},
		"handler in a bound child": func() *Page {
			child := NewFragment("child", "pages/data.html").WithData(effect).Build()
			layout := strategyLayout().WithSlot("aside", false, false).WithSlotFragment("aside", child).Build()
			return NewPage("p").WithLayouts(layout).
				WithContent(NewFragment("content", "pages/data.html").Build()).
				WithPath("en", "/").Build()
		},
		"slot resolver": func() *Page {
			layout := strategyLayout().WithSlot("aside", false, true).
				WithSlotResolver("aside", func(*RenderContext) ([]*Fragment, error) { return nil, nil }).
				Build()
			return NewPage("p").WithLayouts(layout).
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
	fixed := NewPage("fixed").WithLayouts(strategyLayout().Build()).
		WithContent(NewFragment("content", "pages/data.html").Build()).
		WithPath("en", "/fixed").Dynamic().Build()
	handled := NewPage("handled").WithLayouts(strategyLayout().Build()).
		WithContent(NewFragment("content", "pages/data.html").WithData(Load(
			func(context.Context, *RenderContext) (string, error) { return "x", nil })).
			Build()).
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
	content := NewFragment("content", "pages/data.html").WithData(Value(
		"fixed")).WithData(Load(

		func(context.Context, *RenderContext) (string, error) { return "fetched", nil })).
		Build()
	page := NewPage("p").WithLayouts(strategyLayout().Build()).WithContent(content).WithPath("en", "/").Build()
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
		WithTitle("Fixed").WithData(Effect(

		func(_ context.Context, rc *RenderContext) error {
			rc.HoistTitle("From handler")
			return nil
		})).
		Build()
	page := NewPage("p").WithLayouts(strategyLayout().WithTitle("Site").Build()).WithContent(content).WithPath("en", "/").Build()
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
		WithSlotFragment("aside", NewFragment("note", "pages/data.html").WithData(Value("note")).Build()).
		Build()
	page := NewPage("p").WithLayouts(layout).
		WithContent(NewFragment("content", "pages/data.html").WithData(Value("content")).Build()).
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

// TestAutoStrategy_StaticFragment: a fragment that states its handler is the same
// for every request to one URL does not make a page dynamic — so a shared fragment
// with data fixed per URL leaves every page that uses it static. It is the
// fragment's promise alone: another fragment's handler, or a resolver on the
// static fragment, still makes the page dynamic, and a declared Dynamic() is kept.
func TestAutoStrategy_StaticFragment(t *testing.T) {
	load := Load(func(_ context.Context, rc *RenderContext) (string, error) { return "more:" + rc.Param("slug"), nil })
	shared := func() *Fragment {
		return NewFragment("more", "pages/data.html").WithData(load).Static().Build()
	}
	tests := map[string]struct {
		page func() *Page
		want RenderStrategy
	}{
		"static fragment alone": {
			page: func() *Page {
				return NewPage("p").WithLayouts(strategyLayout().WithSlotFragment("aside", shared()).Build()).
					WithContent(NewFragment("content", "pages/data.html").WithData(Value("home")).Build()).
					WithPath("en", "/").Build()
			},
			want: StrategyStatic,
		},
		"beside another fragment's handler": {
			page: func() *Page {
				return NewPage("p").WithLayouts(strategyLayout().WithSlotFragment("aside", shared()).Build()).
					WithContent(NewFragment("content", "pages/data.html").WithData(load).Build()).
					WithPath("en", "/").Build()
			},
			want: StrategyDynamic,
		},
		"with a resolver of its own": {
			page: func() *Page {
				more := NewFragment("more", "pages/slotted.html").WithData(load).Static().
					WithSlotResolver("x", func(*RenderContext) ([]*Fragment, error) { return nil, nil }).Build()
				return NewPage("p").WithLayouts(strategyLayout().WithSlotFragment("aside", more).Build()).
					WithContent(NewFragment("content", "pages/data.html").Build()).
					WithPath("en", "/").Build()
			},
			want: StrategyDynamic,
		},
		"page declared dynamic": {
			page: func() *Page {
				return NewPage("p").WithLayouts(strategyLayout().WithSlotFragment("aside", shared()).Build()).
					WithContent(NewFragment("content", "pages/data.html").Build()).
					WithPath("en", "/").Dynamic().Build()
			},
			want: StrategyDynamic,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			app := strategyApp(t)
			page := tt.page()
			if err := app.RegisterPage(page); err != nil {
				t.Fatalf("RegisterPage: %v", err)
			}
			if page.Strategy != tt.want {
				t.Errorf("registered strategy = %v, want %v", page.Strategy, tt.want)
			}
		})
	}
}

// TestFragment_DataSetTwiceConflicts: a second WithData is as much a conflict as a
// WithData followed by a handler.
func TestFragment_DataSetTwiceConflicts(t *testing.T) {
	app := strategyApp(t)
	content := NewFragment("content", "pages/data.html").WithData(Value("a")).WithData(Value("b")).Build()
	page := NewPage("p").WithLayouts(strategyLayout().Build()).WithContent(content).WithPath("en", "/").Build()
	if err := app.RegisterPage(page); !errors.Is(err, ErrConflictingData) {
		t.Fatalf("RegisterPage = %v, want ErrConflictingData", err)
	}
}

// TestFragment_NilDataIsNoData: a nil handler or nil value is "unset", as before: it
// neither conflicts with data already set nor replaces it.
func TestFragment_NilDataIsNoData(t *testing.T) {
	h := Load(func(context.Context, *RenderContext) (string, error) { return "x", nil })
	cases := map[string]*FragmentBuilder{
		"handler then nil handler": NewFragment("a", "pages/data.html").WithData(h).WithData(Load[string](nil)),
		"handler then nil data":    NewFragment("b", "pages/data.html").WithData(h).WithData(Value[error](nil)),
		"data then nil handler":    NewFragment("c", "pages/data.html").WithData(Value("v")).WithData(nil),
	}
	wantKind := map[string]types.DataKind{
		"handler then nil handler": types.DataFetched,
		"handler then nil data":    types.DataFetched,
		"data then nil handler":    types.DataFixed,
	}
	for name, b := range cases {
		if len(b.errs) != 0 {
			t.Errorf("%s: recorded %v, want no conflict", name, b.errs)
		}
		if got := b.fragment.DataSource().Kind; got != wantKind[name] {
			t.Errorf("%s: kind = %v, want %v (existing source kept)", name, got, wantKind[name])
		}
	}
}

// TestFragment_WithDataRecordsType: the template's dot type is the value's own type.
func TestFragment_WithDataRecordsType(t *testing.T) {
	type view struct{ N int }
	f := NewFragment("v", "pages/data.html").WithData(Value(view{N: 1})).Build()
	if got := f.DataSource().Type; got != reflect.TypeOf(view{}) {
		t.Errorf("Type = %v, want %v", got, reflect.TypeOf(view{}))
	}
}
