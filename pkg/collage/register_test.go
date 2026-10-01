package collage_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// registerApp returns an application with nothing registered yet.
func registerApp(t *testing.T) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/unused.html": {Data: []byte(``)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// pageAt returns a page named name with one inline fragment, at path.
func pageAt(name, path, body string) *collage.Page {
	return collage.NewPage(name).
		WithContent(collage.NewInlineFragment(name+"-content", body).Build()).
		WithPath("en", path).
		Build()
}

// One call registers every kind, and each answers at its path.
func TestRegisterTakesEveryKind(t *testing.T) {
	app := registerApp(t)
	err := app.Register(
		pageAt("home", "/", "<p>Ana sayfa</p>"),
		collage.NewDocument("robots", "text/plain").AtRoot("/robots.txt").WithBody([]byte("User-agent: *")).Build(),
		collage.NewAction("ping").WithPath("en", "/ping").WithMethods(http.MethodPost).WithoutCSRF().
			WithHandler(func(_ context.Context, _ *collage.RenderContext) (*collage.ActionResult, error) {
				return collage.NoContent(http.StatusNoContent), nil
			}).Build(),
	)
	if err != nil {
		t.Fatalf("Register = %v, want nil", err)
	}

	c := collagetest.New(t, app.Handler())
	if res := c.Get("/").WantStatus(http.StatusOK); !strings.Contains(res.Body, "Ana sayfa") {
		t.Errorf("home body = %q", res.Body)
	}
	c.Get("/robots.txt").WantStatus(http.StatusOK)
	c.Do(c.Request(http.MethodPost, "/ping", nil)).WantStatus(http.StatusNoContent)
}

// A refusal names what was refused, keeps its cause, and stops there: what came
// after it is not registered.
func TestRegisterStopsAtTheFirstRefusal(t *testing.T) {
	app := registerApp(t)
	err := app.Register(
		pageAt("home", "/", "<p>a</p>"),
		pageAt("home", "/again", "<p>b</p>"),
		pageAt("after", "/after", "<p>c</p>"),
	)
	if err == nil || !strings.Contains(err.Error(), `register page "home"`) {
		t.Fatalf("Register = %v, want it to name the page", err)
	}
	if !errors.Is(err, collage.ErrDuplicatePage) {
		t.Errorf("Register = %v, want ErrDuplicatePage behind it", err)
	}

	c := collagetest.New(t, app.Handler())
	c.Get("/").WantStatus(http.StatusOK)
	c.Get("/after").WantStatus(http.StatusNotFound)
}

func TestRegisterRefusesNil(t *testing.T) {
	var page *collage.Page
	for name, items := range map[string][]collage.Registrable{
		"untyped nil": {pageAt("home", "/", "x"), nil},
		"nil page":    {page},
	} {
		t.Run(name, func(t *testing.T) {
			if err := registerApp(t).Register(items...); err == nil {
				t.Fatal("Register = nil, want an error")
			}
		})
	}
	if err := registerApp(t).Register(pageAt("home", "/", "x"), nil); !errors.Is(err, collage.ErrNilRegistrable) {
		t.Errorf("Register(nil) = %v, want ErrNilRegistrable", err)
	}
}
