package collage_test

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// lateHoister adds to the head after the render, as a plugin that only learns
// what the page needs from its markup does.
type lateHoister struct {
	name    string
	replace bool
	results []bool
}

func (p *lateHoister) Name() string                             { return p.name }
func (p *lateHoister) Version() string                          { return "0" }
func (p *lateHoister) Init(context.Context, collage.Host) error { return nil }
func (p *lateHoister) Shutdown(context.Context) error           { return nil }
func (p *lateHoister) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if p.replace {
		// A plugin rewriting the page: the place the layout chose is lost.
		ev.HTML = []byte(strings.Replace(string(ev.HTML), "<body>", "<body data-x>", 1))
	}
	p.results = append(p.results,
		ev.Hoist("head", "css:code", template.HTML(`<link rel="stylesheet" href="/code.css">`)),
		ev.Hoist("head", "css:code", template.HTML(`<link rel="stylesheet" href="/twice.css">`)),
		ev.Hoist("head", "declared", template.HTML(`<meta name="late">`)),
		ev.Hoist("foot", "js", template.HTML(`<script src="/x.js"></script>`)),
	)
	return nil
}

func hoistSite(t *testing.T, plugins ...collage.Plugin) string {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<html><head><title>t</title>{{hoist "head"}}<link id="after"></head><body>x{{hoist "foot"}}<i></i></body></html>`)},
		}, Root: "t"},
		Plugins: plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	frag := collage.NewFragment("p", "p.html").WithDataHandler(collage.Effect(func(_ context.Context, rc *collage.RenderContext) error {
		rc.Hoist("head", "declared", template.HTML(`<meta name="early">`))
		return nil
	})).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(frag).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Body.String()
}

// A late hoist lands where the layout put {{hoist}}, after what the render
// declared; a key already there is left alone.
func TestAfterRenderHoist(t *testing.T) {
	p := &lateHoister{name: "test/late"}
	body := hoistSite(t, p)
	want := `<title>t</title><meta name="early"><link rel="stylesheet" href="/code.css"><link id="after">`
	if !strings.Contains(body, want) {
		t.Errorf("head:\n%s\nwant %s", body, want)
	}
	if !strings.Contains(body, `x<script src="/x.js"></script><i></i>`) {
		t.Errorf("foot area:\n%s", body)
	}
	if strings.Contains(body, "twice.css") || strings.Contains(body, `name="late"`) {
		t.Errorf("a key hoisted twice:\n%s", body)
	}
	if got := p.results; len(got) != 4 || !got[0] || got[1] || got[2] || !got[3] {
		t.Errorf("results = %v, want [true false false true]", got)
	}
}

// After an earlier plugin rewrote the page, "head" still lands before </head>;
// another area cannot be found.
func TestAfterRenderHoist_AfterARewrite(t *testing.T) {
	p := &lateHoister{name: "test/late", replace: true}
	body := hoistSite(t, p)
	if !strings.Contains(body, `<link id="after"><link rel="stylesheet" href="/code.css"></head>`) {
		t.Errorf("head after a rewrite:\n%s", body)
	}
	if got := p.results; !got[0] || got[3] {
		t.Errorf("results = %v, want head true, foot false", got)
	}
}
