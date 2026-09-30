package collage_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type secretKey struct{}

// cacheablePage builds an app with one cacheable (Incremental) page whose data
// handler renders read(ctx), behind middleware that stashes the X-Secret header
// in the context under secretKey without declaring a Vary dimension for it — the
// unsafe pattern the fix is about. read is how the handler reaches the value:
// straight from the context, or through collage.Varied.
func cacheablePage(t *testing.T, vary bool, read func(context.Context, *collage.RenderContext) string) http.Handler {
	t.Helper()
	content := collage.NewFragment("secret", "s.html").
		WithDataHandler(collage.DataHandler(func(ctx context.Context, rc *collage.RenderContext) (string, []string, error) {
			return read(ctx, rc), nil, nil
		})).
		Build()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/s.html": {Data: []byte(`<p>secret={{.}}</p>`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := collage.NewPage("home").WithContent(content).WithPath("en", "/").Incremental(time.Minute).Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if vary {
				if err := collage.Vary(r, "X-Secret", r.Header.Get("X-Secret")); err != nil {
					t.Errorf("Vary: %v", err)
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), secretKey{}, r.Header.Get("X-Secret"))))
		})
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app.Handler()
}

func getSecret(h http.Handler, secret string) string {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Secret", secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}

// The unsafe pattern — a cacheable page whose data handler reads a per-reader
// value straight from the request context, with no Vary — no longer serves one
// reader's value to another. The value is stripped from the shared render, so it
// reaches nobody, rather than the first reader's being frozen into the cache for
// everyone after.
func TestSharedRender_DoesNotLeakContextBetweenReaders(t *testing.T) {
	h := cacheablePage(t, false, func(ctx context.Context, _ *collage.RenderContext) string {
		secret, _ := ctx.Value(secretKey{}).(string)
		return secret
	})

	first := getSecret(h, "alice-secret") // renders and fills the cache
	second := getSecret(h, "bob-secret")  // served from that cached render

	if strings.Contains(second, "alice-secret") {
		t.Errorf("reader B was served reader A's context value: %q", second)
	}
	if strings.Contains(first, "alice-secret") {
		t.Errorf("the per-reader context value reached the shared render at all: %q", first)
	}
	if want := "<p>secret=</p>"; !strings.Contains(second, want) {
		t.Errorf("body = %q, want the context value stripped to %q", second, want)
	}
}

// In development, a hidden context value that a data handler actually reads on a
// shared render is logged, so the mistake — a cacheable page whose data is
// per-reader — is visible rather than silent.
func TestSharedRender_DevModeWarnsOnHiddenContextRead(t *testing.T) {
	var logs bytes.Buffer
	content := collage.NewFragment("secret", "s.html").
		WithDataHandler(collage.DataHandler(func(ctx context.Context, _ *collage.RenderContext) (string, []string, error) {
			secret, _ := ctx.Value(secretKey{}).(string)
			return secret, nil, nil
		})).
		Build()
	app, err := collage.New(&collage.Config{
		DevMode:  true,
		Logger:   slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/s.html": {Data: []byte(`<p>secret={{.}}</p>`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.RegisterPage(collage.NewPage("home").WithContent(content).WithPath("en", "/").Incremental(time.Minute).Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), secretKey{}, r.Header.Get("X-Secret"))))
		})
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	getSecret(app.Handler(), "alice-secret")

	if !strings.Contains(logs.String(), "hidden from a shared render") {
		t.Errorf("dev log = %q, want a warning that a context value was hidden from the shared render", logs.String())
	}
}

// The safe pattern — the same page reading the value through collage.Varied, with
// the middleware declaring it a Vary dimension — keeps each reader's value,
// because each is its own cache entry.
func TestSharedRender_VariedValueIsKeptPerReader(t *testing.T) {
	h := cacheablePage(t, true, func(_ context.Context, rc *collage.RenderContext) string {
		secret, _ := collage.Varied(rc, "X-Secret")
		return secret
	})

	if got := getSecret(h, "alice-secret"); !strings.Contains(got, "secret=alice-secret") {
		t.Errorf("reader A body = %q, want its own varied value", got)
	}
	if got := getSecret(h, "bob-secret"); !strings.Contains(got, "secret=bob-secret") {
		t.Errorf("reader B body = %q, want its own varied value, a separate cache entry", got)
	}
}
