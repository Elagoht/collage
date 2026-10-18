package collage_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// fullStore is a SharedCache that, while full is set, fails every write as a
// full Redis under noeviction does — page writes, stale marks, data writes —
// while the epoch and every read still answer.
type fullStore struct {
	collage.SharedCache
	full atomic.Bool
}

var errOOM = errors.New("OOM command not allowed when used memory > 'maxmemory'")

func (s *fullStore) SetSince(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string, meta []byte, since uint64) (string, collage.WriteOutcome, error) {
	if s.full.Load() {
		return "", collage.WriteSkipped, errOOM
	}
	return s.SharedCache.SetSince(ctx, key, content, ttl, tags, meta, since)
}

func (s *fullStore) MarkStaleKey(ctx context.Context, key string) error {
	if s.full.Load() {
		return errOOM
	}
	return s.SharedCache.MarkStaleKey(ctx, key)
}

func (s *fullStore) SetData(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string, since uint64) (collage.WriteOutcome, error) {
	if s.full.Load() {
		return collage.WriteSkipped, errOOM
	}
	return s.SharedCache.SetData(ctx, key, value, ttl, tags, since)
}

// errorCounter counts the ErrorEvents plugins are sent.
type errorCounter struct{ n atomic.Int32 }

func (p *errorCounter) Name() string                             { return "test/errors" }
func (p *errorCounter) Version() string                          { return "0" }
func (p *errorCounter) Init(context.Context, collage.Host) error { return nil }
func (p *errorCounter) Shutdown(context.Context) error           { return nil }
func (p *errorCounter) OnError(context.Context, *collage.ErrorEvent) error {
	p.n.Add(1)
	return nil
}

type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// count returns how many records at level mention the shared store's health,
// or, for ERROR, how many there are at all.
func (l *lockedLog) count(level string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range strings.Split(l.b.String(), "\n") {
		if strings.Contains(line, "level="+level) && (level == "ERROR" || strings.Contains(line, "shared page and data cache")) {
			n++
		}
	}
	return n
}

// A full shared store answers the epoch and reads and fails every write, so
// each cacheable miss reads it fine and fails to write a page and a value. The
// App reports that through one rate-limited health for pages and data: one
// warning and one recovery in all, not one of each per request, and no error
// for plugins, since the store failing is not a request's failure.
func TestSharedHealth_AFullStoreIsReportedOncePerApp(t *testing.T) {
	logs := &lockedLog{}
	errs := &errorCounter{}
	store := &fullStore{SharedCache: collagetest.NewSharedCache()}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>{{.}}</p>`)}}, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Store: store, DefaultTTL: time.Hour, WarmLimit: -1},
		Plugins:  []collage.Plugin{errs},
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	key := collage.NewKey[string]("health:value")
	card := collage.NewFragment("p", "p.html").WithData(collage.LoadTagged(
		func(_ context.Context, rc *collage.RenderContext) (string, []string, error) {
			v, err := collage.Cached(rc, key, time.Hour, []string{"t"}, func(context.Context) (string, error) { return "v", nil })
			return v, nil, err
		})).Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(card).WithPath("en", "/p").Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	h := app.Handler()
	get := func() {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "v") {
			t.Fatalf("response = %d %q, want the page", rec.Code, rec.Body.String())
		}
	}

	store.full.Store(true)
	for range 200 {
		get()
	}
	if n := logs.count("WARN"); n != 1 {
		t.Errorf("200 requests against a full store logged %d warnings, want 1:\n%s", n, logs.b.String())
	}
	if n := logs.count("INFO"); n > 1 {
		t.Errorf("200 requests against a full store logged %d recoveries, want at most 1", n)
	}
	if n := logs.count("ERROR"); n != 0 {
		t.Errorf("logged %d errors, want 0: the store failing is not a request's error", n)
	}

	store.full.Store(false)
	for range 3 {
		get()
	}
	if n := logs.count("WARN"); n != 1 {
		t.Errorf("warnings after the store recovered = %d, want still 1", n)
	}
	if n := logs.count("INFO"); n != 1 {
		t.Errorf("recoveries logged in all = %d, want 1:\n%s", n, logs.b.String())
	}
	if n := errs.n.Load(); n != 0 {
		t.Errorf("plugins were sent %d ErrorEvents, want 0", n)
	}
}
