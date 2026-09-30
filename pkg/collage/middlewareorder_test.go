package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// passage records the middleware a request passed through, in order, and what
// each one found in the request's context from those outside it.
type passage struct {
	mu   sync.Mutex
	path []string
	saw  map[string][]string
}

type seenKey struct{}

// through is middleware named name: it records itself, what the middleware
// outside it left in the context, and adds its own name for the ones inside.
func (p *passage) through(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			outer, _ := r.Context().Value(seenKey{}).([]string)
			p.mu.Lock()
			p.path = append(p.path, name)
			p.saw[name] = outer
			p.mu.Unlock()
			ctx := context.WithValue(r.Context(), seenKey{}, append(slices.Clone(outer), name))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// wrapper is a plugin whose Init adds one middleware per name, as session or
// flash add theirs.
type wrapper struct {
	name  string
	names []string
	p     *passage
}

func (w *wrapper) Name() string                   { return w.name }
func (w *wrapper) Version() string                { return "0" }
func (w *wrapper) Shutdown(context.Context) error { return nil }
func (w *wrapper) Init(_ context.Context, host collage.Host) error {
	for _, name := range w.names {
		if err := host.Use(w.p.through(name)); err != nil {
			return err
		}
	}
	return nil
}

func orderedApp(t *testing.T, plugins ...collage.Plugin) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>p</p>`)}}, Root: "t"},
		Plugins:  plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterPage(collage.NewPage("p").WithPath("en", "/").
		WithContent(collage.NewFragment("p", "p.html").Build()).Build()); err != nil {
		t.Fatal(err)
	}
	return app
}

func (p *passage) serve(t *testing.T, app *collage.App) {
	t.Helper()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// Middleware a plugin in Config.Plugins adds is outside the application's own,
// so the application's middleware reads what the plugin put in the context — a
// session, a flash message.
func TestConfigPluginMiddlewareIsOutsideTheApplications(t *testing.T) {
	p := &passage{saw: map[string][]string{}}
	app := orderedApp(t, &wrapper{name: "test/session", names: []string{"session"}, p: p})
	if err := app.Use(p.through("load-user")); err != nil {
		t.Fatal(err)
	}
	p.serve(t, app)
	if want := []string{"session", "load-user"}; !slices.Equal(p.path, want) {
		t.Errorf("path = %v, want %v", p.path, want)
	}
	if !slices.Contains(p.saw["load-user"], "session") {
		t.Errorf("the application's middleware did not see the plugin's context: %v", p.saw["load-user"])
	}
}

// A plugin registered with RegisterPlugin sits where it was registered: inside
// the middleware added before it, outside what is added after.
func TestRegisterPluginMiddlewareSitsWhereItWasRegistered(t *testing.T) {
	p := &passage{saw: map[string][]string{}}
	app := orderedApp(t, &wrapper{name: "test/session", names: []string{"session"}, p: p})
	if err := app.Use(p.through("load-user")); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterPlugin(&wrapper{name: "test/ratelimit", names: []string{"ratelimit"}, p: p}); err != nil {
		t.Fatal(err)
	}
	if err := app.Use(p.through("audit")); err != nil {
		t.Fatal(err)
	}
	p.serve(t, app)
	if want := []string{"session", "load-user", "ratelimit", "audit"}; !slices.Equal(p.path, want) {
		t.Errorf("path = %v, want %v", p.path, want)
	}
	if !slices.Contains(p.saw["ratelimit"], "load-user") {
		t.Errorf("a plugin registered after app.Use did not see its context: %v", p.saw["ratelimit"])
	}
}

// Plugins keep the order they were registered in, and one plugin's middleware
// the order it added them in.
func TestPluginsMiddlewareKeepsRegistrationOrder(t *testing.T) {
	p := &passage{saw: map[string][]string{}}
	app := orderedApp(t,
		&wrapper{name: "test/first", names: []string{"first-a", "first-b"}, p: p},
		&wrapper{name: "test/second", names: []string{"second"}, p: p},
	)
	if err := app.Use(p.through("app")); err != nil {
		t.Fatal(err)
	}
	p.serve(t, app)
	if want := []string{"first-a", "first-b", "second", "app"}; !slices.Equal(p.path, want) {
		t.Errorf("path = %v, want %v", p.path, want)
	}
}
