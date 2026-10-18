package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// keyRecorder is a SharedCache that remembers every key a page write was
// attempted for, whatever its outcome, so a test can look the entry up in the
// store itself. Embedding keeps every other method, and the SharedCache the
// apps detect, the store's own.
type keyRecorder struct {
	collage.SharedCache
	mu   sync.Mutex
	keys []string
}

func (k *keyRecorder) SetSince(ctx context.Context, key string, content []byte, ttl time.Duration, tags []string, meta []byte, since uint64) (string, collage.WriteOutcome, error) {
	k.mu.Lock()
	if !slices.Contains(k.keys, key) {
		k.keys = append(k.keys, key)
	}
	k.mu.Unlock()
	return k.SharedCache.SetSince(ctx, key, content, ttl, tags, meta, since)
}

// key returns the one page key written so far, failing the test when there is
// not exactly one.
func (k *keyRecorder) key(t *testing.T) string {
	t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.keys) != 1 {
		t.Fatalf("page keys written = %q, want exactly one", k.keys)
	}
	return k.keys[0]
}

var fleetAuthorKey = collage.NewKey[string]("fleet:author")

// fleet is two instances of one app on one shared store: A and B, as two
// processes behind a load balancer on one Redis. /p/{slug} shows the author,
// fetched through collage.Cached under the tag "author".
type fleet struct {
	store  *keyRecorder
	a, b   *collage.App
	ha, hb http.Handler

	mu   sync.Mutex
	name string
	// gate, when set, holds every author fetch until it is closed; entered is
	// sent to, without blocking, as a held fetch begins.
	gate    chan struct{}
	entered chan struct{}
}

// newFleet builds the two instances, A with pluginsA. warmLimit is both
// instances' Cache.WarmLimit.
func newFleet(t *testing.T, warmLimit int, pluginsA ...collage.Plugin) *fleet {
	t.Helper()
	f := &fleet{store: &keyRecorder{SharedCache: collagetest.NewSharedCache()}, name: "ada"}
	// The same templates on both: a fleet runs one build.
	templates := fstest.MapFS{"t/author.html": {Data: []byte(`<p>{{.}}</p>`)}}
	build := func(plugins []collage.Plugin) *collage.App {
		app, err := collage.New(&collage.Config{
			Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
			Template: collage.TemplateConfig{FS: templates, Root: "t"},
			Cache:    collage.CacheConfig{Enabled: true, Store: f.store, DefaultTTL: time.Hour, WarmLimit: warmLimit},
			Plugins:  plugins,
		})
		if err != nil {
			t.Fatal(err)
		}
		card := collage.NewFragment("author", "author.html").WithData(collage.LoadTagged(
			func(_ context.Context, rc *collage.RenderContext) (string, []string, error) {
				name, err := collage.Cached(rc, fleetAuthorKey, time.Hour, []string{"author"}, f.fetch)
				return name, nil, err
			})).
			Build()
		if err := app.RegisterPage(collage.NewPage("p").WithContent(card).WithPath("en", "/p/{slug}").Incremental(time.Hour).Build()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
		return app
	}
	f.a = build(pluginsA)
	f.b = build(nil)
	f.ha = f.a.Handler()
	f.hb = f.b.Handler()
	// Registered after the Shutdowns, so it runs before them: a held fetch is
	// let go, or Shutdown would wait on it.
	t.Cleanup(f.release)
	return f
}

func (f *fleet) fetch(context.Context) (string, error) {
	f.mu.Lock()
	name, gate, entered := f.name, f.gate, f.entered
	f.mu.Unlock()
	if gate != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}
	return name, nil
}

func (f *fleet) setName(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.name = name
}

// hold makes every author fetch wait until release.
func (f *fleet) hold() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = make(chan struct{})
	f.entered = make(chan struct{}, 1)
}

func (f *fleet) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gate != nil {
		close(f.gate)
		f.gate = nil
	}
}

func fleetGet(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/x", nil))
	return rec
}

// getHeld serves /p/x on h in the background while its fetch is held, and
// returns once the fetch has begun; the returned channel has the response after
// release.
func (f *fleet) getHeld(t *testing.T, h http.Handler) <-chan *httptest.ResponseRecorder {
	t.Helper()
	f.hold()
	f.mu.Lock()
	entered := f.entered
	f.mu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- fleetGet(h) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the render never reached its fetch")
	}
	return done
}

