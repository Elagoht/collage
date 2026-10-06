package cli

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ErrAddUsage is returned by the "add" command for arguments it cannot act on.
var ErrAddUsage = errors.New("collage: add")

// addUsage is "collage help add"'s own usage text.
const addUsage = `Usage: collage add <page|fragment|action|document> <[area/]name> [flags]

Writes a page, a fragment, an action or a document into the current project, in
the layout "collage new" scaffolds, and registers it in routes.go:

  page       pages/<area>/<name>.go, and its content in fragments/pages/<area>/<name>.go
  fragment   fragments/pages/<area>/<name>.go alone, for a slot of a page
  action     a builder in actions/<area>.go and its handler in actions/funcs/<area>.go;
             registered when it has a --path, attached to a page otherwise
  document   documents/<name>.go

A name is lowercase letters, digits and dashes: "blog/post", "story-detail". The
page, action or document is named after its last segment, unless --name says
otherwise, and a name the project already uses is refused before anything is
written. Constructors take no arguments; add the services they need by hand.

  --file          keep the template in a file under the template root, not inline
  --path pattern  the URL (default: /<area>/<name> for a page and a document; an
                  action without one answers at the page it is attached to)
  --name name     the page's, action's or document's name (default: <name>)
  --locale code   the locale its path is in (default: main.go's Locale.Default, or en)
  --type type     a document's content type (default: text/plain; charset=utf-8)
  --dir path      the project (default: the current directory)

Nothing is overwritten: a file that exists, or a name already declared in the
package a file goes into, stops the command before it writes anything.
`

// addValueFlags names the "add" command's flags that consume a following
// argument, for splitPositional.
var addValueFlags = map[string]bool{"path": true, "name": true, "locale": true, "type": true, "dir": true}

// segmentPattern is what each segment of an add target may be: lowercase, a
// letter first, so it is a directory name, a URL segment and — once its dashes
// are folded — a Go identifier all at once.
var segmentPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// addKinds are the things "add" writes.
var addKinds = []string{"page", "fragment", "action", "document"}

// addRequest is one "collage add", parsed.
type addRequest struct {
	kind   string
	area   []string // the segments before the name; empty for none
	name   string   // the last segment
	file   bool
	path   string
	ident  string // the page's, action's or document's name in collage
	locale string
	// localeFrom says where locale came from, for the note "add" prints.
	localeFrom string
	ctype      string
	dir        string
}

// project is what "add" reads from the project it writes into.
type project struct {
	dir          string
	module       string
	locale       string // Locale.Default as main.go writes it, or ""
	templateRoot string
	templateExt  string
}

// fileChange is one file "add" writes: new, or an existing one rewritten.
type fileChange struct {
	path    string
	content []byte
	existed bool
}

// runAdd implements the "add" command.
func (c *CLI) runAdd(args []string) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), addUsage) }
	file := fs.Bool("file", false, "keep the template in a file")
	path := fs.String("path", "", "the URL pattern")
	name := fs.String("name", "", "the page's, action's or document's name")
	locale := fs.String("locale", "", "the locale its path is in")
	ctype := fs.String("type", "text/plain; charset=utf-8", "a document's content type")
	dir := fs.String("dir", ".", "the project")

	positional, flagArgs := splitPositional(args, addValueFlags)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(positional) != 2 || !slices.Contains(addKinds, positional[0]) {
		fmt.Fprintf(c.stderr(), "%v: want a kind (%s) and a name\n\n", ErrAddUsage, strings.Join(addKinds, ", "))
		fs.Usage()
		return 2
	}

	req := addRequest{kind: positional[0], file: *file, path: *path, ident: *name, locale: *locale, ctype: *ctype, dir: *dir}
	if err := req.parseTarget(positional[1]); err != nil {
		fmt.Fprintln(c.stderr(), err)
		return 2
	}

	proj, err := readProject(req.dir)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: add: %v\n", err)
		return 1
	}
	switch {
	case req.locale != "":
		req.localeFrom = "--locale"
	case proj.locale != "":
		req.locale, req.localeFrom = proj.locale, "main.go's Locale.Default"
	default:
		req.locale, req.localeFrom = "en", "the default; main.go sets no Locale.Default literal, --locale changes it"
	}

	changes, notes, err := plan(req, proj)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: add: %v\n", err)
		return 1
	}
	for _, change := range changes {
		if err := os.MkdirAll(filepath.Dir(change.path), 0o755); err != nil {
			fmt.Fprintf(c.stderr(), "collage: add: %v\n", err)
			return 1
		}
		if err := os.WriteFile(change.path, change.content, 0o644); err != nil {
			fmt.Fprintf(c.stderr(), "collage: add: %v\n", err)
			return 1
		}
	}

	out := c.stdout()
	for _, change := range changes {
		verb := "wrote "
		if change.existed {
			verb = "edited"
		}
		rel, err := filepath.Rel(proj.dir, change.path)
		if err != nil {
			rel = change.path
		}
		fmt.Fprintf(out, "%s %s\n", verb, filepath.ToSlash(rel))
	}
	for _, note := range notes {
		fmt.Fprintln(out, note)
	}
	return 0
}

