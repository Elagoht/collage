package collage_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func noopAction(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
	return nil, nil
}

// An action may not claim OPTIONS, TRACE or CONNECT, however it was built: a
// standalone action, a page's WithAction shorthand, or one attached with
// WithActionFor. Each is refused by the sentinel, naming the action and the
// method.
func TestRegisterAction_RefusesServerMethods(t *testing.T) {
	for _, method := range []string{http.MethodOptions, http.MethodTrace, http.MethodConnect} {
		t.Run("standalone "+method, func(t *testing.T) {
			app := langApp(t, false, nil)
			action := collage.NewAction("cors").WithPath("en", "/api").WithMethods(http.MethodPost, method).
				WithHandler(noopAction).Build()
			err := app.RegisterAction(action)
			if !errors.Is(err, collage.ErrInvalidActionMethod) {
				t.Fatalf("RegisterAction = %v, want ErrInvalidActionMethod", err)
			}
			if !strings.Contains(err.Error(), `"cors"`) || !strings.Contains(err.Error(), method) {
				t.Errorf("error %q does not name the action and %s", err, method)
			}
		})
		t.Run("page "+method, func(t *testing.T) {
			app := langApp(t, false, nil)
			content := collage.NewFragment("c", "pages/home.html").Build()
			page := collage.NewPage("form").WithContent(content).WithPath("en", "/form").
				Incremental(time.Minute).WithAction(method, noopAction).Build()
			if err := app.RegisterPage(page); !errors.Is(err, collage.ErrInvalidActionMethod) {
				t.Fatalf("RegisterPage = %v, want ErrInvalidActionMethod", err)
			}
		})
		t.Run("page action for "+method, func(t *testing.T) {
			app := langApp(t, false, nil)
			content := collage.NewFragment("c", "pages/home.html").Build()
			page := collage.NewPage("form").WithContent(content).WithPath("en", "/form").
				Incremental(time.Minute).
				WithActionFor(collage.NewAction("submit").WithMethods(method).WithHandler(noopAction).Build()).Build()
			if err := app.RegisterPage(page); !errors.Is(err, collage.ErrInvalidActionMethod) {
				t.Fatalf("RegisterPage = %v, want ErrInvalidActionMethod", err)
			}
		})
	}
}
