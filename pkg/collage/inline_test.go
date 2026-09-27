package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

func inlineApp(t *testing.T, dev bool) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:  collage.ServerConfig{Host: "localhost", Port: 0},
		DevMode: dev,
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"templates/layouts/default.html": {Data: []byte(`<html><head>{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`)},
				"templates/partials/name.html":   {Data: []byte(`<b>{{.}}</b>`)},
			},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Mount("/static/", fstest.MapFS{"row.css": {Data: []byte("tr{}")}}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func inlineBody(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestInlineFragment_RendersInAFileLayout(t *testing.T) {
	app := inlineApp(t, false)
	row := collage.NewInlineFragment("row", `{{stylesheet "/static/row.css"}}<p>{{template "partials/name.html" .}}</p>`).
		WithData("Ada").Build()
	layout := collage.NewFragment("layout", "layouts/default.html").Build()
	if err := app.RegisterPage(collage.NewPage("home").WithLayouts(layout).WithContent(row).WithPath("en", "/").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	got := inlineBody(t, app.Handler(), "/")
	head, _, _ := strings.Cut(got, "</head>")
	if !strings.Contains(got, "<p><b>Ada</b></p>") || !strings.Contains(head, "/static/row") {
		t.Fatalf("body = %q, want the partial rendered and the stylesheet hoisted into the head", got)
	}
}

func TestInlineFragment_WithASlot(t *testing.T) {
	app := inlineApp(t, false)
	child := collage.NewInlineFragment("child", `<i>c</i>`).Build()
	parent := collage.NewInlineFragment("parent", `<div>{{slot "items"}}</div>`).
		WithSlotFragment("items", child).Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(parent).WithPath("en", "/p").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if got := inlineBody(t, app.Handler(), "/p"); got != "<div><i>c</i></div>" {
		t.Fatalf("body = %q", got)
	}
}

func TestInlineFragment_SameNameDifferentSources(t *testing.T) {
	app := inlineApp(t, false)
	for _, page := range []string{"a", "b"} {
		frag := collage.NewInlineFragment("card", "<p>"+page+"</p>").Build()
		if err := app.RegisterPage(collage.NewPage(page).WithContent(frag).WithPath("en", "/"+page).Build()); err != nil {
			t.Fatalf("RegisterPage(%s): %v", page, err)
		}
	}
	if a, b := inlineBody(t, app.Handler(), "/a"), inlineBody(t, app.Handler(), "/b"); a != "<p>a</p>" || b != "<p>b</p>" {
		t.Fatalf("a = %q, b = %q", a, b)
	}
}

func TestInlineFragment_DevModeReloads(t *testing.T) {
	app := inlineApp(t, true)
	frag := collage.NewInlineFragment("x", `<p>x</p>`).Build()
	if err := app.RegisterPage(collage.NewPage("x").WithContent(frag).WithPath("en", "/x").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	for i := 0; i < 2; i++ {
		if got := inlineBody(t, app.Handler(), "/x"); !strings.Contains(got, "<p>x</p>") {
			t.Fatalf("dev render %d = %q", i, got)
		}
	}
}

func TestInlineFragment_ParseErrorNamesPageAndFragment(t *testing.T) {
	app := inlineApp(t, false)
	frag := collage.NewInlineFragment("broken-row", `{{.Nope`).Build()
	err := app.RegisterPage(collage.NewPage("list").WithContent(frag).WithPath("en", "/l").Build())
	if err == nil || !strings.Contains(err.Error(), `page "list"`) || !strings.Contains(err.Error(), `fragment "broken-row"`) {
		t.Fatalf("RegisterPage = %v, want an error naming page and fragment", err)
	}
	if strings.Contains(err.Error(), "inline:broken-row#") {
		t.Fatalf("RegisterPage = %v, want no engine-internal template name in it", err)
	}
}

// The development overlay leads with where a template failed; for an inline
// template that is the fragment, not a hash nobody wrote.
func TestInlineFragment_DevOverlayNamesTheFragment(t *testing.T) {
	app := inlineApp(t, true)
	bad := collage.NewInlineFragment("bad", `<p>{{.Nope.X}}</p>`).WithData("s").Build()
	if err := app.RegisterPage(collage.NewPage("b").WithContent(bad).WithPath("en", "/b").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/b", nil))
	page := w.Body.String()
	if !strings.Contains(page, "collage-dev-overlay") {
		t.Fatalf("no dev overlay in %q", page)
	}
	if strings.Contains(page, "inline:bad#") || !strings.Contains(page, "inline template of fragment") {
		t.Fatalf("overlay = %q, want the fragment named and no engine-internal template name", page)
	}
}

func TestInlineFragment_UnknownSlot(t *testing.T) {
	app := inlineApp(t, false)
	frag := collage.NewInlineFragment("p", `<p/>`).
		WithSlotFragment("never", collage.NewInlineFragment("c", `c`).Build()).Build()
	err := app.RegisterPage(collage.NewPage("p").WithContent(frag).WithPath("en", "/p").Build())
	if !errors.Is(err, collage.ErrUnknownSlot) {
		t.Fatalf("RegisterPage = %v, want ErrUnknownSlot", err)
	}
}

func TestInlineFragment_ConflictingTemplate(t *testing.T) {
	app := inlineApp(t, false)
	frag := &collage.Fragment{Name: "both", TemplatePath: "partials/name.html", Source: "<p/>"}
	err := app.RegisterPage(collage.NewPage("p").WithContent(frag).WithPath("en", "/p").Build())
	if !errors.Is(err, collage.ErrConflictingTemplate) {
		t.Fatalf("RegisterPage = %v, want ErrConflictingTemplate", err)
	}
}

func TestInlineFragment_DefineRefused(t *testing.T) {
	app := inlineApp(t, false)
	frag := collage.NewInlineFragment("sneaky", `{{define "partials/name.html"}}x{{end}}<p/>`).Build()
	err := app.RegisterPage(collage.NewPage("p").WithContent(frag).WithPath("en", "/p").Build())
	if !errors.Is(err, collage.ErrSourceConflict) {
		t.Fatalf("RegisterPage = %v, want ErrSourceConflict", err)
	}
}

// A resolver's fragments are not known at registration; an inline one is added
// on first sight. Rendered from many goroutines at once, under -race.
func TestInlineFragment_FromAResolver(t *testing.T) {
	app := inlineApp(t, false)
	item := collage.NewInlineFragment("item", `<li>i</li>`).Build()
	list := collage.NewInlineFragment("list", `<ul>{{slot "items"}}</ul>`).
		WithSlotResolver("items", func(*collage.RenderContext) ([]*collage.Fragment, error) {
			return []*collage.Fragment{item}, nil
		}).Build()
	if err := app.RegisterPage(collage.NewPage("l").WithContent(list).WithPath("en", "/l").Dynamic().Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	h := app.Handler()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/l", nil))
			if w.Body.String() != "<ul><li>i</li></ul>" {
				t.Errorf("body = %q", w.Body.String())
			}
		}()
	}
	wg.Wait()
}

func TestInlineFragment_StaticBuildAndFragmentPath(t *testing.T) {
	app := inlineApp(t, false)
	results := collage.NewInlineFragment("results", `<ol>r</ol>`).Build()
	page := collage.NewPage("search").
		WithContent(collage.NewInlineFragment("search", `<form></form>`).Build()).
		WithPath("en", "/search").
		WithFragmentPath("en", "/search/results", results).
		Static().Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if got := inlineBody(t, app.Handler(), "/search/results"); got != "<ol>r</ol>" {
		t.Fatalf("fragment path body = %q", got)
	}
	out := t.TempDir()
	builder, err := collage.NewBuilder(app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(out, "search", "index.html"))
	if err != nil || !strings.Contains(string(written), "<form></form>") {
		t.Fatalf("built page = %q, %v", written, err)
	}
}

func TestInspect_InlineFragment(t *testing.T) {
	app := inlineApp(t, false)
	if err := app.RegisterPage(collage.NewPage("p").WithContent(collage.NewInlineFragment("card", `<p/>`).Build()).
		WithPath("en", "/p").Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	for _, f := range app.Inspect().Fragments {
		if f.Name == "card" {
			if !f.Inline || f.Template != "" {
				t.Fatalf("inspected = %+v, want inline and no template path", f)
			}
			return
		}
	}
	t.Fatal("inline fragment not inspected")
}
