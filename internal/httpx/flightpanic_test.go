package httpx

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A render that panics must not leave its key registered. The leader's panic is
// recovered further up (serveGuarded); if the flight kept the call, done would
// never close and every later miss on the key would join the dead render and
// block until restart.
func TestFlight_PanickingRenderDoesNotWedgeKey(t *testing.T) {
	f := newFlight()

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the leader's panic was swallowed, serveGuarded would not see it")
			}
		}()
		f.do(context.Background(), "k", func() *outcome { panic("boom") })
	}()

	if n := f.inFlight(); n != 0 {
		t.Fatalf("in flight after a panic = %d, want 0", n)
	}
	done := make(chan *outcome, 1)
	go func() {
		out, _ := f.do(context.Background(), "k", func() *outcome { return &outcome{content: []byte("ok")} })
		done <- out
	}()
	select {
	case out := <-done:
		if string(out.content) != "ok" {
			t.Fatalf("content = %q, want a fresh render", out.content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a later render of the key blocked on the panicked one")
	}
}

// A waiter that joined the render that panicked gets a 500 outcome, not a nil
// to dereference.
func TestFlight_WaiterOfPanickedRenderGets500(t *testing.T) {
	f := newFlight()
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		defer func() { _ = recover() }()
		f.do(context.Background(), "k", func() *outcome {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started
	res := make(chan *outcome, 1)
	go func() {
		out, _ := f.do(context.Background(), "k", func() *outcome { return &outcome{} })
		res <- out
	}()
	awaitWaiters(t, f, "k", 1)
	close(release)
	out := <-res
	if out == nil || out.fail == nil || out.fail.status != http.StatusInternalServerError {
		t.Fatalf("waiter outcome = %+v, want a 500 failure", out)
	}
}
