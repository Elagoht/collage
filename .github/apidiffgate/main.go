// Command apidiffgate is what .github/apidiff.sh asks "did the public API break?"
// with. apidiff alone cannot answer it for this module, for two opposite reasons:
//
//   - Most of pkg/collage is type aliases of named types in internal packages
//     (type Metrics = observability.Metrics). apidiff compares the alias, not what
//     it names, so renaming a method of Key or adding one to Metrics, which
//     breaks every implementer, reports "no changes".
//   - Host, ConfigHost and Plugin are aliases of interface literals, which apidiff
//     compares structurally. A method added to Host, which is sealed (it embeds
//     ConfigReader, whose one method is unexported, so nothing outside collage
//     implements it) and therefore safe, fails the gate and cascades to everything
//     that mentions Host.
//
// So this runs apidiff on pkg/collage, pkg/collagetest and on every internal
// package they reach, keeps the incompatible changes to names reachable from the
// released API, and drops growth of a sealed interface along with the textually
// unchanged "changed from X to X" lines that growth causes.
//
// It uses the standard library and the pinned apidiff binary it is given, and
// exits 0 when nothing incompatible is left, 1 when something is, 2 when it could
// not tell.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// apiPackages are the packages users import, relative to the module.
var apiPackages = []string{"pkg/collage", "pkg/collagetest"}

func main() {
	apidiff := flag.String("apidiff", "", "path of the apidiff binary")
	oldDir := flag.String("old", "", "checkout of the release to compare with")
	newDir := flag.String("new", ".", "checkout under test")
	tmp := flag.String("tmp", "", "scratch directory")
	flag.Parse()
	if *apidiff == "" || *oldDir == "" || *tmp == "" {
		fmt.Fprintln(os.Stderr, "apidiffgate: -apidiff, -old and -tmp are required")
		os.Exit(2)
	}
	incompatible, err := run(*apidiff, mustAbs(*oldDir), mustAbs(*newDir), mustAbs(*tmp))
	if err != nil {
		fmt.Fprintln(os.Stderr, "apidiffgate:", err)
		os.Exit(2)
	}
	if len(incompatible) > 0 {
		for _, line := range incompatible {
			fmt.Fprintln(os.Stderr, line)
		}
		os.Exit(1)
	}
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apidiffgate:", err)
		os.Exit(2)
	}
	return abs
}

func run(apidiff, oldDir, newDir, tmp string) ([]string, error) {
	module, err := modulePath(newDir)
	if err != nil {
		return nil, err
	}
	oldSurf, err := loadSurface(oldDir, module)
	if err != nil {
		return nil, fmt.Errorf("the release: %w", err)
	}
	newSurf, err := loadSurface(newDir, module)
	if err != nil {
		return nil, fmt.Errorf("this commit: %w", err)
	}
	sealed := sealedGrowth(oldSurf, newSurf)

	// The fallout of sealed growth shows in other packages than the one that grew,
	// so what explains it is looked for across all of them before any is judged.
	type section struct {
		pkg   string
		lines []string
	}
	var sections []section
	add := func(pkg string, full string, lines []string) {
		fmt.Printf("### %s\n```\n%s\n```\n", pkg, orNone(full))
		sections = append(sections, section{pkg, lines})
	}

	for _, rel := range apiPackages {
		path := module + "/" + rel
		full, lines, err := compare(apidiff, oldDir, newDir, tmp, path)
		if err != nil {
			return nil, err
		}
		if full == nil {
			fmt.Printf("### %s\nnot in the release\n", rel)
			continue
		}
		add(rel, *full, lines)
	}

	// Every internal package the released API reached, with the changes limited to
	// the names it reached in each.
	var pkgs []string
	for pkg := range oldSurf.reachable {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		full, lines, err := compare(apidiff, oldDir, newDir, tmp, pkg)
		if err != nil {
			return nil, err
		}
		rel := strings.TrimPrefix(pkg, module+"/")
		if full == nil {
			fmt.Printf("### %s\nnot in the release or not in this commit; what it held that the API reaches is compared where it went\n", rel)
			continue
		}
		var reached []string
		for _, line := range lines {
			if oldSurf.reaches(pkg, leadingIdent(line)) {
				reached = append(reached, line)
			}
		}
		add(rel+" (names the API reaches)", strings.Join(reached, "\n"), reached)
	}

	explained := false
	for _, s := range sections {
		explained = explained || sealedGrowthIn(s.lines, sealed)
	}
	var kept, exempt []string
	for _, s := range sections {
		k, e := filter(s.lines, sealed, explained)
		kept = append(kept, prefixAll(s.pkg, k)...)
		exempt = append(exempt, prefixAll(s.pkg, e)...)
	}

	if len(exempt) > 0 {
		fmt.Printf("### Not counted\nGrowth of a sealed interface, and what it changes in the text of other types: no type outside collage implements them.\n```\n%s\n```\n", strings.Join(exempt, "\n"))
	}
	if len(kept) > 0 {
		fmt.Printf("### Incompatible\n```\n%s\n```\n", strings.Join(kept, "\n"))
	}
	return kept, nil
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "no changes"
	}
	return s
}

