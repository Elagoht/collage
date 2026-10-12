package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestJSON_ValueAndBytes(t *testing.T) {
	r, err := collage.JSON(http.StatusCreated, map[string]int{"n": 1})
	if err != nil || string(r.Body) != `{"n":1}` || r.Status != 201 || r.ContentType != "application/json" {
		t.Errorf("JSON = %+v, %v", r, err)
	}
	if b := collage.JSONBytes(200, []byte(`[]`)); string(b.Body) != "[]" {
		t.Errorf("JSONBytes = %+v", b)
	}
	if s := collage.Status(http.StatusUnauthorized); s.Status != 401 || s.Body != nil {
		t.Errorf("Status = %+v", s)
	}
}

func TestVaried_TakesContext(t *testing.T) {
	if v, ok := collage.Varied(context.Background(), "X-Test"); ok || v != "" {
		t.Errorf("Varied on a bare context = %q, %v", v, ok)
	}
}

func TestRedirect_StatusDefaultsAndPermanent(t *testing.T) {
	r := collage.Redirect{From: "/a", To: "/b"}
	if r.Status != 0 {
		t.Fatal("zero Status expected")
	}
	// A zero Status answers 302.
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/c.html": {Data: []byte(`<html><body>c</body></html>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	served := collage.NewPage("served").WithContent(collage.NewFragment("c", "c.html").Build()).
		WithPath("en", "/b").WithRedirect(r.From, r.To, r.Status).Build()
	if err := app.RegisterPage(served); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/b" {
		t.Errorf("a zero-Status redirect answered %d to %q, want 302 to /b", rec.Code, rec.Header().Get("Location"))
	}
	// The page builder's permanent helper writes 301 into Status.
	frag := collage.NewFragment("c", "c.html").Build()
	p := collage.NewPage("p").WithContent(frag).WithPermanentRedirect("/old", "/new").Build()
	if len(p.Redirects) != 1 || p.Redirects[0].Status != http.StatusMovedPermanently {
		t.Errorf("WithPermanentRedirect redirects = %+v", p.Redirects)
	}
	d := collage.NewDocument("d", "text/plain").WithBody([]byte("x")).WithPermanentRedirect("/old", "/new").Build()
	if len(d.Redirects) != 1 || d.Redirects[0].Status != http.StatusMovedPermanently {
		t.Errorf("document WithPermanentRedirect redirects = %+v", d.Redirects)
	}
}

func TestWithSlot_Rules(t *testing.T) {
	f := collage.NewFragment("f", "f.html").WithSlot("main", collage.SlotRequired, collage.SlotSingle).Build()
	if s := f.Slots["main"]; s == nil || !s.Required || s.AllowMultiple {
		t.Errorf("WithSlot(main, SlotRequired, SlotSingle) = %+v", s)
	}
	g := collage.NewFragment("g", "g.html").WithSlot("side").Build()
	if s := g.Slots["side"]; s == nil || s.Required || !s.AllowMultiple {
		t.Errorf("WithSlot(side) = %+v", s)
	}
	b := collage.NewFragment("h", "h.html").WithSlot("x", collage.SlotRule(99))
	b.Build()
	if !errors.Is(b.BuildErr(), collage.ErrInvalidSlotDefinition) {
		t.Errorf("unknown slot rule: %v", b.BuildErr())
	}
}

func TestSlotResolverFunc_TakesContext(t *testing.T) {
	var resolve collage.SlotResolverFunc = func(ctx context.Context, rc *collage.RenderContext) ([]*collage.Fragment, error) {
		return nil, ctx.Err()
	}
	if _, err := resolve(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestIncremental_ZeroTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -1} {
		b := collage.NewPage("p").Incremental(ttl)
		b.Build()
		if !errors.Is(b.BuildErr(), collage.ErrInvalidTTL) {
			t.Errorf("page Incremental(%v): %v", ttl, b.BuildErr())
		}
		d := collage.NewDocument("d", "text/plain").WithBody([]byte("x")).Incremental(ttl)
		d.Build()
		if !errors.Is(d.BuildErr(), collage.ErrInvalidTTL) {
			t.Errorf("document Incremental(%v): %v", ttl, d.BuildErr())
		}
	}
}
