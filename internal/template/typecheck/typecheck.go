// Package typecheck walks a parsed template against the Go type of the data it
// will run with, and reports what is certain to fail when it renders: a field the
// type does not have, a method called with the wrong number of arguments or
// through a value that cannot reach it, a range over something that cannot be
// ranged over. A value whose type cannot be known before it renders — an
// interface, a map[string]any entry, a reflect.Value a method or function
// returns — is unknown, and nothing below an unknown value is reported: the
// checker speaks only when text/template would fail.
package typecheck

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"text/template/parse"
)

// Dot is the type a template is executed with. A nil Type is unknown.
type Dot struct {
	Type reflect.Type
}

// Finding is one thing in a template certain to fail when it renders.
type Finding struct {
	// Template is the file the node is in: a path, or an inline template's name.
	Template string
	// Line and Col are where text/template names its error, which may lie
	// inside Expr — at the last argument within its parentheses, say — rather
	// than at its start.
	Line, Col int
	// Expr is the expression as written, "{{.Titel}}".
	Expr string
	// Reason says what is wrong.
	Reason string
	// Suggestion is the closest name the type does have, or "".
	Suggestion string
}

// Checker checks the templates of one parsed set.
type Checker struct {
	// Lookup returns the parse tree of the named template, or nil.
	Lookup func(name string) *parse.Tree
	// Funcs holds the type of every function the set was parsed with, by name.
	Funcs map[string]reflect.Type
}

