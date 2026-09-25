package template

import (
	"sort"
	"text/template/parse"
)

// SlotCalls returns, sorted and once each, the slot names the template at path
// calls with a literal — {{slot "aside"}} — including in the templates it
// includes with {{template}} and the blocks it defines, since those run as part
// of the same fragment. A slot named by anything else, {{slot .Name}}, is not
// known until it renders and is not listed. An unknown path lists nothing.
func (e *HTMLEngine) SlotCalls(path string) []string {
	e.mu.RLock()
	set := e.tmpl
	e.mu.RUnlock()

	found := make(map[string]bool)
	visited := make(map[string]bool)

	var walkTemplate func(name string)
	var walkPipe func(pipe *parse.PipeNode)
	var walk func(node parse.Node)

	walkTemplate = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		if t := set.Lookup(name); t != nil && t.Tree != nil {
			walk(t.Tree.Root)
		}
	}
	walkPipe = func(pipe *parse.PipeNode) {
		if pipe == nil {
			return
		}
		for _, command := range pipe.Cmds {
			if len(command.Args) >= 2 {
				ident, isIdent := command.Args[0].(*parse.IdentifierNode)
				name, isString := command.Args[1].(*parse.StringNode)
				if isIdent && isString && ident.Ident == "slot" {
					found[name.Text] = true
				}
			}
			for _, arg := range command.Args {
				if nested, ok := arg.(*parse.PipeNode); ok {
					walkPipe(nested)
				}
			}
		}
	}
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
			walkTemplate(n.Name)
		}
	}

	walkTemplate(path)

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