// parseTarget splits target into its area and name, checks each segment, and
// fills in the defaults that follow from them.
func (r *addRequest) parseTarget(target string) error {
	segments := strings.Split(strings.Trim(target, "/"), "/")
	for _, segment := range segments {
		if !segmentPattern.MatchString(segment) {
			return fmt.Errorf("%w: %q is not a name: lowercase letters, digits and dashes, a letter first", ErrAddUsage, segment)
		}
	}
	r.area, r.name = segments[:len(segments)-1], segments[len(segments)-1]
	if r.kind == "document" && len(r.area) > 0 {
		return fmt.Errorf("%w: a document has no area; documents/ is flat", ErrAddUsage)
	}
	if r.kind == "action" && len(r.area) > 1 {
		return fmt.Errorf("%w: an action's area is one file, actions/<area>.go: %q has %d", ErrAddUsage, target, len(r.area))
	}
	if r.ident == "" {
		r.ident = r.name
	}
	if r.path == "" && (r.kind == "page" || r.kind == "document") {
		r.path = "/" + strings.Join(append(slices.Clone(r.area), r.name), "/")
	}
	if r.path != "" && !strings.HasPrefix(r.path, "/") {
		return fmt.Errorf("%w: --path %q must start with /", ErrAddUsage, r.path)
	}
	return nil
}

// readProject reads go.mod's module path and, from the main package's own
// files, the locale and template settings main.go configures with literals.
// A setting written any other way — read from the environment, built by a
// function — is not found, and its default stands.
func readProject(dir string) (project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return project{}, err
	}
	p := project{dir: abs, templateRoot: "templates", templateExt: ".html"}
	mod, err := os.ReadFile(filepath.Join(abs, "go.mod"))
	if err != nil {
		return project{}, fmt.Errorf("%s is not a Go module: %w", abs, err)
	}
	for line := range strings.SplitSeq(string(mod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			p.module = strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	if p.module == "" {
		return project{}, fmt.Errorf("%s/go.mod names no module", abs)
	}

	files, err := parseDir(abs)
	if err != nil {
		return project{}, err
	}
	for _, f := range files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			switch selectorName(lit.Type) {
			case "LocaleConfig":
				if v, ok := stringField(lit, "Default"); ok {
					p.locale = v
				}
			case "TemplateConfig":
				if v, ok := stringField(lit, "Root"); ok {
					p.templateRoot = v
				}
				if v, ok := stringField(lit, "Extension"); ok {
					p.templateExt = v
				}
			}
			return true
		})
	}
	return p, nil
}

// parsedFile is one Go file of a directory, parsed with its source kept, so an
// edit can be made in bytes at the offsets the parse found.
type parsedFile struct {
	path string
	src  []byte
	fset *token.FileSet
	ast  *ast.File
}

// parseDir parses every non-test .go file directly in dir. A directory that
// does not exist has none.
func parseDir(dir string) ([]parsedFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []parsedFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parseFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// parseFile parses the Go file at path, comments and all.
func parseFile(path string) (parsedFile, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return parsedFile{}, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return parsedFile{}, err
	}
	return parsedFile{path: path, src: src, fset: fset, ast: f}, nil
}

// selectorName returns the Sel of a pkg.Name expression, or "".
func selectorName(expr ast.Expr) string {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

// stringField returns the value lit gives key, when it is a string literal.
func stringField(lit *ast.CompositeLit, key string) (string, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != key {
			continue
		}
		if v, ok := kv.Value.(*ast.BasicLit); ok && v.Kind == token.STRING {
			s, err := strconv.Unquote(v.Value)
			return s, err == nil
		}
	}
	return "", false
}

