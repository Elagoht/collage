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
// the wrong shape reports nothing inside its arguments. Then each argument is
// evaluated and judged against its parameter, up to the first that fails, and
// except those and and or skip; the piped final value is judged last. A
// builtin's own failure is named at node, the whole command; findings are about
// node.
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
	// Each argument is judged against its parameter as it is evaluated, and the
	// first that fails stops the call, as in text/template's evalCall.
	values := make([]value, 0, argc)
	walked := w.reported
	for i, arg := range args {
		values = append(values, w.typedArg(node, name, i, arg, w.param(name, ft, overridden, i), dot, vars))
		if w.reported != walked {
			return unknown
		}
		// and and or stop at the first operand that decides them, so what
		// follows a literal that is false (and) or true (or) never runs.
		if !overridden && (name == "and" || name == "or") {
			if truth, ok := w.truth(arg, true); ok && truth == (name == "or") {
				return unknown
			}
		}
	}
	if hasFinal {
		if !w.validate(node, name, len(args), final, w.param(name, ft, overridden, len(args))) {
			return unknown
		}
		values = append(values, final)
	}
	if overridden || strings.HasPrefix(name, "_html_template_") {
		return result
	}
	// A builtin fails after its arguments are evaluated, named at the
	// command; after it the arguments' last mark stands.
	last := w.at
	defer func() { w.at = last }()
	w.at = node
	return w.builtin(node, name, values)
}

// param is the type of the i'th parameter of the call of name, or nil when it
// is not known. Every builtin takes reflect.Values or ...any but printf, whose
// format is a string; the escaper's functions, which html/template adds after
// parsing, take ...any; a name neither the set nor text/template knows is not
// known.
func (w *walker) param(name string, ft reflect.Type, overridden bool, i int) reflect.Type {
	if overridden {
		return param(ft, false, i)
	}
	if _, builtin := builtinArgs[name]; builtin {
		if name == "printf" && i == 0 {
			return reflect.TypeFor[string]()
		}
		return reflectValueType
	}
	if strings.HasPrefix(name, "_html_template_") {
		return anyType
	}
	return nil
}

var anyType = reflect.TypeFor[any]() // any: the type of the escaper's parameters

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
	// A builtin is given reflect.Values, and one an argument already is reaches
	// it as the value it holds, which is only known when it renders.
	for i, a := range args {
		if a.t == reflectValueType {
			args[i] = unknown
		}
	}
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
			before := w.reported
			v := w.result(node, "call", fn, len(args)-1, false)
			if w.reported != before {
				return unknown
			}
			// call's own prepareArg converts each argument to fn's
			// parameter: it takes one assignable to it, or an integer of
			// another integer type, and nothing else.
			for i, a := range args[1:] {
				if p := param(fn, false, i); p != nil && !prepared(a, p) {
					w.report(node, fmt.Sprintf("argument %d of the function call calls: value has type %s; should be %s", i+1, a.t, p), "")
					return unknown
				}
			}
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
		for _, i := range args[1:] {
			if item = w.indexed(node, item, i); !item.known() {
				return unknown
			}
		}
		return item
	case "slice":
		return w.sliced(node, args)
	}
	return unknown
}

// indexed is what index gives one level into item by i: a slice's element,
// which text/template can take the address of, an array's when the array itself
// is addressable, a map's value, a string's byte. A slice, array or string is
// indexed by an integer; a map's key is converted as call converts an argument.
func (w *walker) indexed(node parse.Node, item, i value) value {
	if !item.known() {
		return unknown
	}
	t, addr := item.t, item.addr
	for t.Kind() == reflect.Pointer {
		t, addr = t.Elem(), true
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.String:
		if !w.integer(node, "index", i) {
			return unknown
		}
		switch t.Kind() {
		case reflect.Slice:
			return typed(t.Elem(), true)
		case reflect.Array:
			return typed(t.Elem(), addr)
		}
		return typed(reflect.TypeFor[uint8](), false)
	case reflect.Map:
		if !prepared(i, t.Key()) {
			w.report(node, fmt.Sprintf("index of type %s by a key of type %s; should be %s", t, i.t, t.Key()), "")
			return unknown
		}
		return typed(t.Elem(), false)
	case reflect.Interface:
		return unknown
	}
	w.report(node, fmt.Sprintf("cannot index into type %s", t), "")
	return unknown
}

// integer says whether i may index a slice, array or string, as text/template's
// indexArg judges it: by an integer kind, signed or not. A key a map may not
// hold arrives as no value, which indexes nothing either.
func (w *walker) integer(node parse.Node, name string, i value) bool {
	if !i.known() || intLike(i.t.Kind()) {
		return true
	}
	w.report(node, fmt.Sprintf("%s by a value of type %s, which is not an integer", name, i.t), "")
	return false
}

// sliced is what slice gives of args[0] by the indexes after it, as
// text/template's slice judges them: through any pointers, a string by up to two
// indexes, a slice or an array — one text/template can take the address of — by
// up to three, each an integer.
func (w *walker) sliced(node parse.Node, args []value) value {
	if len(args) == 0 || !args[0].known() {
		return unknown
	}
	t, addr := args[0].t, args[0].addr
	for t.Kind() == reflect.Pointer {
		t, addr = t.Elem(), true
	}
	indexes := args[1:]
	if t.Kind() == reflect.Interface {
		return unknown
	}
	if len(indexes) > 3 {
		w.report(node, fmt.Sprintf("slice takes at most three indexes, not %d", len(indexes)), "")
		return unknown
	}
	var result value
	switch t.Kind() {
	case reflect.String:
		if len(indexes) == 3 {
			w.report(node, "slice of a string takes at most two indexes", "")
			return unknown
		}
		result = typed(t, false)
	case reflect.Slice:
		result = typed(t, false)
	case reflect.Array:
		result = typed(reflect.SliceOf(t.Elem()), false)
	default:
		w.report(node, fmt.Sprintf("slice of type %s, which is not a string, a slice or an array", t), "")
		return unknown
	}
	for _, i := range indexes {
		if !w.integer(node, "slice", i) {
			return unknown
		}
	}
	// reflect cannot slice an array it cannot address, and text/template
	// fails with it: data passed by value, a map's value, a function's result.
	if t.Kind() == reflect.Array && !addr {
		w.report(node, fmt.Sprintf("slice of a %s that is not addressable: pass the data as a pointer, or reach the array through a slice", t), "")
		return unknown
	}
	return result
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
