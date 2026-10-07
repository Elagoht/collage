package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/plugin"
)

// drainSpy is a minimal plugin that counts how often it heard of the drain.
type drainSpy struct {
	drained atomic.Int32
}

func (*drainSpy) Name() string                            { return "drain-spy" }
func (*drainSpy) Version() string                         { return "1.0.0" }
func (*drainSpy) Init(context.Context, plugin.Host) error { return nil }
func (*drainSpy) Shutdown(context.Context) error          { return nil }
func (d *drainSpy) OnDrain()                              { d.drained.Add(1) }

// startServing builds an App with plugins, serves it on a kernel-chosen port and
// returns it with its base URL and the channel ListenAndServe reports to.
func startServing(t *testing.T, mutate func(*Config), plugins ...plugin.Plugin) (*App, string, chan error) {
	t.Helper()
	app := newTestApp(t, func(c *Config) {
		c.Plugins = append(c.Plugins, plugins...)
		if mutate != nil {
			mutate(c)
		}
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()
	waitListening(t, app)
	app.mu.Lock()
	addr := app.listenAddr.String()
	app.mu.Unlock()
	return app, "http://" + addr, served
}

// waitServed waits for ListenAndServe to return, or fails the test.
func waitServed(t *testing.T, served chan error) error {
	t.Helper()
	select {
	case err := <-served:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return")
		return nil
	}
}

func signalSelf(t *testing.T) {
	t.Helper()
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := self.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
}

// During the drain the server still answers, and tells a kept-alive client to
// reconnect; OnDrain runs once; the port closes only after the delay.
func TestDrain_ServesAndClosesKeepAlives(t *testing.T) {
	spy := &drainSpy{}
	app, base, served := startServing(t, func(c *Config) { c.Server.DrainDelay = 300 * time.Millisecond }, spy)

	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	shut := make(chan error, 1)
	start := time.Now()
	go func() { shut <- app.Shutdown(context.Background()) }()

	// Every request is answered; once the drain has turned keep-alives off, the
	// answer also tells the client to reconnect. The loop ends well inside the
	// 300ms drain, so a refused connection here is a real failure.
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		res, err := client.Get(base + "/")
		if err != nil {
			t.Fatalf("request during the drain: %v", err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("during the drain: status %d, want 200", res.StatusCode)
		}
		if res.Close {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no response carried Connection: close within 200ms of the drain starting")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := <-shut; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("shutdown took %v, want at least the 300ms drain", elapsed)
	}
	if n := spy.drained.Load(); n != 1 {
		t.Fatalf("OnDrain called %d times, want 1", n)
	}
	if err := waitServed(t, served); err != nil {
		t.Fatalf("ListenAndServe = %v", err)
	}
	if conn, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "http://"), 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("port still open after shutdown")
	}
}

// A done ctx cuts the wait short.
func TestDrain_ContextCutsTheWait(t *testing.T) {
	app, _, served := startServing(t, func(c *Config) { c.Server.DrainDelay = time.Hour })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = app.Shutdown(ctx) // its error may report the cancelled ctx; only the time matters
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Shutdown waited %v on a 100ms ctx", elapsed)
	}
	waitServed(t, served)
}

// Ctrl-C twice: the second signal ends the drain at once. The signal path drains
// first and then calls Shutdown, which drains again; OnDrain must still run once.
func TestDrain_SecondSignalCutsTheWait(t *testing.T) {
	spy := &drainSpy{}
	_, _, served := startServing(t, func(c *Config) {
		c.Server.DrainDelay = time.Hour
		c.Server.ShutdownTimeout = time.Second
	}, spy)
	signalSelf(t)
	time.Sleep(100 * time.Millisecond)
	signalSelf(t)
	if err := waitServed(t, served); err != nil {
		t.Fatalf("ListenAndServe = %v, want nil", err)
	}
	if n := spy.drained.Load(); n != 1 {
		t.Fatalf("OnDrain called %d times, want 1", n)
	}
}