// plan works out every file one "add" writes, formatted, without writing any of
// them, and the notes to print after: what it could not do itself. It refuses,
// before anything is written, a file that exists, a name the project already
// gives a page, an action or a document, and an identifier the target package
// already declares.
func plan(req addRequest, proj project) ([]fileChange, []string, error) {
	var notes []string
	if req.kind != "fragment" && (req.kind != "action" || req.path != "") {
		notes = append(notes, fmt.Sprintf("locale %s, from %s", req.locale, req.localeFrom))
	}

	if collageKind := map[string]string{"page": "NewPage", "action": "NewAction", "document": "NewDocument"}[req.kind]; collageKind != "" {
		taken, err := declaredNames(proj.dir, collageKind)
		if err != nil {
			return nil, nil, err
		}
		if where, ok := taken[req.ident]; ok {
			return nil, nil, fmt.Errorf("a %s named %q already exists, in %s; name this one with --name", req.kind, req.ident, where)
		}
	}

	g := generator{req: req, proj: proj, pascal: pascal(req.name), camel: camel(req.name)}
	var changes []fileChange
	var err error
	switch req.kind {
	case "page":
		changes, notes, err = g.page(notes)
	case "fragment":
		changes, err = g.fragment(false)
		notes = append(notes, fmt.Sprintf("bind it into a page's slot: .WithSlotFragment(\"<slot>\", fragments.%s())", g.pascal))
	case "action":
		changes, notes, err = g.action(notes)
	case "document":
		changes, notes, err = g.document(notes)
	}
	if err != nil {
		return nil, nil, err
	}
	for i := range changes {
		formatted := changes[i].content
		if strings.HasSuffix(changes[i].path, ".go") {
			if formatted, err = format.Source(changes[i].content); err != nil {
				return nil, nil, fmt.Errorf("generated %s does not parse (a bug in collage add): %w", changes[i].path, err)
			}
		}
		changes[i].content = formatted
		if !changes[i].existed {
			if _, err := os.Stat(changes[i].path); err == nil {
				return nil, nil, fmt.Errorf("%s exists; collage add does not overwrite", changes[i].path)
			}
		}
	}
	return changes, notes, nil
}

// declaredNames returns the names the project's Go code passes as a string
// literal to collage's constructor fn (NewPage, NewAction, NewDocument), each
// with the file that declares it.
func declaredNames(root, fn string) (map[string]string, error) {
	names := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
			call, ok := n.(*ast.CallExpr)
			if !ok || selectorName(call.Fun) != fn || len(call.Args) == 0 {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					rel, _ := filepath.Rel(root, path)
					names[s] = filepath.ToSlash(rel)
				}
			}
			return true
		})
		return nil
	})
	return names, err
}

// generator writes the files of one add request.
type generator struct {
	req    addRequest
	proj   project
	pascal string // the name as an exported identifier: StoryDetail
	camel  string // the name as an unexported one's stem: storyDetail
}

// areaDir returns base joined with the request's area, as a path on disk.
func (g generator) areaDir(base ...string) string {
	return filepath.Join(append(append([]string{g.proj.dir}, base...), g.req.area...)...)
}

// importPath returns the module path of base joined with the request's area.
func (g generator) importPath(base string) string {
	return strings.Join(append([]string{g.proj.module, base}, g.req.area...), "/")
}

// title is the name as words, for a heading: "story-detail" is "Story detail".
func (g generator) title() string {
	return upperFirst(strings.ReplaceAll(g.req.name, "-", " "))
}

// page writes the page and its content, and registers the page.
func (g generator) page(notes []string) ([]fileChange, []string, error) {
	changes, err := g.fragment(true)
	if err != nil {
		return nil, nil, err
	}

	dir := g.areaDir("pages")
	if err := checkIdentifiers(g.proj, dir, g.pascal); err != nil {
		return nil, nil, err
	}
	layout, err := findLayout(filepath.Join(g.proj.dir, "fragments", "layouts"))
	if err != nil {
		return nil, nil, err
	}

	var b strings.Builder
	b.WriteString("package pages\n\nimport (\n")
	if layout != "" {
		fmt.Fprintf(&b, "\t%q\n", g.proj.module+"/fragments/layouts")
	}
	fmt.Fprintf(&b, "\tfragments %q\n\n\t\"github.com/Elagoht/collage/pkg/collage\"\n)\n\n", g.importPath("fragments/pages"))
	fmt.Fprintf(&b, "// Returns the %s page: its layout, content and path.\n", g.req.name)
	fmt.Fprintf(&b, "func %s() *collage.Page {\n\treturn collage.NewPage(%q).\n", g.pascal, g.req.ident)
	if layout != "" {
		fmt.Fprintf(&b, "\t\tWithLayouts(layouts.%s()).\n", layout)
	} else {
		notes = append(notes, "no layout: fragments/layouts declares no func returning a *collage.Fragment that takes no arguments")
	}
	fmt.Fprintf(&b, "\t\tWithContent(fragments.%s()).\n\t\tWithPath(%q, %q).\n\t\tBuild()\n}\n", g.pascal, g.req.locale, g.req.path)
	changes = append(changes, fileChange{path: filepath.Join(dir, g.req.name+".go"), content: []byte(b.String())})

	pkgPath := strings.Join(append([]string{g.proj.module, "pages"}, g.req.area...), "/")
	alias := ""
	if len(g.req.area) > 0 {
		alias = identStem(g.req.area[len(g.req.area)-1]) + "pages"
	}
	routes, note, err := registerInRoutes(g.proj, "page", pkgPath, alias, "pages", func(ident string) string {
		return ident + "." + g.pascal + "()"
	})
	if err != nil {
		return nil, nil, err
	}
	if routes != nil {
		changes = append(changes, *routes)
	}
	if note != "" {
		notes = append(notes, note)
	}
	notes = append(notes, fmt.Sprintf("page %q at %s", g.req.ident, g.req.path))
	return changes, notes, nil
}

