package httpx

import (
	"net/http"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// A middleware may return before serve does: http.TimeoutHandler answers 503
// and leaves the inner handler running on its own goroutine. serveGuarded and
// ServeHTTP then read the route and the status while serve is still writing
// them. Run under -race, this is the test that finds an unguarded one.
func TestHandler_MiddlewareReturningBeforeServeFinishesIsRaceFree(t *testing.T) {
	page := testPage("slow", "/slow", types.StrategyDynamic)
	inner := newFakeEngine(fakeRender{html: "<html>slow</html>"})
	blocking := newBlockingEngine(inner)
	done := make(chan struct{})

	env := newEnv(t, []*types.Page{page}, func(d *Deps) {
		d.Renderer = blocking
		d.Middleware = append(d.Middleware,
			func(next http.Handler) http.Handler {
				return http.TimeoutHandler(next, 20*time.Millisecond, "timed out")
			},
			func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(done)
					next.ServeHTTP(w, r)
				})
			},
		)
	})

	rec := env.get("/slow")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 from the timeout", rec.Code)
	}

	// The render is still running. Let it finish, and wait for serve to write
	// its status and route state, which is what the race detector checks
	// against the reads ServeHTTP made while it was blocked.
	close(blocking.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serve never finished after the timeout")
	}
}
