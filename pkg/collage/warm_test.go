package collage_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// warmingAuthorSite is the author site with warming left on.
func warmingAuthorSite(t *testing.T, devMode bool) *authorSite {
	t.Helper()
	site := newAuthorSiteWith(t, devMode, collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour})
	t.Cleanup(func() { _ = site.app.Shutdown(context.Background()) })
	return site
}

// waitWarmed waits for the application's warmer to go idle. The handler's
// WaitWarmed is the framework's own, not public API: reached through an
// interface, as a test outside the module would have to.
func waitWarmed(t *testing.T, app *collage.App) {
	t.Helper()
	idle, ok := app.Handler().(interface{ WaitWarmed() })
	if !ok {
		t.Fatalf("handler %T has no WaitWarmed", app.Handler())
	}
	done := make(chan struct{})
	go func() {
		idle.WaitWarmed()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warmer never went idle")
	}
}

// renamed changes what the upstream says author id is called.
func (s *authorSite) renamed(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names[id] = name
}

// After an invalidation every page it reached is rendered again in the
// background: with nobody asking in between, each is a fresh HIT with the new
// content, and the upstream was asked for the changed data once.
func TestWarmer_WarmsAfterInvalidate(t *testing.T) {
	site := warmingAuthorSite(t, false)
	for i := range 30 {
		site.get(t, i, false)
	}
	site.renamed("A", "Ada Lovelace")

	if n, err := site.app.InvalidateTagsN(context.Background(), "author:A"); err != nil || n != 20 {
		t.Fatalf("InvalidateTagsN = %d, %v; want A's twenty pages", n, err)
	}
	waitWarmed(t, site.app)

	if got := site.fetched(); got["A"] != 2 || got["B"] != 1 {
		t.Fatalf("fetched %v, want A once more and B not again", got)
	}
	for i := range 20 {
		rec := site.serve(t, i)
		if got := rec.Header().Get(collage.CacheStatusHeader); got != "HIT" || rec.Body.String() != "<article><p>Ada Lovelace</p></article>" {
			t.Fatalf("page %d = %s %q, want a HIT on the new name", i, got, rec.Body.String())
		}
	}
	if rec := site.serve(t, 25); rec.Header().Get(collage.CacheStatusHeader) != "HIT" {
		t.Fatalf("B's page %s = %q, want a HIT: it was never invalidated",
			collage.CacheStatusHeader, rec.Header().Get(collage.CacheStatusHeader))
	}
}

// A purge warms too: the page comes back fresh, with no stale copy to serve and
// no reader made to render it.
func TestPurgeTags_Warms(t *testing.T) {
	site := warmingAuthorSite(t, false)
	site.get(t, 3, false)
	site.renamed("A", "Ada Lovelace")
	if err := site.app.PurgeTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	waitWarmed(t, site.app)
	rec := site.serve(t, 3)
	if got := rec.Header().Get(collage.CacheStatusHeader); got != "HIT" || rec.Body.String() != "<article><p>Ada Lovelace</p></article>" {
		t.Fatalf("after the purge = %s %q, want a HIT on the new name", got, rec.Body.String())
	}
}

// Warm renders never pass through the application's middleware: a rate limit,
// an access log or an auth check sees readers only.
func TestWarmer_SkipsMiddleware(t *testing.T) {
	site := warmingAuthorSite(t, false)
	var seen atomic.Int32
	if err := site.app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.Add(1)
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		site.get(t, i, false)
	}
	before := seen.Load()
	if err := site.app.InvalidateTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	waitWarmed(t, site.app)
	if got := seen.Load(); got != before {
		t.Fatalf("middleware saw %d warm requests, want none", got-before)
	}
	if rec := site.serve(t, 2); rec.Header().Get(collage.CacheStatusHeader) != "HIT" {
		t.Fatal("the page was not warmed")
	}
}

// With the upstream down a warm run stops at its first failure: the upstream is
// asked once, not once per page, and the pages stay stale for their readers.
func TestWarmer_StopsAtTheFirstFailure(t *testing.T) {
	site := warmingAuthorSite(t, false)
	for i := range 30 {
		site.get(t, i, false)
	}
	site.fail(errors.New("cms down"))
	before := site.attempts.Load()
	if err := site.app.InvalidateTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	waitWarmed(t, site.app)
	if got := site.attempts.Load() - before; got != 1 {
		t.Fatalf("upstream asked %d times by the warm, want once", got)
	}
	rec := site.serve(t, 4)
	if got := rec.Header().Get(collage.CacheStatusHeader); rec.Code != http.StatusOK || got != "STALE" {
		t.Fatalf("page after a stopped warm = %d %s, want the stale page", rec.Code, got)
	}
}

