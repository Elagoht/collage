package typecheck

import (
	"fmt"
	"reflect"
	"text/template/parse"
)

// value is what the checker knows about a value: its type, and whether
// text/template could take its address — which decides whether a method with a
// pointer receiver is reachable. A nil t is unknown.
type value struct {
	t    reflect.Type
	addr bool
}

var unknown = value{}

var errorType = reflect.TypeFor[error]()

// typed is a value of type t; an interface is unknown, since what it holds is
// only known when it renders.
func typed(t reflect.Type, addr bool) value {
	if t == nil || t.Kind() == reflect.Interface {
		return unknown
	}
	return value{t: t, addr: addr}
}

func (v value) known() bool { return v.t != nil }

// field resolves .name on recv as text/template's evalField does: through any
// pointers first, then a method — a pointer receiver's only when the value is
// addressable — then a struct field or a map key.
func (w *walker) field(node parse.Node, recv value, name string, argc int) value {
	if !recv.known() {
		return unknown
	}
	t, addr := recv.t, recv.addr
	for t.Kind() == reflect.Pointer {
		t, addr = t.Elem(), true
	}
	if t.Kind() == reflect.Interface {
		return unknown
	}
	if m, ok := t.MethodByName(name); ok && m.IsExported() {
		return w.result(node, name, m.Type, argc, true)
	}
	if m, ok := reflect.PointerTo(t).MethodByName(name); ok && m.IsExported() {
		if !addr {
			w.report(node, fmt.Sprintf("method %s has a pointer receiver, and this %s is not addressable: pass the data as a pointer, or reach the value through a slice", name, t), "")
			return unknown
		}
		return w.result(node, name, m.Type, argc, true)
	}
	switch t.Kind() {
	case reflect.Struct:
		f, ok := t.FieldByName(name)
		if !ok {
			w.report(node, fmt.Sprintf("type %s has no field or method %s", t, name), suggest(name, members(t)))
			return unknown
		}
		if !f.IsExported() {
			w.report(node, fmt.Sprintf("%s is an unexported field of type %s", name, t), "")
			return unknown
		}
		if argc > 0 {
			w.report(node, fmt.Sprintf("%s is a field of type %s, not a method, and takes no arguments", name, t), "")
			return unknown
		}
		return typed(f.Type, addr)
	case reflect.Map:
		if !reflect.TypeFor[string]().AssignableTo(t.Key()) {
			w.report(node, fmt.Sprintf("type %s is keyed by %s, which .%s cannot look up", t, t.Key(), name), "")
			return unknown
		}
		if argc > 0 {
			w.report(node, fmt.Sprintf("%s is a key of type %s, not a method, and takes no arguments", name, t), "")
			return unknown
		}
		return typed(t.Elem(), false)
	}
	w.report(node, fmt.Sprintf("type %s has no field or method %s", t, name), suggest(name, members(t)))
	return unknown
}

// result checks a call of a method or function of type ft with argc arguments,
// and returns what it gives back. A method's type counts its receiver.
func (w *walker) result(node parse.Node, name string, ft reflect.Type, argc int, method bool) value {
	in := ft.NumIn()
	if method {
		in--
	}
	switch {
	case ft.IsVariadic() && argc < in-1:
		w.report(node, fmt.Sprintf("wrong number of arguments for %s: want at least %d, got %d", name, in-1, argc), "")
	case !ft.IsVariadic() && argc != in:
		w.report(node, fmt.Sprintf("wrong number of arguments for %s: want %d, got %d", name, in, argc), "")
	}
	switch {
	case ft.NumOut() == 1, ft.NumOut() == 2 && ft.Out(1) == errorType:
		return typed(ft.Out(0), false)
	}
	w.report(node, fmt.Sprintf("%s returns %d values; a template can call one that returns a value, or a value and an error", name, ft.NumOut()), "")
	return unknown
}

// members lists the names .x can reach on t: its exported fields, promoted ones
// included, and the exported methods of t and *t.
func members(t reflect.Type) []string {
	var names []string
	if t.Kind() == reflect.Struct {
		for _, f := range reflect.VisibleFields(t) {
			if f.IsExported() {
				names = append(names, f.Name)
			}
		}
	}
	pt := reflect.PointerTo(t)
	for i := range pt.NumMethod() {
		names = append(names, pt.Method(i).Name)
	}
	return names
}
