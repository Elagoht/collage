package collage_test

// RenderFragment reuses a Shared render for every reader the chain and the
// guards let through, so a change pushed to many readers is rendered once; an
// invalidation of what it depends on makes the next render fresh, a refused
// reader never gets it, and every PersonaliseHook still runs for each reader.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// memoCounter is a Shared, tagged data handler that counts its runs and
// renders the version it is at.
type memoCounter struct {
	runs    atomic.Int32
	version atomic.Int32
	delay   time.Duration
}

func (c *memoCounter) fragment(guard collage.GuardFunc) *collage.Fragment {
	b := collage.NewFragment("score", "score.html").Shared().WithData(collage.LoadTagged(
		func(context.Context, *collage.RenderContext) (string, []string, error) {
			c.runs.Add(1)
			// Read before the wait: a render overlapped by a change holds
			// the value from before it.
			version := c.version.Load()
			time.Sleep(c.delay)
			return fmt.Sprintf("v%d", version), []string{"score"}, nil
		}))
	if guard != nil {
		b = b.WithGuard(guard)
	}
	return b.Build()
}

// memoSite is a Dynamic page at /match opening its Shared score fragment at
// /match/score, behind middleware.
func memoSite(t *testing.T, c *memoCounter, guard collage.GuardFunc, plugins []collage.Plugin, edit func(*collage.Config)) (*collagetest.Site, *rfePlugin) {
	t.Helper()
	p := &rfePlugin{name: "test/live"}
	score := c.fragment(guard)
	page := collage.NewPage("match").WithContent(score).WithPath("en", "/match").
		WithFragmentPath("en", "/match/score", score).Dynamic().Build()
	options := []collagetest.SiteOption{
		collagetest.WithPlugins(append([]collage.Plugin{p}, plugins...)...),
		collagetest.WithTemplates(map[string]string{"score.html": `score {{.}}`}),
		collagetest.WithRegister(page),
	}
	if edit != nil {
		options = append(options, collagetest.WithConfig(edit))
	}
	return collagetest.NewSite(t, options...), p
}

func memoReader(name string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/_live/stream", nil)
	r.Header.Set("X-Reader", name)
	return r
}