func prefixAll(pkg string, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pkg + ": " + strings.TrimPrefix(l, "- ")
	}
	return out
}

func modulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", fmt.Errorf("no module line in %s/go.mod", dir)
}

// compare runs apidiff on pkg in both checkouts. full is nil when the package is
// missing from either; otherwise it is the whole report, and lines are the
// incompatible changes in it. A package that cannot be loaded is "missing" only
// if its directory is; any other failure is an error, so a broken tree cannot
// pass the gate by being unreadable.
func compare(apidiff, oldDir, newDir, tmp, pkg string) (full *string, lines []string, err error) {
	module, err := modulePath(newDir)
	if err != nil {
		return nil, nil, err
	}
	rel := strings.TrimPrefix(pkg, module+"/")
	if !dirExists(filepath.Join(oldDir, rel)) || !dirExists(filepath.Join(newDir, rel)) {
		return nil, nil, nil
	}
	tag := strings.NewReplacer("/", "_", ".", "_").Replace(rel)
	oldFile, newFile := filepath.Join(tmp, tag+".gate.old"), filepath.Join(tmp, tag+".gate.new")
	if err := execIn(oldDir, apidiff, "-w", oldFile, pkg); err != nil {
		return nil, nil, err
	}
	if err := execIn(newDir, apidiff, "-w", newFile, pkg); err != nil {
		return nil, nil, err
	}
	// -allow-internal: apidiff otherwise prints "Ignoring internal package" and
	// compares nothing, which reads exactly like "no changes".
	report, err := execOut(newDir, apidiff, "-allow-internal", oldFile, newFile)
	if err != nil {
		return nil, nil, err
	}
	incompatible, err := execOut(newDir, apidiff, "-allow-internal", "-incompatible", oldFile, newFile)
	if err != nil {
		return nil, nil, err
	}
	if strings.Contains(report+incompatible, "Ignoring internal package") {
		return nil, nil, fmt.Errorf("apidiff ignored %s: its comparison is empty, not clean", pkg)
	}
	return &report, changeLines(incompatible), nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func execIn(dir, name string, args ...string) error {
	_, err := execOut(dir, name, args...)
	return err
}

func execOut(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s (in %s): %w\n%s", filepath.Base(name), strings.Join(args, " "), dir, err, stderr.String())
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// changeLines are the "- Name: ..." lines of an apidiff report.
func changeLines(report string) []string {
	var lines []string
	seen := map[string]bool{}
	for _, line := range strings.Split(report, "\n") {
		// A generic type's change is reported once per instantiation it is
		// used with; once is enough to read.
		if strings.HasPrefix(line, "- ") && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	return lines
}

var leadingIdentRE = regexp.MustCompile(`^- \(?\*?([A-Za-z_][A-Za-z0-9_]*)`)

// leadingIdent is the package-level name a change line is about: "Key.Name:
// removed" and "(*App).Run: ..." are about Key and App.
func leadingIdent(line string) string {
	if m := leadingIdentRE.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// methodAddedRE matches "Metrics.Foo: added", "(*T).Foo: added" and the generic
// "Key[T].Foo: added".
var methodAddedRE = regexp.MustCompile(`^- \(?\*?([A-Za-z_][A-Za-z0-9_]*)(?:\[[^\]]*\])?\)?\.[A-Za-z_][A-Za-z0-9_]*: added$`)

// identicalChange reports a "changed from X to Y" line whose X and Y are the
// same text: the type underneath changed where the report cannot show it.
func identicalChange(line string) bool {
	_, rest, ok := strings.Cut(line, ": changed from ")
	if !ok {
		return false
	}
	from, to, ok := strings.Cut(rest, " to ")
	return ok && from == to
}

// growth is what the sealed interfaces did between the release and this commit.
type growth struct {
	// sealed names the interfaces that were sealed in the release.
	sealed map[string]bool
	// grewOnly names the sealed interface aliases of a literal whose method set in
	// this commit contains the release's, unchanged.
	grewOnly map[string]bool
}

// isSealedGrowth reports a line that is a sealed interface gaining a method.
func isSealedGrowth(line string, g growth) bool {
	ident := leadingIdent(line)
	return (methodAddedRE.MatchString(line) && g.sealed[ident]) ||
		(strings.HasPrefix(line, "- "+ident+": changed from ") && g.grewOnly[ident])
}

// sealedGrowthIn reports whether any of lines is a sealed interface gaining a
// method.
func sealedGrowthIn(lines []string, g growth) bool {
	for _, line := range lines {
		if isSealedGrowth(line, g) {
			return true
		}
	}
	return false
}

// filter splits lines into those that count and those that are growth of a sealed
// interface or its textual fallout. The fallout is exempt only when explained:
// some sealed growth was found, in any package. An identical-looking change with
// no such cause is left to fail.
func filter(lines []string, g growth, explained bool) (kept, exempt []string) {
	for _, line := range lines {
		if isSealedGrowth(line, g) || (explained && identicalChange(line)) {
			exempt = append(exempt, line)
			continue
		}
		kept = append(kept, line)
	}
	return kept, exempt
}

// surface is what the released API reaches: which names of which internal
// packages, and which of its interfaces are sealed.
type surface struct {
	// reachable maps an internal package's import path to the names an exported
	// declaration of pkg/collage or pkg/collagetest reaches in it.
	reachable map[string]map[string]bool
	// bare are names that match in any package: aliases of interface literals,
	// which no package declares under that name.
	bare map[string]bool
	// sealed maps the name of an interface with an unexported method to true.
	sealed map[string]bool
	// literals maps a sealed alias of an interface literal to its exported
	// methods' signatures.
	literals map[string]map[string]string
}

func (s *surface) reaches(pkg, name string) bool {
	return name != "" && (s.bare[name] || s.reachable[pkg][name])
}

// sealedGrowth finds, among the interfaces sealed in the release, the literal
// aliases that only gained methods.
func sealedGrowth(old, now *surface) growth {
	g := growth{sealed: old.sealed, grewOnly: map[string]bool{}}
	for name, oldMethods := range old.literals {
		newMethods, ok := now.literals[name]
		if !ok {
			continue
		}
		grew := true
		for m, sig := range oldMethods {
			if newMethods[m] != sig {
				grew = false
			}
		}
		if grew {
			g.grewOnly[name] = true
		}
	}
	return g
}

// loadSurface type-checks the API packages of the module checked out in dir,
// from source, and walks what their exported declarations reach.
func loadSurface(dir, module string) (*surface, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// The source importer resolves imports from the working directory's module.
	if err := os.Chdir(dir); err != nil {
		return nil, err
	}
	defer os.Chdir(cwd)

	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	s := &surface{
		reachable: map[string]map[string]bool{},
		bare:      map[string]bool{},
		sealed:    map[string]bool{},
		literals:  map[string]map[string]string{},
	}
	internalPrefix := module + "/internal/"
	seen := map[types.Type]bool{}

	var walk func(t types.Type)
	walk = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Alias:
			walk(types.Unalias(t))
		case *types.Named:
			if obj := t.Obj(); obj.Pkg() != nil && strings.HasPrefix(obj.Pkg().Path(), internalPrefix) {
				if s.reachable[obj.Pkg().Path()] == nil {
					s.reachable[obj.Pkg().Path()] = map[string]bool{}
				}
				s.reachable[obj.Pkg().Path()][obj.Name()] = true
				if iface, ok := t.Underlying().(*types.Interface); ok && hasUnexported(iface) {
					s.sealed[obj.Name()] = true
				}
			}
			for i := range t.NumMethods() {
				if m := t.Method(i); m.Exported() {
					walk(m.Type())
				}
			}
			for i := range t.TypeArgs().Len() {
				walk(t.TypeArgs().At(i))
			}
			walk(t.Underlying())
		case *types.Pointer:
			walk(t.Elem())
		case *types.Slice:
			walk(t.Elem())
		case *types.Array:
			walk(t.Elem())
		case *types.Chan:
			walk(t.Elem())
		case *types.Map:
			walk(t.Key())
			walk(t.Elem())
		case *types.Struct:
			for i := range t.NumFields() {
				if f := t.Field(i); f.Exported() || f.Embedded() {
					walk(f.Type())
				}
			}
		case *types.Signature:
			walk(t.Params())
			walk(t.Results())
		case *types.Tuple:
			for i := range t.Len() {
				walk(t.At(i).Type())
			}
		case *types.Interface:
			for i := range t.NumMethods() {
				if m := t.Method(i); m.Exported() {
					walk(m.Type())
				}
			}
			for i := range t.NumEmbeddeds() {
				walk(t.EmbeddedType(i))
			}
		case *types.TypeParam:
			walk(t.Constraint())
		}
	}

	qualify := func(p *types.Package) string { return p.Path() }
	for _, rel := range apiPackages {
		path := module + "/" + rel
		if !dirExists(filepath.Join(dir, rel)) {
			continue
		}
		pkg, err := imp.Import(path)
		if err != nil {
			return nil, fmt.Errorf("type-checking %s: %w", path, err)
		}
		for _, name := range pkg.Scope().Names() {
			obj := pkg.Scope().Lookup(name)
			if !obj.Exported() {
				continue
			}
			walk(obj.Type())
			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			if !tn.IsAlias() {
				// An interface pkg/collage declares itself, such as Data.
				if iface, ok := tn.Type().Underlying().(*types.Interface); ok && hasUnexported(iface) {
					s.sealed[name] = true
				}
				continue
			}
			// An alias of an interface literal is declared by no internal package
			// under this name, so its reports match by name alone.
			if iface, ok := types.Unalias(tn.Type()).(*types.Interface); ok {
				s.bare[name] = true
				if hasUnexported(iface) {
					s.sealed[name] = true
					methods := map[string]string{}
					for i := range iface.NumMethods() {
						if m := iface.Method(i); m.Exported() {
							methods[m.Name()] = types.TypeString(m.Type(), qualify)
						}
					}
					s.literals[name] = methods
				}
			}
		}
		if err := s.constantsOf(dir, rel, module); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func hasUnexported(iface *types.Interface) bool {
	for i := range iface.NumMethods() {
		if !iface.Method(i).Exported() {
			return true
		}
	}
	return false
}

// constantsOf records the internal names a package re-exports as constants and
// variables ("StageRoute = types.StageRoute"): their types do not lead back to
// the declaration, so the source says which ones they are.
func (s *surface) constantsOf(dir, rel, module string) error {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filepath.Join(dir, rel), func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return err
	}
	internalPrefix := module + "/internal/"
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			imports := map[string]string{}
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(path, internalPrefix) {
					continue
				}
				name := filepath.Base(path)
				if imp.Name != nil {
					name = imp.Name.Name
				}
				imports[name] = path
			}
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
					continue
				}
				for _, spec := range gen.Specs {
					for _, value := range spec.(*ast.ValueSpec).Values {
						sel, ok := value.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						id, ok := sel.X.(*ast.Ident)
						if !ok || imports[id.Name] == "" {
							continue
						}
						path := imports[id.Name]
						if s.reachable[path] == nil {
							s.reachable[path] = map[string]bool{}
						}
						s.reachable[path][sel.Sel.Name] = true
					}
				}
			}
		}
	}
	return nil
}
