package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

var (
	pathsActionKey = collage.NewKey[string]("test:action")
	pathsErrorKey  = collage.NewKey[string]("test:error-page")
	pathsBeforeKey = collage.NewKey[string]("test:before")
)

// pathsHookPlugin records, per page rendered, what the render's values held.
type pathsHookPlugin struct {
	mu   sync.Mutex
	seen map[string]string
}

func (p *pathsHookPlugin) Name() string                             { return "test/paths" }
func (p *pathsHookPlugin) Version() string                          { return "0" }
func (p *pathsHookPlugin) Init(context.Context, collage.Host) error { return nil }
func (p *pathsHookPlugin) Shutdown(context.Context) error           { return nil }
func (p *pathsHookPlugin) OnBeforeRender(_ context.Context, ev *collage.BeforeRenderEvent) error {
	pathsBeforeKey.Set(ev.Context, "before:"+ev.Page.Name)
	return nil
}
func (p *pathsHookPlugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	action, _ := pathsActionKey.In(ev.Values)
	errorPage, _ := pathsErrorKey.In(ev.Values)
	before, _ := pathsBeforeKey.In(ev.Values)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[ev.Page.Name] = "action=" + action + " error=" + errorPage + " " + before
	return nil
}

// An action that answers with its page (a 422 re-render) and an error page each
// hand AfterRender hooks the values of the render they produced — what the action,
// the error page's fragments and BeforeRender stored.
func TestAfterRenderValues_ActionAndErrorPage(t *testing.T) {
	p := &pathsHookPlugin{seen: map[string]string{}}
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<html><head></head><body><form method="post" action="/">{{csrfToken}}<input name="a"></form></body></html>`)},
		}, Root: "t"},
		Plugins: []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}
	errorPage := collage.NewPage("oops").WithPath("en", "/oops").WithContent(
		collage.NewFragment("e", "p.html").WithData(collage.Effect(func(_ context.Context, rc *collage.RenderContext) error {
			pathsErrorKey.Set(rc, "from-error-page")
			return nil
		})).Build()).Build()
	form := collage.NewPage("form").WithPath("en", "/").WithErrorPage(errorPage).
		WithContent(collage.NewFragment("f", "p.html").Build()).
		WithAction(http.MethodPost, func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			pathsActionKey.Set(rc, "from-action")
			return &collage.ActionResult{Status: http.StatusUnprocessableEntity, Page: rc.Page}, nil
		}).Build()
	boom := collage.NewPage("boom").WithPath("en", "/boom").WithErrorPage(errorPage).WithContent(
		collage.NewFragment("b", "p.html").WithData(collage.Effect(func(context.Context, *collage.RenderContext) error {
			return errors.New("boom")
		})).Required().Build()).Build()
	for _, page := range []*collage.Page{errorPage, form, boom} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatal(err)
		}
	}

	h := app.Handler()
	c := collagetest.New(t, h)
	if res := c.Submit(c.Get("/"), "/", url.Values{"a": {"1"}}); res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("POST / = %d, want 422", res.Status)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /boom = %d, want 500", rec.Code)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if got, want := p.seen["form"], "action=from-action error= before:form"; got != want {
		t.Errorf("action's re-render: hook saw %q, want %q", got, want)
	}
	if got, want := p.seen["oops"], "action= error=from-error-page before:oops"; got != want {
		t.Errorf("error page: hook saw %q, want %q", got, want)
	}
}

var isolationKey = collage.NewKey[string]("test:isolation")

// isolationPlugin checks that a render's hook sees the value its own render set.
type isolationPlugin struct {
	mu       sync.Mutex
	problems []string
}

func (p *isolationPlugin) Name() string                             { return "test/isolation" }
func (p *isolationPlugin) Version() string                          { return "0" }
func (p *isolationPlugin) Init(context.Context, collage.Host) error { return nil }
func (p *isolationPlugin) Shutdown(context.Context) error           { return nil }
func (p *isolationPlugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	v, ok := isolationKey.In(ev.Values)
	if !ok || !strings.Contains(string(ev.HTML), "<p>"+v+"</p>") {
		p.mu.Lock()
		p.problems = append(p.problems, "hook saw "+v+" in a render of "+string(ev.HTML))
		p.mu.Unlock()
	}
	return nil
}

// Each render has its own values: two renders in flight at once each read back
// only what they stored under one key, and a later render starts with none of
// an earlier one's.
func TestRenderValues_IsolatedBetweenRenders(t *testing.T) {
	p := &isolationPlugin{}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/unused.html": {Data: []byte(`x`)}}, Root: "t"},
		Plugins:  []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both renders store their value, then wait for the other to have stored its
	// own before reading back, so the two are in flight together.
	var arrived sync.WaitGroup
	arrived.Add(2)
	together := make(chan struct{})
	go func() { arrived.Wait(); close(together) }()

	var mu sync.Mutex
	var problems []string
	report := func(s string) { mu.Lock(); problems = append(problems, s); mu.Unlock() }

	frag := collage.NewInlineFragment("v", `<p>{{.}}</p>`).WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			mine := rc.Request.URL.Query().Get("v")
			if earlier, ok := isolationKey.Get(rc); ok {
				report(mine + " started with " + earlier)
			}
			isolationKey.Set(rc, mine)
			if mine != "later" {
				arrived.Done()
				select {
				case <-together:
				case <-time.After(5 * time.Second):
					report(mine + " never saw the other render arrive")
				}
			}
			if got, _ := isolationKey.Get(rc); got != mine {
				report(mine + " read back " + got)
			}
			return mine, nil
		})).Build()
	if err := app.RegisterPage(collage.NewPage("v").WithContent(frag).WithPath("en", "/").Dynamic().Build()); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	get := func(v string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?v="+v, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<p>"+v+"</p>") {
			report("GET /?v=" + v + " served " + rec.Body.String())
		}
	}

	var wg sync.WaitGroup
	for _, v := range []string{"a", "b"} {
		wg.Add(1)
		go func() { defer wg.Done(); get(v) }()
	}
	wg.Wait()
	get("later")

	for _, s := range append(problems, p.problems...) {
		t.Error(s)
	}
}