// The spec's Why: a webhook reaches A, and B's next render is built from the new
// data, not from the value B would otherwise still hold fresh.
func TestFleet_DataInvalidatedOnOneReachesTheOther(t *testing.T) {
	// Off: B's reader, not A's warm, is what this is about.
	f := newFleet(t, -1)
	for _, h := range []http.Handler{f.ha, f.hb} {
		if body := fleetGet(h).Body.String(); !strings.Contains(body, "ada") {
			t.Fatalf("before the change: %s, want ada", body)
		}
	}
	f.setName("grace")
	if _, err := f.a.InvalidateTagsN(context.Background(), "author"); err != nil {
		t.Fatal(err)
	}
	// A stale page served first is acceptable; within 2s B shows the new data.
	deadline := time.Now().Add(2 * time.Second)
	for {
		body := fleetGet(f.hb).Body.String()
		if strings.Contains(body, "grace") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("B after A's invalidation: %s, want grace", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A render on B that A's invalidation overlapped is not stored as fresh: A's
// next reader is not served it as a HIT.
func TestFleet_OverlapOnOtherInstanceNotFresh(t *testing.T) {
	f := newFleet(t, -1)
	done := f.getHeld(t, f.hb)
	f.setName("grace")
	if _, err := f.a.InvalidateTagsN(context.Background(), "author"); err != nil {
		t.Fatal(err)
	}
	f.release()
	<-done

	rec := fleetGet(f.ha)
	if rec.Header().Get(collage.CacheStatusHeader) == "HIT" && !strings.Contains(rec.Body.String(), "grace") {
		t.Errorf("A served B's overlapped render as a fresh HIT: %s", rec.Body.String())
	}
}

// A's invalidation names a page only B served, so a CDN purge on A purges it,
// and counts it.
func TestFleet_EventCoversPagesOnlyOtherServed(t *testing.T) {
	p := &entryRecorder{}
	f := newFleet(t, -1, p)
	fleetGet(f.hb)

	n, err := f.a.InvalidateTagsN(context.Background(), "author")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1: the page B wrote", n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entries) != 1 {
		t.Fatalf("events = %d, want 1", len(p.entries))
	}
	want := collage.InvalidatedEntry{Host: "example.com", Path: "/p/x"}
	if !slices.Contains(p.entries[0], want) {
		t.Errorf("entries = %+v, want them to include %+v", p.entries[0], want)
	}
}

// A warms a page only B served: the store holds it fresh again without another
// request.
func TestFleet_WarmsPagesOnlyOtherServed(t *testing.T) {
	f := newFleet(t, 0)
	fleetGet(f.hb)
	key := f.store.key(t)
	ctx := context.Background()
	f.setName("grace")
	if _, err := f.a.InvalidateTagsN(ctx, "author"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		content, _, ok := f.store.Get(ctx, key)
		if ok && strings.Contains(string(content), "grace") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("store after A's warm: %q fresh=%v, want grace held fresh", content, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A purge on the instance whose render it overlapped leaves nothing behind: the
// render's write is not stored, not even stale.
func TestFleet_LocalPurgeOverlappingRenderNotStored(t *testing.T) {
	// Off: a warm after the purge would write the page again.
	f := newFleet(t, -1)
	done := f.getHeld(t, f.ha)
	if err := f.a.PurgeTags(context.Background(), "author"); err != nil {
		t.Fatal(err)
	}
	f.release()
	<-done

	key := f.store.key(t)
	if _, _, _, found := f.store.GetStale(context.Background(), key); found {
		t.Errorf("the store holds %q after a purge overlapped its render", key)
	}
}

// A render on B that begins after A's invalidation, while a fetch that
// invalidation overtook is still running on B, joins that fetch rather than
// starting its own. What it is given is the old value, so its page must not be
// stored fresh: its own mark postdates the drop, and only the data being stale
// keeps it out. /p/y is another page key on the same data key, so the second
// render is not coalesced into the first one's page render.
func TestFleet_JoinerOfOvertakenFetchNotStoredFresh(t *testing.T) {
	f := newFleet(t, -1)
	first := f.getHeld(t, f.hb)
	f.setName("grace")
	if _, err := f.a.InvalidateTagsN(context.Background(), "author"); err != nil {
		t.Fatal(err)
	}
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		f.hb.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/y", nil))
		second <- rec
	}()
	// Nothing tells when the second render is waiting on the running fetch;
	// give it time to get there. Its body below says whether it did.
	time.Sleep(200 * time.Millisecond)
	f.release()
	<-first
	rec := <-second
	if !strings.Contains(rec.Body.String(), "ada") {
		t.Fatalf("the second render did not join the overtaken fetch (body %q); the test needs it to", rec.Body.String())
	}

	ctx := context.Background()
	f.store.mu.Lock()
	keys := slices.Clone(f.store.keys)
	f.store.mu.Unlock()
	for _, key := range keys {
		if content, _, ok := f.store.Get(ctx, key); ok && strings.Contains(string(content), "ada") {
			t.Errorf("the store holds %q fresh with the data A's invalidation replaced: %s", key, content)
		}
	}
}
