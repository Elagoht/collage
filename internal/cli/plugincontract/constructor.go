package plugincontract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Constructor is how "collage plugin add" calls a plugin's New.
type Constructor struct {
	// Package is the package name the plugin's source declares.
	Package string
	// Arg is what New is passed: "" for nothing, "nil", or the name of a struct
	// type the package declares, passed as its zero value.
	Arg string
}

// Call is the call written into plugins.go, with ident naming the import.
func (c Constructor) Call(ident string) string {
	switch c.Arg {
	case "":
		return ident + ".New()"
	case "nil":
		return ident + ".New(nil)"
	}
	return ident + ".New(" + ident + "." + c.Arg + "{})"
}

// ReadConstructor finds the package-level func New in dir's non-test Go files
// and works out how to call it with nothing configured.
func ReadConstructor(dir string) (Constructor, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Constructor{}, err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			return Constructor{}, refuse(fmt.Sprintf("%s does not parse: %v", n, err))
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return Constructor{}, refuse("no Go package at the module root")
	}
	pkg := files[0].Name.Name
	types := map[string]ast.Expr{} // type name → its definition
	var fn *ast.FuncDecl
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "New" && fn == nil {
					fn = d
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						types[ts.Name.Name] = ts.Type
					}
				}
			}
		}
	}
	if fn == nil {
		return Constructor{}, refuse("no func New at the module root")
	}
	if fn.Type.Results == nil || fieldCount(fn.Type.Results) != 1 {
		return Constructor{}, refuse("New must return exactly one result, the plugin")
	}
	params := fn.Type.Params
	switch n := fieldCount(params); {
	case n == 0:
		return Constructor{Package: pkg}, nil
	case n == 1:
		t := params.List[0].Type
		if _, ok := t.(*ast.Ellipsis); ok {
			return Constructor{Package: pkg}, nil
		}
		arg, err := zeroArg(t, types)
		if err != nil {
			return Constructor{}, err
		}
		return Constructor{Package: pkg, Arg: arg}, nil
	default:
		return Constructor{}, refuse(fmt.Sprintf("New takes %d parameters (two parameters or more cannot be filled in); it may take none, or one whose zero value works", n))
	}
}

// fieldCount counts the names in a parameter or result list: "a, b string" is two.
func fieldCount(l *ast.FieldList) int {
	n := 0
	for _, f := range l.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}

// zeroArg is how the zero value of t is written in plugins.go.
func zeroArg(t ast.Expr, types map[string]ast.Expr) (string, error) {
	switch t := t.(type) {
	case *ast.StarExpr, *ast.ArrayType, *ast.MapType, *ast.FuncType, *ast.ChanType, *ast.InterfaceType:
		if a, ok := t.(*ast.ArrayType); ok && a.Len != nil {
			return "", refuse("New's parameter is a fixed-size array; it may take none, or one whose zero value works")
		}
		return "nil", nil
	case *ast.SelectorExpr:
		// A type from another package: only an interface can be written
		// without importing that package, and only nil says it. A struct from
		// elsewhere is refused rather than guessed at.
		return "nil", nil
	case *ast.Ident:
		def, ok := types[t.Name]
		if !ok {
			return "", refuse(fmt.Sprintf("New takes a %s; it may take none, or one parameter whose zero value works (a struct, pointer, interface, slice, map or func)", t.Name))
		}
		switch def.(type) {
		case *ast.StructType:
			return t.Name, nil
		case *ast.InterfaceType:
			return "nil", nil
		}
		return "", refuse(fmt.Sprintf("New takes a %s, whose zero value cannot be written for it; use a struct of options", t.Name))
	}
	return "", refuse("New's parameter has a type collage plugin add cannot fill in")
}
