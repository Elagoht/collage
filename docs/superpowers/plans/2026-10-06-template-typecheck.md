# Typed Fragment Data and Template Type Checking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `any`-typed fragment data with a sealed `collage.Data`, and check every fragment's template against its data's Go type at `RegisterPage`, failing startup on anything certain to fail when it renders.

**Architecture:** A pure walker (`internal/template/typecheck`) mirrors `text/template`'s runtime resolution over a parse tree, tracking each value's `reflect.Type` and addressability. Fragments carry an internal `types.DataSource` (kind, handler or value, and `reflect.Type`) made only by `collage.Load/DataHandler/Value/Effect`. Registration walks each fragment's template with that type and joins every finding into one error; `collage inspect` exports the types for the editor.

**Tech Stack:** Go 1.26, `html/template`, `text/template/parse`, `reflect`.

**Spec:** `docs/superpowers/specs/2026-10-06-template-typecheck-design.md`

## Global Constraints

- Never write the type `any` in new code. Where the framework must hand data to html/template, keep the repo's convention: a trailing `// any: <why>` comment on the line.
- A finding is reported only when `text/template` would certainly fail on reaching that node. When unsure, the value is *unknown* and nothing below it is reported. When unsure whether a value is addressable, assume it is.
- Data with no `WithData`, or `Effect`, is walked with an unknown `.`: html/template renders a field of nil data as nothing (probed on Go 1.26).
- `collage inspect` keeps `"version": 1`; changes are additive.
- Error message shape: `collage: page "post": fragment "post-body" (templates/post.html:12:9): {{.Titel}}: type blog.Post has no field or method Titel (did you mean Title?)`.
- Run tests with `go test -count=1` (the test cache hides scaffold breakage), and grep `.go.tmpl` files on every API rename.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. Read `git status` before every `git add`; never `git add -A`.
- Work on a branch in a git worktree: `git worktree add -b feat/typed-data ../collage-typed main` (another session may be using `~/Desktop/collage`).

## Review Focus

1. **A pointer-receiver method reached through an addressable path** (`range` over a slice, `index`, a pointer, `with`, a variable) must not be flagged; only the non-addressable paths (data passed by value, a field of it, a map element, a function result) are. Pinned in Task 1 and Task 4.
2. **Values the walker cannot know** (`map[string]any` entries, interface fields, plugin render functions whose stand-in is `func(...any) (any, error)`, the placeholders taking `...reflect.Value`) must never produce a finding. Pinned in Task 1 and Task 3.
3. **One partial included with two types**, one fitting and one not: only the misfit is reported, located in the partial's own file and line, and the error names the page and fragment that included it. Pinned in Task 2 and Task 7.
4. **A data-less layout** (`{{.Title}}`, `{{range .}}`, `{{template "p"}}` with nil data) produces no finding. Pinned in Task 7.
5. **Many findings in one page** come back in one error, deterministic in order, each readable with `errors.As` as `*collage.TemplateTypeError`, and still match `errors.Is(err, collage.ErrTemplateType)`. Pinned in Task 7.

---

### Task 1: The walker: fields, methods, maps and addressability

**Files:**
- Create: `internal/template/typecheck/typecheck.go`
- Create: `internal/template/typecheck/value.go`
- Create: `internal/template/typecheck/suggest.go`
- Create: `internal/template/typecheck/funcs.go` (a stub `call`, replaced in Task 3)
- Test: `internal/template/typecheck/typecheck_test.go`

**Interfaces:**
- Produces:
  - `type Dot struct{ Type reflect.Type }` — nil `Type` means unknown.
  - `type Finding struct{ Template string; Line, Col int; Expr, Reason, Suggestion string }`
  - `type Checker struct{ Lookup func(name string) *parse.Tree; Funcs map[string]reflect.Type }`
  - `func (c *Checker) Check(name string, dot Dot) []Finding` — sorted by Template, Line, Col.

- [ ] **Step 1: Write the failing tests**

`internal/template/typecheck/typecheck_test.go`:

```go
package typecheck

import (
	"html/template"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"text/template/parse"
)

type user struct{ Name string }

func (u user) Display() string { return u.Name }
func (u *user) Edit() string   { return "edit" }

type slug string

type comment struct {
	Body string
	By   user
}

type embedded struct{ Promoted string }

type post struct {
	Title    string
	secret   string
	Author   *user
	Owner    user
	Tags     []string
	Comments []comment
	Meta     map[string]string
	ByID     map[slug]comment
	Loose    map[string]any // any: a value the checker must not look into
	Extra    any            // any: a value the checker must not look into
	embedded
}

func (p post) URL() string                  { return "/" + p.Title }
func (p post) Two(a, b string) string        { return a + b }
func (p post) Bad() (string, string)         { return "", "" }
func (p post) Err() (string, error)          { return "", nil }
func (p post) Join(sep string, parts ...string) string { return strings.Join(parts, sep) }

// checker parses files into one html/template set, as the engine does, and
// returns a Checker over it.
func checker(t *testing.T, files map[string]string, funcs template.FuncMap) *Checker {
	t.Helper()
	set := template.New("").Funcs(funcs)
	for name, src := range files {
		if _, err := set.New(name).Parse(src); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
	}
	types := make(map[string]reflect.Type, len(funcs))
	for name, fn := range funcs {
		types[name] = reflect.TypeOf(fn)
	}
	return &Checker{
		Lookup: func(name string) *parse.Tree {
			if found := set.Lookup(name); found != nil {
				return found.Tree
			}
			return nil
		},
		Funcs: types,
	}
}

// reasons runs src as "t.html" against dot and returns each finding as
// "t.html:line reason [suggestion]".
func reasons(t *testing.T, src string, dot reflect.Type) []string {
	t.Helper()
	c := checker(t, map[string]string{"t.html": src}, nil)
	var out []string
	for _, f := range c.Check("t.html", Dot{Type: dot}) {
		out = append(out, describe(f))
	}
	return out
}

// describe writes a finding as "file:line reason [suggestion]". Columns are left
// to Task 4's test, which compares them with text/template's own errors.
func describe(f Finding) string {
	line := f.Template + ":" + strconv.Itoa(f.Line) + " " + f.Reason
	if f.Suggestion != "" {
		line += " [" + f.Suggestion + "]"
	}
	return line
}

var postType = reflect.TypeFor[post]()
var postPtr = reflect.TypeFor[*post]()

func TestCheck_FieldsMethodsMaps(t *testing.T) {
	tests := []struct {
		name string
		src  string
		dot  reflect.Type
		want []string
	}{
		{"field", `{{.Title}}`, postType, nil},
		{"missing field suggests", `{{.Titel}}`, postType,
			[]string{"t.html:1 type typecheck.post has no field or method Titel [Title]"}},
		{"unexported field", `{{.secret}}`, postType,
			[]string{"t.html:1 secret is an unexported field of type typecheck.post"}},
		{"promoted field", `{{.Promoted}}`, postType, nil},
		{"through a pointer", `{{.Author.Name}}`, postType, nil},
		{"missing through a pointer", `{{.Author.Nmae}}`, postType,
			[]string{"t.html:1 type typecheck.user has no field or method Nmae [Name]"}},
		{"value method", `{{.URL}}`, postType, nil},
		{"field of a string", `{{.Title.Len}}`, postType,
			[]string{"t.html:1 type string has no field or method Len"}},
		{"field given arguments", `{{.Title "x"}}`, postType,
			[]string{"t.html:1 Title is a field of type typecheck.post, not a method, and takes no arguments"}},
		{"method argument count", `{{.Two "a"}}`, postType,
			[]string{"t.html:1 wrong number of arguments for Two: want 2, got 1"}},
		{"variadic method", `{{.Join "," "a" "b"}}`, postType, nil},
		{"variadic method too few", `{{.Join}}`, postType,
			[]string{"t.html:1 wrong number of arguments for Join: want at least 1, got 0"}},
		{"two results without error", `{{.Bad}}`, postType,
			[]string{"t.html:1 Bad returns 2 values; a template can call one that returns a value, or a value and an error"}},
		{"value and error", `{{.Err}}`, postType, nil},
		{"string-keyed map", `{{.Meta.anything}}`, postType, nil},
		{"map keyed by a named string", `{{.ByID.first}}`, postType,
			[]string{"t.html:1 type map[typecheck.slug]typecheck.comment is keyed by typecheck.slug, which .first cannot look up"}},
		{"map[string]any is unknown", `{{.Loose.a.b.c}}`, postType, nil},
		{"interface field is unknown", `{{.Extra.Whatever}}`, postType, nil},
		{"unknown dot reports nothing", `{{.Nope}}`, nil, nil},

		{"pointer method on data passed by value", `{{.Owner.Edit}}`, postType,
			[]string{"t.html:1 method Edit has a pointer receiver, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
		{"pointer method on data passed by pointer", `{{.Owner.Edit}}`, postPtr, nil},
		{"pointer method through a pointer field", `{{.Author.Edit}}`, postType, nil},
		{"value method on a map element", `{{.x.By.Display}}`, reflect.TypeFor[map[string]comment](), nil},
		{"pointer method on a string-map element", `{{.x.By.Edit}}`, reflect.TypeFor[map[string]comment](),
			[]string{"t.html:1 method Edit has a pointer receiver, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reasons(t, test.src, test.dot)
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}

func TestCheck_ExprAndOrder(t *testing.T) {
	c := checker(t, map[string]string{"t.html": "{{.Zz}}\n  {{.Aa}} {{.Title}}"}, nil)
	got := c.Check("t.html", Dot{Type: postType})
	if len(got) != 2 {
		t.Fatalf("findings = %+v, want 2", got)
	}
	if got[0].Line != 1 || got[0].Expr != "{{.Zz}}" || got[1].Line != 2 || got[1].Col != 4 || got[1].Expr != "{{.Aa}}" {
		t.Errorf("findings = %+v, want .Zz at 1 then .Aa at 2:4", got)
	}
}

func TestCheck_ExprIsNotTruncated(t *testing.T) {
	c := checker(t, map[string]string{"t.html": `{{.Author.Nmaaaaaaaaaaaaaaaaaaaaaaaaaa}}`}, nil)
	got := c.Check("t.html", Dot{Type: postType})
	if len(got) != 1 || got[0].Expr != "{{.Author.Nmaaaaaaaaaaaaaaaaaaaaaaaaaa}}" {
		t.Errorf("findings = %+v, want the whole expression", got)
	}
}

func TestCheck_UnknownTemplate(t *testing.T) {
	c := checker(t, map[string]string{"t.html": `x`}, nil)
	if got := c.Check("nope.html", Dot{Type: postType}); len(got) != 0 {
		t.Errorf("Check(unknown) = %+v, want none", got)
	}
}
```