// fragment writes a fragment into fragments/pages/<area>, inline or with its
// template in a file. forPage says it is a page's content rather than a piece
// for a slot, which is only a matter of its comments.
func (g generator) fragment(forPage bool) ([]fileChange, error) {
	dir := g.areaDir("fragments", "pages")
	if err := checkIdentifiers(g.proj, dir, g.pascal, g.camel+"View", g.camel+"Block", g.camel+"Data"); err != nil {
		return nil, err
	}

	what, data := "fragment", "this fragment"
	if forPage {
		what, data = "page's content", "this page"
	}
	markup := "<main>\n  <h1>{{.Title}}</h1>\n</main>\n"

	var b strings.Builder
	b.WriteString("package fragments\n\nimport (\n\t\"context\"\n\n\t\"github.com/Elagoht/collage/pkg/collage\"\n)\n\n")
	fmt.Fprintf(&b, "// Types data used on %s.\ntype %sView struct {\n\tTitle string\n}\n\n", data, g.camel)
	fmt.Fprintf(&b, "// Returns the %s %s.\nfunc %s() *collage.Fragment {\n", g.req.name, what, g.pascal)

	var changes []fileChange
	if g.req.file {
		rel := strings.Join(append(append([]string{"pages"}, g.req.area...), g.req.name+g.proj.templateExt), "/")
		fmt.Fprintf(&b, "\treturn collage.NewFragment(%q, %q).\n", g.req.ident, rel)
		changes = append(changes, fileChange{
			path:    filepath.Join(g.proj.dir, g.proj.templateRoot, filepath.FromSlash(rel)),
			content: []byte(markup),
		})
	} else {
		fmt.Fprintf(&b, "\treturn collage.NewInlineFragment(%q, %sBlock).\n", g.req.ident, g.camel)
	}
	fmt.Fprintf(&b, "\t\tWithData(collage.Load(%sData)).\n\t\tBuild()\n}\n\n", g.camel)
	if !g.req.file {
		fmt.Fprintf(&b, "const %sBlock collage.InlineHTML = `%s`\n\n", g.camel, strings.TrimSuffix(markup, "\n"))
	}
	rc := "_"
	if forPage {
		rc = "rc"
	}
	fmt.Fprintf(&b, "// Reads what %s renders.\nfunc %sData(\n\t_ context.Context,\n\t%s *collage.RenderContext,\n) (%sView, error) {\n", data, g.camel, rc, g.camel)
	if forPage {
		fmt.Fprintf(&b, "\trc.HoistTitle(%q)\n", g.title())
	}
	fmt.Fprintf(&b, "\treturn %sView{Title: %q}, nil\n}\n", g.camel, g.title())

	return append([]fileChange{{path: filepath.Join(dir, g.req.name+".go"), content: []byte(b.String())}}, changes...), nil
}