// No warming in development, where the page cache is not read.
func TestWarmer_OffInDevMode(t *testing.T) {
	site := warmingAuthorSite(t, true)
	site.get(t, 3, false)
	before := site.attempts.Load()
	if err := site.app.InvalidateTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	waitWarmed(t, site.app)
	if got := site.attempts.Load(); got != before {
		t.Fatalf("development warmed: %d fetches", got-before)
	}
}

// A static build renders around the page cache and writes nothing to it, so it
// leaves nothing to warm: an invalidation after it renders nothing.
func TestWarmer_NothingAfterAStaticBuild(t *testing.T) {
	site := warmingAuthorSite(t, false)
	if _, err := collage.Build(context.Background(), site.app, collage.BuildOptions{OutDir: t.TempDir(), Concurrency: 4}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	before := site.attempts.Load()
	if err := site.app.InvalidateTags(context.Background(), "author:A", "author:B"); err != nil {
		t.Fatal(err)
	}
	waitWarmed(t, site.app)
	if got := site.attempts.Load(); got != before {
		t.Fatalf("an invalidation after a build warmed: %d fetches", got-before)
	}
}

// Shutdown stops the warmer: an invalidation after it warms nothing.
func TestWarmer_ShutdownStopsIt(t *testing.T) {
	site := warmingAuthorSite(t, false)
	site.get(t, 3, false)
	if err := site.app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := site.attempts.Load()
	if err := site.app.InvalidateTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	waitWarmed(t, site.app)
	if got := site.attempts.Load(); got != before {
		t.Fatalf("warmed after Shutdown: %d fetches", got-before)
	}
}

// Shutdown does not wait on a warm render past its context: a warm held on the
// upstream is let go, and Shutdown returns.
func TestWarmer_ShutdownDropsTheQueue(t *testing.T) {
	site := warmingAuthorSite(t, false)
	for i := range 20 {
		site.get(t, i, false)
	}
	gate := site.hold()
	defer close(gate)
	before := site.attempts.Load()
	if err := site.app.InvalidateTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for site.attempts.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("the warm never reached the upstream")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = site.app.Shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown waited on a held warm past its context")
	}
	if got := site.attempts.Load() - before; got != 1 {
		t.Fatalf("upstream asked %d times, want once: the rest of the queue was dropped", got)
	}
}

// slowInvalidation holds every invalidation of the tag "slow" inside its
// OnCacheInvalidate until release is closed, so the invalidation is under way
// for as long as a test likes.
type slowInvalidation struct {
	entered chan struct{}
	release chan struct{}
}

func (p *slowInvalidation) Name() string                             { return "test/slow-invalidation" }
func (p *slowInvalidation) Version() string                          { return "0" }
func (p *slowInvalidation) Init(context.Context, collage.Host) error { return nil }
func (p *slowInvalidation) Shutdown(context.Context) error           { return nil }
func (p *slowInvalidation) OnCacheInvalidate(_ context.Context, ev *collage.CacheInvalidateEvent) error {
	if len(ev.Tags) == 1 && ev.Tags[0] == "slow" {
		p.entered <- struct{}{}
		<-p.release
	}
	return nil
}

// A warm overlapped by an unrelated invalidation — one that reached no page,
// under way when the warm render ends — is not lost: once that invalidation is
// over the page is warmed again, and its next reader is a HIT on the new name.
func TestWarmer_AnUnrelatedInvalidationDoesNotLoseAWarm(t *testing.T) {
	site := warmingAuthorSite(t, false)
	slow := &slowInvalidation{entered: make(chan struct{}, 1), release: make(chan struct{})}
	if err := site.app.RegisterPlugin(slow); err != nil {
		t.Fatal(err)
	}
	site.get(t, 3, false)
	site.renamed("A", "Ada Lovelace")
	gate := site.hold()
	before := site.attempts.Load()
	if err := site.app.InvalidateTags(context.Background(), "author:A"); err != nil {
		t.Fatal(err)
	}
	for site.attempts.Load() == before {
		time.Sleep(time.Millisecond)
	}
	slowDone := make(chan error, 1)
	go func() { slowDone <- site.app.InvalidateTags(context.Background(), "slow") }()
	<-slow.entered
	close(gate)
	// The warm render ends while "slow" is under way, and is refused its write.
	time.Sleep(50 * time.Millisecond)
	close(slow.release)
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
	waitWarmed(t, site.app)
	rec := site.serve(t, 3)
	if got := rec.Header().Get(collage.CacheStatusHeader); got != "HIT" || rec.Body.String() != "<article><p>Ada Lovelace</p></article>" {
		t.Fatalf("after both invalidations = %s %q, want a HIT on the new name", got, rec.Body.String())
	}
}
