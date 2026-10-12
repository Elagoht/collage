package plugin

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// drainPlugin hears of the drain, or panics hearing it.
type drainPlugin struct {
	testPlugin
	panics bool
}

func (p *drainPlugin) OnDrain() {
	p.log.add(p.name + ".OnDrain")
	if p.panics {
		panic(errPluginPanic)
	}
}

// Every drain hook is called, in order, and one that panics does not keep the
// next from hearing of the drain.
func TestRegistry_DrainInOrderAndContainsPanics(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	mustRegister(t, r, &drainPlugin{testPlugin: testPlugin{name: "a", log: log}, panics: true})
	mustRegister(t, r, &testPlugin{name: "b", log: log})
	mustRegister(t, r, &drainPlugin{testPlugin: testPlugin{name: "c", log: log}})
	mustInit(t, r)
	log.reset()
	r.Drain()
	if got := strings.Join(log.get(), ","); got != "a.OnDrain,c.OnDrain" {
		t.Errorf("calls = %s, want a.OnDrain,c.OnDrain despite a's panic", got)
	}
	var nilRegistry *Registry
	nilRegistry.Drain()
}

// A drain hook that panics is logged at Warn with its plugin's name: a silent
// failure would leave readiness green through the drain.
func TestRegistry_DrainLogsAPanic(t *testing.T) {
	var buf bytes.Buffer
	r := NewRegistry(slog.New(slog.NewTextHandler(&buf, nil)))
	mustRegister(t, r, &drainPlugin{testPlugin: testPlugin{name: "a", log: &callLog{}}, panics: true})
	mustRegister(t, r, &drainPlugin{testPlugin: testPlugin{name: "b", log: &callLog{}}})
	mustInit(t, r)
	r.Drain()
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "drain hook panicked") || !strings.Contains(out, "plugin=a") {
		t.Errorf("log = %q, want a Warn naming plugin a", out)
	}
	if strings.Contains(out, "plugin=b") {
		t.Errorf("log = %q, plugin b did not panic", out)
	}
}

// Only a plugin whose Init succeeded hears of the drain: one whose Init never ran,
// or failed and was rolled back, was never running, and telling it a drain began
// would leave it draining for a later start (collagetest.TrySite shuts an app
// down after a failed start).
func TestRegistry_DrainSkipsPluginsNotInitialised(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	mustRegister(t, r, &drainPlugin{testPlugin: testPlugin{name: "a", log: log}})
	r.Drain()
	if got := strings.Join(log.get(), ","); got != "" {
		t.Errorf("before Init: calls = %s, want none", got)
	}

	log = &callLog{}
	r = NewRegistry(nil)
	mustRegister(t, r, &drainPlugin{testPlugin: testPlugin{name: "a", log: log}})
	mustRegister(t, r, &testPlugin{name: "b", initErr: errors.New("no")})
	if err := r.Init(context.Background(), hostFor); err == nil {
		t.Fatal("Init = nil, want b's error")
	}
	log.reset()
	r.Drain()
	if got := strings.Join(log.get(), ","); got != "" {
		t.Errorf("after a failed Init: calls = %s, want none", got)
	}
}

func mustInit(t *testing.T, r *Registry) {
	t.Helper()
	if err := r.Init(context.Background(), hostFor); err != nil {
		t.Fatalf("Init: %v", err)
	}
}
