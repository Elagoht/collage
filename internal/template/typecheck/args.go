package typecheck

import (
	"fmt"
	"reflect"
	"text/template/parse"
)

// param is the type text/template converts the i'th argument of a call of fn to:
// the element type for an argument a variadic parameter takes, nil past the
// last parameter. A method's type counts its receiver, which takes no argument.
func param(fn reflect.Type, method bool, i int) reflect.Type {
	if fn == nil || fn.Kind() != reflect.Func {
		return nil
	}
	first := 0
	if method {
		first = 1
	}
	n := fn.NumIn() - first
	switch {
	case fn.IsVariadic() && i >= n-1:
		return fn.In(fn.NumIn() - 1).Elem()
	case i < n:
		return fn.In(first + i)
	}
	return nil
}

// takesAnything says whether text/template hands an argument to a parameter of
// type p as it is: a reflect.Value, or an interface with no methods, which a
// number fills as idealConstant types it.
func takesAnything(p reflect.Type) bool {
	return p == reflectValueType || p.Kind() == reflect.Interface && p.NumMethod() == 0
}

// typedArg walks an argument given to a parameter of type p of the call of name,
// as text/template's evalArg evaluates it, and reports what evalArg fails on. A
// nil p is a parameter not known: a number there is converted to it, which is
// not followed, and anything else is walked as it is. Findings are about node,
// the whole command, and placed at the argument, where evalArg marks it.
func (w *walker) typedArg(node parse.Node, name string, i int, arg parse.Node, p reflect.Type, dot value, vars []variable) value {
	if p == nil {
		if num, ok := arg.(*parse.NumberNode); ok {
			w.at = num
			return unknown
		}
		return w.arg(arg, dot, vars)
	}
	switch n := arg.(type) {
	case *parse.NilNode:
		w.at = n
		if !canBeNil(p) {
			w.report(node, fmt.Sprintf("argument %d of %s: cannot assign nil to %s", i+1, name, p), "")
		}
		return unknown
	case *parse.NumberNode, *parse.StringNode, *parse.BoolNode:
		if takesAnything(p) {
			return w.arg(arg, dot, vars)
		}
		return w.constantArg(node, name, i, arg, p)
	}
	before := w.reported
	v := w.arg(arg, dot, vars)
	if w.reported == before {
		w.validate(node, name, i, v, p)
	}
	return v
}

// constantArg judges a number, string or bool written as the argument to a
// parameter of type p by p's kind, as evalArg does: an integer kind takes a
// number the parser read as an integer — converted, not checked for range, so
// 300 fits an int8 — an unsigned kind an unsigned one, a float or complex kind
// its own, a string or bool kind a string or a bool. Any other kind, an
// interface with methods among them, takes no constant at all.
func (w *walker) constantArg(node parse.Node, name string, i int, arg parse.Node, p reflect.Type) value {
	w.at = arg
	num, _ := arg.(*parse.NumberNode)
	var ok bool
	var want string
	switch p.Kind() {
	case reflect.Bool:
		_, ok = arg.(*parse.BoolNode)
		want = "bool"
	case reflect.String:
		_, ok = arg.(*parse.StringNode)
		want = "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		ok, want = num != nil && num.IsInt, "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		ok, want = num != nil && num.IsUint, "unsigned integer"
	case reflect.Float32, reflect.Float64:
		ok, want = num != nil && num.IsFloat, "float"
	case reflect.Complex64, reflect.Complex128:
		ok, want = num != nil && num.IsComplex, "complex"
	default:
		w.report(node, fmt.Sprintf("argument %d of %s: a constant cannot be given as a %s", i+1, name, p), "")
		return unknown
	}
	if !ok {
		w.report(node, fmt.Sprintf("argument %d of %s: expected %s; found %s", i+1, name, want, arg), "")
		return unknown
	}
	return typed(p, false)
}

// validate judges a value passed to a parameter of type p as text/template's
// validateType does, and reports it when it certainly fails: a value is taken
// when it is assignable to p, when it is a pointer to something that is, or
// when its address is and text/template can take it. An unknown value, one a
// reflect.Value parameter takes, and a key a map may not hold — which arrives
// as no value, and is p's zero when p can be nil — are not judged.
func (w *walker) validate(node parse.Node, name string, i int, v value, p reflect.Type) bool {
	if p == nil || !v.known() || v.t == reflectValueType || p == reflectValueType || v.absent && canBeNil(p) {
		return true
	}
	t := v.t
	switch {
	case t.AssignableTo(p):
		return true
	case t.Kind() == reflect.Pointer && t.Elem().AssignableTo(p):
		return true
	case reflect.PointerTo(t).AssignableTo(p):
		if v.addr {
			return true
		}
		w.report(node, fmt.Sprintf("argument %d of %s: expected %s, and this %s is not addressable: pass the data as a pointer, or reach the value through a slice", i+1, name, p, t), "")
		return false
	}
	w.report(node, fmt.Sprintf("argument %d of %s: wrong type for value; expected %s; got %s", i+1, name, p, t), "")
	return false
}

// prepared says whether text/template's prepareArg, which call and index use,
// takes v for a parameter of type p: assignable, or an integer converted to
// another integer type. It neither follows a pointer nor takes an address. An
// unknown value, and a key a map may not hold where p can be nil, are taken.
func prepared(v value, p reflect.Type) bool {
	if !v.known() || v.t == reflectValueType || v.absent && canBeNil(p) {
		return true
	}
	if v.t.AssignableTo(p) {
		return true
	}
	return intLike(v.t.Kind()) && intLike(p.Kind()) && v.t.ConvertibleTo(p)
}

// canBeNil is text/template's: whether an untyped nil can be given as a t.
func canBeNil(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	case reflect.Struct:
		return t == reflectValueType
	}
	return false
}

func intLike(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}
