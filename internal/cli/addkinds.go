package cli

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// layoutsPackage returns the package fragments/layouts declares, or "layouts"
// when it holds no Go file yet.
func (g generator) layoutsPackage() (string, error) {
	files, err := parseDir(filepath.Join(g.proj.dir, "fragments", "layouts"))
	if err != nil {
		return "", err
	}
	for _, f := range files {
		return f.ast.Name.Name, nil
	}
	return "layouts", nil
}

// layout writes fragments/layouts/<name>.go, a layout that nests inside the
// site's own around a page's content, marked so findLayout never takes it for
// the site's layout.
func (g generator) layout(notes []string) ([]fileChange, []string, error) {
	dir := filepath.Join(g.proj.dir, "fragments", "layouts")
	if err := checkIdentifiers(g.proj, dir, g.pascal, g.camel+"Block"); err != nil {
		return nil, nil, err
	}
	pkg, err := g.layoutsPackage()
	if err != nil {
		return nil, nil, err
	}
	markup := fmt.Sprintf("<div class=%q>\n  {{slot \"content\"}}\n</div>\n", g.req.name)

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport \"github.com/Elagoht/collage/pkg/collage\"\n\n", pkg)
	fmt.Fprintf(&b, "// Returns the %s layout: it nests inside the site's own, around a page's\n", g.req.name)
	b.WriteString("// content, which it renders where it says {{slot \"content\"}}.\n//\n")
	fmt.Fprintf(&b, "//\tWithLayouts(layouts.Master(), layouts.%s())\n//\n%s\n", g.pascal, nestedMarker)
	fmt.Fprintf(&b, "func %s() *collage.Fragment {\n", g.pascal)
	changes := []fileChange{}
	if g.req.file {
		rel := "layouts/" + g.req.name + g.proj.templateExt
		fmt.Fprintf(&b, "\treturn collage.NewFragment(%q, %q).\n\t\tBuild()\n}\n", g.camel+"Layout", rel)
		changes = append(changes, fileChange{
			path:    filepath.Join(g.proj.dir, g.proj.templateRoot, filepath.FromSlash(rel)),
			content: []byte(markup),
		})
	} else {
		fmt.Fprintf(&b, "\treturn collage.NewInlineFragment(%q, %sBlock).\n\t\tBuild()\n}\n\n", g.camel+"Layout", g.camel)
		fmt.Fprintf(&b, "const %sBlock collage.InlineHTML = `%s`\n", g.camel, strings.TrimSuffix(markup, "\n"))
	}
	changes = append([]fileChange{{path: filepath.Join(dir, g.req.name+".go"), content: []byte(b.String())}}, changes...)
	return changes, append(notes,
		fmt.Sprintf("wrap a new page in it: collage add page <[area/]name> --layout %s", g.req.name),
		fmt.Sprintf("wrap a page you have: .WithLayouts(layouts.Master(), layouts.%s())", g.pascal),
	), nil
}

// middleware writes middleware/<name>.go and registers it with app.Use in
// routes.go, after the middleware already there.
func (g generator) middleware(notes []string) ([]fileChange, []string, error) {
	dir := filepath.Join(g.proj.dir, "middleware")
	if err := checkIdentifiers(g.proj, dir, g.pascal); err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	b.WriteString("package middleware\n\nimport \"net/http\"\n\n")
	fmt.Fprintf(&b, "// Runs around every request the site answers — pages, actions, documents and\n")
	b.WriteString("// mounts — before routing. The first middleware app.Use registers is the\n")
	b.WriteString("// outermost. Guards run after routing, so a guard reads what this puts in the\n")
	b.WriteString("// request's context:\n//\n")
	b.WriteString("//\tnext.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))\n//\n")
	b.WriteString("// It may also answer by itself — a redirect, a 401 — and not call next.\n")
	fmt.Fprintf(&b, "func %s(next http.Handler) http.Handler {\n", g.pascal)
	b.WriteString("\treturn http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n\t\tnext.ServeHTTP(w, r)\n\t})\n}\n")
	changes := []fileChange{{path: filepath.Join(dir, g.req.name+".go"), content: []byte(b.String())}}

	change, note, err := registerStatement(g.proj, "Use", g.proj.module+"/middleware", "middleware", func(ident string) string {
		return ident + "." + g.pascal
	})
	if err != nil {
		return nil, nil, err
	}
	if change != nil {
		changes = append(changes, *change)
	}
	if note != "" {
		notes = append(notes, note)
	}
	return changes, append(notes,
		fmt.Sprintf("middleware.%s runs around every request; the first app.Use is the outermost", g.pascal),
		"a middleware that needs a service takes it in a constructor you write: func Load(users *Users) func(http.Handler) http.Handler",
	), nil
}

