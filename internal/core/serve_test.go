package core

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/plugin"
)

// serveSpy is a minimal plugin that records each OnServe call and the ctx it
// was given, or panics on it.
type serveSpy struct {
	panics bool
	served atomic.Int32
	once   sync.Once
	called chan struct{}
	mu     sync.Mutex
	ctx    context.Context
}

func newServeSpy() *serveSpy { return &serveSpy{called: make(chan struct{})} }

func (*serveSpy) Name() string                            { return "serve-spy" }
func (*serveSpy) Version() string                         { return "1.0.0" }
func (*serveSpy) Init(context.Context, plugin.Host) error { return nil }
func (*serveSpy) Shutdown(context.Context) error          { return nil }
func (s *serveSpy) OnServe(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	s.served.Add(1)
	s.once.Do(func() { close(s.called) })
	if s.panics {
		panic("serve spy panicked")
	}
}

// waitCalled waits for OnServe and returns the ctx it was given.
func (s *serveSpy) waitCalled(t *testing.T) context.Context {
	t.Helper()
	select {
	case <-s.called:
	case <-time.After(5 * time.Second):
		t.Fatal("OnServe was never called")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctx
}

// waitDone fails the test unless ctx is done within d.
func waitDone(t *testing.T, ctx context.Context, d time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(d):
		t.Fatalf("the OnServe ctx was not cancelled within %v of the drain starting", d)
	}
}

// getOK fails the test unless GET url answers 200.
func getOK(t *testing.T, url string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, res.StatusCode)
	}
}

// ListenAndServe calls OnServe once, after it listens, with a ctx that stays
// live while it serves.
func TestServe_OnceInListenAndServe(t *testing.T) {
	spy := newServeSpy()
	app, base, served := startServing(t, nil, spy)
	ctx := spy.waitCalled(t)
	getOK(t, base+"/")
	if err := ctx.Err(); err != nil {
		t.Fatalf("OnServe ctx done while serving: %v", err)
	}
	if n := spy.served.Load(); n != 1 {
		t.Fatalf("OnServe called %d times, want 1", n)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitServed(t, served)
	if n := spy.served.Load(); n != 1 {
		t.Fatalf("OnServe called %d times after shutdown, want 1", n)
	}
}

// Shutdown cancels the OnServe ctx when the drain starts, not when it ends:
// the server still serves.
func TestServe_CtxCancelledAtDrain(t *testing.T) {
	spy := newServeSpy()
	app, base, served := startServing(t, func(c *Config) { c.Server.DrainDelay = 300 * time.Millisecond }, spy)
	ctx := spy.waitCalled(t)

	shut := make(chan error, 1)
	go func() { shut <- app.Shutdown(context.Background()) }()
	waitDone(t, ctx, 100*time.Millisecond)
	getOK(t, base+"/")
	select {
	case err := <-shut:
		t.Fatalf("Shutdown returned (%v) before the 300ms drain ended", err)
	default:
	}
	if err := <-shut; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitServed(t, served)
}

// A SIGTERM cancels the OnServe ctx when the drain starts, too.
func TestServe_CtxCancelledOnSignal(t *testing.T) {
	spy := newServeSpy()
	_, base, served := startServing(t, func(c *Config) {
		c.Server.DrainDelay = 300 * time.Millisecond
		c.Server.ShutdownTimeout = time.Second
	}, spy)
	ctx := spy.waitCalled(t)
	signalSelf(t)
	waitDone(t, ctx, 100*time.Millisecond)
	getOK(t, base+"/")
	if err := waitServed(t, served); err != nil {
		t.Fatalf("ListenAndServe = %v, want nil", err)
	}
}

// Start, Handler and a Shutdown before ListenAndServe never call OnServe, nor
// does the ListenAndServe that Shutdown has already stopped.
func TestServe_NotCalledWithoutServing(t *testing.T) {
	spy := newServeSpy()
	app := newTestApp(t, func(c *Config) { c.Plugins = append(c.Plugins, spy) })
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if app.Handler() == nil {
		t.Fatal("Handler is nil")
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := app.ListenAndServe(); err != nil {
		t.Fatalf("ListenAndServe after Shutdown = %v, want nil", err)
	}
	if n := spy.served.Load(); n != 0 {
		t.Fatalf("OnServe called %d times without serving, want 0", n)
	}
}

// A panicking OnServe is contained: the server still serves.
func TestServe_PanicContained(t *testing.T) {
	spy := newServeSpy()
	spy.panics = true
	app, base, served := startServing(t, nil, spy)
	spy.waitCalled(t)
	getOK(t, base+"/")
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitServed(t, served)
}

// A drain that ran before ListenAndServe reached its server — a Shutdown racing
// it, which drains before it marks the App closing — leaves nothing to start:
// OnServe is not called with a ctx no later drain would cancel.
func TestServe_NotCalledAfterAnEarlierDrain(t *testing.T) {
	spy := newServeSpy()
	app := newTestApp(t, func(c *Config) { c.Plugins = append(c.Plugins, spy) })
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	app.drain(context.Background()) // the racing Shutdown's drain, before closing is set
	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()
	waitListening(t, app)
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitServed(t, served)
	if n := spy.served.Load(); n != 0 {
		t.Fatalf("OnServe called %d times after the drain, want 0", n)
	}
}