// action adds a builder to actions/<area>.go and its handler to
// actions/funcs/<area>.go, creating either file when it is not there, and
// registers the action when it has a path of its own.
func (g generator) action(notes []string) ([]fileChange, []string, error) {
	domain := g.req.name
	if len(g.req.area) == 1 {
		domain = g.req.area[0]
	}
	actionsDir := filepath.Join(g.proj.dir, "actions")
	funcsDir := filepath.Join(actionsDir, "funcs")
	if err := checkIdentifiers(g.proj, actionsDir, g.pascal); err != nil {
		return nil, nil, err
	}
	if err := checkIdentifiers(g.proj, funcsDir, g.pascal); err != nil {
		return nil, nil, err
	}

	standalone := g.req.path != ""
	var builder strings.Builder
	if standalone {
		fmt.Fprintf(&builder, "\n// Returns the %s action, at %s.\n", g.req.name, g.req.path)
	} else {
		fmt.Fprintf(&builder, "\n// Returns the %s action. It has no path of its own: a page attaches it with\n// WithActionFor and it answers at the page's URL.\n", g.req.name)
	}
	fmt.Fprintf(&builder, "func %s() *collage.Action {\n\treturn collage.NewAction(%q).\n", g.pascal, g.req.ident)
	if standalone {
		fmt.Fprintf(&builder, "\t\tWithPath(%q, %q).\n", g.req.locale, g.req.path)
	}
	fmt.Fprintf(&builder, "\t\tWithMethods(http.MethodPost).\n\t\tWithHandler(funcs.%s()).\n\t\tBuild()\n}\n", g.pascal)

	var handler strings.Builder
	fmt.Fprintf(&handler, "\n// Answers the %s action.\nfunc %s() collage.ActionHandlerFunc {\n\treturn func(\n\t\t_ context.Context,\n", g.req.name, g.pascal)
	if standalone {
		handler.WriteString("\t\t_ *collage.RenderContext,\n\t) (*collage.ActionResult, error) {\n\t\treturn collage.NoContent(http.StatusNoContent), nil\n\t}\n}\n")
	} else {
		handler.WriteString("\t\trc *collage.RenderContext,\n\t) (*collage.ActionResult, error) {\n\t\treturn collage.RenderPage(rc.Page), nil\n\t}\n}\n")
	}

	builderImports := []importSpec{{path: "net/http"}, {path: g.proj.module + "/actions/funcs"}, {path: collageImport}}
	handlerImports := []importSpec{{path: "context"}, {path: collageImport}}
	if standalone {
		handlerImports = append(handlerImports, importSpec{path: "net/http"})
	}
	builderFile, err := appendToFile(g.proj, filepath.Join(actionsDir, domain+".go"), "actions", builderImports, builder.String())
	if err != nil {
		return nil, nil, err
	}
	handlerFile, err := appendToFile(g.proj, filepath.Join(funcsDir, domain+".go"), "funcs", handlerImports, handler.String())
	if err != nil {
		return nil, nil, err
	}
	changes := []fileChange{builderFile, handlerFile}

	if !standalone {
		notes = append(notes, fmt.Sprintf("attach it to its page: .WithActionFor(actions.%s())", g.pascal))
		return changes, notes, nil
	}
	routes, note, err := registerInRoutes(g.proj, "action", g.proj.module+"/actions", "", "actions", func(ident string) string {
		return ident + "." + g.pascal + "()"
	})
	if err != nil {
		return nil, nil, err
	}
	if routes != nil {
		changes = append(changes, *routes)
	}
	if note != "" {
		notes = append(notes, note)
	}
	return changes, append(notes, fmt.Sprintf("action %q at %s", g.req.ident, g.req.path)), nil
}

// document writes documents/<name>.go and registers the document.
func (g generator) document(notes []string) ([]fileChange, []string, error) {
	dir := filepath.Join(g.proj.dir, "documents")
	if err := checkIdentifiers(g.proj, dir, g.pascal, g.camel+"Body"); err != nil {
		return nil, nil, err
	}

	var b strings.Builder
	b.WriteString("package documents\n\nimport (\n\t\"context\"\n\n\t\"github.com/Elagoht/collage/pkg/collage\"\n)\n\n")
	fmt.Fprintf(&b, "// Returns the %s document, at %s.\nfunc %s() *collage.Document {\n", g.req.name, g.req.path, g.pascal)
	fmt.Fprintf(&b, "\treturn collage.NewDocument(%q, %q).\n\t\tWithPath(%q, %q).\n\t\tWithHandler(%sBody).\n\t\tBuild()\n}\n\n", g.req.ident, g.req.ctype, g.req.locale, g.req.path, g.camel)
	fmt.Fprintf(&b, "// Renders the %s document's body, and the tags of what it was made from.\nfunc %sBody(\n\t_ context.Context,\n\t_ *collage.RenderContext,\n) ([]byte, []string, error) {\n\treturn []byte(%q), nil, nil\n}\n", g.req.name, g.camel, g.req.name+"\n")
	changes := []fileChange{{path: filepath.Join(dir, g.req.name+".go"), content: []byte(b.String())}}

	routes, note, err := registerInRoutes(g.proj, "document", g.proj.module+"/documents", "", "documents", func(ident string) string {
		return ident + "." + g.pascal + "()"
	})
	if err != nil {
		return nil, nil, err
	}
	if routes != nil {
		changes = append(changes, *routes)
	}
	if note != "" {
		notes = append(notes, note)
	}
	return changes, append(notes, fmt.Sprintf("document %q at %s", g.req.ident, g.req.path)), nil
}

// collageImport is the package every generated file builds with.
const collageImport = "github.com/Elagoht/collage/pkg/collage"

