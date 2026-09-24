package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// An action answering with a page it built on the spot is told what went wrong,
// rather than served a layout around nothing.
func TestAction_AnUnregisteredPageIsNamed(t *testing.T) {
	app, err := collage.New(&collage.Config{
		DevMode: true,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"t/layout.html": {Data: []byte(`<main>{{slot "content"}}</main>`)},
				"t/form.html":   {Data: []byte(`<form></form>`)},
			},
			Root: "t",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	build := func() *collage.Page {
		return collage.NewPage("form").WithLayout(layout).
			WithContent(collage.NewFragment("form-content", "form.html").Build()).Dynamic().Build()
	}
	registered := build()
	registered.Paths = map[string]string{"en": "/form"}
	if err := app.RegisterPage(registered); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.RegisterAction(collage.NewAction("submit").WithPath("en", "/submit").WithMethods(http.MethodPost).
		WithoutCSRF().
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.RenderPage(build()), nil
		}).Build()); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/submit", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "never registered") || !strings.Contains(body, "RegisterPage") {
		t.Errorf("body = %q, want it to say the page was never registered and what to do", body)
	}
}
