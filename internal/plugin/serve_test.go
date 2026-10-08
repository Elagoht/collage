package plugin

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// servePlugin starts its work when the server serves, or panics starting it.
type servePlugin struct {
	testPlugin
	panics bool
	mu     sync.Mutex
	ctx    context.Context
}

func (p *servePlugin) OnServe(ctx context.Context) {
	p.mu.Lock()
	p.ctx = ctx
	p.mu.Unlock()
	p.log.add(p.name + ".OnServe")
	if p.panics {
		panic(errPluginPanic)
	}
}

func (p *servePlugin) got() context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctx
}

// Every serve hook is called, in order, with the ctx passed in, and one that
// panics does not keep the next from starting.
func TestRegistry_ServeInOrderAndContainsPanics(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	a := &servePlugin{testPlugin: testPlugin{name: "a", log: log}, panics: true}
	c := &servePlugin{testPlugin: testPlugin{name: "c", log: log}}
	mustRegister(t, r, a)
	mustRegister(t, r, &testPlugin{name: "b", log: log})
	mustRegister(t, r, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Serve(ctx)
	if got := strings.Join(log.get(), ","); got != "a.OnServe,c.OnServe" {
		t.Errorf("calls = %s, want a.OnServe,c.OnServe despite a's panic", got)
	}
	if a.got() != ctx || c.got() != ctx {
		t.Error("a serve hook did not receive the ctx passed to Serve")
	}
	var nilRegistry *Registry
	nilRegistry.Serve(ctx)
}

// A serve hook that panics is logged at Warn with its plugin's name: a silent
// failure would leave its scheduler never started.
func TestRegistry_ServeLogsAPanic(t *testing.T) {
	var buf bytes.Buffer
	r := NewRegistry(slog.New(slog.NewTextHandler(&buf, nil)))
	mustRegister(t, r, &servePlugin{testPlugin: testPlugin{name: "a", log: &callLog{}}, panics: true})
	mustRegister(t, r, &servePlugin{testPlugin: testPlugin{name: "b", log: &callLog{}}})
	r.Serve(context.Background())
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "serve hook panicked") || !strings.Contains(out, "plugin=a") {
		t.Errorf("log = %q, want a Warn naming plugin a", out)
	}
	if strings.Contains(out, "plugin=b") {
		t.Errorf("log = %q, plugin b did not panic", out)
	}
}

// cancellingPlugin cancels the serve ctx from its OnServe, as a drain landing
// mid-loop would.
type cancellingPlugin struct {
	testPlugin
	cancel context.CancelFunc
}

func (p *cancellingPlugin) OnServe(context.Context) {
	p.log.add(p.name + ".OnServe")
	p.cancel()
}

// Once the ctx is done, Serve starts no later hook: it would hand that plugin
// a ctx already cancelled, possibly after its Shutdown.
func TestRegistry_ServeStopsOnceCtxIsDone(t *testing.T) {
	log := &callLog{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRegistry(nil)
	mustRegister(t, r, &cancellingPlugin{testPlugin: testPlugin{name: "a", log: log}, cancel: cancel})
	mustRegister(t, r, &testPlugin{name: "b", log: log})
	mustRegister(t, r, &servePlugin{testPlugin: testPlugin{name: "c", log: log}})
	r.Serve(ctx)
	if got := strings.Join(log.get(), ","); got != "a.OnServe" {
		t.Errorf("calls = %s, want only a.OnServe once the ctx is done", got)
	}
}
