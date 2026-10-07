package plugin

import (
	"bytes"
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
	r.Drain()
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "drain hook panicked") || !strings.Contains(out, "plugin=a") {
		t.Errorf("log = %q, want a Warn naming plugin a", out)
	}
	if strings.Contains(out, "plugin=b") {
		t.Errorf("log = %q, plugin b did not panic", out)
	}
}
