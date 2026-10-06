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
	content := collage.NewFragment("secret", "s.html").WithData(collage.DataHandler(
		func(ctx context.Context, rc *collage.RenderContext) (string, []string, error) {
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
	content := collage.NewFragment("secret", "s.html").WithData(collage.DataHandler(
		func(ctx context.Context, _ *collage.RenderContext) (string, []string, error) {
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

// cacheableDocument is cacheablePage's counterpart for a document: one at /feed.txt
// whose handler writes read(ctx, rc), behind the same middleware. cached picks an
// Incremental document, whose one render is served to every reader, over a Dynamic
// one, rendered per request.
func cacheableDocument(t *testing.T, vary, cached bool, read func(context.Context, *collage.RenderContext) string) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/s.html": {Data: []byte(`unused`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doc := collage.NewDocument("feed", "text/plain").WithPath("en", "/feed.txt").
		WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
			return []byte("secret=" + read(ctx, rc) + "."), nil, nil
		})
	if cached {
		doc = doc.Incremental(time.Minute)
	} else {
		doc = doc.Dynamic()
	}
	if err := app.RegisterDocument(doc.Build()); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
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

func getDocSecret(h http.Handler, secret string) string {
	req := httptest.NewRequest(http.MethodGet, "/feed.txt", nil)
	req.Header.Set("X-Secret", secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}

// A cacheable document's one render is served to every reader, as a page's is, so
// it is stripped of per-reader context values the same way: through the context
// its handler is given and through the request's own.
func TestSharedDocument_DoesNotLeakContextBetweenReaders(t *testing.T) {
	reads := map[string]func(context.Context, *collage.RenderContext) string{
		"ctx": func(ctx context.Context, _ *collage.RenderContext) string {
			secret, _ := ctx.Value(secretKey{}).(string)
			return secret
		},
		"rc.Request": func(_ context.Context, rc *collage.RenderContext) string {
			secret, _ := rc.Request.Context().Value(secretKey{}).(string)
			return secret
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			h := cacheableDocument(t, false, true, read)
			first := getDocSecret(h, "alice-secret")
			second := getDocSecret(h, "bob-secret")
			if strings.Contains(second, "alice-secret") {
				t.Errorf("reader B was served reader A's context value: %q", second)
			}
			if first != "secret=." {
				t.Errorf("first body = %q, want the context value stripped to %q", first, "secret=.")
			}
		})
	}
}

// A varied value still reaches a cacheable document, one cache entry per value.
func TestSharedDocument_VariedValueIsKeptPerReader(t *testing.T) {
	h := cacheableDocument(t, true, true, func(_ context.Context, rc *collage.RenderContext) string {
		secret, _ := collage.Varied(rc, "X-Secret")
		return secret
	})
	if got := getDocSecret(h, "alice-secret"); got != "secret=alice-secret." {
		t.Errorf("reader A body = %q, want its own varied value", got)
	}
	if got := getDocSecret(h, "bob-secret"); got != "secret=bob-secret." {
		t.Errorf("reader B body = %q, want its own varied value", got)
	}
}

// A Dynamic document is rendered for its one reader, so it keeps the whole context.
func TestDynamicDocument_SeesContext(t *testing.T) {
	h := cacheableDocument(t, false, false, func(ctx context.Context, _ *collage.RenderContext) string {
		secret, _ := ctx.Value(secretKey{}).(string)
		return secret
	})
	if got := getDocSecret(h, "alice-secret"); got != "secret=alice-secret." {
		t.Errorf("body = %q, want the reader's own context value", got)
	}
}

// A varied value reaches code that holds only a context — a cacheable document's
// ctx, stripped of everything per-reader but the vary set.
func TestVariedContext_InSharedDocument(t *testing.T) {
	h := cacheableDocument(t, true, true, func(ctx context.Context, _ *collage.RenderContext) string {
		secret, _ := collage.VariedContext(ctx, "X-Secret")
		return secret
	})
	if got := getDocSecret(h, "alice-secret"); got != "secret=alice-secret." {
		t.Errorf("reader A body = %q, want its varied value", got)
	}
	if got := getDocSecret(h, "bob-secret"); got != "secret=bob-secret." {
		t.Errorf("reader B body = %q, want its varied value", got)
	}
}

// pageURLsLister registers a document listing a page's URLs through Host.PageURLs,
// as a sitemap does: the page's StaticParams run with the document's context.
type pageURLsLister struct{}

func (pageURLsLister) Name() string                   { return "test/lister" }
func (pageURLsLister) Version() string                { return "0" }
func (pageURLsLister) Shutdown(context.Context) error { return nil }
func (pageURLsLister) Init(_ context.Context, host collage.Host) error {
	return host.RegisterDocument(collage.NewDocument("list", "text/plain").AtRoot("/list.txt").
		WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			urls, err := host.PageURLs(ctx, "post")
			if err != nil {
				return nil, nil, err
			}
			var b strings.Builder
			for _, u := range urls {
				b.WriteString(u.Path + ";")
			}
			return []byte(b.String()), nil, nil
		}).Incremental(time.Minute).Build())
}

// StaticParams, reached through PageURLs from a shared document render, reads the
// varied value with VariedContext: a URL set per tenant.
func TestVariedContext_InStaticParams(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/s.html": {Data: []byte(`x`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
		Plugins:  []collage.Plugin{pageURLsLister{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	post := collage.NewPage("post").WithContent(collage.NewFragment("p", "s.html").Build()).WithPath("en", "/posts/{slug}").
		WithStaticParams(func(ctx context.Context, _ string) ([]map[string]string, error) {
			tenant, _ := collage.VariedContext(ctx, "X-Secret")
			return []map[string]string{{"slug": tenant + "-1"}, {"slug": tenant + "-2"}}, nil
		}).Build()
	if err := app.RegisterPage(post); err != nil {
		t.Fatal(err)
	}
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = collage.Vary(r, "X-Secret", r.Header.Get("X-Secret"))
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	for _, tenant := range []string{"acme", "globex", "acme"} {
		req := httptest.NewRequest(http.MethodGet, "/list.txt", nil)
		req.Header.Set("X-Secret", tenant)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if want := "/posts/" + tenant + "-1;/posts/" + tenant + "-2;"; rec.Body.String() != want {
			t.Errorf("%s list = %q, want %q", tenant, rec.Body.String(), want)
		}
	}
}
