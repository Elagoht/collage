package collage_test

import (
	"context"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func exportedMethods(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	return names
}

// An internal type reached through an alias publishes every exported method it
// has. These are the ones meant for users; a new one fails here until it is
// added on purpose.
func TestAliasedTypes_MethodAllowlist(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		want []string
	}{
		{reflect.TypeOf((*collage.Page)(nil)), []string{"Locales", "PathFor", "Validate"}},
		{reflect.TypeOf((*collage.Fragment)(nil)), []string{"Validate"}},
		{reflect.TypeOf((*collage.Result)(nil)), []string{"Degraded"}},
		{reflect.TypeOf((*collage.Document)(nil)), []string{"Locales", "PathFor", "RootPath", "Validate"}},
		{reflect.TypeOf((*collage.Action)(nil)), nil},
		{reflect.TypeOf((*collage.RenderContext)(nil)), []string{"ActionURL", "Asset", "Context", "Hoist", "HoistAlternate", "HoistLink", "HoistMeta", "HoistProperty", "HoistStylesheet", "HoistTitle", "Param", "URL", "WithContext"}},
		{reflect.TypeOf((*collage.Redirect)(nil)), nil},
	} {
		if got := exportedMethods(c.typ); !slices.Equal(got, c.want) {
			t.Errorf("%v methods = %v, want %v", c.typ, got, c.want)
		}
	}
}

// A store that only gets, sets and drops entries is a Cache; invalidating tags
// belongs to a TaggedCache, the only kind the framework invalidates.
type getSetCache struct{}

func (getSetCache) Get(context.Context, string) ([]byte, string, bool) { return nil, "", false }
func (getSetCache) Set(context.Context, string, []byte, time.Duration) (string, error) {
	return "", nil
}
func (getSetCache) InvalidateKey(context.Context, string) error { return nil }
func (getSetCache) Clear(context.Context) error                 { return nil }

func TestCache_GetSetIsEnough(t *testing.T) {
	var _ collage.Cache = getSetCache{}
}

// Every exported sentinel is declared in errors.go, so the list a user reads is
// the whole list.
func TestErrors_AllInErrorsGo(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					if strings.HasPrefix(n.Name, "Err") && e.Name() != "errors.go" {
						t.Errorf("%s declared in %s", n.Name, e.Name())
					}
				}
			}
		}
	}
}

func TestErrors_GoneFromAPI(t *testing.T) {
	src, err := os.ReadFile("errors.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ErrNilConfig", "ErrNilPage", "ErrNilPlugin", "ErrEmptyCommandName",
		"ErrEmptyPluginName", "ErrNoRootFragment", "ErrEmptyCacheVersion", "ErrDictKeyNotString",
		"ErrDictOddArgs", "ErrNoMountForAsset", "ErrEmptyLocaleDefault", "ErrEmptyTemplateRoot",
		"ErrUnsupportedCache", "ErrInvalidHandlerPrefix", "ErrInvalidBaseURL", "ErrNoMethods",
		"ErrMissingTTL", "ErrNilRenderer", "ErrInvalidOutDir"} {
		if regexp.MustCompile(`\b` + name + `\s*=`).Match(src) {
			t.Errorf("%s is still exported", name)
		}
	}
}

func TestErrInvalidOrigin_DoesNotBlameBaseURL(t *testing.T) {
	if strings.Contains(collage.ErrInvalidOrigin.Error(), "BaseURL") {
		t.Errorf("message = %q", collage.ErrInvalidOrigin.Error())
	}
}

// Review Focus 4: an untyped string literal still fills Cache.Type.
func TestCacheType_UntypedLiteral(t *testing.T) {
	c := collage.CacheConfig{Type: "disk"}
	if c.Type != collage.CacheDisk {
		t.Errorf("Type = %q", c.Type)
	}
	if collage.CacheMemory != "memory" {
		t.Errorf("CacheMemory = %q", collage.CacheMemory)
	}
}