Columns are not asserted here: the parser places a multi-segment field (`.Title.Len`) at its last segment, not where it starts, and Task 4 checks every column against `text/template`'s own error for the same node.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -count=1 ./internal/template/typecheck/`
Expected: FAIL to compile: `undefined: Checker`, `undefined: Dot`.

- [ ] **Step 3: Write the walker core**

`internal/template/typecheck/typecheck.go`:

```go
// Package typecheck walks a parsed template against the Go type of the data it
// will run with, and reports what is certain to fail when it renders: a field the
// type does not have, a method called with the wrong number of arguments or
// through a value that cannot reach it, a range over something that cannot be
// ranged over. A value whose type cannot be known before it renders — an
// interface, a map[string]any entry, what call returns — is unknown, and nothing
// below an unknown value is reported: the checker speaks only when
// text/template would fail.
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
	// Line and Col locate the node as text/template's own errors do.
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
	c        *Checker
	tree     *parse.Tree
	done     map[visit]bool
	seen     map[Finding]bool
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
	switch n := node.(type) {
	case *parse.ActionNode:
		_, vars = w.pipe(n.Pipe, dot, vars)
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
		return w.call(ident, cmd.Args[1:], dot, vars, hasFinal, final)
	}
	for _, arg := range cmd.Args[1:] {
		w.arg(arg, dot, vars)
	}
	switch n := cmd.Args[0].(type) {
	case *parse.FieldNode:
		return w.chain(n, dot, n.Ident, argc)
	case *parse.ChainNode:
		return w.chain(n, w.arg(n.Node, dot, vars), n.Field, argc)
	case *parse.VariableNode:
		return w.chain(n, lookup(vars, n.Ident[0]), n.Ident[1:], argc)
	}
	return w.arg(cmd.Args[0], dot, vars)
}

// arg evaluates a node used as an argument, or as a command on its own.
func (w *walker) arg(node parse.Node, dot value, vars []variable) value {
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
		return w.call(n, nil, dot, vars, false, unknown)
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

// report records a finding at node, once.
func (w *walker) report(node parse.Node, reason, suggestion string) {
	// ErrorContext gives the location; its context is cut at twenty characters,
	// so the expression is the node's own text.
	location, _ := w.tree.ErrorContext(node)
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
```

`internal/template/typecheck/value.go`:

```go
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
```

`internal/template/typecheck/suggest.go`:

```go
package typecheck

import (
	"sort"
	"strings"
)

// suggest returns the candidate closest to name: an exact match ignoring case,
// or the nearest by edit distance (a swap of two letters counting once) within
// one edit for short names and two for names of five letters or more. Ties go
// to the alphabetically first. It returns "" when nothing is that close.
func suggest(name string, candidates []string) string {
	limit := 1
	if len(name) >= 5 {
		limit = 2
	}
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	best, bestDistance := "", limit+1
	for _, c := range sorted {
		if strings.EqualFold(c, name) {
			return c
		}
		if d := distance(strings.ToLower(name), strings.ToLower(c)); d < bestDistance {
			best, bestDistance = c, d
		}
	}
	return best
}

// distance is the optimal string alignment distance between a and b.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range rb {
		d[0][j+1] = j + 1
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}
```

`internal/template/typecheck/funcs.go` (stub, replaced in Task 3):

```go
package typecheck

import "text/template/parse"

// call evaluates a function call. Functions are checked in a later step; until
// then every argument is still walked, and the result is unknown.
func (w *walker) call(ident *parse.IdentifierNode, args []parse.Node, dot value, vars []variable, hasFinal bool, final value) value {
	for _, arg := range args {
		w.arg(arg, dot, vars)
	}
	return unknown
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -count=1 ./internal/template/typecheck/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/template/typecheck/
git commit -m "feat: typecheck walks a template's fields and methods against a Go type"
```

---

### Task 2: The walker: if, with, range, variables, partials

**Files:**
- Create: `internal/template/typecheck/control.go`
- Modify: `internal/template/typecheck/typecheck.go` (the `node` switch)
- Test: `internal/template/typecheck/control_test.go`

**Interfaces:**
- Consumes: `walker`, `value`, `typed`, `declare`, `lookup`, `w.pipe`, `w.eval`, `w.list`, `w.template`, `w.report` from Task 1.
- Produces: no new exported names.

- [ ] **Step 1: Write the failing tests**

`internal/template/typecheck/control_test.go`:

```go
package typecheck

import (
	"iter"
	"reflect"
	"strings"
	"testing"
)

type board struct {
	Cards []card
	Fixed [2]card
	ByCol map[string]card
	Count int
	Seq   iter.Seq[card]
	Title string
	Owner user
}

type card struct {
	Name  string
	Owner user
}

var boardType = reflect.TypeFor[board]()

func TestCheck_Control(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"range slice", `{{range .Cards}}{{.Name}}{{end}}`, nil},
		{"range slice wrong field", `{{range .Cards}}{{.Nam}}{{end}}`,
			[]string{"t.html:1 type typecheck.card has no field or method Nam [Name]"}},
		{"range else keeps outer dot", `{{range .Cards}}{{else}}{{.Title}}{{end}}`, nil},
		{"range two variables", `{{range $i, $c := .Cards}}{{$c.Name}}{{$i}}{{end}}`, nil},
		{"range map", `{{range $k, $v := .ByCol}}{{$v.Name}}{{end}}`, nil},
		{"range int", `{{range .Count}}{{.}}{{end}}`, nil},
		{"range int two variables", `{{range $i, $v := .Count}}{{end}}`,
			[]string{"t.html:1 range over type int cannot declare two variables"}},
		{"range iter.Seq", `{{range .Seq}}{{.Name}}{{end}}`, nil},
		{"range string", `{{range .Title}}{{end}}`,
			[]string{"t.html:1 range cannot iterate over type string"}},
		{"range struct", `{{range .Owner}}{{end}}`,
			[]string{"t.html:1 range cannot iterate over type typecheck.user"}},
		{"slice element is addressable", `{{range .Cards}}{{.Owner.Edit}}{{end}}`, nil},
		{"map element is not addressable", `{{range .ByCol}}{{.Owner.Edit}}{{end}}`,
			[]string{"t.html:1 method Edit has a pointer receiver, and this typecheck.user is not addressable: pass the data as a pointer, or reach the value through a slice"}},
		{"with narrows dot", `{{with .Owner}}{{.Name}}{{end}}`, nil},
		{"with narrows wrong field", `{{with .Owner}}{{.Title}}{{end}}`,
			[]string{"t.html:1 type typecheck.user has no field or method Title"}},
		{"with else keeps outer dot", `{{with .Owner}}{{else}}{{.Title}}{{end}}`, nil},
		{"if walks both branches", `{{if .Title}}{{.Nope}}{{else}}{{.Nada}}{{end}}`,
			[]string{"t.html:1 type typecheck.board has no field or method Nope", "t.html:1 type typecheck.board has no field or method Nada"}},
		{"variable in scope", `{{$o := .Owner}}{{$o.Name}}`, nil},
		{"variable wrong field", `{{$o := .Owner}}{{$o.Nme}}`,
			[]string{"t.html:1 type typecheck.user has no field or method Nme [Name]"}},
		{"root variable", `{{range .Cards}}{{$.Title}}{{end}}`, nil},
		{"variable out of scope after end", `{{with .Owner}}{{$x := .Name}}{{end}}{{$y := 1}}`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reasons(t, test.src, boardType)
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}

func TestCheck_Partials(t *testing.T) {
	files := map[string]string{
		"page.html": `{{template "card.html" .Owner}}{{range .Cards}}{{template "card.html" .}}{{end}}`,
		"card.html": `<b>{{.Name}}</b>`,
	}
	c := checker(t, files, nil)
	if got := c.Check("page.html", Dot{Type: boardType}); len(got) != 0 {
		t.Errorf("partial fitting both types: %+v, want none", got)
	}

	files["card.html"] = `<b>{{.Owner.Name}}</b>`
	c = checker(t, files, nil)
	got := c.Check("page.html", Dot{Type: boardType})
	if len(got) != 1 || got[0].Template != "card.html" || got[0].Line != 1 || !strings.Contains(got[0].Reason, "type typecheck.user has no field or method Owner") {
		t.Errorf("partial misfitting one type: %+v, want one finding in card.html about typecheck.user", got)
	}
}

func TestCheck_PartialWithoutPipeIsUnknown(t *testing.T) {
	c := checker(t, map[string]string{"page.html": `{{template "p.html"}}`, "p.html": `{{.Anything}}`}, nil)
	if got := c.Check("page.html", Dot{Type: boardType}); len(got) != 0 {
		t.Errorf("partial with no data: %+v, want none", got)
	}
}

func TestCheck_DefineBlocks(t *testing.T) {
	c := checker(t, map[string]string{"page.html": `{{define "row"}}{{.Nme}}{{end}}{{range .Cards}}{{template "row" .}}{{end}}`}, nil)
	got := c.Check("page.html", Dot{Type: boardType})
	if len(got) != 1 || got[0].Template != "page.html" || !strings.Contains(got[0].Reason, "typecheck.card has no field or method Nme") {
		t.Errorf("define: %+v, want one finding in page.html", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -count=1 -run 'Control|Partial|Define' ./internal/template/typecheck/`
Expected: FAIL: findings inside `range`/`with`/`if` bodies and partials are missing (the walker ignores those nodes).

- [ ] **Step 3: Implement control flow**

In `internal/template/typecheck/typecheck.go`, replace the `node` switch:

```go
	switch n := node.(type) {
	case *parse.ActionNode:
		_, vars = w.pipe(n.Pipe, dot, vars)
	case *parse.IfNode:
		w.branch(&n.BranchNode, dot, vars, false)
	case *parse.WithNode:
		w.branch(&n.BranchNode, dot, vars, true)
	case *parse.RangeNode:
		w.rangeOver(n, dot, vars)
	case *parse.TemplateNode:
		w.include(n, dot, vars)
	}
```

`internal/template/typecheck/control.go`:

```go
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
	w.list(b.List, body, inner)
	w.list(b.ElseList, dot, inner)
}

// rangeOver walks a range: its body with each element as dot, its else with the
// outer dot.
func (w *walker) rangeOver(r *parse.RangeNode, dot value, vars []variable) {
	over := w.eval(r.Pipe, dot, vars)
	key, elem, two := w.elements(r, over)
	inner := vars
	switch len(r.Pipe.Decl) {
	case 1:
		inner = declare(inner, r.Pipe.Decl[0].Ident[0], elem)
	case 2:
		if !two {
			w.report(r.Pipe, fmt.Sprintf("range over type %s cannot declare two variables", over.t), "")
		}
		inner = declare(inner, r.Pipe.Decl[0].Ident[0], key)
		inner = declare(inner, r.Pipe.Decl[1].Ident[0], elem)
	}
	w.list(r.List, elem, inner)
	w.list(r.ElseList, dot, vars)
}

// elements returns what ranging over v yields — the key and the element — and
// whether it may declare two variables. It reports a type range cannot iterate
// over.
func (w *walker) elements(r *parse.RangeNode, v value) (key, elem value, two bool) {
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
		return unknown, typed(t.Elem(), false), false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return unknown, typed(t, false), false
	case reflect.Func:
		return seqElements(t)
	case reflect.Interface:
		return unknown, unknown, true
	}
	w.report(r.Pipe, fmt.Sprintf("range cannot iterate over type %s", t), "")
	return unknown, unknown, true
}

// seqElements reads the element types of an iter.Seq or iter.Seq2 shaped
// function; any other function is left unknown rather than judged.
func seqElements(t reflect.Type) (key, elem value, two bool) {
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
```

Range findings are reported at `r.Pipe`; a field inside a body at its own node; a chain such as `(index .X 0).Name` at the chain node. Task 4 checks that each of these is where `text/template` itself points.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -count=1 ./internal/template/typecheck/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/template/typecheck/
git commit -m "feat: typecheck follows if, with, range, variables and partials"
```

---

### Task 3: The walker: functions and builtins

**Files:**
- Modify: `internal/template/typecheck/funcs.go` (replace the stub)
- Test: `internal/template/typecheck/funcs_test.go`

**Interfaces:**
- Consumes: `walker.result`, `typed`, `unknown`, `w.arg`, `w.report` from Task 1.
- Produces: no new exported names.

- [ ] **Step 1: Write the failing tests**

`internal/template/typecheck/funcs_test.go`:

```go
package typecheck

import (
	"html/template"
	"reflect"
	"strings"
	"testing"
)

func pick(n int) []card { return make([]card, n) }

func TestCheck_Funcs(t *testing.T) {
	funcs := template.FuncMap{
		"pick":   pick,
		"upper":  strings.ToUpper,
		"stand":  func(...any) (any, error) { return nil, nil }, // any: the stand-in a plugin's render function is parsed with
		"urlFor": func(name string, _ ...reflect.Value) (string, error) { return name, nil },
	}
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"result type flows", `{{range pick 2}}{{.Name}}{{end}}`, nil},
		{"result type wrong field", `{{range pick 2}}{{.Nme}}{{end}}`,
			[]string{"t.html:1 type typecheck.card has no field or method Nme [Name]"}},
		{"function argument count", `{{upper}}`,
			[]string{"t.html:1 wrong number of arguments for upper: want 1, got 0"}},
		{"piped final argument counts", `{{.Title | upper}}`, nil},
		{"arguments are walked", `{{upper .Titl}}`,
			[]string{"t.html:1 type typecheck.board has no field or method Titl [Title]"}},
		{"plugin stand-in is unknown", `{{(stand 1 2).Anything}}`, nil},
		{"reflect.Value variadic", `{{urlFor "post" "id" 1}}`, nil},
		{"len", `{{len .Cards}}`, nil},
		{"len of struct", `{{len .Owner}}`,
			[]string{"t.html:1 len of type typecheck.user"}},
		{"index slice", `{{(index .Cards 0).Name}}`, nil},
		{"index slice element is addressable", `{{(index .Cards 0).Owner.Edit}}`, nil},
		{"index map", `{{(index .ByCol "a").Nme}}`,
			[]string{"t.html:1 type typecheck.card has no field or method Nme [Name]"}},
		{"index struct", `{{index .Owner 0}}`,
			[]string{"t.html:1 cannot index into type typecheck.user"}},
		{"slice keeps the type", `{{range slice .Cards 1}}{{.Name}}{{end}}`, nil},
		{"comparisons are bool", `{{if eq .Title "x"}}{{end}}`, nil},
		{"print is string", `{{(print .Title).Nope}}`,
			[]string{"t.html:1 type string has no field or method Nope"}},
		{"call is unknown", `{{(call .Seq).Whatever}}`, nil},
		{"and/or are unknown", `{{(and .Title .Count).Whatever}}`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := checker(t, map[string]string{"t.html": test.src}, funcs)
			var got []string
			for _, f := range c.Check("t.html", Dot{Type: boardType}) {
				got = append(got, describe(f))
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Errorf("Check(%q)\n got: %q\nwant: %q", test.src, got, test.want)
			}
		})
	}
}

func TestCheck_EscaperFunctionsIgnored(t *testing.T) {
	set := template.Must(template.New("t.html").Parse(`<a href="{{.Title}}">{{.Title}}</a>`))
	if err := set.Execute(new(strings.Builder), board{}); err != nil {
		t.Fatal(err)
	}
	c := &Checker{Lookup: func(name string) *parse.Tree { return set.Lookup(name).Tree }}
	if got := c.Check("t.html", Dot{Type: boardType}); len(got) != 0 {
		t.Errorf("escaped tree: %+v, want none", got)
	}
}
```

Add `"text/template/parse"` to this file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -count=1 -run 'Funcs|Escaper' ./internal/template/typecheck/`
Expected: FAIL: the stub returns unknown for every call, so no function findings and no result types.

- [ ] **Step 3: Implement function calls**

Replace `internal/template/typecheck/funcs.go`:

```go
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
// neither knows, are unknown. Every argument is walked.
func (w *walker) call(ident *parse.IdentifierNode, args []parse.Node, dot value, vars []variable, hasFinal bool, final value) value {
	values := make([]value, 0, len(args)+1)
	for _, arg := range args {
		values = append(values, w.arg(arg, dot, vars))
	}
	if hasFinal {
		values = append(values, final)
	}
	name := ident.Ident
	if strings.HasPrefix(name, "_html_template_") {
		return unknown
	}
	if ft, ok := w.c.Funcs[name]; ok && ft != nil && ft.Kind() == reflect.Func {
		return w.result(ident, name, ft, len(values), false)
	}
	return w.builtin(ident, name, values)
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -count=1 ./internal/template/typecheck/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/template/typecheck/
git commit -m "feat: typecheck checks function calls and text/template's builtins"
```

---

### Task 4: The walker agrees with text/template

The previous tasks pin the walker to this plan's reading of `text/template`. This task pins it to `text/template` itself: for every case, the checker reports something **if and only if** executing the template fails, and its first finding is at the line and column `text/template`'s error names. Each case has at most one failing node, so "first" is unambiguous.

**Files:**
- Test: `internal/template/typecheck/differential_test.go`

**Interfaces:**
- Consumes: `Checker`, `Dot`, the test types and `checker` helper from Tasks 1–3.

- [ ] **Step 1: Write the test**

```go
package typecheck

import (
	"html/template"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// filledBoard and filledPost have every pointer set and every collection
// non-empty, so an execution error can only come from what the checker judges,
// never from a nil it does not model.
func filledBoard() board {
	c := card{Name: "c", Owner: user{Name: "o"}}
	return board{
		Cards: []card{c}, Fixed: [2]card{c, c}, ByCol: map[string]card{"a": c},
		Count: 2, Seq: func(yield func(card) bool) { yield(c) }, Title: "t", Owner: user{Name: "o"},
	}
}

func filledPost() post {
	return post{
		Title: "t", Author: &user{Name: "a"}, Owner: user{Name: "o"}, Tags: []string{"x"},
		Comments: []comment{{Body: "b"}}, Meta: map[string]string{"anything": "m"},
		ByID: map[slug]comment{"first": {}}, Loose: map[string]any{"a": map[string]any{"b": map[string]any{"c": 1}}},
		embedded: embedded{Promoted: "p"},
	}
}

func TestCheck_AgreesWithTextTemplate(t *testing.T) {
	funcs := template.FuncMap{"pick": pick, "upper": strings.ToUpper}
	cases := []struct {
		src  string
		data reflect.Value
	}{}
	add := func(data any, srcs ...string) { // any: the test hands each case's data to html/template as the engine does
		for _, src := range srcs {
			cases = append(cases, struct {
				src  string
				data reflect.Value
			}{src, reflect.ValueOf(data)})
		}
	}
	p, b := filledPost(), filledBoard()
	add(p,
		`{{.Title}}`, `{{.Titel}}`, `{{.Promoted}}`, `{{.Author.Name}}`, `{{.Author.Nmae}}`,
		`{{.URL}}`, `{{.Title.Len}}`, `{{.Title "x"}}`, `{{.Two "a"}}`, `{{.Two "a" "b"}}`,
		`{{.Join "," "a"}}`, `{{.Bad}}`, `{{.Err}}`, `{{.Meta.anything}}`, `{{.ByID.first}}`,
		`{{.Loose.a.b.c}}`, `{{.Owner.Edit}}`, `{{.Author.Edit}}`, `{{.Owner.Display}}`,
	)
	add(&p, `{{.Owner.Edit}}`, `{{with .Owner}}{{.Edit}}{{end}}`, `{{$o := .Owner}}{{$o.Edit}}`)
	add(map[string]comment{"x": {}}, `{{.x.By.Edit}}`, `{{.x.By.Display}}`)
	add(b,
		`{{range .Cards}}{{.Name}}{{end}}`, `{{range .Cards}}{{.Nam}}{{end}}`,
		`{{range $i, $c := .Cards}}{{$c.Name}}{{end}}`, `{{range $k, $v := .ByCol}}{{$v.Name}}{{end}}`,
		`{{range .Count}}{{.}}{{end}}`, `{{range $i, $v := .Count}}{{end}}`, `{{range .Seq}}{{.Name}}{{end}}`,
		`{{range .Title}}{{end}}`, `{{range .Owner}}{{end}}`,
		`{{range .Cards}}{{.Owner.Edit}}{{end}}`, `{{range .ByCol}}{{.Owner.Edit}}{{end}}`,
		`{{range .Fixed}}{{.Owner.Edit}}{{end}}`,
		`{{with .Owner}}{{.Name}}{{end}}`, `{{with .Owner}}{{.Title}}{{end}}`,
		`{{$o := .Owner}}{{$o.Name}}`, `{{$o := .Owner}}{{$o.Nme}}`,
		`{{range pick 2}}{{.Name}}{{end}}`, `{{upper .Title}}`,
		`{{len .Cards}}`, `{{len .Owner}}`, `{{(index .Cards 0).Name}}`, `{{(index .Cards 0).Owner.Edit}}`,
		`{{(index .ByCol "a").Owner.Edit}}`, `{{index .Owner 0}}`, `{{range slice .Cards 0}}{{.Name}}{{end}}`,
		`{{(print .Title).Nope}}`,
	)
	add(&b, `{{range .Fixed}}{{.Owner.Edit}}{{end}}`, `{{(index .Fixed 0).Owner.Edit}}`)

	for _, tc := range cases {
		t.Run(tc.data.Type().String()+" "+tc.src, func(t *testing.T) {
			set := template.Must(template.New("t.html").Funcs(funcs).Parse(tc.src))
			c := checker(t, map[string]string{"t.html": tc.src}, funcs)
			findings := c.Check("t.html", Dot{Type: tc.data.Type()})
			execErr := set.Execute(io.Discard, tc.data.Interface())
			if (len(findings) > 0) != (execErr != nil) {
				t.Fatalf("checker found %+v; execution error: %v", findings, execErr)
			}
			// text/template stops at its first error; the first finding must
			// point where it does: "template: t.html:1:8: executing ...".
			if execErr != nil {
				at := regexp.MustCompile(`t\.html:(\d+):(\d+)`).FindStringSubmatch(execErr.Error())
				if at == nil {
					t.Fatalf("no position in %v", execErr)
				}
				if want := at[1] + ":" + at[2]; strconv.Itoa(findings[0].Line)+":"+strconv.Itoa(findings[0].Col) != want {
					t.Errorf("first finding at %d:%d, text/template at %s (%v)", findings[0].Line, findings[0].Col, want, execErr)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test -count=1 -run AgreesWithTextTemplate ./internal/template/typecheck/`