// pascal returns a dashed name as an exported identifier: "story-detail" is
// StoryDetail.
func pascal(name string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(name, "-") {
		b.WriteString(upperFirst(part))
	}
	return b.String()
}

// camel returns a dashed name as an unexported identifier's stem:
// "story-detail" is storyDetail. It is only ever used with a suffix, so a name
// that is a Go keyword — "type" — still makes a valid identifier.
func camel(name string) string {
	p := pascal(name)
	return string(p[0]-'A'+'a') + p[1:]
}

// upperFirst returns s with its first byte uppercased when it is an ASCII
// lowercase letter. A name has passed segmentPattern, so it starts with one;
// nothing here folds anything that is not ASCII.
func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// identStem returns a dashed segment with its dashes dropped, for an import
// alias: "user-settings" is usersettings.
func identStem(segment string) string {
	return strings.ReplaceAll(segment, "-", "")
}

// checkIdentifiers refuses names that a package in dir already declares at its
// top level, which the generated file would otherwise redeclare.
func checkIdentifiers(proj project, dir string, names ...string) error {
	files, err := parseDir(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			for _, declared := range declaredIdents(decl) {
				if slices.Contains(names, declared) {
					rel, err := filepath.Rel(proj.dir, f.path)
					if err != nil {
						rel = f.path
					}
					return fmt.Errorf("%s already declares %s; name this one differently", filepath.ToSlash(rel), declared)
				}
			}
		}
	}
	return nil
}

// declaredIdents returns the top-level names decl declares; a method declares
// none at package level.
func declaredIdents(decl ast.Decl) []string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil {
			return []string{d.Name.Name}
		}
	case *ast.GenDecl:
		var names []string
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
		return names
	}
	return nil
}

// findLayout returns the layout a new page wraps itself in: Master, the
// scaffold's, when fragments/layouts declares it, otherwise Layout, otherwise
// the first exported function there that takes nothing and returns a
// *collage.Fragment. "" means there is none to use.
func findLayout(dir string) (string, error) {
	files, err := parseDir(dir)
	if err != nil {
		return "", err
	}
	var candidates []string
	for _, f := range files {
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Params.NumFields() != 0 {
				continue
			}
			if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if star, ok := fn.Type.Results.List[0].Type.(*ast.StarExpr); ok && selectorName(star.X) == "Fragment" {
				candidates = append(candidates, fn.Name.Name)
			}
		}
	}
	for _, preferred := range []string{"Master", "Layout"} {
		if slices.Contains(candidates, preferred) {
			return preferred, nil
		}
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return "", nil
}

// importSpec is one import a file needs: its path, and the alias it is
// imported under ("" for none).
type importSpec struct {
	path, alias string
}

// edit is a byte insertion into a file's source.
type edit struct {
	offset int
	text   string
}

// applyEdits returns src with edits inserted, each at its offset in the
// original.
func applyEdits(src []byte, edits []edit) []byte {
	slices.SortStableFunc(edits, func(a, b edit) int { return b.offset - a.offset })
	out := slices.Clone(src)
	for _, e := range edits {
		out = slices.Insert(out, e.offset, []byte(e.text)...)
	}
	return out
}

// appendToFile returns the change that adds code to the end of the Go file at
// path, adding the imports it needs, or creates the file as package pkg when it
// does not exist.
func appendToFile(proj project, path, pkg string, imports []importSpec, code string) (fileChange, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		var b strings.Builder
		fmt.Fprintf(&b, "package %s\n\nimport (\n", pkg)
		for i, class := range []string{"std", "module", "other"} {
			wrote := false
			for _, imp := range imports {
				if importClass(imp.path, proj.module) != class {
					continue
				}
				if !wrote && i > 0 && b.String()[b.Len()-2:] != "(\n" {
					b.WriteString("\n")
				}
				fmt.Fprintf(&b, "\t%s%q\n", aliasPrefix(imp.alias), imp.path)
				wrote = true
			}
		}
		b.WriteString(")\n")
		b.WriteString(code)
		return fileChange{path: path, content: []byte(b.String())}, nil
	}

	f, err := parseFile(path)
	if err != nil {
		return fileChange{}, err
	}
	var edits []edit
	for _, imp := range imports {
		if _, ok := importedAs(proj, f.ast, imp.path); ok {
			continue
		}
		edits = append(edits, importEdit(f, imp, proj.module))
	}
	src := applyEdits(f.src, edits)
	if len(src) > 0 && src[len(src)-1] != '\n' {
		src = append(src, '\n')
	}
	return fileChange{path: path, content: append(src, code...), existed: true}, nil
}

