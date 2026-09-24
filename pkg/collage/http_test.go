package collage_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type langKey struct{}

// langApp is an application whose one page renders the language its middleware
// resolved from Accept-Language, and is cached: exactly the combination that
// serves one reader's language to the next unless the cache is told.
func langApp(t *testing.T, vary bool, extra func(*collage.App)) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"templates/layouts/default.html": {Data: []byte(`<main>{{slot "content"}}</main>`)},
				"templates/pages/home.html":      {Data: []byte(`<p>lang={{.}}</p>`)},
			},
			Root: "templates",
		},
		Cache: collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	layout := collage.NewFragment("layout", "layouts/default.html").WithSlot("content", true, false).Build()
	content := collage.NewFragment("home-content", "pages/home.html").
		WithDataHandler(collage.DataHandler(func(ctx context.Context, _ *collage.RenderContext) (string, []string, error) {
			lang, _ := ctx.Value(langKey{}).(string)
			return lang, nil, nil
		})).
		Build()
	page := collage.NewPage("home").WithLayout(layout).WithContent(content).WithPath("en", "/").Incremental(time.Minute).Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lang := "en"
			if strings.HasPrefix(r.Header.Get("Accept-Language"), "tr") {
				lang = "tr"
			}
			if vary {
				if err := collage.Vary(r, "Accept-Language", lang); err != nil {
					t.Errorf("Vary() = %v, want nil", err)
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), langKey{}, lang)))
		})
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if extra != nil {
		extra(app)
	}
	return app
}

func getWith(h http.Handler, target, acceptLanguage string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// What middleware puts in the request context is what a data handler reads, and
// a dimension it declares keeps the cached versions apart.
func TestVary_KeepsCachedVersionsApart(t *testing.T) {
	h := langApp(t, true, nil).Handler()

	for _, c := range []struct{ header, want string }{
		{"tr-TR,tr;q=0.9", "lang=tr"},
		{"en-US", "lang=en"},
		{"tr", "lang=tr"}, // a second spelling of tr, served from tr's entry
		{"", "lang=en"},
	} {
		rec := getWith(h, "/", c.header)
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("Accept-Language %q: body = %q, want %q", c.header, rec.Body.String(), c.want)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Language") {
			t.Errorf("Vary = %q, want it to name Accept-Language", got)
		}
	}
}

// A query string cannot spell a declared dimension: the two live in separate
// sections of the key.
func TestVary_AQueryCannotImpersonateADimension(t *testing.T) {
	h := langApp(t, true, nil).Handler()

	getWith(h, "/", "tr") // caches tr under the declared dimension
	rec := getWith(h, "/?Accept-Language=tr", "en")
	if !strings.Contains(rec.Body.String(), "lang=en") {
		t.Errorf("body = %q, want lang=en: a query reached a declared dimension's entry", rec.Body.String())
	}
}

func TestVary_OutsideARequest(t *testing.T) {
	err := collage.Vary(httptest.NewRequest(http.MethodGet, "/", nil), "Accept-Language", "tr")
	if !errors.Is(err, collage.ErrVaryOutsideRequest) {
		t.Errorf("Vary() = %v, want ErrVaryOutsideRequest", err)
	}
}

// A mounted handler sees the full path, runs after middleware, and is answered
// with whatever it writes.
func TestHandle_ServesUnderItsPrefix(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang, _ := r.Context().Value(langKey{}).(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, `{"path":"`+r.URL.Path+`","lang":"`+lang+`"}`)
	})
	h := langApp(t, false, func(app *collage.App) {
		if err := app.Handle("/api/", api); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}).Handler()

	rec := getWith(h, "/api/users/7", "tr")
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the handler's own 418", rec.Code)
	}
	if body := rec.Body.String(); body != `{"path":"/api/users/7","lang":"tr"}` {
		t.Errorf("body = %q, want the full path and the middleware's value", body)
	}
	// The page is still the page.
	if rec := getWith(h, "/", ""); !strings.Contains(rec.Body.String(), "lang=en") {
		t.Errorf("GET / = %q, want the page", rec.Body.String())
	}
}

// A handler that panics is a 500, not a dropped connection.
func TestHandle_APanicIsA500(t *testing.T) {
	h := langApp(t, false, func(app *collage.App) {
		_ = app.Handle("/boom/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	}).Handler()

	if rec := getWith(h, "/boom/now", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// A handler over a route is refused at startup, whichever was registered first.
func TestHandle_RefusesToShadowARoute(t *testing.T) {
	app := langApp(t, false, nil)
	action := collage.NewAction("count").WithPath("en", "/api/count").WithMethods(http.MethodPost).
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) { return nil, nil }).
		Build()
	if err := app.Handle("/api/", http.NotFoundHandler()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if err := app.RegisterAction(action); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}

	if err := app.Start(); !errors.Is(err, collage.ErrMountShadowsRoute) {
		t.Errorf("Start() = %v, want ErrMountShadowsRoute", err)
	}
}

func TestHandle_Rejections(t *testing.T) {
	app := langApp(t, false, nil)
	for _, prefix := range []string{"/", "api/", "/api", "//api/"} {
		if err := app.Handle(prefix, http.NotFoundHandler()); !errors.Is(err, collage.ErrInvalidHandlerPrefix) {
			t.Errorf("Handle(%q) = %v, want ErrInvalidHandlerPrefix", prefix, err)
		}
	}
	if err := app.Handle("/api/", nil); !errors.Is(err, collage.ErrNilHandler) {
		t.Errorf("Handle(nil) = %v, want ErrNilHandler", err)
	}
	if err := app.Use(nil); !errors.Is(err, collage.ErrNilHandler) {
		t.Errorf("Use(nil) = %v, want ErrNilHandler", err)
	}
}

// Middleware may answer by itself, and runs in registration order.
func TestUse_OrderAndShortCircuit(t *testing.T) {
	var order []string
	h := langApp(t, false, func(app *collage.App) {
		_ = app.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, "second")
				if r.Header.Get("Authorization") == "" && r.URL.Path == "/private/" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		_ = app.Handle("/private/", http.NotFoundHandler())
	}).Handler()

	if rec := getWith(h, "/private/", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want the middleware's 401", rec.Code)
	}
	if len(order) != 1 {
		t.Errorf("order = %v, want the second middleware to have run once", order)
	}
}

func TestJSONOf(t *testing.T) {
	result, err := collage.JSONOf(http.StatusCreated, struct {
		Count int `json:"count"`
	}{3})
	if err != nil {
		t.Fatalf("JSONOf() = %v", err)
	}
	if result.Status != http.StatusCreated || result.ContentType != "application/json" || string(result.Body) != `{"count":3}` {
		t.Errorf("JSONOf() = %+v", result)
	}
	if _, err := collage.JSONOf(http.StatusOK, func() {}); err == nil {
		t.Error("JSONOf(func) = nil error, want the marshal error")
	}
}