Expected: PASS. A failure is a real disagreement: fix the walker so it matches `text/template` — for a position, report at the node `text/template` names — re-run Tasks 1–3's tests, and correct any `want` line those tests got wrong. Do not delete a case to make this pass.

- [ ] **Step 3: Commit**

```bash
git add internal/template/typecheck/differential_test.go
git commit -m "test: typecheck reports exactly what text/template fails on"
```

---

### Task 5: Fragments carry a typed data source internally

The internal model changes first, with the public builder kept working on top of it, so this task ends green with no public API change.

**Files:**
- Create: `internal/types/datasource.go`
- Modify: `internal/types/fragment.go` (remove `DataHandler` and `Data` fields; add `data DataSource`, `SkipTypeCheck bool`; drop the conflicting-data check from `validate`)
- Modify: `internal/types/errors.go:45-48` (`ErrConflictingData` message)
- Modify: `internal/render/fragment.go:270-293`, `internal/render/prefetch.go:76,114`, `internal/core/fragmentrender.go:159`, `internal/core/inspect.go:175`
- Modify: `pkg/collage/fragment.go` (`WithDataHandler`, `WithData` set the source)
- Test: `internal/types/datasource_test.go`; migrate internal tests that set the removed fields

**Interfaces:**
- Produces (package `types`):
  ```go
  type DataKind uint8
  const (DataNone DataKind = iota; DataFixed; DataFetched; DataEffect)
  type DataSource struct {
  	Kind    DataKind
  	Handler DataHandlerFunc // DataFetched and DataEffect
  	Value   any             // DataFixed // any: fixed data flows straight into html/template
  	Type    reflect.Type    // the template's . for DataFixed and DataFetched; nil when unknown
  }
  func FixedData(v any, t reflect.Type) DataSource   // any: as DataSource.Value
  func FetchedData(h DataHandlerFunc, t reflect.Type) DataSource
  func EffectData(h DataHandlerFunc) DataSource
  func (f *Fragment) DataSource() DataSource         // nil-safe
  func (f *Fragment) SetDataSource(d DataSource)
  // Fragment gains: SkipTypeCheck bool
  ```
  `FetchedData(nil, …)` and `EffectData(nil)` return the zero `DataSource` (no data).

- [ ] **Step 1: Write the failing test**

`internal/types/datasource_test.go`:

```go
package types

import (
	"context"
	"reflect"
	"testing"
)

func TestDataSource_Constructors(t *testing.T) {
	h := func(context.Context, *RenderContext) (any, []string, error) { return nil, nil, nil } // any: matches DataHandlerFunc
	strType := reflect.TypeFor[string]()

	if got := FixedData("x", strType); got.Kind != DataFixed || got.Value != "x" || got.Type != strType {
		t.Errorf("FixedData = %+v", got)
	}
	if got := FetchedData(h, strType); got.Kind != DataFetched || got.Handler == nil || got.Type != strType {
		t.Errorf("FetchedData = %+v", got)
	}
	if got := EffectData(h); got.Kind != DataEffect || got.Handler == nil || got.Type != nil {
		t.Errorf("EffectData = %+v", got)
	}
	if got := FetchedData(nil, strType); got.Kind != DataNone {
		t.Errorf("FetchedData(nil) = %+v, want no data", got)
	}
	if got := EffectData(nil); got.Kind != DataNone {
		t.Errorf("EffectData(nil) = %+v, want no data", got)
	}

	var f *Fragment
	if got := f.DataSource(); got.Kind != DataNone {
		t.Errorf("nil Fragment DataSource = %+v", got)
	}
	f = &Fragment{}
	f.SetDataSource(FixedData("x", strType))
	if f.DataSource().Value != "x" {
		t.Errorf("SetDataSource did not stick: %+v", f.DataSource())
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 -run DataSource_Constructors ./internal/types/`
Expected: FAIL to compile: `undefined: FixedData`.

- [ ] **Step 3: Implement**

`internal/types/datasource.go`:

```go
package types

import "reflect"

// DataKind says where a fragment's data comes from.
type DataKind uint8

const (
	// DataNone: the fragment renders with no data.
	DataNone DataKind = iota
	// DataFixed: a value fixed when the program starts.
	DataFixed
	// DataFetched: a handler run for every render.
	DataFetched
	// DataEffect: a handler run for what it declares; the template gets no data.
	DataEffect
)

// DataSource is what a fragment renders with, and the Go type its template sees
// as dot. Only pkg/collage's constructors make one for an application, so the
// type always matches the data: it is the handler's or the value's own static
// type, nil when that type is an interface and so unknown until it renders.
type DataSource struct {
	Kind DataKind
	// Handler fetches the data for DataFetched, and runs for DataEffect.
	Handler DataHandlerFunc
	// Value is the data for DataFixed.
	Value any // any: fixed data flows straight into html/template, whose parameter is any
	// Type is the template's dot for DataFixed and DataFetched; nil when unknown.
	Type reflect.Type
}

// FixedData is a source rendering v, whose static type is t.
func FixedData(v any, t reflect.Type) DataSource { // any: as DataSource.Value
	return DataSource{Kind: DataFixed, Value: v, Type: t}
}

// FetchedData is a source running h for every render, its data of type t. A nil
// h is no data.
func FetchedData(h DataHandlerFunc, t reflect.Type) DataSource {
	if h == nil {
		return DataSource{}
	}
	return DataSource{Kind: DataFetched, Handler: h, Type: t}
}

// EffectData is a source running h for what it declares, giving the template no
// data. A nil h is no data.
func EffectData(h DataHandlerFunc) DataSource {
	if h == nil {
		return DataSource{}
	}
	return DataSource{Kind: DataEffect, Handler: h}
}

// DataSource returns what f renders with. It is nil-safe.
func (f *Fragment) DataSource() DataSource {
	if f == nil {
		return DataSource{}
	}
	return f.data
}

// SetDataSource sets what f renders with.
func (f *Fragment) SetDataSource(d DataSource) { f.data = d }
```

In `internal/types/fragment.go`:
- Delete the `DataHandler DataHandlerFunc` and `Data any` fields and their comments (lines 52–59).
- Add, where they were:

  ```go
  	// data is what the fragment renders with; see DataSource. Unexported so
  	// only collage's constructors set it, and its type always matches its data.
  	data DataSource
  	// SkipTypeCheck excludes the fragment's template from registration's type
  	// check, for a check that is wrong about it. See WithoutTypeCheck.
  	SkipTypeCheck bool
  ```
- In `validate`, delete the `if f.DataHandler != nil && f.Data != nil { … ErrConflictingData … }` block: the builder now reports a second `WithData` itself.
- Update the comments on `Title`, `Static`, `Shared`, `Timeout` that say "DataHandler" to say "data handler".

In `internal/types/errors.go`, change `ErrConflictingData` to:

```go
// ErrConflictingData is returned when a fragment's data is set twice.
var ErrConflictingData = errors.New("collage: fragment data set twice")
```

Consumers:
- `internal/render/fragment.go` `attempt`: replace the `case f.DataHandler != nil:` arm's two uses with `ds := f.DataSource()` read once before the `switch`, `case ds.Handler != nil:` and `ds.Handler(ctx, rc.WithContext(ctx))`; the `default:` arm becomes `data = ds.Value`.
- `internal/render/prefetch.go:76`: `if child == nil || child.DataSource().Handler == nil {`; line 114: `f.DataSource().Handler(ctx, handlerRC.WithContext(ctx))`.
- `internal/core/fragmentrender.go:159`: `if f.DataSource().Handler != nil && !trusted(f) {`
- `internal/core/inspect.go:175`: `Handler: f.DataSource().Handler != nil,`
- `pkg/collage/fragment.go`: `WithDataHandler(h)` body becomes `b.setData(types.FetchedData(h, nil))`; `WithData(v)` body becomes `b.setData(types.FixedData(v, reflect.TypeOf(v)))`; add

  ```go
  // setData sets the fragment's data source, recording ErrConflictingData when
  // one is already set.
  func (b *FragmentBuilder) setData(d types.DataSource) *FragmentBuilder {
  	if b.fragment.DataSource().Kind != types.DataNone {
  		b.errs = append(b.errs, fmt.Errorf("%w: fragment %q", ErrConflictingData, b.fragment.Name))
  	}
  	b.fragment.SetDataSource(d)
  	return b
  }
  ```
  (`Effect`, `Load`, `DataHandler` keep returning `DataHandlerFunc` until Task 6.)

