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

// timeoutDocApp serves one document at /slow.txt whose handler takes wait
// unless its context ends first, bounded by own and by the application's
// TemplateConfig.Timeout engine.
func timeoutDocApp(t *testing.T, engine, own, wait time.Duration) *collage.App {
	t.Helper()
	return timeoutDocAppWith(t, engine, own, wait, false)
}

// timeoutDocAppWith is timeoutDocApp, with the document Static and the cache on
// when static is set.
func timeoutDocAppWith(t *testing.T, engine, own, wait time.Duration, static bool) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t", Timeout: engine},
		Cache:    collage.CacheConfig{Enabled: static},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := collage.NewDocument("slow", "text/plain").AtRoot("/slow.txt").
		WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			select {
			case <-time.After(wait):
				return []byte("done"), nil, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}).
		WithTimeout(own)
	if static {
		b.Static()
	}
	doc := b.Build()
	if err := b.BuildErr(); err != nil {
		t.Fatal(err)
	}
	if err := app.Register(doc); err != nil {
		t.Fatal(err)
	}
	return app
}

// A document's own short timeout ends its handler although the application's
// default would have waited: on a live request and on the render a static
// build uses.
func TestDocumentTimeout_OwnShortTimeoutWins(t *testing.T) {
	app := timeoutDocApp(t, time.Minute, 20*time.Millisecond, 10*time.Second)

	started := time.Now()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow.txt", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /slow.txt = %d, want 500", rec.Code)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the live request took %v; the document's own 20ms timeout did not apply", elapsed)
	}

	_, err := app.RenderDocumentPath(context.Background(), "/slow.txt", "", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("RenderDocumentPath error = %v, want the timeout", err)
	}
}

// A document's own long timeout lets its handler finish although the
// application's default is shorter than it takes.
func TestDocumentTimeout_OwnLongTimeoutWins(t *testing.T) {
	app := timeoutDocApp(t, 10*time.Millisecond, time.Minute, 100*time.Millisecond)

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "done" {
		t.Errorf("GET /slow.txt = %d %q, want 200 done", rec.Code, rec.Body)
	}

	result, err := app.RenderDocumentPath(context.Background(), "/slow.txt", "", nil)
	if err != nil || string(result.Body) != "done" {
		t.Errorf("RenderDocumentPath = %v, want done", err)
	}
}

// A negative timeout is a mistake the builder records, and a Document written
// as a literal with one is refused at registration.
func TestDocumentTimeout_Negative(t *testing.T) {
	b := collage.NewDocument("d", "text/plain").AtRoot("/d.txt").WithBody([]byte("x")).WithTimeout(-time.Second)
	doc := b.Build()
	if !errors.Is(b.BuildErr(), collage.ErrInvalidTimeout) {
		t.Errorf("BuildErr() = %v, want ErrInvalidTimeout", b.BuildErr())
	}
	if doc.Timeout != 0 {
		t.Errorf("Timeout = %v, want it left unchanged at 0", doc.Timeout)
	}

	literal := &collage.Document{Name: "d", ContentType: "text/plain", Paths: map[string]string{"": "/d.txt"}, Body: []byte("x"), Timeout: -time.Second}
	if err := literal.Validate(); !errors.Is(err, collage.ErrInvalidTimeout) {
		t.Errorf("Validate() = %v, want ErrInvalidTimeout", err)
	}
}

// A cached document's render, shared by every reader, keeps its own long
// timeout too.
func TestDocumentTimeout_OwnLongTimeoutWinsWhenCached(t *testing.T) {
	app := timeoutDocAppWith(t, 10*time.Millisecond, time.Minute, 100*time.Millisecond, true)

	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "done" {
		t.Errorf("GET /slow.txt = %d %q, want 200 done", rec.Code, rec.Body)
	}
}