// command writes commands/<name>.go and registers it with
// app.RegisterCommand in routes.go.
func (g generator) command(notes []string) ([]fileChange, []string, error) {
	dir := filepath.Join(g.proj.dir, "commands")
	if err := checkIdentifiers(g.proj, dir, g.pascal); err != nil {
		return nil, nil, err
	}
	taken, err := declaredCommands(g.proj.dir)
	if err != nil {
		return nil, nil, err
	}
	if where, ok := taken[g.req.ident]; ok {
		return nil, nil, fmt.Errorf("a command named %q already exists, in %s; name this one with --name", g.req.ident, where)
	}

	var b strings.Builder
	b.WriteString("package commands\n\nimport (\n\t\"context\"\n\t\"fmt\"\n\n\t\"github.com/Elagoht/collage/pkg/collage\"\n)\n\n")
	fmt.Fprintf(&b, "// Returns the %s command: \"go run . %s\" while developing, or the built\n", g.req.ident, g.req.ident)
	fmt.Fprintf(&b, "// binary followed by %s on a server. It runs instead of the server, after\n", g.req.ident)
	b.WriteString("// every plugin's Init, and every plugin is shut down when it returns.\n")
	fmt.Fprintf(&b, "func %s() collage.Command {\n\treturn collage.Command{\n", g.pascal)
	fmt.Fprintf(&b, "\t\tName:  %q,\n\t\tUsage: %q,\n\t\tShort: %q,\n", g.req.ident, g.req.ident+" [args]", "what "+g.req.ident+" does, in the command list")
	b.WriteString("\t\tRun: func(_ context.Context, args []string) error {\n")
	fmt.Fprintf(&b, "\t\t\tfmt.Printf(%q, len(args))\n\t\t\treturn nil\n\t\t},\n\t}\n}\n", g.req.ident+": %d arguments\n")
	changes := []fileChange{{path: filepath.Join(dir, g.req.name+".go"), content: []byte(b.String())}}

	change, note, err := registerStatement(g.proj, "RegisterCommand", g.proj.module+"/commands", "commands", func(ident string) string {
		return ident + "." + g.pascal + "()"
	})
	if err != nil {
		return nil, nil, err
	}
	if change != nil {
		changes = append(changes, *change)
	}
	if note != "" {
		notes = append(notes, note)
	}
	return changes, append(notes,
		fmt.Sprintf("run it: go run . %s", g.req.ident),
		"a command that needs a service takes it as an argument you add, passed in where it is registered",
	), nil
}

// declaredCommands returns the names the project's Go code gives a
// collage.Command literal, each with the file that declares it.
func declaredCommands(root string) (map[string]string, error) {
	names := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || slices.Contains([]string{"vendor", "testdata", "node_modules", "bin", "dist"}, name)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parseFile(path)
		if err != nil {
			return err
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || selectorName(lit.Type) != "Command" {
				return true
			}
			if name, ok := stringField(lit, "Name"); ok {
				rel, _ := filepath.Rel(root, path)
				names[name] = filepath.ToSlash(rel)
			}
			return true
		})
		return nil
	})
	return names, err
}

// ensureImport returns the identifier f refers to the package at pkgPath by,
// and the edit that imports it when f does not yet — as pkgName, or pkgName
// with a number when that name is taken.
func ensureImport(proj project, f parsedFile, pkgPath, pkgName string) (string, []edit) {
	if ident, ok := importedAs(proj, f.ast, pkgPath); ok {
		return ident, nil
	}
	ident := pkgName
	if taken := identsInUse(proj, f.ast); slices.Contains(taken, ident) {
		for n := 2; ; n++ {
			if candidate := ident + strconv.Itoa(n); !slices.Contains(taken, candidate) {
				ident = candidate
				break
			}
		}
	}
	spec := importSpec{path: pkgPath}
	if ident != pkgName {
		spec.alias = ident
	}
	return ident, []edit{importEdit(f, spec, proj.module)}
}

