package template

import (
	"reflect"
	"text/template/parse"

	"github.com/Elagoht/collage/internal/template/typecheck"
)

// TypeCheck walks the template at path against dot with the functions the set
// was parsed with: collage's own, the application's and the plugins'.
func (e *HTMLEngine) TypeCheck(path string, dot reflect.Type) []typecheck.Finding {
	e.mu.RLock()
	set, base := e.tmpl, e.base
	e.mu.RUnlock()

	funcs := make(map[string]reflect.Type, len(base))
	for name, fn := range base {
		funcs[name] = reflect.TypeOf(fn)
	}
	c := typecheck.Checker{
		Lookup: func(name string) *parse.Tree {
			if t := set.Lookup(name); t != nil {
				return t.Tree
			}
			return nil
		},
		Funcs: funcs,
	}
	return c.Check(path, typecheck.Dot{Type: dot})
}
