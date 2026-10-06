package typecheck

import (
	"fmt"
	"reflect"
	"text/template/parse"
)

// branch walks an if or a with. A with's body runs with the condition's value
// as dot; an else runs with the outer dot. Variables the condition declares are
// in scope in both.
func (w *walker) branch(b *parse.BranchNode, dot value, vars []variable, with bool) {
	cond, inner := w.pipe(b.Pipe, dot, vars)
	body := dot
	if with {
		body = cond
	}
	// A literal condition decides which side runs; the other is never walked.
	taken, constant := literalBool(b.Pipe)
	if !constant || taken {
		w.list(b.List, body, inner)
	}
	if !constant || !taken {
		w.list(b.ElseList, dot, inner)
	}
}

// literalBool reports whether p is nothing but true or false.
func literalBool(p *parse.PipeNode) (value, ok bool) {
	if p == nil || len(p.Decl) != 0 || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return false, false
	}
	b, isBool := p.Cmds[0].Args[0].(*parse.BoolNode)
	if !isBool {
		return false, false
	}
	return b.True, true
}

// rangeOver walks a range: its body with each element as dot, its else with the
// outer dot.
func (w *walker) rangeOver(r *parse.RangeNode, dot value, vars []variable) {
	over := w.eval(r.Pipe, dot, vars)
	last := lastCommand(r)
	// A body may run again after an assignment later in it, so what it assigns
	// is uncertain from its first statement.
	vars = forget(vars, assignedIn(r))
	key, elem, two := w.elements(r, over, len(r.Pipe.Decl))
	inner := vars
	switch len(r.Pipe.Decl) {
	case 1:
		inner = declare(inner, r.Pipe.Decl[0].Ident[0], elem)
	case 2:
		if !two {
			w.report(last, fmt.Sprintf("range over type %s cannot declare two variables", derefType(over.t)), "")
		}
		inner = declare(inner, r.Pipe.Decl[0].Ident[0], key)
		inner = declare(inner, r.Pipe.Decl[1].Ident[0], elem)
	}
	w.list(r.List, elem, inner)
	w.list(r.ElseList, dot, vars)
}

// lastCommand is the node text/template points range errors at.
func lastCommand(r *parse.RangeNode) parse.Node {
	return r.Pipe.Cmds[len(r.Pipe.Cmds)-1]
}

// assignedIn lists the variables an assignment anywhere inside node, its
// condition included, gives a new value.
func assignedIn(node parse.Node) []string {
	var names []string
	var visit func(parse.Node)
	list := func(l *parse.ListNode) {
		if l == nil {
			return
		}
		for _, n := range l.Nodes {
			visit(n)
		}
	}
	pipe := func(p *parse.PipeNode) {
		if p != nil && p.IsAssign {
			for _, d := range p.Decl {
				names = append(names, d.Ident[0])
			}
		}
	}
	visit = func(n parse.Node) {
		switch n := n.(type) {
		case *parse.ActionNode:
			pipe(n.Pipe)
		case *parse.IfNode:
			pipe(n.Pipe)
			list(n.List)
			list(n.ElseList)
		case *parse.WithNode:
			pipe(n.Pipe)
			list(n.List)
			list(n.ElseList)
		case *parse.RangeNode:
			pipe(n.Pipe)
			list(n.List)
			list(n.ElseList)
		}
	}
	visit(node)
	return names
}

// forget makes the named variables unknown: a branch may or may not have
// assigned them, so their type after it is uncertain.
func forget(vars []variable, names []string) []variable {
	for _, name := range names {
		vars = assign(vars, name, unknown)
	}
	return vars
}

// elements returns what ranging over v yields — the key and the element — and
// whether it may declare two variables. It reports a type range cannot iterate
// over.
func (w *walker) elements(r *parse.RangeNode, v value, decls int) (key, elem value, two bool) {
	if !v.known() {
		return unknown, unknown, true
	}
	t, addr := v.t, v.addr
	for t.Kind() == reflect.Pointer {
		t, addr = t.Elem(), true
	}
	intType := reflect.TypeFor[int]()
	switch t.Kind() {
	case reflect.Slice:
		return typed(intType, false), typed(t.Elem(), true), true
	case reflect.Array:
		return typed(intType, false), typed(t.Elem(), addr), true
	case reflect.Map:
		return typed(t.Key(), false), typed(t.Elem(), false), true
	case reflect.Chan:
		return typed(intType, false), typed(t.Elem(), false), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return unknown, typed(t, false), false
	case reflect.Func:
		return seqElements(t, decls)
	case reflect.Interface:
		return unknown, unknown, true
	}
	w.report(lastCommand(r), fmt.Sprintf("range cannot iterate over type %s", t), "")
	return unknown, unknown, true
}

// seqElements reads the element types of an iter.Seq or iter.Seq2 shaped
// function; any other function is left unknown rather than judged.
func seqElements(t reflect.Type, decls int) (key, elem value, two bool) {
	if t.NumIn() != 1 || t.NumOut() != 0 {
		return unknown, unknown, true
	}
	yield := t.In(0)
	if yield.Kind() != reflect.Func || yield.NumOut() != 1 || yield.Out(0).Kind() != reflect.Bool {
		return unknown, unknown, true
	}
	switch yield.NumIn() {
	case 1:
		return unknown, typed(yield.In(0), false), false
	case 2:
		// With fewer than two variables text/template takes the first value.
		if decls < 2 {
			return unknown, typed(yield.In(0), false), true
		}
		return typed(yield.In(0), false), typed(yield.In(1), false), true
	}
	return unknown, unknown, true
}

// include walks a {{template}} call's target with the value passed to it. With
// no value passed, the partial runs with nil data, whose fields are not errors.
func (w *walker) include(n *parse.TemplateNode, dot value, vars []variable) {
	passed := unknown
	if n.Pipe != nil {
		passed = w.eval(n.Pipe, dot, vars)
	}
	w.template(n.Name, passed)
}

// derefType is t with any pointers removed.
func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
