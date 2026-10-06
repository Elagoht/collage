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
// neither knows, are unknown. Every argument is walked, except those and and or
// skip. Findings are reported at node, the whole command.
func (w *walker) call(ident *parse.IdentifierNode, node parse.Node, args []parse.Node, dot value, vars []variable, hasFinal bool, final value) value {
	name := ident.Ident
	_, overridden := w.c.Funcs[name]
	values := make([]value, 0, len(args)+1)
	for _, arg := range args {
		values = append(values, w.arg(arg, dot, vars))
		// and and or stop at the first operand that decides them, so what
		// follows a literal false (and) or true (or) never runs.
		if b, ok := arg.(*parse.BoolNode); ok && !overridden && (name == "and" && !b.True || name == "or" && b.True) {
			return unknown
		}
	}
	if hasFinal {
		values = append(values, final)
	}
	if strings.HasPrefix(name, "_html_template_") {
		return unknown
	}
	if ft, ok := w.c.Funcs[name]; ok && ft != nil && ft.Kind() == reflect.Func {
		return w.result(node, name, ft, len(values), false)
	}
	return w.builtin(node, name, values)
}

func (w *walker) builtin(node parse.Node, name string, args []value) value {
	str := typed(reflect.TypeFor[string](), false)
	boolean := typed(reflect.TypeFor[bool](), false)
	switch name {
	case "not", "eq", "ne", "lt", "le", "gt", "ge":
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