// No exported variable holds a function: anyone could reassign it.
func TestNoExportedFuncVars(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	conf := gotypes.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("github.com/Elagoht/collage/pkg/collage", fset, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range pkg.Scope().Names() {
		v, ok := pkg.Scope().Lookup(name).(*gotypes.Var)
		if !ok || !v.Exported() {
			continue
		}
		if _, isFunc := v.Type().Underlying().(*gotypes.Signature); isFunc {
			t.Errorf("exported var %s holds a function; declare a func", name)
		}
	}
}

// Public doc comments name no internal package or identifier.
func TestDocComments_NoInternalNames(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	bad := regexp.MustCompile(`internal/|\b(types|core|plugin|render|httpx|build|router|cache|asset|observability)\.[A-Z]`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, cg := range f.Comments {
			for _, m := range bad.FindAllString(cg.Text(), -1) {
				t.Errorf("%s: comment names %q", fset.Position(cg.Pos()), m)
			}
		}
	}
}

// aliasedInternals returns, per internal package (types, plugin), the names of
// the types pkg/collage declares aliases of — "Page = types.Page", and generic
// ones such as "Key[T any] = types.Key[T]".
func aliasedInternals(t *testing.T) map[string]map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	aliased := map[string]map[string]bool{"types": {}, "plugin": {}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts := spec.(*ast.TypeSpec)
				if !ts.Assign.IsValid() {
					continue
				}
				target := ts.Type
				switch x := target.(type) {
				case *ast.IndexExpr:
					target = x.X
				case *ast.IndexListExpr:
					target = x.X
				}
				sel, ok := target.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && aliased[pkg.Name] != nil {
					aliased[pkg.Name][sel.Sel.Name] = true
				}
			}
		}
	}
	return aliased
}

// receiverTypeName is the name of the type a method's receiver expression
// names: T, *T, T[P] and *T[P, Q] all name T.
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch x := expr.(type) {
	case *ast.IndexExpr:
		expr = x.X
	case *ast.IndexListExpr:
		expr = x.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// The internal types pkg/collage aliases show their own doc comments on hover:
// such a type's comment, its exported fields' and interface methods', and its
// exported methods' name no internal package or identifier either. Types
// pkg/collage does not alias are the engine's own business and out of scope.
func TestDocComments_AliasedInternalsNoInternalNames(t *testing.T) {
	bad := regexp.MustCompile(`internal/|\b(types|core|plugin|render|httpx|build|router|cache|asset|observability)\.[A-Z]`)
	aliased := aliasedInternals(t)
	if len(aliased["types"]) == 0 || len(aliased["plugin"]) == 0 {
		t.Fatalf("found no aliases of internal types: %v", aliased)
	}
	seen := map[string]bool{}
	for pkg, names := range aliased {
		dir := "../../internal/" + pkg
		fset := token.NewFileSet()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, dir+"/"+e.Name(), nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			check := func(what string, groups ...*ast.CommentGroup) {
				for _, cg := range groups {
					if cg == nil {
						continue
					}
					for _, m := range bad.FindAllString(cg.Text(), -1) {
						t.Errorf("%s: %s's comment names %q", fset.Position(cg.Pos()), what, m)
					}
				}
			}
			for _, decl := range f.Decls {
				switch decl := decl.(type) {
				case *ast.GenDecl:
					if decl.Tok != token.TYPE {
						continue
					}
					for _, spec := range decl.Specs {
						ts := spec.(*ast.TypeSpec)
						if !names[ts.Name.Name] {
							continue
						}
						seen[pkg+"."+ts.Name.Name] = true
						doc := ts.Doc
						if doc == nil && len(decl.Specs) == 1 {
							doc = decl.Doc
						}
						check(ts.Name.Name, doc, ts.Comment)
						var fields *ast.FieldList
						switch typ := ts.Type.(type) {
						case *ast.StructType:
							fields = typ.Fields
						case *ast.InterfaceType:
							fields = typ.Methods
						}
						if fields == nil {
							continue
						}
						for _, field := range fields.List {
							for _, name := range field.Names {
								if name.IsExported() {
									check(ts.Name.Name+"."+name.Name, field.Doc, field.Comment)
								}
							}
						}
					}
				case *ast.FuncDecl:
					if decl.Recv == nil || !decl.Name.IsExported() {
						continue
					}
					if name := receiverTypeName(decl.Recv.List[0].Type); names[name] {
						check(name+"."+decl.Name.Name, decl.Doc)
					}
				}
			}
		}
	}
	for pkg, names := range aliased {
		for name := range names {
			if !seen[pkg+"."+name] {
				t.Errorf("pkg/collage aliases %s.%s, which this test did not find", pkg, name)
			}
		}
	}
}