// aliasPrefix returns alias as it is written before an import path.
func aliasPrefix(alias string) string {
	if alias == "" {
		return ""
	}
	return alias + " "
}

// importedAs returns the identifier f refers to the package at path by — its
// alias, or its package name — and whether f imports it at all.
func importedAs(proj project, f *ast.File, path string) (string, bool) {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		return importIdent(proj, imp), true
	}
	return "", false
}

// importIdent returns the identifier imp binds: its alias, or the name the
// package declares — read from the project's own directory for one of its
// packages, since pages/demo declares package pages — or, for another module's,
// the last element of its path.
func importIdent(proj project, imp *ast.ImportSpec) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	p, _ := strconv.Unquote(imp.Path.Value)
	if rest, ok := strings.CutPrefix(p, proj.module+"/"); ok {
		if files, err := parseDir(filepath.Join(proj.dir, filepath.FromSlash(rest))); err == nil && len(files) > 0 {
			return files[0].ast.Name.Name
		}
	}
	return p[strings.LastIndex(p, "/")+1:]
}

// importEdit returns the edit that imports imp into f, in the group it
// belongs to: after the last import of the same class — the standard library,
// the project's own module, or another module — so a file's groups stay as its
// author drew them. A class f imports nothing from becomes a group of its own:
// the standard library's first, the others last.
func importEdit(f parsedFile, imp importSpec, module string) edit {
	line := "\t" + aliasPrefix(imp.alias) + strconv.Quote(imp.path)
	offset := func(p token.Pos) int { return f.fset.Position(p).Offset }
	class := importClass(imp.path, module)

	var last *ast.ImportSpec
	for _, spec := range f.ast.Imports {
		if p, _ := strconv.Unquote(spec.Path.Value); importClass(p, module) == class {
			last = spec
		}
	}
	if last != nil {
		return edit{offset: offset(last.End()), text: "\n" + line}
	}
	for _, decl := range f.ast.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		switch {
		case !gen.Lparen.IsValid():
			return edit{offset: offset(gen.End()), text: "\nimport " + strings.TrimPrefix(line, "\t")}
		case class == "std":
			return edit{offset: offset(gen.Lparen) + 1, text: "\n" + line + "\n"}
		default:
			return edit{offset: offset(gen.Rparen), text: "\n" + line + "\n"}
		}
	}
	return edit{offset: offset(f.ast.Name.End()), text: "\n\nimport " + strings.TrimPrefix(line, "\t") + "\n"}
}

// importClass sorts an import path into the group it goes in: "module" for the
// project's own packages, "std" for the standard library — no dot in its first
// element — and "other" for every other module.
func importClass(path, module string) string {
	switch first, _, _ := strings.Cut(path, "/"); {
	case path == module || strings.HasPrefix(path, module+"/"):
		return "module"
	case !strings.Contains(first, "."):
		return "std"
	default:
		return "other"
	}
}

// registerInRoutes returns the change to the main package's routes file that
// registers what expr names, importing pkgPath for it — under alias, or as
// pkgName when it needs none — and a note when it found nowhere to put it.
//
// It looks for the list that registers this kind: a []*collage.Page (Document,
// Action) literal, or an app.Register call. In a Register call the new item goes
// after the last one of its own kind, so pages stay with pages; otherwise at the
// end. The edit is made in bytes at the parsed offsets and then formatted, so
// the file's comments stay where they were.
func registerInRoutes(proj project, kind, pkgPath, alias, pkgName string, expr func(ident string) string) (*fileChange, string, error) {
	files, err := parseDir(proj.dir)
	if err != nil {
		return nil, "", err
	}
	typeName := map[string]string{"page": "Page", "document": "Document", "action": "Action"}[kind]
	ownPrefix := proj.module + "/" + map[string]string{"page": "pages", "document": "documents", "action": "actions"}[kind]

	for _, f := range files {
		fn := registerFunc(f.ast)
		if fn == nil {
			continue
		}
		list, elements, close := findRegistrationList(fn, f.ast, proj, typeName)
		if list == nil {
			continue
		}

		ident, imported := importedAs(proj, f.ast, pkgPath)
		var edits []edit
		if !imported {
			ident = alias
			if ident == "" {
				ident = pkgName
			}
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
			edits = append(edits, importEdit(f, spec, proj.module))
		}

		after := lastOfKind(proj, f, elements, ownPrefix)
		if _, isCall := list.(*ast.CallExpr); !isCall || after == nil {
			if len(elements) > 0 {
				after = elements[len(elements)-1]
			}
		}
		edits = append(edits, insertElement(f, after, close, expr(ident)))
		return &fileChange{path: f.path, content: applyEdits(f.src, edits), existed: true}, "", nil
	}
	ident := alias
	if ident == "" {
		ident = pkgName
	}
	return nil, fmt.Sprintf("register it in routes.go yourself, which has no list of %ss to add it to: app.Register%s(%s)", kind, typeName, expr(ident)), nil
}

