package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

type afterPathPlugin struct {
	mu    sync.Mutex
	paths []string
}

func (p *afterPathPlugin) Name() string                             { return "test/after-path" }
func (p *afterPathPlugin) Version() string                          { return "0" }
func (p *afterPathPlugin) Init(context.Context, collage.Host) error { return nil }
func (p *afterPathPlugin) Shutdown(context.Context) error           { return nil }
func (p *afterPathPlugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, ev.Path)
	return nil
}

// AfterRender names the path it rendered, as BeforeRender does.
func TestAfterRenderEvent_Path(t *testing.T) {
	p := &afterPathPlugin{}
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
	page := collage.NewPage("p").WithPath("en", "/the/path").WithContent(collage.NewFragment("f", "p.html").Build()).Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/the/path", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.paths) != 1 || p.paths[0] != "/the/path" {
		t.Errorf("AfterRender paths = %v, want [/the/path]", p.paths)
	}
}