// Check walks the template named name with dot and returns what it found,
// sorted by template, line and column. A template Lookup does not know yields
// nothing.
func (c *Checker) Check(name string, dot Dot) []Finding {
	w := &walker{c: c, done: make(map[visit]bool), seen: make(map[Finding]bool)}
	w.template(name, typed(dot.Type, false))
	sort.SliceStable(w.findings, func(i, j int) bool {
		a, b := w.findings[i], w.findings[j]
		if a.Template != b.Template {
			return a.Template < b.Template
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	return w.findings
}

// visit is one template walked with one value; a partial included twice with
// the same type is walked once.
type visit struct {
	name string
	dot  value
}

type variable struct {
	name string
	v    value
}

type walker struct {
	c    *Checker
	tree *parse.Tree
	// at is the node text/template last marked while executing — where it
	// names its error. A finding is placed there, as text/template's would be.
	at   parse.Node
	done map[visit]bool
	seen map[Finding]bool
	// reported counts every report, repeats included: text/template stops at
	// its first error, so what an erring call would have evaluated next is
	// not walked.
	reported int
	findings []Finding
}

// template walks the named template with dot as both . and $.
func (w *walker) template(name string, dot value) {
	key := visit{name: name, dot: dot}
	if w.done[key] {
		return
	}
	w.done[key] = true
	tree := w.c.Lookup(name)
	if tree == nil || tree.Root == nil {
		return
	}
	outer := w.tree
	w.tree = tree
	w.list(tree.Root, dot, []variable{{name: "$", v: dot}})
	w.tree = outer
}

func (w *walker) list(list *parse.ListNode, dot value, vars []variable) {
	if list == nil {
		return
	}
	for _, node := range list.Nodes {
		vars = w.node(node, dot, vars)
	}
}

// node walks one node and returns the variables in scope after it: an action
// declaring a variable adds it for the nodes that follow it in the same list.
func (w *walker) node(node parse.Node, dot value, vars []variable) []variable {
	w.at = node
	switch n := node.(type) {
	case *parse.ActionNode:
		_, vars = w.pipe(n.Pipe, dot, vars)
	case *parse.IfNode:
		w.branch(&n.BranchNode, dot, vars, false)
		vars = forget(vars, assignedIn(n))
	case *parse.WithNode:
		w.branch(&n.BranchNode, dot, vars, true)
		vars = forget(vars, assignedIn(n))
	case *parse.RangeNode:
		w.rangeOver(n, dot, vars)
		vars = forget(vars, assignedIn(n))
	case *parse.TemplateNode:
		w.include(n, dot, vars)
	}
	return vars
}

// pipe evaluates p and declares or assigns its variables, returning its value
// and the variables in scope after it.
func (w *walker) pipe(p *parse.PipeNode, dot value, vars []variable) (value, []variable) {
	v := w.eval(p, dot, vars)
	if p == nil || len(p.Decl) == 0 {
		return v, vars
	}
	if p.IsAssign {
		return v, assign(vars, p.Decl[0].Ident[0], v)
	}
	return v, declare(vars, p.Decl[0].Ident[0], v)
}

// eval evaluates p's commands left to right, each one's result passed to the
// next as its final argument.
func (w *walker) eval(p *parse.PipeNode, dot value, vars []variable) value {
	if p == nil {
		return unknown
	}
	w.at = p
	result, hasFinal := unknown, false
	for _, cmd := range p.Cmds {
		result = w.command(cmd, dot, vars, hasFinal, result)
		hasFinal = true
	}
	return result
}

func (w *walker) command(cmd *parse.CommandNode, dot value, vars []variable, hasFinal bool, final value) value {
	if len(cmd.Args) == 0 {
		return unknown
	}
	argc := len(cmd.Args) - 1
	if hasFinal {
		argc++
	}
	if ident, ok := cmd.Args[0].(*parse.IdentifierNode); ok {
		return w.call(ident, cmd, cmd.Args[1:], dot, vars, hasFinal, final)
	}
	// The receiver resolves before its arguments are evaluated, as in
	// text/template; when it fails, its arguments are never evaluated.
	var result value
	before := w.reported
	switch n := cmd.Args[0].(type) {
	case *parse.FieldNode:
		w.at = n
		result = w.chain(n, dot, n.Ident, argc)
	case *parse.ChainNode:
		w.at = n
		result = w.chain(n, w.arg(n.Node, dot, vars), n.Field, argc)
	case *parse.VariableNode:
		w.at = n
		result = w.chain(n, lookup(vars, n.Ident[0]), n.Ident[1:], argc)
	default:
		for _, arg := range cmd.Args[1:] {
			w.arg(arg, dot, vars)
		}
		return w.arg(cmd.Args[0], dot, vars)
	}
	if w.reported != before {
		return unknown
	}
	for _, arg := range cmd.Args[1:] {
		w.arg(arg, dot, vars)
	}
	return result
}

// arg evaluates a node used as an argument, or as a command on its own.
func (w *walker) arg(node parse.Node, dot value, vars []variable) value {
	w.at = node
	switch n := node.(type) {
	case *parse.FieldNode:
		return w.chain(n, dot, n.Ident, 0)
	case *parse.ChainNode:
		return w.chain(n, w.arg(n.Node, dot, vars), n.Field, 0)
	case *parse.VariableNode:
		return w.chain(n, lookup(vars, n.Ident[0]), n.Ident[1:], 0)
	case *parse.PipeNode:
		return w.eval(n, dot, vars)
	case *parse.DotNode:
		return dot
	case *parse.IdentifierNode:
		return w.call(n, n, nil, dot, vars, false, unknown)
	case *parse.StringNode:
		return typed(reflect.TypeFor[string](), false)
	case *parse.BoolNode:
		return typed(reflect.TypeFor[bool](), false)
	}
	return unknown
}

// chain resolves names one after another from recv; only the last may be given
// arguments.
func (w *walker) chain(node parse.Node, recv value, names []string, argc int) value {
	for i, name := range names {
		n := 0
		if i == len(names)-1 {
			n = argc
		}
		recv = w.field(node, recv, name, n)
	}
	return recv
}

func declare(vars []variable, name string, v value) []variable {
	return append(vars[:len(vars):len(vars)], variable{name: name, v: v})
}

// assign gives an existing variable a new value: its type when the new value has
// the same one, unknown when it does not.
func assign(vars []variable, name string, v value) []variable {
	out := append([]variable(nil), vars...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].name == name {
			if out[i].v.t != v.t {
				v = unknown
			}
			out[i].v = v
			break
		}
	}
	return out
}

func lookup(vars []variable, name string) value {
	for i := len(vars) - 1; i >= 0; i-- {
		if vars[i].name == name {
			return vars[i].v
		}
	}
	return unknown
}

// report records a finding about node, once. It is placed where text/template
// would name its error: the node it last marked, which for a chain such as
// (index .Cards 0).Owner.Edit is the last argument inside the parentheses.
func (w *walker) report(node parse.Node, reason, suggestion string) {
	w.reported++
	at := w.at
	if at == nil {
		at = node
	}
	// ErrorContext gives the location; its context is cut at twenty characters,
	// so the expression is the node's own text.
	location, _ := w.tree.ErrorContext(at)
	name, line, col := splitLocation(location)
	f := Finding{Template: name, Line: line, Col: col, Expr: "{{" + node.String() + "}}", Reason: reason, Suggestion: suggestion}
	if w.seen[f] {
		return
	}
	w.seen[f] = true
	w.findings = append(w.findings, f)
}

// splitLocation reads "name:line:col", where name may itself hold colons, as an
// inline template's does.
func splitLocation(location string) (string, int, int) {
	colAt := strings.LastIndexByte(location, ':')
	if colAt < 0 {
		return location, 0, 0
	}
	lineAt := strings.LastIndexByte(location[:colAt], ':')
	if lineAt < 0 {
		return location, 0, 0
	}
	line, _ := strconv.Atoi(location[lineAt+1 : colAt])
	col, _ := strconv.Atoi(location[colAt+1:])
	return location[:lineAt], line, col
}