// registerFunc returns the main package's func register, or the first func
// that calls a Register method when there is none by that name.
func registerFunc(f *ast.File) *ast.FuncDecl {
	var calling *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "register" {
			return fn
		}
		if calling == nil {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && strings.HasPrefix(selectorName(call.Fun), "Register") {
					calling = fn
					return false
				}
				return true
			})
		}
	}
	return calling
}

// findRegistrationList returns, within fn, the list a typeName is registered
// through — a []*collage.<typeName> literal, preferred, or an app.Register call
// — with its elements and the position of its closing brace or parenthesis.
//
// A Register call counts only on the application: on fn's *collage.App
// parameter when it has one, and otherwise on anything that is not a package,
// since a page constructor may well be called Register — authpages.Register()
// is a sign-up page, not a place to put an action.
func findRegistrationList(fn *ast.FuncDecl, f *ast.File, proj project, typeName string) (ast.Node, []ast.Expr, token.Pos) {
	apps := appParams(fn)
	packages := identsInUse(proj, f)
	onApp := func(call *ast.CallExpr) bool {
		x, ok := call.Fun.(*ast.SelectorExpr).X.(*ast.Ident)
		if !ok {
			return false
		}
		if len(apps) > 0 {
			return slices.Contains(apps, x.Name)
		}
		return !slices.Contains(packages, x.Name)
	}
	var literal *ast.CompositeLit
	var call *ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if arr, ok := n.Type.(*ast.ArrayType); ok && arr.Len == nil {
				if star, ok := arr.Elt.(*ast.StarExpr); ok && selectorName(star.X) == typeName && literal == nil {
					literal = n
				}
			}
		case *ast.CallExpr:
			if selectorName(n.Fun) == "Register" && call == nil && !n.Ellipsis.IsValid() && onApp(n) {
				call = n
			}
		}
		return true
	})
	if literal != nil {
		return literal, literal.Elts, literal.Rbrace
	}
	if call != nil {
		return call, call.Args, call.Rparen
	}
	return nil, nil, token.NoPos
}

// appParams returns the names of fn's *collage.App parameters.
func appParams(fn *ast.FuncDecl) []string {
	var names []string
	for _, field := range fn.Type.Params.List {
		if star, ok := field.Type.(*ast.StarExpr); ok && selectorName(star.X) == "App" {
			for _, n := range field.Names {
				names = append(names, n.Name)
			}
		}
	}
	return names
}

// lastOfKind returns the last of elements that calls into a package under
// prefix — the last page among a Register call's arguments — or nil.
func lastOfKind(proj project, f parsedFile, elements []ast.Expr, prefix string) ast.Expr {
	var last ast.Expr
	for _, el := range elements {
		call, ok := el.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			continue
		}
		for _, imp := range f.ast.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if importIdent(proj, imp) == pkg.Name && (p == prefix || strings.HasPrefix(p, prefix+"/")) {
				last = el
			}
		}
	}
	return last
}

// insertElement returns the edit that puts text into a list as an element after
// after — or as the first, when after is nil — whose closing brace or
// parenthesis is at close. A list written one element per line gets a line of
// its own, after any comment that ends the line it follows; a list on one line
// stays on one line.
func insertElement(f parsedFile, after ast.Expr, close token.Pos, text string) edit {
	src := f.src
	closeAt := f.fset.Position(close).Offset
	if after == nil {
		return edit{offset: closeAt, text: text + ",\n"}
	}

	at := f.fset.Position(after.End()).Offset
	i := at
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	hasComma := i < len(src) && src[i] == ','
	if hasComma {
		i++
	}

	j := i
	for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
		j++
	}
	restOfLine := j >= len(src) || src[j] == '\n' || strings.HasPrefix(string(src[j:]), "//")
	if !restOfLine {
		// Another element, or the closing brace, on the same line: one line.
		if hasComma {
			return edit{offset: i, text: " " + text + ","}
		}
		return edit{offset: at, text: ", " + text}
	}
	eol := j
	for eol < len(src) && src[eol] != '\n' {
		eol++
	}
	if !hasComma {
		return edit{offset: at, text: ",\n" + text}
	}
	return edit{offset: eol, text: "\n" + text + ","}
}

// identsInUse returns the identifiers f's imports bind.
func identsInUse(proj project, f *ast.File) []string {
	var names []string
	for _, imp := range f.Imports {
		names = append(names, importIdent(proj, imp))
	}
	return names
}
