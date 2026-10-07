package plugin

import (
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
