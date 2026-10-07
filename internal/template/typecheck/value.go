package typecheck

import (
	"fmt"
	"reflect"
	"text/template/parse"
)

// value is what the checker knows about a value: its type, and whether
// text/template could take its address — which decides whether a method with a
// pointer receiver is reachable. A nil t is unknown.
//
// absent marks a value read by a map key the map may not hold: text/template
// gives a missing key as no value at all, which a chain of fields goes on to
// give too, and which a parameter that can be nil takes as its zero.
type value struct {
	t      reflect.Type
	addr   bool
	absent bool
}

var unknown = value{}

var (
	errorType        = reflect.TypeFor[error]()
	reflectValueType = reflect.TypeFor[reflect.Value]()
)

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
// addressable — then a struct field or a map key. A method's type, receiver
// included, is returned with what it gives back, so its arguments can be judged.
func (w *walker) field(node parse.Node, recv value, name string, argc int) (value, reflect.Type) {
	if !recv.known() {
		return unknown, nil
	}
	t, addr := recv.t, recv.addr
	for t.Kind() == reflect.Pointer {
		t, addr = t.Elem(), true
	}
	if t.Kind() == reflect.Interface {
		return unknown, nil
	}
	if m, ok := t.MethodByName(name); ok && m.IsExported() {
		return w.result(node, name, m.Type, argc, true), m.Type
	}
	// A pointer-receiver method of an unaddressable value is invisible to
	// text/template, which goes on to a struct field or map key of that name.
	hidden := false
	if m, ok := reflect.PointerTo(t).MethodByName(name); ok && m.IsExported() {
		if addr {
			return w.result(node, name, m.Type, argc, true), m.Type
		}
		hidden = true
	}
	missing := func() {
		if hidden {
			w.report(node, fmt.Sprintf("method %s has a pointer receiver, and this %s is not addressable: pass the data as a pointer, or reach the value through a slice", name, t), "")
			return
		}
		w.report(node, fmt.Sprintf("type %s has no field or method %s", t, name), suggest(name, members(t)))
	}
	switch t.Kind() {
	case reflect.Struct:
		f, ok := t.FieldByName(name)
		if !ok {
			missing()
			return unknown, nil
		}
		if !f.IsExported() {
			w.report(node, fmt.Sprintf("%s is an unexported field of type %s", name, t), "")
			return unknown, nil
		}
		if argc > 0 {
			w.report(node, fmt.Sprintf("%s is a field of type %s, not a method, and takes no arguments", name, t), "")
			return unknown, nil
		}
		// Anything reached through an embedded pointer is addressable.
		ft := t
		for _, i := range f.Index[:len(f.Index)-1] {
			ft = ft.Field(i).Type
			if ft.Kind() == reflect.Pointer {
				addr, ft = true, ft.Elem()
			}
		}
		return typed(f.Type, addr), nil
	case reflect.Map:
		if !reflect.TypeFor[string]().AssignableTo(t.Key()) {
			w.report(node, fmt.Sprintf("type %s is keyed by %s, which .%s cannot look up", t, t.Key(), name), "")
			return unknown, nil
		}
		if argc > 0 {
			w.report(node, fmt.Sprintf("%s is a key of type %s, not a method, and takes no arguments", name, t), "")
			return unknown, nil
		}
		elem := typed(t.Elem(), false)
		elem.absent = elem.known()
		return elem, nil
	}
	missing()
	return unknown, nil
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
		// text/template unwraps a reflect.Value a call returns and goes on
		// with what it holds, which is only known when it renders.
		if ft.Out(0) == reflectValueType {
			return unknown
		}
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
