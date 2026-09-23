package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// awaitWaiters spins until n requests have joined the render of key.
//
// A spin rather than a sleep: every waiter is a runnable goroutine, so the
// condition is certain to arrive, and spinning reaches it as soon as it does
// instead of guessing how long to wait. A sleep would be slower when it worked and
// silently wrong when it did not.
//
// The deadline is not a timing assumption — the condition arrives in microseconds —
// but a failure mode. Without it, a build where requests no longer coalesce spins
// here until the whole package times out, and reports that as a timeout in some
// test rather than as this one saying what it wanted.
func awaitWaiters(t *testing.T, f *flight, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for f.waitingOn(key) < n {
		if time.Now().After(deadline) {
			t.Fatalf("waiters on %q = %d, want %d: requests are not coalescing",
				key, f.waitingOn(key), n)
		}
		runtime.Gosched()
	}
}

// The synchronisation in these tests carries no sleeps, and that is deliberate:
// once the leader's fn has begun, the key is registered, so every caller that
// starts after that point is guaranteed to find it. A test that waited a while and
// hoped would pass on a busy machine for the wrong reason.

func TestFlight_OneRenderServesEveryWaiter(t *testing.T) {
	f := newFlight()
	var calls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})

	produce := func() *outcome {
		calls.Add(1)
		close(started)
		<-release
		return &outcome{content: []byte("rendered"), etag: `"e"`}
	}

	var leaderOut *outcome
	var leaderShared bool
	leaderDone := make(chan struct{})
	go func() {
		leaderOut, leaderShared = f.do(context.Background(), "k", produce)
		close(leaderDone)
	}()
	<-started

	const waiters = 16
	outs := make([]*outcome, waiters)
	shared := make([]bool, waiters)
	joined := sync.WaitGroup{}
	joined.Add(waiters)
	for i := range waiters {
		go func() {
			defer joined.Done()
			outs[i], shared[i] = f.do(context.Background(), "k", func() *outcome {
				calls.Add(1)
				return &outcome{content: []byte("second render")}
			})
		}()
	}

	awaitWaiters(t, f, "k", waiters)
	close(release)
	joined.Wait()
	<-leaderDone

	if got := calls.Load(); got != 1 {
		t.Fatalf("renders = %d, want 1", got)
	}
	if leaderShared {
		t.Error("leader reported a shared outcome; it produced the outcome itself")
	}
	for i := range waiters {
		if !shared[i] {
			t.Errorf("waiter %d reported it produced the outcome itself", i)
		}
		if outs[i] != leaderOut {
			t.Errorf("waiter %d got a different outcome than the leader produced", i)
		}
	}
}

func TestFlight_DifferentKeysDoNotWaitOnEachOther(t *testing.T) {
	f := newFlight()
	started := make(chan struct{})
	release := make(chan struct{})

	go f.do(context.Background(), "slow", func() *outcome {
		close(started)
		<-release
		return &outcome{}
	})
	<-started

	// Would block forever if the flight were keyed by anything coarser than the
	// cache key.
	out, shared := f.do(context.Background(), "other", func() *outcome {
		return &outcome{content: []byte("own")}
	})
	close(release)

	if shared {
		t.Error("a different key waited on an unrelated render")
	}
	if string(out.content) != "own" {
		t.Errorf("content = %q, want %q", out.content, "own")
	}
}

func TestFlight_KeyIsReleasedAfterwards(t *testing.T) {
	f := newFlight()
	var calls atomic.Int64
	produce := func() *outcome {
		calls.Add(1)
		return &outcome{}
	}

	f.do(context.Background(), "k", produce)
	f.do(context.Background(), "k", produce)

	if got := calls.Load(); got != 2 {
		t.Fatalf("renders = %d, want 2: a completed flight must not answer later requests", got)
	}
	if n := f.inFlight(); n != 0 {
		t.Errorf("inFlight() = %d, want 0", n)
	}
}

// A leader whose own request was cancelled must not hand its failure to everyone
// waiting behind it. One reader pressing stop would otherwise turn into an error
// page for every reader who happened to ask at the same moment.
func TestFlight_CancelledLeaderDoesNotPoisonWaiters(t *testing.T) {
	f := newFlight()
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	started := make(chan struct{})
	var calls atomic.Int64

	go f.do(leaderCtx, "k", func() *outcome {
		calls.Add(1)
		close(started)
		<-leaderCtx.Done()
		return &outcome{fail: &failure{err: leaderCtx.Err()}}
	})
	<-started

	waiterDone := make(chan *outcome)
	go func() {
		out, _ := f.do(context.Background(), "k", func() *outcome {
			calls.Add(1)
			return &outcome{content: []byte("mine")}
		})
		waiterDone <- out
	}()
	awaitWaiters(t, f, "k", 1)

	cancelLeader()
	out := <-waiterDone

	if out.fail != nil {
		t.Fatalf("waiter inherited the leader's failure: %v", out.fail.err)
	}
	if string(out.content) != "mine" {
		t.Errorf("content = %q, want %q", out.content, "mine")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("renders = %d, want 2: the waiter must render for itself", got)
	}
}

