package collage_test

// RenderFragment's request is the fragment URL's own: the caller's context
// values do not reach its middleware, a middleware answering over the render is
// a refusal rather than an empty success, the run's state is safe from a
// middleware's goroutines, a middleware's SkipCache is honoured, and a
// middleware rendering a fragment for its own request stops.

import (
	"context"
	"errors"
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

type rfiPublic struct{}

// rfiPublicPaths marks a request under /_live/ public in its context, as an
// application's allow-list middleware might.
func rfiPublicPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_live") {
			r = r.WithContext(context.WithValue(r.Context(), rfiPublic{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

// rfiAuth answers 401 to a request without credentials, unless the context
// says the path is public.
func rfiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public, _ := r.Context().Value(rfiPublic{}).(bool); !public && r.Header.Get("Authorization") != "Bearer ok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rfiAllow(context.Context, *http.Request) (*collage.GuardDecision, error) { return nil, nil }

// A value the caller's context carries — set by the stream path's middleware —
// does not reach the fragment request's middleware: it decides on what its own
// chain puts there, as for a request to the URL.
func TestRenderFragment_CallerContextValuesDoNotReachChain(t *testing.T) {
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{rfiPublicPaths, rfiAuth}}
	site := collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(rfeBoards(rfiAllow)))
	site.Get("/boards/x/live").WantStatus(http.StatusUnauthorized)

	stream := httptest.NewRequest(http.MethodGet, "/_live/stream", nil)
	stream = stream.WithContext(context.WithValue(stream.Context(), rfiPublic{}, true))
	got, err := p.host.RenderFragment(stream, collage.FragmentRequest{Path: "/boards/x/live"})
	if !errors.Is(err, collage.ErrGuardRefused) || !strings.Contains(fmtErr(err), "401") {
		t.Errorf("RenderFragment = %q, %v; want ErrGuardRefused naming 401", rfeHTML(got), err)
	}

	// The caller's cancellation still reaches the render.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	authed := httptest.NewRequest(http.MethodGet, "/_live/stream", nil).WithContext(ctx)
	authed.Header.Set("Authorization", "Bearer ok")
	if got, err := p.host.RenderFragment(authed, collage.FragmentRequest{Path: "/boards/x/live"}); err == nil && got == nil {
		t.Error("RenderFragment for a cancelled caller returned nil, nil")
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// rfiStream mounts /_live/ and renders the fragment ?f= names for the stream's
// own request, as collage-live does.
type rfiStream struct {
	rfePlugin
	mu   sync.Mutex
	got  *collage.FragmentRender
	err  error
	done bool
}

func (p *rfiStream) Init(ctx context.Context, h collage.Host) error {
	if err := p.rfePlugin.Init(ctx, h); err != nil {
		return err
	}
	return h.Handle("/_live/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := h.RenderFragment(r, collage.FragmentRequest{Path: r.URL.Query().Get("f")})
		p.mu.Lock()
		p.got, p.err, p.done = got, err, true
		p.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
}

// End to end: the stream's request passed its own chain, which marked it
// public; the fragment request passes the chain again and is not public.
func TestRenderFragment_StreamContextDoesNotReachFragmentChain(t *testing.T) {
	p := &rfiStream{rfePlugin: rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{rfiPublicPaths, rfiAuth}}}
	site := collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(rfeBoards(rfiAllow)))
	site.Get("/boards/x/live").WantStatus(http.StatusUnauthorized)
	site.Get("/_live/stream?f=/boards/x/live").WantStatus(http.StatusNoContent)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done {
		t.Fatal("the stream handler was not reached")
	}
	if !errors.Is(p.err, collage.ErrGuardRefused) {
		t.Errorf("HTTP answers 401; RenderFragment through the stream = %q, %v", rfeHTML(p.got), p.err)
	}
}

// rfiRecoverer answers 500 to a panic below it, as a recovery middleware does.
func rfiRecoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				http.Error(w, "oops", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// A panicking guard under a recovering middleware: the render was reached and
// never finished, so RenderFragment reports the panic. It used to return nil,
// nil, which a plugin dereferences.
func TestRenderFragment_RecoveredPanicIsError(t *testing.T) {
	guard := func(context.Context, *http.Request) (*collage.GuardDecision, error) { panic("guard boom") }
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{rfiRecoverer}}
	collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(rfeBoards(guard)))
	got, err := p.host.RenderFragment(httptest.NewRequest(http.MethodGet, "/_live", nil), collage.FragmentRequest{Path: "/boards/x/live"})
	if got != nil || !errors.Is(err, collage.ErrPanic) {
		t.Errorf("RenderFragment = %v, %v; want nil and ErrPanic", got, err)
	}
}

// A http.TimeoutHandler answers 503 and returns while the render still runs:
// a refusal naming 503, and RenderFragment returns only once the render has.
func TestRenderFragment_TimeoutHandlerIsRefused(t *testing.T) {
	var running atomic.Int32
	timeout := func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, 20*time.Millisecond, "slow")
	}
	slow := collage.NewFragment("board", "board.html").WithGuard(rfiAllow).WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			running.Add(1)
			defer running.Add(-1)
			time.Sleep(80 * time.Millisecond)
			return rc.Param("id"), nil
		})).Build()
	page := collage.NewPage("board").WithContent(slow).WithPath("en", "/boards/{id}").
		WithFragmentPath("en", "/boards/{id}/live", slow).Dynamic().Build()
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{timeout}}
	site := collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(page))
	site.Get("/boards/x/live").WantStatus(http.StatusServiceUnavailable)
	got, err := p.host.RenderFragment(site.Request(http.MethodGet, "/_live", nil), collage.FragmentRequest{Path: "/boards/x/live"})
	if got != nil || !errors.Is(err, collage.ErrGuardRefused) || !strings.Contains(fmtErr(err), "503") {
		t.Errorf("RenderFragment = %v, %v; want nil and ErrGuardRefused naming 503", got, err)
	}
	if n := running.Load(); n != 0 {
		t.Errorf("RenderFragment returned with %d renders still running", n)
	}
}