- [ ] **Step 4: Migrate the internal tests that set the removed fields**

Run: `grep -rn '\.DataHandler = \|DataHandler: \|\.Data = ' --include='*_test.go' internal pkg`
For each hit on a `*types.Fragment` (not `RenderContext`, events or other structs):
- `x.DataHandler = h` → `x.SetDataSource(types.FetchedData(h, nil))` (inside package `types`, drop the `types.` prefix)
- `x.Data = v` → `x.SetDataSource(types.FixedData(v, nil))`
- a composite literal `Fragment{…, DataHandler: h}` → build it, then call `SetDataSource` on it.
Tests asserting `ErrConflictingData` from `Validate` move to the builder: `NewFragment(…).WithData(a).WithData(b)` records it (find them with `grep -rn ErrConflictingData --include='*_test.go'`).

- [ ] **Step 5: Run everything**

Run: `go vet ./... && go test -count=1 ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git status --short
git add internal/types/ internal/render/ internal/core/ pkg/collage/fragment.go <each migrated test file>
git commit -m "refactor: a fragment's data is one typed source"
```

---

### Task 6: The public API: `collage.Data`

**Files:**
- Create: `pkg/collage/data.go`
- Modify: `pkg/collage/fragment.go` (remove `WithDataHandler`, old `DataHandler`, `Load`, `Effect`; `WithData(d Data)`; add `WithoutTypeCheck`)
- Modify: `pkg/collage/types.go:52` (remove the `DataHandlerFunc` alias)
- Modify: `internal/cli/add.go:531`, `internal/cli/scaffold/**/*.go.tmpl` (4 files), every `*_test.go` and example in the repo using the old API
- Test: `pkg/collage/data_test.go`

**Interfaces:**
- Consumes: `types.DataSource`, `types.FixedData`, `types.FetchedData`, `types.EffectData`, `Fragment.SetDataSource`, `Fragment.SkipTypeCheck`, `FragmentBuilder.setData` (Task 5).
- Produces (package `collage`):
  ```go
  type Data interface{ source() types.DataSource }
  func Load[T any](fn func(context.Context, *RenderContext) (T, error)) Data
  func DataHandler[T any](fn func(context.Context, *RenderContext) (T, []string, error)) Data
  func Value[T any](v T) Data
  func Effect(fn func(context.Context, *RenderContext) error) Data
  func (b *FragmentBuilder) WithData(d Data) *FragmentBuilder
  func (b *FragmentBuilder) WithoutTypeCheck() *FragmentBuilder
  ```
  A nil `fn` gives a nil `Data`; `WithData(nil)` leaves the fragment with no data.

- [ ] **Step 1: Write the failing test**

`pkg/collage/data_test.go` (package `collage`, internal test, to read the source):

```go
package collage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

type dataView struct{ Title string }

func TestData_RecordsTheType(t *testing.T) {
	viewType := reflect.TypeFor[dataView]()
	load := Load(func(context.Context, *RenderContext) (dataView, error) { return dataView{Title: "t"}, nil })
	handler := DataHandler(func(context.Context, *RenderContext) (*dataView, []string, error) {
		return &dataView{}, []string{"tag"}, nil
	})
	loose := Load(func(context.Context, *RenderContext) (any, error) { return 1, nil }) // any: the explicit opt-out the check skips

	tests := []struct {
		name string
		d    Data
		kind types.DataKind
		typ  reflect.Type
	}{
		{"Load", load, types.DataFetched, viewType},
		{"DataHandler", handler, types.DataFetched, reflect.TypeFor[*dataView]()},
		{"Value", Value(dataView{}), types.DataFixed, viewType},
		{"Effect", Effect(func(context.Context, *RenderContext) error { return nil }), types.DataEffect, nil},
		{"Load[any] is unknown", loose, types.DataFetched, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := NewFragment("f", "f.html").WithData(test.d).Build()
			got := f.DataSource()
			if got.Kind != test.kind || got.Type != test.typ {
				t.Errorf("source = kind %v type %v, want kind %v type %v", got.Kind, got.Type, test.kind, test.typ)
			}
		})
	}
}

func TestData_HandlersKeepTheirBehaviour(t *testing.T) {
	failing := DataHandler(func(context.Context, *RenderContext) (*dataView, []string, error) {
		return &dataView{}, []string{"t"}, errors.New("boom")
	}).source()
	data, tags, err := failing.Handler(context.Background(), nil)
	if data != nil || len(tags) != 1 || err == nil {
		t.Errorf("failed handler = %v, %v, %v; want nil data, its tags, the error", data, tags, err)
	}
	if Load[dataView](nil) != nil || DataHandler[dataView](nil) != nil || Effect(nil) != nil {
		t.Error("a nil fn must give a nil Data")
	}
}

func TestData_SetTwiceIsAConflict(t *testing.T) {
	b := NewFragment("f", "f.html").WithData(Value(1)).WithData(Value(2))
	if err := b.BuildErr(); !errors.Is(err, ErrConflictingData) {
		t.Errorf("BuildErr = %v, want ErrConflictingData", err)
	}
}

func TestData_WithoutTypeCheck(t *testing.T) {
	if !NewFragment("f", "f.html").WithoutTypeCheck().Build().SkipTypeCheck {
		t.Error("WithoutTypeCheck did not set SkipTypeCheck")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 -run '^TestData_' ./pkg/collage/`
Expected: FAIL to compile (`Value` undefined; `WithData` takes `any`).

- [ ] **Step 3: Implement**

`pkg/collage/data.go`:

```go
package collage

import (
	"context"
	"reflect"

	"github.com/Elagoht/collage/internal/types"
)

// Data is what a fragment renders with: a handler run for every render, a value
// fixed when the program starts, or an effect. Only Load, DataHandler, Value and
// Effect make one, so the Go type a fragment's template sees is always known —
// which is what lets registration check the template against it.
//
//	collage.NewFragment("post", "post.html").WithData(collage.Load(loadPost))
type Data interface {
	source() types.DataSource
}

type data struct{ s types.DataSource }

func (d data) source() types.DataSource { return d.s }

// templateType is T as the template's dot: unknown, and so not checked, when T
// is an interface — collage.Load[any] written on purpose.
func templateType[T any]() reflect.Type {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Interface {
		return nil
	}
	return t
}

// Load fetches a fragment's data on every render, reporting no dependency tags
// — for a page that is not cached, or whose data does not change:
//
//	collage.NewFragment("clock", "fragments/clock.html").
//		WithData(collage.Load(func(ctx context.Context, rc *collage.RenderContext) (clockView, error) {
//			return clockView{Now: time.Now()}, nil
//		})).
//		Build()
//
// A handler whose page is cached and whose data changes — a post, a count —
// should report that data's tags, which is DataHandler's shape. On an error the
// data is dropped. A nil fn is a nil Data.
func Load[T any](fn func(context.Context, *RenderContext) (T, error)) Data {
	if fn == nil {
		return nil
	}
	return data{types.FetchedData(func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: html/template's parameter type
		v, err := fn(ctx, rc)
		if err != nil {
			return nil, nil, err
		}
		return v, nil, nil
	}, templateType[T]())}
}

// DataHandler fetches a fragment's data on every render and reports the
// dependency tags it was derived from, so a cached page built from it is
// invalidated with it:
//
//	collage.NewFragment("post", "post.html").WithData(collage.DataHandler(loadPost))
//
// where loadPost returns (Post, []string, error). On an error the data is
// dropped rather than boxed — a nil *view returned with an error would
// otherwise become a typed nil that reads as present — and the tags are kept. A
// nil fn is a nil Data.
func DataHandler[T any](fn func(context.Context, *RenderContext) (T, []string, error)) Data {
	if fn == nil {
		return nil
	}
	return data{types.FetchedData(func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: html/template's parameter type
		v, tags, err := fn(ctx, rc)
		if err != nil {
			return nil, tags, err
		}
		return v, tags, nil
	}, templateType[T]())}
}

// Value hands the fragment's template v on every render — data fixed when the
// program starts, a list of links or a heading. Unlike a handler, it leaves a
// page that declares no strategy static.
func Value[T any](v T) Data {
	return data{types.FixedData(v, templateType[T]())}
}

// Effect runs fn on every render for what it declares — rc.HoistTitle, a
// plugin's Emit — and gives the template no data. It reports no dependency
// tags; see DataHandler for a handler whose declarations come from data that
// changes. A nil fn is a nil Data.
func Effect(fn func(context.Context, *RenderContext) error) Data {
	if fn == nil {
		return nil
	}
	return data{types.EffectData(func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: html/template's parameter type
		return nil, nil, fn(ctx, rc)
	})}
}
```

In `pkg/collage/fragment.go`:
- Delete `WithDataHandler`, and the old `DataHandler`, `Load`, `Effect` (their doc comments move to `data.go` above).
- Replace `WithData(v any)` with:

  ```go
  // WithData sets what the fragment renders with — collage.Load, DataHandler,
  // Value or Effect. A page rendering a fragment with a handler, and declaring
  // no strategy, is dynamic. Setting data twice records ErrConflictingData. A
  // nil d leaves the fragment with no data.
  //
  // A value several handlers need is fetched once through Once, within one
  // render, or Cached, across renders.
  func (b *FragmentBuilder) WithData(d Data) *FragmentBuilder {
  	if d == nil {
  		return b
  	}
  	return b.setData(d.source())
  }

  // WithoutTypeCheck leaves the fragment's template out of registration's check
  // against its data's type — for the rare check that is wrong, until it is
  // fixed. The fragment renders as before.
  func (b *FragmentBuilder) WithoutTypeCheck() *FragmentBuilder {
  	b.fragment.SkipTypeCheck = true
  	return b
  }
  ```
- Fix every doc-comment example in `pkg/collage` that shows `WithDataHandler(` or `WithData(v)` (`grep -n 'WithDataHandler\|WithData(' pkg/collage/*.go`).

In `pkg/collage/types.go`, delete the `DataHandlerFunc` alias and its comment.

- [ ] **Step 4: Migrate every caller in the repo**

