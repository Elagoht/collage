package render

import (
	"context"
	"reflect"
	"testing"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// The type check counts a call's arguments against the function the templates
// were parsed with; a render calls the one slotFuncs binds. If the two ever
// differed in what they take or return first, every app calling it would fail
// at startup over a template that renders fine.
func TestSlotFuncs_MatchTheirPlaceholders(t *testing.T) {
	engine := newEngine(t, Options{})
	f := fragment("c", "leaf.html")
	rc := types.NewRenderContext(context.Background(), nil, pageWith(f), "en", nil)
	state := &renderState{page: "p", tags: map[string]struct{}{}, hoistToken: newHoistToken()}
	bound := engine.slotFuncs(rc, f, state, nil, nil)
	for name, placeholder := range template.DefaultFuncs() {
		fn, ok := bound[name]
		if !ok || fn == nil {
			continue
		}
		pt, bt := reflect.TypeOf(placeholder), reflect.TypeOf(fn)
		if pt.NumIn() != bt.NumIn() || pt.IsVariadic() != bt.IsVariadic() || pt.Out(0) != bt.Out(0) {
			t.Errorf("%s: placeholder %v, bound %v", name, pt, bt)
		}
	}
}