// readAll renders /match/score for n readers at once.
func readAll(t *testing.T, p *rfePlugin, n int) []*collage.FragmentRender {
	t.Helper()
	out := make([]*collage.FragmentRender, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = p.host.RenderFragment(memoReader(fmt.Sprint("r", i)), collage.FragmentRequest{Path: "/match/score"})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
	return out
}

// N readers at once, after each change: one data-handler run per change, and
// every reader the new value.
func TestRenderFragment_SharedRenderedOncePerChange(t *testing.T) {
	c := &memoCounter{delay: 20 * time.Millisecond}
	site, p := memoSite(t, c, nil, nil, nil)
	const readers = 32
	for change := range 3 {
		c.version.Store(int32(change))
		if change > 0 {
			if err := site.App.InvalidateTags(context.Background(), "score"); err != nil {
				t.Fatal(err)
			}
		}
		c.runs.Store(0)
		renders := readAll(t, p, readers)
		if n := c.runs.Load(); n != 1 {
			t.Errorf("change %d: %d readers ran the data handler %d times, want 1", change, readers, n)
		}
		want := fmt.Sprintf("score v%d", change)
		for i, r := range renders {
			if string(r.HTML) != want || !r.Shared {
				t.Fatalf("change %d, reader %d: %q Shared %v, want %q", change, i, r.HTML, r.Shared, want)
			}
		}
		// What a reader does to its copy reaches no other reader.
		renders[0].HTML[0] = 'X'
		renders[0].DependencyTags[0] = "mine"
		again := readAll(t, p, 1)[0]
		if string(again.HTML) != want || again.DependencyTags[0] == "mine" {
			t.Fatalf("a reader's copy changed the kept render: %q %v", again.HTML, again.DependencyTags)
		}
	}
}

// An invalidation by the fragment URL's path drops the kept render too.
func TestRenderFragment_SharedDroppedByPathTag(t *testing.T) {
	c := &memoCounter{}
	site, p := memoSite(t, c, nil, nil, nil)
	readAll(t, p, 1)
	c.version.Store(7)
	if err := site.App.InvalidateTags(context.Background(), collage.PathTag("/match/score")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, p, 1)[0]; string(got.HTML) != "score v7" {
		t.Errorf("after invalidating the path: %q", got.HTML)
	}
}

// A render under way as its tags are invalidated is handed to the readers who
// asked before the invalidation, and kept for nobody after it.
func TestRenderFragment_SharedOverlappedByInvalidationIsNotKept(t *testing.T) {
	c := &memoCounter{delay: 50 * time.Millisecond}
	site, p := memoSite(t, c, nil, nil, nil)
	done := make(chan *collage.FragmentRender)
	go func() { done <- readAll(t, p, 1)[0] }()
	time.Sleep(15 * time.Millisecond)
	c.version.Store(1)
	if err := site.App.InvalidateTags(context.Background(), "score"); err != nil {
		t.Fatal(err)
	}
	<-done
	c.delay = 0
	if got := readAll(t, p, 1)[0]; string(got.HTML) != "score v1" {
		t.Errorf("after an invalidation that overlapped a render: %q, want the new value", got.HTML)
	}
}

// A reader the middleware or the guard refuses gets no render, kept or not.
func TestRenderFragment_SharedNotHandedToRefusedReader(t *testing.T) {
	ban := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Reader") == "banned" {
				http.Error(w, "banned", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	guard := func(_ context.Context, r *http.Request) (*collage.GuardDecision, error) {
		if r.Header.Get("X-Reader") == "unpaid" {
			return &collage.GuardDecision{Status: http.StatusPaymentRequired}, nil
		}
		return nil, nil
	}
	c := &memoCounter{}
	banned := &rfePlugin{name: "test/ban", middleware: []func(http.Handler) http.Handler{ban}}
	_, p := memoSite(t, c, guard, []collage.Plugin{banned}, nil)
	if got := readAll(t, p, 1)[0]; string(got.HTML) != "score v0" {
		t.Fatalf("an allowed reader: %q", got.HTML)
	}
	for _, name := range []string{"banned", "unpaid"} {
		got, err := p.host.RenderFragment(memoReader(name), collage.FragmentRequest{Path: "/match/score"})
		if got != nil || !errors.Is(err, collage.ErrGuardRefused) {
			t.Errorf("%s reader with a render kept: %v, %v; want nil and ErrGuardRefused", name, got, err)
		}
	}
}

// memoGreeter is a PersonaliseHook that writes the reader's name over a marker.
type memoGreeter struct{ rfePlugin }

func (g *memoGreeter) OnPersonalise(_ context.Context, ev *collage.PersonaliseEvent) error {
	if !bytes.Contains(ev.Body, []byte("score")) {
		return nil
	}
	ev.Body = append(ev.Body, []byte(" for "+ev.Request.Header.Get("X-Reader"))...)
	ev.Personal = true
	return nil
}

// The hooks run on each reader's copy of the one render: each reader gets their
// own, none of it Shared, and the data handler runs once.
func TestRenderFragment_SharedPersonalisedPerReader(t *testing.T) {
	c := &memoCounter{}
	_, p := memoSite(t, c, nil, []collage.Plugin{&memoGreeter{rfePlugin{name: "test/greet"}}}, nil)
	renders := readAll(t, p, 4)
	for i, r := range renders {
		want := fmt.Sprintf("score v0 for r%d", i)
		if string(r.HTML) != want || r.Shared {
			t.Errorf("reader %d: %q Shared %v, want %q and not Shared", i, r.HTML, r.Shared, want)
		}
	}
	if n := c.runs.Load(); n != 1 {
		t.Errorf("the data handler ran %d times for 4 readers, want 1", n)
	}
}

// In development nothing is kept: a template edit shows on the next push.
func TestRenderFragment_SharedNotKeptInDevelopment(t *testing.T) {
	c := &memoCounter{}
	_, p := memoSite(t, c, nil, nil, func(cfg *collage.Config) { cfg.DevMode = true })
	for range 3 {
		readAll(t, p, 1)
	}
	if n := c.runs.Load(); n != 3 {
		t.Errorf("in development, 3 renders ran the data handler %d times", n)
	}
}

// A reader whose request a middleware marked SkipCache renders for themselves,
// and is not handed the kept render.
func TestRenderFragment_SharedNotUsedWhenSkippingCache(t *testing.T) {
	preview := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Reader") == "editor" {
				if err := collage.SkipCache(r); err != nil {
					t.Error(err)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	c := &memoCounter{}
	_, p := memoSite(t, c, nil, []collage.Plugin{&rfePlugin{name: "test/preview", middleware: []func(http.Handler) http.Handler{preview}}}, nil)
	readAll(t, p, 1)
	c.version.Store(1)
	got, err := p.host.RenderFragment(memoReader("editor"), collage.FragmentRequest{Path: "/match/score"})
	if err != nil || got.Shared || !strings.Contains(string(got.HTML), "v1") {
		t.Errorf("a reader skipping the cache: %v, %v; want a fresh render, not Shared", got, err)
	}
}
