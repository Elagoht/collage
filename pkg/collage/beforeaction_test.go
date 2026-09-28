package collage_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// gate is a plugin that reads each submission's form in OnBeforeAction and
// refuses the ones marked "spam".
type gate struct {
	name  string
	calls atomic.Int64
	seen  atomic.Value
}

func (g *gate) Name() string                             { return g.name }
func (g *gate) Version() string                          { return "0" }
func (g *gate) Init(context.Context, collage.Host) error { return nil }
func (g *gate) Shutdown(context.Context) error           { return nil }
func (g *gate) OnBeforeAction(_ context.Context, ev *collage.BeforeActionEvent) error {
	g.calls.Add(1)
	form, err := ev.Form()
	if err != nil {
		return err
	}
	g.seen.Store(ev.Action.Name + ":" + form.Get("message"))
	if form.Get("message") == "spam" {
		ev.Result = &collage.ActionResult{Status: http.StatusBadRequest, Body: []byte("refused"), ContentType: "text/plain; charset=utf-8"}
	}
	return nil
}

func beforeActionApp(t *testing.T, csrf bool, plugins ...collage.Plugin) (http.Handler, *atomic.Value) {
	t.Helper()
	cfg := &collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Plugins:  plugins,
	}
	if csrf {
		cfg.Security = collage.SecurityConfig{CSRFKey: bytes.Repeat([]byte("k"), 32)}
	}
	a, err := collage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := &atomic.Value{}
	handler := func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
		got.Store(rc.Request.PostFormValue("message"))
		return collage.SeeOther("/thanks"), nil
	}
	build := func(name, path string, limit int64) {
		b := collage.NewAction(name).WithPath("en", path).WithMethods(http.MethodPost).WithHandler(handler)
		if !csrf {
			b = b.WithoutCSRF()
		}
		if limit != 0 {
			b = b.WithMaxBodyBytes(limit)
		}
		if err := a.RegisterAction(b.Build()); err != nil {
			t.Fatal(err)
		}
	}
	build("send", "/send", 0)
	build("upload", "/upload", 8<<20)
	build("tiny", "/tiny", 16)
	return a.Handler(), got
}

func postForm(h http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A plugin reads the form before the handler, which still finds it; and a result
// the plugin sets answers in the handler's place.
func TestBeforeActionReadsAndRefuses(t *testing.T) {
	g := &gate{name: "test/gate"}
	h, got := beforeActionApp(t, false, g)
	if w := postForm(h, "/send", url.Values{"message": {"hello"}}); w.Code != http.StatusSeeOther || got.Load() != "hello" {
		t.Errorf("accepted: %d, handler saw %v", w.Code, got.Load())
	}
	if g.seen.Load() != "send:hello" {
		t.Errorf("the plugin saw %v", g.seen.Load())
	}
	got.Store("")
	w := postForm(h, "/send", url.Values{"message": {"spam"}})
	if w.Code != http.StatusBadRequest || w.Body.String() != "refused" || got.Load() != "" {
		t.Errorf("refused: %d %q, handler saw %v", w.Code, w.Body.String(), got.Load())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
	}
}

// The body the plugin reads is bounded by the action's own limit: larger than
// the default for an action that allows it, and a 413 past it.
func TestBeforeActionUsesTheActionsLimit(t *testing.T) {
	h, got := beforeActionApp(t, false, &gate{name: "test/gate"})
	big := strings.Repeat("x", 5<<20)
	if w := postForm(h, "/upload", url.Values{"message": {big}}); w.Code != http.StatusSeeOther || got.Load() != big {
		t.Errorf("5 MiB to an 8 MiB action: %d", w.Code)
	}
	if w := postForm(h, "/tiny", url.Values{"message": {"more than sixteen bytes"}}); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("past the action's limit: %d, want 413", w.Code)
	}
}

// A forged submission is refused before any plugin reads it, and the first
// plugin to answer is the last one asked.
func TestBeforeActionOrder(t *testing.T) {
	first, second := &gate{name: "test/first"}, &gate{name: "test/second"}
	h, _ := beforeActionApp(t, true, first, second)
	if w := postForm(h, "/send", url.Values{"message": {"hello"}}); w.Code != http.StatusForbidden || first.calls.Load() != 0 {
		t.Errorf("forged: %d, plugin called %d times", w.Code, first.calls.Load())
	}

	first, second = &gate{name: "test/first"}, &gate{name: "test/second"}
	h, _ = beforeActionApp(t, false, first, second)
	postForm(h, "/send", url.Values{"message": {"spam"}})
	if first.calls.Load() != 1 || second.calls.Load() != 0 {
		t.Errorf("calls: first %d, second %d", first.calls.Load(), second.calls.Load())
	}
}
