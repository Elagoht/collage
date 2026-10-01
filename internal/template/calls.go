package template

import (
	"html/template"
	"slices"
	"strings"
	"text/template/parse"
)

// Call is one call of a template function, as the template wrote it.
type Call struct {
	// Template is the name of the template the call is in.
	Template string
	// Location is where in it: "name:line:col", as the template engine reports it.
	Location string
	// Func is the function called.
	Func string
	// Args are its arguments, in order.
	Args []CallArg
}

// CallArg is one argument of a Call: a string literal's text, or nothing known
// until the template renders — a field, a variable, a pipeline.
type CallArg struct {
	Text    string
	Literal bool
}

// Calls returns every call of the functions named funcs in every loaded
// template, inline ones included, in template order and then in the order they
// are written, so a check can look at what a template links to without
// rendering it. A function given as the target of a pipe — {{"x" | pageURL}} —
// is not found: what it receives is not written beside it.
func (e *HTMLEngine) Calls(funcs ...string) []Call {
	e.mu.RLock()
	set := e.tmpl
	e.mu.RUnlock()

	var calls []Call
	templates := set.Templates()
	slices.SortFunc(templates, func(a, b *template.Template) int { return strings.Compare(a.Name(), b.Name()) })
	for _, t := range templates {
		if t.Tree == nil || t.Tree.Root == nil {
			continue
		}
		tree := t.Tree
		walkCommands(tree.Root, func(command *parse.CommandNode) {
			if len(command.Args) == 0 {
				return
			}
			ident, ok := command.Args[0].(*parse.IdentifierNode)
			if !ok || !slices.Contains(funcs, ident.Ident) {
				return
			}
			location, _ := tree.ErrorContext(command)
			call := Call{Template: t.Name(), Location: location, Func: ident.Ident}
			for _, arg := range command.Args[1:] {
				if s, ok := arg.(*parse.StringNode); ok {
					call.Args = append(call.Args, CallArg{Text: s.Text, Literal: true})
					continue
				}
				call.Args = append(call.Args, CallArg{})
			}
			calls = append(calls, call)
		})
	}
	return calls
}

// walkCommands calls visit for every command in the tree under node, nested
// pipelines included.
func walkCommands(node parse.Node, visit func(*parse.CommandNode)) {
	var walkPipe func(pipe *parse.PipeNode)
	walkPipe = func(pipe *parse.PipeNode) {
		if pipe == nil {
			return
		}
		for _, command := range pipe.Cmds {
			visit(command)
			for _, arg := range command.Args {
				if nested, ok := arg.(*parse.PipeNode); ok {
					walkPipe(nested)
				}
			}
		}
	}
	var walk func(node parse.Node)
	walk = func(node parse.Node) {
		switch n := node.(type) {
		case *parse.ListNode:
			if n == nil {
				return
			}
			for _, child := range n.Nodes {
				walk(child)
			}
		case *parse.ActionNode:
			walkPipe(n.Pipe)
		case *parse.IfNode:
			walkPipe(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.RangeNode:
			walkPipe(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.WithNode:
			walkPipe(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.TemplateNode:
			walkPipe(n.Pipe)
		}
	}
	walk(node)
}