// A waiter whose own request is cancelled gives up rather than holding a goroutine
// for as long as the leader takes.
func TestFlight_WaiterGivesUpWhenItsOwnRequestEnds(t *testing.T) {
	f := newFlight()
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	go f.do(context.Background(), "k", func() *outcome {
		close(started)
		<-release
		return &outcome{content: []byte("late")}
	})
	<-started

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()

	out, _ := f.do(waiterCtx, "k", func() *outcome {
		t.Error("a cancelled waiter must not start a render of its own")
		return &outcome{}
	})

	if out.fail == nil {
		t.Fatal("outcome.fail = nil, want the waiter's own cancellation")
	}
	if !errors.Is(out.fail.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", out.fail.err)
	}
}

// ---------------------------------------------------------------------------
// Through the handler
// ---------------------------------------------------------------------------

// blockingEngine is a render.Engine that holds the first render open until the
// test lets it go, so the requests behind it are provably concurrent with it
// rather than merely launched at the same time.
type blockingEngine struct {
	inner   *fakeEngine
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func newBlockingEngine(inner *fakeEngine) *blockingEngine {
	return &blockingEngine{
		inner:   inner,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingEngine) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.inner.Render(ctx, rc)
}

func TestHandler_ConcurrentMissesRenderOnce(t *testing.T) {
	page := testPage("home", "/", types.StrategyIncremental)
	inner := newFakeEngine(fakeRender{html: "<html>home</html>"})
	blocking := newBlockingEngine(inner)

	env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Renderer = blocking })

	const readers = 12
	bodies := make([]string, readers)
	statuses := make([]int, readers)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		rec := env.get("/")
		bodies[0], statuses[0] = rec.Body.String(), rec.Code
	}()
	<-blocking.started

	for i := 1; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := env.get("/")
			bodies[i], statuses[i] = rec.Body.String(), rec.Code
		}()
	}
	// Every reader but the first is now either waiting on the flight or about to
	// be; releasing before they have all joined is safe, because one that arrives
	// after the render completes finds the entry in the cache. What must not
	// happen — and is what this test is about — is a second render.
	awaitWaiters(t, env.handler.flight, flightKeyFor(t, env, "/"), readers-1)
	close(blocking.release)
	wg.Wait()

	if got := inner.totalCalls(); got != 1 {
		t.Fatalf("renders = %d, want 1 for %d concurrent requests", got, readers)
	}
	for i := range readers {
		if statuses[i] != http.StatusOK {
			t.Errorf("reader %d: status = %d, want 200", i, statuses[i])
		}
		if bodies[i] != "<html>home</html>" {
			t.Errorf("reader %d: body = %q, want the rendered page", i, bodies[i])
		}
	}
}

// A page that is not cacheable has no key to coalesce on, and coalescing it would
// be wrong anyway: two requests for a page declared dynamic are two renders by the
// page's own declaration.
func TestHandler_DynamicPagesAreNotCoalesced(t *testing.T) {
	page := testPage("live", "/live", types.StrategyDynamic)
	inner := newFakeEngine(fakeRender{html: "<html>live</html>"})
	blocking := newBlockingEngine(inner)

	env := newEnv(t, []*types.Page{page}, func(d *Deps) { d.Renderer = blocking })

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.get("/live")
		}()
	}
	<-blocking.started
	close(blocking.release)
	wg.Wait()

	if got := inner.totalCalls(); got != 2 {
		t.Fatalf("renders = %d, want 2: a dynamic page must render per request", got)
	}
}

// flightKeyFor is the cache key the handler will use for path, derived the same way
// the handler derives it so the test is not asserting against a key it invented.
func flightKeyFor(t *testing.T, env *testEnv, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	match, err := env.router.Match(req)
	if err != nil || match == nil || match.Page == nil {
		t.Fatalf("Match(%q) = %v, %v", path, match, err)
	}
	return cache.Key(cache.KeyInput{
		Path:   req.URL.Path,
		Locale: match.Locale,
		Params: match.PathParams,
		Vary:   queryVary(req.URL, match.Page.CacheParams),
	})
}
