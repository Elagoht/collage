package httpx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Every stage this package reports, and every route kind, is defined from the
// names in types, which pkg/collage publishes: a stage spelled as a literal here
// would reach plugins with no public constant to compare it against.
func TestStagesAndKinds_ComeFromTypes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
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
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "stage") {
						continue
					}
					found++
					sel, ok := vs.Values[i].(*ast.SelectorExpr)
					var pkg ast.Expr
					if ok {
						pkg = sel.X
					}
					if id, isIdent := pkg.(*ast.Ident); !ok || !isIdent || id.Name != "types" || !strings.HasPrefix(sel.Sel.Name, "Stage") {
						t.Errorf("%s: %s is not defined as a types.Stage constant", fset.Position(name.Pos()), name.Name)
					}
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("found no stage constants")
	}
	for kind, want := range map[routeKind]string{
		routeKindPage: "page", routeKindDocument: "document", routeKindAction: "action",
		routeKindMount: "mount", routeKindHandler: "handler",
	} {
		if kind.String() != want {
			t.Errorf("routeKind(%d).String() = %q, want %q", kind, kind.String(), want)
		}
	}
}
