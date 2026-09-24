package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// sectionsApp renders a page whose sections, and their order, come from content
// that changes between requests — what a CMS's block list is.
func sectionsApp(t *testing.T, order *[]string, mu *sync.Mutex, required, multiple bool) (*collage.App, error) {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/layout.html":   {Data: []byte(`{{slot "content"}}`)},
				"t/sections.html": {Data: []byte(`<main>{{slot "sections"}}</main>`)},
				"t/section.html":  {Data: []byte(`[{{.}}]`)},
			},
			Root: "t",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	section := func(name string) *collage.Fragment {
		return collage.NewFragment("section-"+name, "section.html").
			WithDataHandler(collage.DataHandler(func(context.Context, *collage.RenderContext) (string, []string, error) {
				return name, []string{"section:" + name}, nil
			})).
			Build()
	}
	known := map[string]*collage.Fragment{"hero": section("hero"), "posts": section("posts"), "about": section("about")}

	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	builder := collage.NewFragment("sections", "sections.html").
		WithDataHandler(collage.DataHandler(func(_ context.Context, rc *collage.RenderContext) (struct{}, []string, error) {
			mu.Lock()
			rc.Set("order", append([]string(nil), (*order)...))
			mu.Unlock()
			return struct{}{}, nil, nil
		})).
		WithSlot("sections", required, multiple).
		WithSlotResolver("sections", func(rc *collage.RenderContext) ([]*collage.Fragment, error) {
			value, _ := rc.Get("order")
			names, _ := value.([]string)
			var fragments []*collage.Fragment
			for _, name := range names {
				if name == "broken" {
					return nil, errors.New("no such section")
				}
				fragments = append(fragments, known[name])
			}
			return fragments, nil
		})
	if err := builder.BuildErr(); err != nil {
		return nil, err
	}
	page := collage.NewPage("home").WithLayout(layout).WithContent(builder.Build()).WithPath("en", "/").Dynamic().Build()
	return app, app.RegisterPage(page)
}

func serve(app *collage.App) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

// The content decides which sections render and in which order, per request, with
// no rebuild — and each section's own data handler still runs.
func TestSlotResolver_FollowsTheContent(t *testing.T) {
	var mu sync.Mutex
	order := []string{"hero", "posts"}
	app, err := sectionsApp(t, &order, &mu, false, true)
	if err != nil {
		t.Fatalf("sectionsApp: %v", err)
	}

	if got := serve(app).Body.String(); got != "<main>[hero][posts]</main>" {
		t.Errorf("first render = %q", got)
	}
	mu.Lock()
	order = []string{"about", "hero"}
	mu.Unlock()
	if got := serve(app).Body.String(); got != "<main>[about][hero]</main>" {
		t.Errorf("after the content changed = %q, want the new order with no restart", got)
	}
	mu.Lock()
	order = nil
	mu.Unlock()
	if got := serve(app).Body.String(); got != "<main></main>" {
		t.Errorf("with no sections = %q", got)
	}
}

// What a resolver returns is held to the slot's rules, and its error fails its
// fragment — here the page's content, so the page.
func TestSlotResolver_Rules(t *testing.T) {
	for _, c := range []struct {
		name               string
		order              []string
		required, multiple bool
	}{
		{"two for a slot that holds one", []string{"hero", "posts"}, false, false},
		{"none for a required slot", nil, true, true},
		{"the resolver fails", []string{"hero", "broken"}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			order := c.order
			app, err := sectionsApp(t, &order, &mu, c.required, c.multiple)
			if err != nil {
				t.Fatalf("sectionsApp: %v", err)
			}
			if rec := serve(app); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "[hero]") {
				t.Errorf("status = %d body = %q, want the render to fail", rec.Code, rec.Body.String())
			}
		})
	}
}

// One slot has one source of what is in it.
func TestSlotResolver_CannotBeMixedWithBoundFragments(t *testing.T) {
	child := collage.NewFragment("child", "child.html").Build()
	resolve := func(*collage.RenderContext) ([]*collage.Fragment, error) { return nil, nil }

	bound := collage.NewFragment("a", "a.html").WithSlot("s", false, true).
		WithSlotFragment("s", child).WithSlotResolver("s", resolve)
	if err := bound.BuildErr(); !errors.Is(err, collage.ErrSlotResolved) {
		t.Errorf("resolver after a bound fragment: BuildErr() = %v, want ErrSlotResolved", err)
	}
	resolved := collage.NewFragment("b", "b.html").WithSlot("s", false, true).
		WithSlotResolver("s", resolve).WithSlotFragment("s", child)
	if err := resolved.BuildErr(); !errors.Is(err, collage.ErrSlotResolved) {
		t.Errorf("bound fragment after a resolver: BuildErr() = %v, want ErrSlotResolved", err)
	}
	undeclared := collage.NewFragment("c", "c.html").WithSlotResolver("s", resolve)
	if err := undeclared.BuildErr(); !errors.Is(err, collage.ErrUnknownSlot) {
		t.Errorf("resolver for an undeclared slot: BuildErr() = %v, want ErrUnknownSlot", err)
	}
}