Do it in this order — the `Value` rewrite must run before the handler rewrites, or it would wrap them. Files in `package collage` (pkg/collage's internal tests) take the names unqualified; every other file qualifies them with `collage.`:

```bash
calls='WithData(Handler)?\('
ext=$(grep -rlE --include='*.go' "$calls" . | xargs grep -L '^package collage$')
int=$(grep -lE "$calls" pkg/collage/*_test.go | xargs grep -l '^package collage$')
# fixed data
gofmt -l -w -r 'a.WithData(b) -> a.WithData(collage.Value(b))' $ext
gofmt -l -w -r 'a.WithData(b) -> a.WithData(Value(b))' $int
# typed handlers
for f in Load DataHandler Effect; do
  gofmt -l -w -r "a.WithDataHandler(collage.$f(b)) -> a.WithData(collage.$f(b))" $ext
  gofmt -l -w -r "a.WithDataHandler($f(b)) -> a.WithData($f(b))" $int
done
# anything left is a raw DataHandlerFunc
gofmt -l -w -r 'a.WithDataHandler(b) -> a.WithData(collage.DataHandler(b))' $ext
gofmt -l -w -r 'a.WithDataHandler(b) -> a.WithData(DataHandler(b))' $int
go build ./... && go vet ./...
```

Then by hand:
- Fix what `go vet` reports: a raw handler declared as `collage.DataHandlerFunc` (a variable or a factory's result type) becomes `collage.Data`, built with `collage.DataHandler(...)`.
- `grep -rn '(any, \[\]string, error)\|) (any, error)' --include='*_test.go' .` — give each handler its real return type. Keep `any` only where a test is about unknown data, with a `// any:` comment saying so.
- `internal/cli/add.go:531`: `WithData(collage.Load(%sData))`.
- `grep -rln 'WithDataHandler\|DataHandlerFunc\|WithData(' --include='*.tmpl' internal/cli/scaffold` — migrate each `.go.tmpl` by the same table (gofmt cannot parse templates; edit by hand).

- [ ] **Step 5: Run everything, the scaffold included**

```bash
go vet ./... && go test -count=1 ./...
grep -rn 'WithDataHandler\|DataHandlerFunc' --include='*.go' --include='*.tmpl' . | grep -v '^./internal/types/'
```
Expected: tests PASS; the grep prints nothing. The scaffold tests (`internal/cli`) build a generated project, so they prove the templates compile.

- [ ] **Step 6: Commit**

```bash
git status --short
git add pkg/ internal/ <each migrated file outside them, by name>
git commit -m "feat!: fragment data is a typed collage.Data

WithDataHandler and DataHandlerFunc are gone; WithData takes Load,
DataHandler, Value or Effect, and the template's data type is known."
```

---

### Task 7: Registration checks templates against their data

**Files:**
- Create: `internal/types/typeerror.go`
- Modify: `internal/template/engine.go` (add `TypeCheck` to `Engine`), create `internal/template/typecheck_engine.go` (the `HTMLEngine` method)
- Modify: `internal/core/registry.go` (`checkTemplates`)
- Modify: `pkg/collage/types.go` (aliases `TemplateTypeError`, `ErrTemplateType`)
- Test: `internal/types/typeerror_test.go`, `pkg/collage/typecheck_test.go`

**Interfaces:**
- Consumes: `typecheck.Checker`, `typecheck.Dot`, `typecheck.Finding` (Tasks 1–3); `Fragment.DataSource`, `SkipTypeCheck` (Task 5); `collage.Load`, `Value`, `WithoutTypeCheck` (Task 6).
- Produces:
  ```go
  // package types
  var ErrTemplateType = errors.New("collage: template does not fit its data")
  type TemplateTypeError struct {
  	Page, Fragment string
  	Template       string // file path, or `inline template of fragment "x"`
  	Line, Col      int
  	Expr, Reason, Suggestion string
  }
  func (e *TemplateTypeError) Error() string
  func (e *TemplateTypeError) Unwrap() error // ErrTemplateType
  // package template
  TypeCheck(path string, dot reflect.Type) []typecheck.Finding // on Engine and *HTMLEngine
  // package collage
  type TemplateTypeError = types.TemplateTypeError
  var ErrTemplateType = types.ErrTemplateType
  ```

- [ ] **Step 1: Write the failing tests**

`internal/types/typeerror_test.go`:

```go
package types

import (
	"errors"
	"testing"
)

func TestTemplateTypeError_Message(t *testing.T) {
	err := &TemplateTypeError{
		Page: "post", Fragment: "post-body", Template: "templates/post.html", Line: 12, Col: 9,
		Expr: "{{.Titel}}", Reason: "type blog.Post has no field or method Titel", Suggestion: "Title",
	}
	want := `collage: page "post": fragment "post-body" (templates/post.html:12:9): {{.Titel}}: type blog.Post has no field or method Titel (did you mean Title?)`
	if err.Error() != want {
		t.Errorf("Error() =\n%s\nwant\n%s", err.Error(), want)
	}
	if !errors.Is(err, ErrTemplateType) {
		t.Error("errors.Is(err, ErrTemplateType) = false")
	}
	err.Suggestion = ""
	if got := err.Error(); got[len(got)-1] == ')' {
		t.Errorf("no suggestion, yet the message ends with one: %s", got)
	}
}
```

`pkg/collage/typecheck_test.go` (package `collage_test`):

```go
package collage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type tcAuthor struct{ Name string }
type tcPost struct {
	Title  string
	Author tcAuthor
}

func tcApp(t *testing.T, files fstest.MapFS) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{FS: files, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: false, DefaultTTL: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func loadPost(context.Context, *collage.RenderContext) (tcPost, error) { return tcPost{Title: "t"}, nil }

func TestRegister_TemplateTypeErrors(t *testing.T) {
	files := fstest.MapFS{
		"t/layout.html":  {Data: []byte(`<html>{{.Whatever}}{{range .}}{{end}}{{template "partial.html"}}{{slot "content"}}</html>`)},
		"t/partial.html": {Data: []byte(`{{.Anything}}`)},
		"t/post.html":    {Data: []byte("{{.Titel}}\n{{.Author.Nmae}}\n{{.Title}}")},
	}
	app := tcApp(t, files)
	layout := collage.NewFragment("layout", "layout.html").Build()
	content := collage.NewFragment("post-body", "post.html").WithData(collage.Load(loadPost)).Build()
	err := app.RegisterPage(collage.NewPage("post").WithLayouts(layout).WithContent(content).WithPath("en", "/").Build())
	if !errors.Is(err, collage.ErrTemplateType) {
		t.Fatalf("RegisterPage error = %v, want ErrTemplateType", err)
	}
	var typeErrs []*collage.TemplateTypeError
	for _, e := range unwrapAll(err) {
		var te *collage.TemplateTypeError
		if errors.As(e, &te) {
			typeErrs = append(typeErrs, te)
		}
	}
	if len(typeErrs) != 2 {
		t.Fatalf("findings = %d (%v), want 2: the data-less layout reports nothing", len(typeErrs), err)
	}
	first, second := typeErrs[0], typeErrs[1]
	if first.Page != "post" || first.Fragment != "post-body" || first.Template != "post.html" || first.Line != 1 || first.Suggestion != "Title" {
		t.Errorf("first = %+v", first)
	}
	if second.Line != 2 || !strings.Contains(second.Reason, "Nmae") {
		t.Errorf("second = %+v", second)
	}
}

func unwrapAll(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range joined.Unwrap() {
			out = append(out, unwrapAll(e)...)
		}
		return out
	}
	return []error{err}
}

func TestRegister_TemplateTypeCheckOptOuts(t *testing.T) {
	files := fstest.MapFS{"t/post.html": {Data: []byte(`{{.Titel}}`)}}
	tests := []struct {
		name string
		f    *collage.Fragment
	}{
		{"WithoutTypeCheck", collage.NewFragment("p", "post.html").WithData(collage.Load(loadPost)).WithoutTypeCheck().Build()},
		{"Load[any]", collage.NewFragment("p", "post.html").WithData(collage.Load(func(context.Context, *collage.RenderContext) (any, error) { // any: the explicit opt-out under test
			return tcPost{}, nil
		})).Build()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := tcApp(t, files)
			if err := app.RegisterPage(collage.NewPage("p").WithContent(test.f).WithPath("en", "/").Build()); err != nil {
				t.Errorf("RegisterPage = %v, want nil", err)
			}
		})
	}
}

func TestRegister_TemplateTypeCheckSeesInlineAndFallback(t *testing.T) {
	app := tcApp(t, fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}})
	fallback := collage.NewInlineFragment("fb", `{{.Nope}}`).WithData(collage.Value(tcAuthor{})).Build()
	content := collage.NewInlineFragment("c", `{{.Titel}}`).WithData(collage.Load(loadPost)).WithFallback(fallback).Build()
	err := app.RegisterPage(collage.NewPage("p").WithContent(content).WithPath("en", "/").Build())
	if err == nil || !strings.Contains(err.Error(), `inline template of fragment "c"`) || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("RegisterPage = %v, want findings in the inline content and its fallback", err)
	}
}

func TestRegister_SharedPartialNamesTheIncludingFragment(t *testing.T) {
	files := fstest.MapFS{
		"t/card.html": {Data: []byte(`{{.Name}}`)},
		"t/a.html":    {Data: []byte(`{{template "card.html" .Author}}`)},
		"t/b.html":    {Data: []byte(`{{template "card.html" .}}`)},
	}
	app := tcApp(t, files)
	a := collage.NewFragment("a", "a.html").WithData(collage.Load(loadPost)).Build()
	b := collage.NewFragment("b", "b.html").WithData(collage.Load(loadPost)).Build()
	if err := app.RegisterPage(collage.NewPage("pa").WithContent(a).WithPath("en", "/a").Build()); err != nil {
		t.Errorf("page a = %v, want nil: the partial fits tcAuthor", err)
	}
	err := app.RegisterPage(collage.NewPage("pb").WithContent(b).WithPath("en", "/b").Build())
	var te *collage.TemplateTypeError
	if !errors.As(err, &te) || te.Fragment != "b" || te.Template != "card.html" {
		t.Errorf("page b = %v, want a finding in card.html naming fragment b", err)
	}
}
```

Template paths are relative to `TemplateConfig.Root`, as everywhere in the repo's tests (`t/p.html` on disk is `"p.html"`).

Add to `pkg/collage/typecheck_test.go`, for Review Focus #5:

```go
func TestRegister_TemplateTypeErrorsInAFixedOrder(t *testing.T) {
	files := fstest.MapFS{
		"t/layout.html": {Data: []byte(`{{slot "a"}}{{slot "b"}}`)},
		"t/a.html":      {Data: []byte(`{{.Zz}}`)},
		"t/b.html":      {Data: []byte(`{{.Aa}}`)},
	}
	for run := range 20 {
		app := tcApp(t, files)
		layout := collage.NewFragment("layout", "layout.html").
			WithSlotFragment("b", collage.NewFragment("b", "b.html").WithData(collage.Load(loadPost)).Build()).
			WithSlotFragment("a", collage.NewFragment("a", "a.html").WithData(collage.Load(loadPost)).Build()).
			Build()
		err := app.RegisterPage(collage.NewPage("p").WithContent(layout).WithPath("en", "/").Build())
		var order []string
		for _, e := range unwrapAll(err) {
			var te *collage.TemplateTypeError
			if errors.As(e, &te) {
				order = append(order, te.Template)
			}
		}
		if strings.Join(order, ",") != "a.html,b.html" {
			t.Fatalf("run %d: order = %v, want a.html then b.html", run, order)
		}
	}
}
```

And `internal/render/placeholders_test.go` (package `render`), so the checker's argument counts — read from the parse-time placeholders — can never disagree with the functions a render binds:

```go
package render

import (
	"context"
	"reflect"
	"testing"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// The type check counts a call's arguments against the function the templates
// were parsed with; a render calls the one slotFuncs binds. If the two ever
// differed in what they take or return first, every app calling it would fail
// at startup over a template that renders fine.
func TestSlotFuncs_MatchTheirPlaceholders(t *testing.T) {
	engine := newEngine(t, Options{})
	f := fragment("c", "leaf.html")
	rc := types.NewRenderContext(context.Background(), nil, pageWith(f), "en", nil)
	state := &renderState{page: "p", tags: map[string]struct{}{}, hoistToken: newHoistToken()}
	bound := engine.slotFuncs(rc, f, state, nil, nil)
	for name, placeholder := range template.DefaultFuncs() {
		fn, ok := bound[name]
		if !ok || fn == nil {
			continue
		}
		pt, bt := reflect.TypeOf(placeholder), reflect.TypeOf(fn)
		if pt.NumIn() != bt.NumIn() || pt.IsVariadic() != bt.IsVariadic() || pt.Out(0) != bt.Out(0) {
			t.Errorf("%s: placeholder %v, bound %v", name, pt, bt)
		}
	}
}
```

This passes today (probed: `hoist` differs only in its error result, which the check does not read).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run 'TemplateTypeError|TemplateType' ./internal/types/ ./pkg/collage/`
Expected: FAIL to compile (`TemplateTypeError`, `ErrTemplateType` undefined).

- [ ] **Step 3: Implement**

`internal/types/typeerror.go`:

```go
package types

import (
	"errors"
	"fmt"
)

// ErrTemplateType is what every TemplateTypeError matches with errors.Is.
var ErrTemplateType = errors.New("collage: template does not fit its data")

// TemplateTypeError is one expression in a fragment's template certain to fail
// when it renders, found at registration by checking the template against the
// Go type of the fragment's data.
type TemplateTypeError struct {
	Page, Fragment string
	// Template is the file the expression is in — the fragment's own, or a
	// partial it includes — or `inline template of fragment "x"`.
	Template  string
	Line, Col int
	// Expr is the expression as written, "{{.Titel}}".
	Expr string
	// Reason says what is wrong; Suggestion is the closest name that exists, or "".
	Reason, Suggestion string
}

func (e *TemplateTypeError) Error() string {
	msg := fmt.Sprintf("collage: page %q: fragment %q (%s:%d:%d): %s: %s",
		e.Page, e.Fragment, e.Template, e.Line, e.Col, e.Expr, e.Reason)
	if e.Suggestion != "" {
		msg += " (did you mean " + e.Suggestion + "?)"
	}
	return msg
}

func (e *TemplateTypeError) Unwrap() error { return ErrTemplateType }
```

In `pkg/collage/types.go`, beside `ErrUnknownSlot`:

```go
// TemplateTypeError is one expression in a fragment's template that does not fit
// the fragment's data type, reported by RegisterPage. A page's findings come back
// joined; read each with errors.As.
type TemplateTypeError = types.TemplateTypeError

// ErrTemplateType matches every TemplateTypeError.
var ErrTemplateType = types.ErrTemplateType
```

In `internal/template/engine.go`, add to `Engine` after `SlotCalls`:

```go
	// TypeCheck walks the template at path against dot, the Go type its data
	// will have — nil when unknown — and returns what is certain to fail when it
	// renders.
	TypeCheck(path string, dot reflect.Type) []typecheck.Finding
```

`internal/template/typecheck_engine.go`:

```go
package template

import (
	"reflect"
	"text/template/parse"

	"github.com/Elagoht/collage/internal/template/typecheck"
)

// TypeCheck walks the template at path against dot with the functions the set
// was parsed with: collage's own, the application's and the plugins'.
func (e *HTMLEngine) TypeCheck(path string, dot reflect.Type) []typecheck.Finding {
	e.mu.RLock()
	set, base := e.tmpl, e.base
	e.mu.RUnlock()

	funcs := make(map[string]reflect.Type, len(base))
	for name, fn := range base {
		funcs[name] = reflect.TypeOf(fn)
	}
	c := typecheck.Checker{
		Lookup: func(name string) *parse.Tree {
			if t := set.Lookup(name); t != nil {
				return t.Tree
			}
			return nil
		},
		Funcs: funcs,
	}
	return c.Check(path, typecheck.Dot{Type: dot})
}
```

Find every other implementation of `Engine` (`grep -rn 'func (.*) SlotCalls' --include='*.go' .`) and give it `TypeCheck`; an embedding test fake gets it for free.

In `internal/core/registry.go` `checkTemplates`:
- Declare `var findings []*types.TemplateTypeError` before `visit`.
- In `visit`, right after the `Lookup` check and **before** `calls, dynamic := …` (whose early `return nil` would skip it), add:

  ```go
  		findings = append(findings, a.typeFindings(p, f, name)...)
  ```
- Replace the final `return nil` with `return joinTypeErrors(findings)`.
- Update the doc comment: it now also reports, joined, every `types.TemplateTypeError` of the page's fragments, after the first structural error it would return has not occurred.
- Add:

  ```go
  // typeFindings checks f's template against its data's type. A fragment built
  // WithoutTypeCheck, or whose data type is unknown, is not walked; a fragment
  // with no data is walked with an unknown dot, which still checks its function
  // calls.
  func (a *App) typeFindings(p *types.Page, f *types.Fragment, name string) []*types.TemplateTypeError {
  	if f.SkipTypeCheck {
  		return nil
  	}
  	ds := f.DataSource()
  	if (ds.Kind == types.DataFixed || ds.Kind == types.DataFetched) && ds.Type == nil {
  		return nil
  	}
  	var out []*types.TemplateTypeError
  	for _, found := range a.tmpl.TypeCheck(name, ds.Type) {
  		out = append(out, &types.TemplateTypeError{
  			Page: p.Name, Fragment: f.Name, Template: types.HumanizeTemplateNames(found.Template),
  			Line: found.Line, Col: found.Col, Expr: found.Expr, Reason: found.Reason, Suggestion: found.Suggestion,
  		})
  	}
  	return out
  }
  ```

  `HumanizeTemplateNames` turns `inline:c#…` into `inline template of fragment "c"`.

  ```go
  // joinTypeErrors joins a page's findings into one error in a fixed order —
  // template, line, column, fragment — whatever order the fragments were
  // visited in. Nil when there are none.
  func joinTypeErrors(found []*types.TemplateTypeError) error {
  	if len(found) == 0 {
  		return nil
  	}
  	sort.SliceStable(found, func(i, j int) bool {
  		a, b := found[i], found[j]
  		if a.Template != b.Template {
  			return a.Template < b.Template
  		}
  		if a.Line != b.Line {
  			return a.Line < b.Line
  		}
  		if a.Col != b.Col {
  			return a.Col < b.Col
  		}
  		return a.Fragment < b.Fragment
  	})
  	errs := make([]error, len(found))
  	for i, e := range found {
  		errs[i] = e
  	}
  	return errors.Join(errs...)
  }
  ```

- [ ] **Step 4: Run them to verify they pass, then everything**

Run: `go test -count=1 -run 'TemplateTypeError|TemplateType|SharedPartial' ./internal/types/ ./pkg/collage/ && go vet ./... && go test -count=1 ./...`
Expected: PASS. If an existing test now fails with `ErrTemplateType`, read the finding: either the test template really does not fit its data (fix the test's template or data and say so in the commit), or the walker is wrong (add the case to Task 4's differential test first, then fix the walker).

- [ ] **Step 5: Benchmark registration**

Run: `go test -count=1 -run x -bench 'LargeProject' -benchmem ./pkg/collage/`
Expected: no regression worth noting in render (the check runs at registration only). Note the numbers in the commit message.

- [ ] **Step 6: Commit**

```bash
git status --short
git add internal/types/typeerror.go internal/types/typeerror_test.go internal/template/ internal/core/registry.go internal/render/placeholders_test.go pkg/collage/types.go pkg/collage/typecheck_test.go
git commit -m "feat: RegisterPage checks every template against its data's type"
```

---

### Task 8: `collage inspect` exports data types

**Files:**
- Create: `internal/core/inspecttypes.go`
- Modify: `internal/core/inspect.go` (`Inspection.Types`, `InspectedFragment.DataType`, `InspectedFragment.TypeCheck`, `addFragment`)
- Test: `internal/core/inspecttypes_test.go`

**Interfaces:**
- Consumes: `Fragment.DataSource`, `SkipTypeCheck` (Task 5).
- Produces:
  ```go
  type InspectedType struct {
  	Kind    string            `json:"kind"`
  	Fields  []InspectedField  `json:"fields,omitempty"`
  	Methods []InspectedMethod `json:"methods,omitempty"`
  }
  type InspectedField struct{ Name, Type string } // json "name", "type"
  type InspectedMethod struct{ Name string; Args int; Returns string } // json "name", "args", "returns"
  // Inspection gains: Types map[string]InspectedType `json:"types,omitempty"`
  // InspectedFragment gains: DataType *string `json:"dataType"`; TypeCheck *bool `json:"typeCheck,omitempty"`
  func typeTable(roots []reflect.Type) map[string]InspectedType
  ```

- [ ] **Step 1: Write the failing test**

`internal/core/inspecttypes_test.go`:

```go
package core

import (
	"reflect"
	"testing"
	"time"
)

type itUser struct{ Name string }
type itComment struct {
	Body    string
	Replies []itComment
	By      *itUser
}
type itPost struct {
	Title    string
	At       time.Time
	Comments []itComment
	hidden   string
}

func (p itPost) URL() string                 { return "/" }
func (p *itPost) Edit(a, b string) string    { return a + b }

func TestTypeTable(t *testing.T) {
	table := typeTable([]reflect.Type{reflect.TypeFor[*itPost]()})
	post, ok := table["core.itPost"]
	if !ok {
		t.Fatalf("table = %v, want core.itPost", table)
	}
	if post.Kind != "struct" || len(post.Fields) != 3 {
		t.Errorf("itPost = %+v, want struct with Title, At, Comments", post)
	}
	if post.Fields[2].Type != "[]core.itComment" {
		t.Errorf("Comments field = %+v", post.Fields[2])
	}
	if len(post.Methods) != 2 || post.Methods[0].Name != "Edit" || post.Methods[0].Args != 2 || post.Methods[1].Returns != "string" {
		t.Errorf("itPost methods = %+v, want Edit(2) and URL", post.Methods)
	}
	if _, ok := table["core.itComment"]; !ok {
		t.Error("recursive itComment missing")
	}
	if _, ok := table["core.itUser"]; !ok {
		t.Error("itUser, reached through a pointer field, missing")
	}
	if _, ok := table["time.Time"]; ok {
		t.Error("time.Time is a standard library type and must stay opaque")
	}
}

func TestIsStandard(t *testing.T) {
	if !isStandard(reflect.TypeFor[time.Time]()) {
		t.Error("time.Time is standard")
	}
	if isStandard(reflect.TypeFor[itPost]()) {
		t.Error("a type of this module is not standard")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -count=1 -run TypeTable ./internal/core/`
Expected: FAIL to compile: `undefined: typeTable`.

- [ ] **Step 3: Implement**

`internal/core/inspecttypes.go`:

```go
package core

import (
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
)

// InspectedType is the shape of a type a fragment's template can reach, for an
// editor completing {{.}}: its exported fields and the exported methods of it
// and its pointer.
type InspectedType struct {
	Kind    string            `json:"kind"`
	Fields  []InspectedField  `json:"fields,omitempty"`
	Methods []InspectedMethod `json:"methods,omitempty"`
}

// InspectedField is one exported field, promoted ones included.
type InspectedField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// InspectedMethod is one exported method a template can call.
type InspectedMethod struct {
	Name    string `json:"name"`
	Args    int    `json:"args"`
	Returns string `json:"returns"`
}

// typeTable describes every named type reachable from roots through fields,
// element types and method results, keyed by its Go name ("blog.Post"). A
// standard library type is opaque: a template's author does not need time.Time
// spelled out, and the table stays the size of the application.
func typeTable(roots []reflect.Type) map[string]InspectedType {
	table := make(map[string]InspectedType)
	seen := make(map[reflect.Type]bool)
	var visit func(t reflect.Type)
	visit = func(t reflect.Type) {
		for {
			switch t.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
				t = t.Elem()
				continue
			case reflect.Map:
				visit(t.Key())
				t = t.Elem()
				continue
			}
			break
		}
		if seen[t] || t.Name() == "" || isStandard(t) {
			return
		}
		seen[t] = true
		entry := InspectedType{Kind: t.Kind().String()}
		if t.Kind() == reflect.Struct {
			for _, f := range reflect.VisibleFields(t) {
				if f.IsExported() && !f.Anonymous {
					entry.Fields = append(entry.Fields, InspectedField{Name: f.Name, Type: f.Type.String()})
					visit(f.Type)
				}
			}
		}
		pt := reflect.PointerTo(t)
		for i := range pt.NumMethod() {
			m := pt.Method(i)
			if m.Type.NumOut() == 0 {
				continue
			}
			entry.Methods = append(entry.Methods, InspectedMethod{Name: m.Name, Args: m.Type.NumIn() - 1, Returns: m.Type.Out(0).String()})
			visit(m.Type.Out(0))
		}
		sort.Slice(entry.Methods, func(i, j int) bool { return entry.Methods[i].Name < entry.Methods[j].Name })
		table[t.String()] = entry
	}
	for _, root := range roots {
		if root != nil {
			visit(root)
		}
	}
	return table
}

// modules are the paths of the running program's main module and every module
// it depends on: a package under one of them is not the standard library,
// whatever its path looks like — `module sen-de-yaz` has no dot in it.
var modules = func() []string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	out := []string{info.Main.Path}
	for _, dep := range info.Deps {
		out = append(out, dep.Path)
	}
	return out
}()

// isStandard reports a type from the standard library: one in no module the
// program is built from, not in package main, and whose path's first element
// has no dot.
func isStandard(t reflect.Type) bool {
	path := t.PkgPath()
	if path == "" {
		return true
	}
	if path == "main" {
		return false
	}
	for _, m := range modules {
		if m != "" && (path == m || strings.HasPrefix(path, m+"/")) {
			return false
		}
	}
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}
```

In `internal/core/inspect.go`:
- Add `Types map[string]InspectedType `json:"types,omitempty"`` to `Inspection`.
- Add to `InspectedFragment`:

  ```go
  	// DataType is the Go type the template sees as dot: "blog.Post", "nil"
  	// when the fragment has no data, null when the type is unknown.
  	DataType *string `json:"dataType"`
  	// TypeCheck is false when the fragment was built WithoutTypeCheck.
  	TypeCheck *bool `json:"typeCheck,omitempty"`
  ```
- In `addFragment`, compute:

  ```go
  		ds := f.DataSource()
  		var dataType *string
  		switch {
  		case ds.Kind == types.DataNone || ds.Kind == types.DataEffect:
  			s := "nil"
  			dataType = &s
  		case ds.Type != nil:
  			s := ds.Type.String()
  			dataType = &s
  			roots = append(roots, ds.Type)
  		}
  		var typeCheck *bool
  		if f.SkipTypeCheck {
  			off := false
  			typeCheck = &off
  		}
  ```
  and set `DataType: dataType, TypeCheck: typeCheck` on the entry; declare `var roots []reflect.Type` before `addFragment`, and after every page is visited set `out.Types = typeTable(roots)` when `len(roots) > 0`.

- [ ] **Step 4: Run it, and the inspect tests**

Run: `go test -count=1 ./internal/core/ ./internal/cli/`
Expected: PASS. Existing golden JSON for inspect (if any: `grep -rln 'collage-inspect\|"fragments"' --include='*_test.go' internal`) gains `"dataType"`; update the golden values.

- [ ] **Step 5: Commit**

```bash
git status --short
git add internal/core/inspect.go internal/core/inspecttypes.go internal/core/inspecttypes_test.go <updated goldens>
git commit -m "feat: collage inspect describes each fragment's data type"
```

---

### Task 9: False-alarm sweep over real apps

Copies only. **Never edit the user's repositories.**

**Files:** none in the repo (work in the scratchpad).

- [ ] **Step 1: Copy the apps**

```bash
S=<scratchpad>/sweep; mkdir -p $S
for app in collage-docs furkanbaytekin sen-de-yaz pusula; do
  rsync -a --exclude .git --exclude bin --exclude .cache ~/Desktop/$app/ $S/$app/
done
```
Also generate a fresh scaffolded demo project with the branch's CLI: `go run ./cmd/collage new $S/demo` (check the subcommand name with `go run ./cmd/collage help`).

- [ ] **Step 2: Point each copy at the branch and migrate it mechanically**

In each copy: `go mod edit -replace github.com/Elagoht/collage=<worktree path>`, then apply Task 6 Step 4's gofmt rewrites and fix what `go build ./...` reports (raw `any` handlers become `collage.DataHandler(...)`, factories return `collage.Data`). Plugins the app imports are on the old API: for each, clone it next to the copy, migrate it the same way and `replace` it too. Keep a list of what each migration needed — it becomes the migration guide's examples.

- [ ] **Step 3: Run each app's registration**

Each app's own tests (`go test -count=1 ./...`) and its startup (`go run . collage-check` or a short `go run .` with a timeout) exercise `RegisterPage`.

- [ ] **Step 4: Classify every finding**

For each `TemplateTypeError`:
- **Real bug** (the template would fail on that path): record app, file:line, finding. Do not fix it.
- **False alarm**: reproduce it as a case in Task 4's differential test (it must fail there), fix the walker, re-run Tasks 1–4's tests, commit `fix: typecheck <what>`, and re-run the sweep.

Expected end state: zero false alarms. Report the real bugs to the user as a list in the final summary.

---

### Task 10: Documentation and changelog

**Files:**
- Modify: `docs/fragments.md`, `docs/caching.md`, `docs/architecture.md` (data API; a "How templates are checked" section in `docs/fragments.md`), `README.md`, `CHANGELOG.md`
- Grep: `grep -rn 'WithDataHandler\|DataHandlerFunc\|WithData(' docs README.md | grep -v superpowers`

- [ ] **Step 1: Migrate every code sample** by the table in the spec's Migration section, with real return types, never `any`.

- [ ] **Step 2: Write "How templates are checked"** in `docs/fragments.md`: what is checked (fields, methods and addressability, maps, range, len/index, function argument counts, partials per type), what is not (unknown types, nil data, nil pointers inside data), the error shape, `errors.As` with `collage.TemplateTypeError`, `WithoutTypeCheck()`, and `collage.Load[any]` as the explicit opt-out.

- [ ] **Step 3: CHANGELOG** — a `## Unreleased` section: **Breaking** first, with the migration table; then **Added** (the type check, `TemplateTypeError`, `ErrTemplateType`, `WithoutTypeCheck`, `Value`, inspect's `types`/`dataType`); state plainly that an app may now fail at startup over a template that would already fail when rendered.

- [ ] **Step 4: Verify and commit**

```bash
go test -count=1 ./...
git status --short
git add docs/fragments.md docs/caching.md docs/architecture.md README.md CHANGELOG.md
git commit -m "docs: typed fragment data and template type checking"
```

---

## After this plan

Not part of this plan; each is its own piece of work once this branch is reviewed and merged:
- Tag v0.49.0, then re-release the plugins that build fragments or handlers (order and CI rerun in the spec's Release section).
- Docs site EN+TR, the VS Code extension's snippets and its `{{.` completion from `types`/`dataType`.
- v0.50.0: `docs/superpowers/specs/2026-10-06-typed-keys-design.md`.