// registerStatement returns the change to routes.go that calls app.<method>
// with what expr names, as a statement of its own in the register function:
//
//	if err := app.Use(middleware.Load); err != nil {
//		return err
//	}
//
// after the last call to the same method there, or first in the body, so
// middleware keeps the order it was added in. It needs a register function
// that takes the *collage.App and returns an error; without one it changes
// nothing and returns the line to add by hand.
func registerStatement(proj project, method, pkgPath, pkgName string, expr func(ident string) string) (*fileChange, string, error) {
	files, err := parseDir(proj.dir)
	if err != nil {
		return nil, "", err
	}
	for _, f := range files {
		fn := registerFunc(f.ast)
		if fn == nil {
			continue
		}
		apps := appParams(fn)
		results := fn.Type.Results
		if len(apps) == 0 || results == nil || len(results.List) != 1 || len(results.List[0].Names) > 1 {
			continue
		}
		if id, ok := results.List[0].Type.(*ast.Ident); !ok || id.Name != "error" {
			continue
		}
		app := apps[0]
		ident, edits := ensureImport(proj, f, pkgPath, pkgName)
		stmt := fmt.Sprintf("if err := %s.%s(%s); err != nil {\nreturn err\n}", app, method, expr(ident))

		var after ast.Stmt
		for _, s := range fn.Body.List {
			ast.Inspect(s, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != method {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == app {
					after = s
				}
				return true
			})
		}
		offset := func(p token.Pos) int { return f.fset.Position(p).Offset }
		if after != nil {
			edits = append(edits, edit{offset: offset(after.End()), text: "\n" + stmt})
		} else {
			edits = append(edits, edit{offset: offset(fn.Body.Lbrace) + 1, text: "\n" + stmt + "\n"})
		}
		return &fileChange{path: f.path, content: applyEdits(f.src, edits), existed: true}, "", nil
	}
	return nil, fmt.Sprintf("register it yourself, in the function that builds the app: app.%s(%s)", method, expr(pkgName)), nil
}

// attachToPage returns the change that adds .WithActionFor(actions.<Name>())
// to the page the request's --page names, before the Build that ends its
// NewPage chain. The file must hold exactly one such chain: with none, or
// several, it changes nothing and returns the line to add.
func (g generator) attachToPage() (*fileChange, string, error) {
	area, name := g.req.page[:len(g.req.page)-1], g.req.page[len(g.req.page)-1]
	path := filepath.Join(append(append([]string{g.proj.dir, "pages"}, area...), name+".go")...)
	rel := filepath.ToSlash(strings.TrimPrefix(path, g.proj.dir+string(filepath.Separator)))
	if _, err := os.Stat(path); err != nil {
		return nil, "", fmt.Errorf("--page %s: %s does not exist", strings.Join(g.req.page, "/"), rel)
	}
	f, err := parseFile(path)
	if err != nil {
		return nil, "", err
	}
	var builds []*ast.SelectorExpr
	ast.Inspect(f.ast, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Build" || !chainStartsWith(sel.X, "NewPage") {
			return true
		}
		builds = append(builds, sel)
		return true
	})
	attach := fmt.Sprintf(".WithActionFor(actions.%s())", g.pascal)
	if len(builds) != 1 {
		return nil, fmt.Sprintf("%s has %d NewPage chains; attach it yourself: %s", rel, len(builds), attach), nil
	}
	ident, edits := ensureImport(g.proj, f, g.proj.module+"/actions", "actions")
	edits = append(edits, edit{
		offset: f.fset.Position(builds[0].Sel.Pos()).Offset,
		text:   fmt.Sprintf("WithActionFor(%s.%s()).\n", ident, g.pascal),
	})
	return &fileChange{path: path, content: applyEdits(f.src, edits), existed: true}, fmt.Sprintf("attached to the page in %s", rel), nil
}

// chainStartsWith reports whether a builder chain — x.A().B().C — begins with a
// call to a function or method named fn.
func chainStartsWith(x ast.Expr, fn string) bool {
	for {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return false
		}
		switch f := call.Fun.(type) {
		case *ast.SelectorExpr:
			if f.Sel.Name == fn {
				return true
			}
			x = f.X
		case *ast.Ident:
			return f.Name == fn
		default:
			return false
		}
	}
}
