package collage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

// A renamed forgery field is renamed on both sides: the form carries it, and the
// verifier reads it.
func TestCSRF_ARenamedFieldIsRenamedInTheForm(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/f.html": {Data: []byte(`<form method="post">{{csrfToken}}</form>`)}}, Root: "t"},
		Security: collage.SecurityConfig{CSRFKey: []byte("a key for this test only"), CSRFFieldName: "authenticity"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := collage.NewPage("f").WithContent(collage.NewFragment("f", "f.html").Build()).WithPath("en", "/f").Dynamic().
		WithAction(http.MethodPost, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.SeeOther("/f"), nil
		}).Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/f", nil))
	match := regexp.MustCompile(`name="authenticity" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if match == nil {
		t.Fatalf("the form does not carry the renamed field:\n%s", rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/f", strings.NewReader("authenticity="+match[1]))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		req.AddCookie(c)
	}
	submit := httptest.NewRecorder()
	h.ServeHTTP(submit, req)
	if submit.Code != http.StatusSeeOther {
		t.Errorf("submission = %d, want 303: the verifier did not read the field the form sent", submit.Code)
	}
}

// A builder's mistake reaches registration even when nobody asked the builder —
// here a slot declared twice, deep in the content, and a document with no handler.
func TestRegister_RefusesWhatABuilderRecorded(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte(`x`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := collage.NewFragment("inner", "x.html").WithSlot("s", false, false).WithSlot("s", false, false).Build()
	outer := collage.NewFragment("outer", "x.html").WithSlot("child", false, false).WithSlotFragment("child", inner).Build()
	page := collage.NewPage("p").WithContent(outer).WithPath("en", "/").Dynamic().Build()
	if err := app.RegisterPage(page); !errors.Is(err, collage.ErrDuplicateSlot) {
		t.Errorf("RegisterPage = %v, want ErrDuplicateSlot from the inner fragment's builder", err)
	}

	doc := collage.NewDocument("d", "text/plain").WithPath("en", "/d.txt").Build()
	if err := app.RegisterDocument(doc); !errors.Is(err, collage.ErrNoDocumentHandler) {
		t.Errorf("RegisterDocument = %v, want ErrNoDocumentHandler", err)
	}
}

// The development 500 page names the fragment whose template broke, not the layout
// the failure passed through.
func TestDev500_NamesTheBrokenFragment(t *testing.T) {
	app, err := collage.New(&collage.Config{
		DevMode: true,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html": {Data: []byte(`<main>{{slot "content"}}</main>`)},
			"t/recipe.html": {Data: []byte(`{{.Missing}}`)},
		}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	content := collage.NewFragment("recipe-content", "recipe.html").
		WithDataHandler(collage.DataHandler(func(context.Context, *collage.RenderContext) (struct{ Name string }, []string, error) {
			return struct{ Name string }{"soup"}, nil, nil
		})).Required().Build()
	if err := app.RegisterPage(collage.NewPage("recipe").WithLayout(layout).WithContent(content).WithPath("en", "/").Dynamic().Build()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Fragment: <code>recipe-content</code>") {
		t.Errorf("the 500 page does not name recipe-content:\n%s", rec.Body.String())
	}
}

// A page's action is held to RegisterAction's rules: one without a handler is
// refused at registration, not on its first request.
func TestRegister_APageActionNeedsAHandler(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte(`x`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := collage.NewPage("p").WithContent(collage.NewFragment("p", "x.html").Build()).WithPath("en", "/").
		Dynamic().WithAction(http.MethodPost, nil).Build()
	if err := app.RegisterPage(page); !errors.Is(err, collage.ErrNoActionHandler) {
		t.Errorf("RegisterPage = %v, want ErrNoActionHandler", err)
	}
}

// A fragment a slot resolver returns never went through registration, so its
// builder's mistakes are caught when it is first rendered.
func TestSlotResolver_AResolvedFragmentsBuilderMistakeFailsTheRender(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte(`[{{slot "s"}}]`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	broken := collage.NewFragment("broken", "x.html").WithSlot("s", false, false).WithSlot("s", false, false).Build()
	host := collage.NewFragment("host", "x.html").WithSlot("s", false, true).
		WithSlotResolver("s", func(*collage.RenderContext) ([]*collage.Fragment, error) { return []*collage.Fragment{broken}, nil }).
		Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(host).WithPath("en", "/").Dynamic().Build()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "[[") {
		t.Errorf("the broken fragment rendered: %q", rec.Body.String())
	}
}

// A disk cache directory that cannot be created is a slower site, not one that
// does not start: the application falls back to memory.
func TestNew_AnUnwritableCacheDirectoryFallsBackToMemory(t *testing.T) {
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	_, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte(`x`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "disk", Dir: filepath.Join(locked, "cache")},
	})
	if err != nil {
		t.Errorf("New = %v, want the application built on a memory cache", err)
	}
}