// An application that traps the signal itself and calls Shutdown with its own
// deadline, while ListenAndServe's signal-started drain is waiting: the caller's
// ctx must end that wait, not sit out the whole DrainDelay.
func TestDrain_ShutdownCtxCutsASignalDrain(t *testing.T) {
	app, _, served := startServing(t, func(c *Config) { c.Server.DrainDelay = 3 * time.Second })
	signalSelf(t)
	time.Sleep(100 * time.Millisecond) // the signal path is now waiting in the drain

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = app.Shutdown(ctx) // the expired ctx may be reported; only the time matters
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Shutdown with a 200ms ctx took %v during a signal-started 3s drain", elapsed)
	}
	waitServed(t, served)
}

// One signal with a short delay: the signal path's own drain and Shutdown's drain
// are one drain.
func TestDrain_SignalDrainsOnce(t *testing.T) {
	spy := &drainSpy{}
	_, _, served := startServing(t, func(c *Config) { c.Server.DrainDelay = 100 * time.Millisecond }, spy)
	start := time.Now()
	signalSelf(t)
	if err := waitServed(t, served); err != nil {
		t.Fatalf("ListenAndServe = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("stopped after %v, want at least the 100ms drain", elapsed)
	}
	if n := spy.drained.Load(); n != 1 {
		t.Fatalf("OnDrain called %d times, want 1", n)
	}
}

// DrainDelay 0 is today's behaviour: no wait, OnDrain still runs once.
func TestDrain_ZeroDelayDoesNotWait(t *testing.T) {
	spy := &drainSpy{}
	app, _, served := startServing(t, nil, spy)
	start := time.Now()
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitServed(t, served)
	if time.Since(start) > time.Second || spy.drained.Load() != 1 {
		t.Fatalf("zero delay: took %v, OnDrain %d", time.Since(start), spy.drained.Load())
	}
}

// A repeated Shutdown drains once.
func TestDrain_ShutdownTwiceDrainsOnce(t *testing.T) {
	spy := &drainSpy{}
	app, _, served := startServing(t, func(c *Config) { c.Server.DrainDelay = 50 * time.Millisecond }, spy)
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	waitServed(t, served)
	if n := spy.drained.Load(); n != 1 {
		t.Fatalf("OnDrain called %d times, want 1", n)
	}
}

// Development mode skips the wait.
func TestDrain_DevModeSkipsTheWait(t *testing.T) {
	spy := &drainSpy{}
	app, _, served := startServing(t, func(c *Config) {
		c.Server.DrainDelay = time.Hour
		c.DevMode = true
	}, spy)
	start := time.Now()
	_ = app.Shutdown(context.Background())
	waitServed(t, served)
	if time.Since(start) > 2*time.Second {
		t.Fatal("development mode waited for DrainDelay")
	}
	if n := spy.drained.Load(); n != 1 {
		t.Fatalf("OnDrain called %d times in development mode, want 1", n)
	}
}

// Shutdown before anything serves: OnDrain runs, no wait.
func TestDrain_BeforeServingDoesNotWait(t *testing.T) {
	spy := &drainSpy{}
	app := newTestApp(t, func(c *Config) {
		c.Server.DrainDelay = time.Hour
		c.Plugins = append(c.Plugins, spy)
	})
	start := time.Now()
	_ = app.Shutdown(context.Background())
	if time.Since(start) > time.Second || spy.drained.Load() != 1 {
		t.Fatalf("took %v, OnDrain %d", time.Since(start), spy.drained.Load())
	}
}

// A DrainDelay with no ShutdownTimeout leaves in-flight requests no time to
// finish, which New says without refusing the configuration.
func TestDrain_WarnsWithoutShutdownTimeout(t *testing.T) {
	const warning = "collage: DrainDelay is set but ShutdownTimeout is 0; in-flight requests get no time to finish"

	logger, logs := newCapturingLogger()
	newTestApp(t, func(c *Config) {
		c.Logger = logger
		c.Server.DrainDelay = time.Second
		c.Server.ShutdownTimeout = 0
	})
	if !strings.Contains(logs.String(), warning) {
		t.Fatalf("no warning for DrainDelay without ShutdownTimeout:\n%s", logs.String())
	}

	logger, logs = newCapturingLogger()
	newTestApp(t, func(c *Config) {
		c.Logger = logger
		c.Server.DrainDelay = time.Second
	})
	if strings.Contains(logs.String(), "DrainDelay") {
		t.Fatalf("warned although ShutdownTimeout is set:\n%s", logs.String())
	}
}
