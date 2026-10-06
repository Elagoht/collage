package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

var hookStateKey = collage.NewKey[string]("hook-state")

// valuesHookPlugin reads the render's values from OnAfterRender with keys.
type valuesHookPlugin struct {
	got       string
	ok        bool
	missingOK bool
}

func (p *valuesHookPlugin) Name() string                             { return "test/values" }
func (p *valuesHookPlugin) Version() string                          { return "0" }
func (p *valuesHookPlugin) Init(context.Context, collage.Host) error { return nil }
func (p *valuesHookPlugin) Shutdown(context.Context) error           { return nil }
func (p *valuesHookPlugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	p.got, p.ok = hookStateKey.In(ev.Values)
	_, p.missingOK = collage.NewKey[int]("never-set").In(ev.Values)
	return nil
}

// An AfterRender hook reads what the render stored, by key; a key nothing set,
// and a nil Values, are simply absent.
func TestAfterRenderValues(t *testing.T) {
	p := &valuesHookPlugin{}
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<html><head></head><body>x</body></html>`)},
		}, Root: "t"},
		Plugins: []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}
	frag := collage.NewFragment("p", "p.html").WithData(collage.Effect(func(_ context.Context, rc *collage.RenderContext) error {
		hookStateKey.Set(rc, "from-render")
		return nil
	})).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(frag).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if p.got != "from-render" || !p.ok {
		t.Errorf("In(ev.Values) = %q, %v; want from-render, true", p.got, p.ok)
	}
	if p.missingOK {
		t.Error("a key nothing set reported present")
	}
	if _, ok := collage.NewKey[int]("x").In(nil); ok {
		t.Error("In(nil) reported present")
	}
}
