package typecheck

import (
	"fmt"
	"reflect"
	"strings"
	"text/template/parse"
)

// call evaluates a call of the function ident names. A name the set was parsed
// with comes first, as in text/template; then text/template's builtins. The
// escaper's own functions, which html/template adds after parsing, and any name
// neither knows, are unknown.
//
// As in text/template, the call's shape — how many arguments, how many results
// — is checked at the identifier before any argument is evaluated, so a call of
// the wrong shape reports nothing inside its arguments. Then every argument is
// walked, except those and and or skip. A builtin's own failure is named at
// node, the whole command; findings are about node.
func (w *walker) call(ident *parse.IdentifierNode, node parse.Node, args []parse.Node, dot value, vars []variable, hasFinal bool, final value) value {
	name := ident.Ident
	w.at = ident
	argc := len(args)
	if hasFinal {
		argc++
	}
	ft, overridden := w.c.Funcs[name]
	var result value
	switch {
	case strings.HasPrefix(name, "_html_template_"):
		result = unknown
	case overridden && ft != nil && ft.Kind() == reflect.Func:
		before := w.reported
		result = w.result(node, name, ft, argc, false)
		if w.reported != before {
			return unknown
		}
	case !overridden:
		if !w.builtinArity(node, name, argc) {
			return unknown
		}
	}
	values := make([]value, 0, argc)
	walked := w.reported
	for i, arg := range args {
		// A number given to a typed parameter is converted to it, not made an
		// ideal constant; whether it converts is not judged.
		if num, ok := arg.(*parse.NumberNode); ok && !w.idealParam(name, ft, overridden, i) {
			w.at = num
			values = append(values, unknown)
			continue
		}
		values = append(values, w.arg(arg, dot, vars))
		// and and or stop at the first operand that decides them, so what
		// follows a literal that is false (and) or true (or) never runs.
		if !overridden && (name == "and" || name == "or") {
			if truth, ok := w.truth(arg, true); ok && truth == (name == "or") {
				return unknown
			}
		}
	}
	if hasFinal {
		values = append(values, final)
	}
	if overridden || strings.HasPrefix(name, "_html_template_") {
		return result
	}
	// An argument that failed stopped text/template before the call ran.
	if w.reported != walked {
		return unknown
	}
	// A builtin fails after its arguments are evaluated, named at the
	// command; after it the arguments' last mark stands.
	last := w.at
	defer func() { w.at = last }()
	w.at = node
	return w.builtin(node, name, values)
}

// idealParam says whether the i'th argument of the call of name is an any or a
// reflect.Value, where text/template makes an ideal constant of a number. Every
// builtin's parameters are, but printf's format, a string; a function the set
// was not parsed with is not known to take one.
func (w *walker) idealParam(name string, ft reflect.Type, overridden bool, i int) bool {
	if !overridden {
		if _, builtin := builtinArgs[name]; builtin {
			return name != "printf" || i != 0
		}
		// The escaper's functions take ...any; a name neither knows is unknown.
		return strings.HasPrefix(name, "_html_template_")
	}
	if ft == nil || ft.Kind() != reflect.Func {
		return false
	}
	var p reflect.Type
	switch n := ft.NumIn(); {
	case ft.IsVariadic() && i >= n-1:
		p = ft.In(n - 1).Elem()
	case i < n:
		p = ft.In(i)
	default:
		return false
	}
	return p == reflectValueType || p.Kind() == reflect.Interface && p.NumMethod() == 0
}

// builtinArgs holds how many arguments each of text/template's builtins takes:
// at least min, and no more than max unless max is -1.
var builtinArgs = map[string]struct{ min, max int }{
	"and": {1, -1}, "or": {1, -1}, "not": {1, 1},
	"call": {1, -1}, "index": {1, -1}, "slice": {1, -1}, "len": {1, 1},
	"html": {0, -1}, "js": {0, -1}, "urlquery": {0, -1},
	"print": {0, -1}, "printf": {1, -1}, "println": {0, -1},
	"eq": {1, -1}, "ne": {2, 2}, "lt": {2, 2}, "le": {2, 2}, "gt": {2, 2}, "ge": {2, 2},
}