// A middleware calling next from a goroutine of its own: one that waits for it
// renders, as over HTTP; one that returns first answered with nothing, and the
// late next renders nothing. Run under -race, the run's state is safe.
func TestRenderFragment_NextFromGoroutine(t *testing.T) {
	var late sync.WaitGroup
	async := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("X-Mode") {
			case "wait":
				var wg sync.WaitGroup
				wg.Add(1)
				go func() { defer wg.Done(); next.ServeHTTP(w, r) }()
				wg.Wait()
			case "late":
				late.Add(1)
				go func() {
					defer late.Done()
					time.Sleep(20 * time.Millisecond)
					next.ServeHTTP(w, r)
				}()
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
	var renders atomic.Int32
	board := collage.NewFragment("board", "board.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) {
			renders.Add(1)
			return rc.Param("id"), nil
		})).Build()
	page := collage.NewPage("board").WithContent(board).WithPath("en", "/boards/{id}").
		WithFragmentPath("en", "/boards/{id}/live", board).Dynamic().Build()
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{async}}
	collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(page))

	waiting := httptest.NewRequest(http.MethodGet, "/_live", nil)
	waiting.Header.Set("X-Mode", "wait")
	if got, err := p.host.RenderFragment(waiting, collage.FragmentRequest{Path: "/boards/x/live"}); err != nil || rfeHTML(got) != "board x" {
		t.Errorf("next from a goroutine the middleware waits for = %q, %v", rfeHTML(got), err)
	}

	renders.Store(0)
	early := httptest.NewRequest(http.MethodGet, "/_live", nil)
	early.Header.Set("X-Mode", "late")
	got, err := p.host.RenderFragment(early, collage.FragmentRequest{Path: "/boards/x/live"})
	late.Wait()
	if got != nil || !errors.Is(err, collage.ErrGuardRefused) {
		t.Errorf("next after the middleware returned = %v, %v; want nil and ErrGuardRefused", got, err)
	}
	if n := renders.Load(); n != 0 {
		t.Errorf("a next called after the chain returned rendered %d times", n)
	}
}

type rfiDraft struct{}

// A middleware's SkipCache — a preview cookie — makes a request to the URL
// render for its reader; RenderFragment renders the same, and does not call it
// Shared.
func TestRenderFragment_SkipCacheIsNotShared(t *testing.T) {
	preview := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie("preview"); err == nil && c.Value == "1" {
				if err := collage.SkipCache(r); err != nil {
					t.Error(err)
				}
				r = r.WithContext(context.WithValue(r.Context(), rfiDraft{}, true))
			}
			next.ServeHTTP(w, r)
		})
	}
	post := collage.NewFragment("post", "post.html").WithData(collage.Load(
		func(ctx context.Context, _ *collage.RenderContext) (string, error) {
			if draft, _ := ctx.Value(rfiDraft{}).(bool); draft {
				return "DRAFT", nil
			}
			return "published", nil
		})).Build()
	page := collage.NewPage("post").WithContent(post).WithPath("en", "/post").
		WithFragmentPath("en", "/post/live", post).Incremental(time.Minute).Build()
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{preview}}
	site := collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"post.html": `{{.}}`}),
		collagetest.WithRegister(page))

	stream := httptest.NewRequest(http.MethodGet, "/_live", nil)
	stream.AddCookie(&http.Cookie{Name: "preview", Value: "1"})
	got, err := p.host.RenderFragment(stream, collage.FragmentRequest{Path: "/post/live"})
	if err != nil {
		t.Fatal(err)
	}
	req := site.Request(http.MethodGet, "/post/live", nil)
	req.AddCookie(&http.Cookie{Name: "preview", Value: "1"})
	res := site.Do(req)
	if string(got.HTML) != res.Body || got.Shared {
		t.Errorf("RenderFragment = %q, Shared %v; HTTP answered %q", got.HTML, got.Shared, res.Body)
	}
	if !strings.Contains(string(got.HTML), "DRAFT") {
		t.Errorf("a preview reader was rendered %q", got.HTML)
	}
}

// A middleware that renders a fragment for the request it was handed — the
// fragment request itself — would pass through itself until the stack ran out.
// A few levels deep it is refused, and the renders around it go on.
func TestRenderFragment_MiddlewareRecursionStops(t *testing.T) {
	var p *rfePlugin
	var mu sync.Mutex
	var errs []error
	recursive := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/live") {
				_, err := p.host.RenderFragment(r, collage.FragmentRequest{Path: r.URL.Path})
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			next.ServeHTTP(w, r)
		})
	}
	p = &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{recursive}}
	collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(rfeBoards(rfiAllow)))
	got, err := p.host.RenderFragment(httptest.NewRequest(http.MethodGet, "/_live", nil), collage.FragmentRequest{Path: "/boards/x/live"})
	if err != nil || rfeHTML(got) != "board x" {
		t.Errorf("RenderFragment = %q, %v", rfeHTML(got), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) == 0 || len(errs) > 8 || errs[0] == nil || !strings.Contains(errs[0].Error(), "middleware of a fragment it is rendering") {
		t.Errorf("%d nested renders, the deepest answered %v", len(errs), errs)
	}
	for _, err := range errs[1:] {
		if err != nil {
			t.Errorf("a render within the limit = %v", err)
		}
	}
}
