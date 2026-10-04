package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type personaliser struct {
	name    string
	replace string // appended to the body
	err     error
	panics  bool
}

func (p *personaliser) Name() string                     { return p.name }
func (p *personaliser) Version() string                  { return "0" }
func (p *personaliser) Init(context.Context, Host) error { return nil }
func (p *personaliser) Shutdown(context.Context) error   { return nil }
func (p *personaliser) OnPersonalise(_ context.Context, ev *PersonaliseEvent) error {
	if p.panics {
		panic("boom")
	}
	if p.err != nil {
		return p.err
	}
	ev.Body = append(append([]byte(nil), ev.Body...), p.replace...)
	ev.Personal = true
	return nil
}

func TestRegistryPersonalise_InOrder(t *testing.T) {
	r := NewRegistry(nil)
	for _, p := range []Plugin{&personaliser{name: "a", replace: "1"}, &personaliser{name: "b", replace: "2"}} {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	ev := &PersonaliseEvent{Body: []byte("x")}
	if err := r.Personalise(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if string(ev.Body) != "x12" || !ev.Personal {
		t.Errorf("Body = %q, Personal = %v; want x12, true", ev.Body, ev.Personal)
	}
}

func TestRegistryPersonalise_ErrorAndPanicStop(t *testing.T) {
	for name, first := range map[string]*personaliser{
		"error": {name: "a", err: errors.New("no")},
		"panic": {name: "a", panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry(nil)
			_ = r.Register(first)
			_ = r.Register(&personaliser{name: "b", replace: "2"})
			ev := &PersonaliseEvent{Body: []byte("x")}
			err := r.Personalise(context.Background(), ev)
			if err == nil || !strings.Contains(err.Error(), `"a"`) {
				t.Errorf("err = %v, want one naming plugin a", err)
			}
			if string(ev.Body) != "x" {
				t.Errorf("Body = %q: a later hook ran after the failure", ev.Body)
			}
		})
	}
}

func TestRegistryPersonalise_NilAndNone(t *testing.T) {
	var nilRegistry *Registry
	if err := nilRegistry.Personalise(context.Background(), &PersonaliseEvent{}); err != nil {
		t.Errorf("nil registry: %v", err)
	}
}