// builtinArity reports a builtin called with the wrong number of arguments at
// its identifier, where text/template checks it, and says whether the count
// is right. A name that is not a builtin is left alone.
func (w *walker) builtinArity(node parse.Node, name string, argc int) bool {
	want, ok := builtinArgs[name]
	if !ok {
		return true
	}
	switch {
	case want.max < 0 && argc < want.min:
		w.report(node, fmt.Sprintf("wrong number of arguments for %s: want at least %d, got %d", name, want.min, argc), "")
	case want.max >= 0 && argc != want.min:
		w.report(node, fmt.Sprintf("wrong number of arguments for %s: want %d, got %d", name, want.min, argc), "")
	default:
		return true
	}
	return false
}

func (w *walker) builtin(node parse.Node, name string, args []value) value {
	str := typed(reflect.TypeFor[string](), false)
	boolean := typed(reflect.TypeFor[bool](), false)
	switch name {
	case "eq":
		// eq takes one argument, then fails when it runs with nothing to
		// compare it to.
		if len(args) == 1 {
			w.report(node, "eq has nothing to compare its argument to: give it at least two", "")
			return unknown
		}
		return boolean
	case "call":
		// call fails on anything but a function, and on a function called
		// with the wrong number of arguments; a nil one fails too.
		if len(args) == 0 || !args[0].known() {
			return unknown
		}
		if fn := args[0].t; fn.Kind() == reflect.Func {
			v := w.result(node, "call", fn, len(args)-1, false)
			// call hands back fn's result wrapped once more, and text/template
			// unwraps only that: a reflect.Value fn returns stays one.
			if fn.NumOut() > 0 && fn.Out(0) == reflectValueType {
				return typed(reflectValueType, false)
			}
			return v
		}
		w.report(node, fmt.Sprintf("call of type %s, which is not a function", args[0].t), "")
		return unknown
	case "not", "ne", "lt", "le", "gt", "ge":
		return boolean
	case "print", "printf", "println", "html", "js", "urlquery":
		return str
	case "len":
		if len(args) == 1 && args[0].known() {
			t := deref(args[0].t)
			switch t.Kind() {
			case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String, reflect.Interface:
			default:
				w.report(node, fmt.Sprintf("len of type %s", t), "")
			}
		}
		return typed(reflect.TypeFor[int](), false)
	case "index":
		if len(args) == 0 {
			return unknown
		}
		item := args[0]
		for range args[1:] {
			item = w.indexed(node, item)
		}
		return item
	case "slice":
		if len(args) == 0 || !args[0].known() {
			return unknown
		}
		t := deref(args[0].t)
		switch t.Kind() {
		case reflect.String, reflect.Slice:
			return typed(t, false)
		case reflect.Array:
			// text/template fails on an array it cannot address; that is not
			// reported, since addressability is only a guess here.
			return typed(reflect.SliceOf(t.Elem()), false)
		}
		return unknown
	}
	return unknown
}

// indexed is what index gives one level into item: a slice's element, which
// text/template can take the address of, an array's when the array itself is
// addressable, a map's value, a string's byte.
func (w *walker) indexed(node parse.Node, item value) value {
	if !item.known() {
		return unknown
	}
	t, addr := item.t, item.addr
	for t.Kind() == reflect.Pointer {
		t, addr = t.Elem(), true
	}
	switch t.Kind() {
	case reflect.Slice:
		return typed(t.Elem(), true)
	case reflect.Array:
		return typed(t.Elem(), addr)
	case reflect.Map:
		return typed(t.Elem(), false)
	case reflect.String:
		return typed(reflect.TypeFor[uint8](), false)
	case reflect.Interface:
		return unknown
	}
	w.report(node, fmt.Sprintf("cannot index into type %s", t), "")
	return unknown
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
