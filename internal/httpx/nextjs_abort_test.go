package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// Next.js e2e/on-request-error/client-abort: a reader who leaves is not an
// application failure — not an error-level log line, not an error hook call.
func TestNextjs_AnAbortedActionIsNotReportedAsAFailure(t *testing.T) {
	recorder := &recordingPlugin{}
	create := action("create", "/posts", []string{http.MethodPost},
		func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			return nil, ctx.Err()
		})
	env := actionEnv(t, nil, []*types.Action{create}, withPlugins(t, recorder))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := post("/posts", "title=x").WithContext(ctx)
	res := env.do(req)
	t.Logf("status %d", res.Code)
	for _, rec := range env.logs.recordsFor("collage: request failed") {
		if rec.level >= slog.LevelError {
			t.Errorf("an aborted action was logged at %v (stage %s)", rec.level, rec.stage)
		}
	}
	if errs := recorder.reportedErrors(); len(errs) != 0 {
		t.Errorf("ErrorHook received %v for a reader who left", errs)
	}
}

// cancelEngine fails a render with the request's own cancellation, as the
// real engine does when a required fragment's data handler is cut off.
type cancelEngine struct{ *fakeEngine }

func (e cancelEngine) Render(ctx context.Context, rc *types.RenderContext) (*render.Result, error) {
	<-ctx.Done()
	return &render.Result{Metadata: &render.Metadata{Page: rc.Page.Name}},
		fmt.Errorf("collage: execution stopped by context: %w", ctx.Err())
}

func TestNextjs_AnAbortedRenderIsNotReportedAsAFailure(t *testing.T) {
	recorder := &recordingPlugin{}
	page := testPage("live", "/live", types.StrategyDynamic)
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder),
		func(d *Deps) { d.Renderer = cancelEngine{newFakeEngine(fakeRender{})} })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := env.do(httptest.NewRequest(http.MethodGet, "/live", nil).WithContext(ctx))
	t.Logf("status %d", res.Code)
	for _, rec := range env.logs.recordsFor("collage: request failed") {
		if rec.level >= slog.LevelError {
			t.Errorf("an aborted render was logged at %v (stage %s)", rec.level, rec.stage)
		}
	}
	if errs := recorder.reportedErrors(); len(errs) != 0 {
		t.Errorf("ErrorHook received %v for a reader who left", errs)
	}
}

// A reader waiting on another's render who leaves: no response is written for
// them, and none with the status 0 that WriteHeader panics on.
func TestNextjs_AnAbortedWaiterDoesNotPanic(t *testing.T) {
	recorder := &recordingPlugin{}
	page := testPage("home", "/", types.StrategyIncremental)
	blocking := newBlockingEngine(newFakeEngine(fakeRender{html: "<html>home</html>"}))
	env := newEnv(t, []*types.Page{page}, withPlugins(t, recorder), func(d *Deps) { d.Renderer = blocking })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); env.get("/") }()
	<-blocking.started

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- env.do(httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)) }()
	awaitWaiters(t, env.handler.flight, flightKeyFor(t, env, "/"), 1)
	cancel()
	res := <-done
	close(blocking.release)
	wg.Wait()

	t.Logf("waiter status %d", res.Code)
	for _, rec := range env.logs.recordsFor("collage: request failed") {
		t.Logf("logged %v stage %s", rec.level, rec.stage)
		if rec.level >= slog.LevelError {
			t.Errorf("a waiter who left was logged at %v (stage %s)", rec.level, rec.stage)
		}
	}
	for _, err := range recorder.reportedErrors() {
		t.Errorf("ErrorHook received %.200v", err)
	}
}
