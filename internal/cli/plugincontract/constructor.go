package plugincontract

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
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
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || otherPlatformName(n) {
			continue
		}
		path := filepath.Join(dir, n)
		head, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return Constructor{}, refuse(fmt.Sprintf("%s does not parse: %v", n, err))
		}
		if !buildOK(head) {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return Constructor{}, refuse(fmt.Sprintf("%s does not parse: %v", n, err))
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return Constructor{}, refuse("no Go package at the module root")
	}
	pkg := files[0].Name.Name
	types := map[string]*ast.TypeSpec{} // type name → its declaration
	var fn *ast.FuncDecl
	for _, f := range files {
		if f.Name.Name != pkg {
			return Constructor{}, refuse(fmt.Sprintf("the module root declares more than one package (%s and %s)", pkg, f.Name.Name))
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "New" {
					if fn != nil {
						return Constructor{}, refuse("more than one func New at the module root")
					}
					fn = d
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						types[ts.Name.Name] = ts
					}
				}
			}
		}
	}
	if fn == nil {
		return Constructor{}, refuse("no func New at the module root")
	}
	if fn.Type.TypeParams != nil {
		return Constructor{}, refuse("New is generic; it cannot be called without type arguments")
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

// buildOK reports whether the //go:build line in f's leading comments (when there is one)
// holds for the current GOOS and GOARCH. "ignore" is never set; "gc", "unix"
// and the go1.N release tags are; cgo and every other tag are not.
func buildOK(f *ast.File) bool {
	for _, g := range f.Comments {
		if g.Pos() >= f.Package {
			break
		}
		for _, c := range g.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			x, err := constraint.Parse(c.Text)
			if err != nil {
				return false
			}
			return x.Eval(func(tag string) bool {
				switch {
				case tag == runtime.GOOS || tag == runtime.GOARCH || tag == "gc":
					return true
				case tag == "unix":
					return runtime.GOOS != "windows" && runtime.GOOS != "plan9" && runtime.GOOS != "js" && runtime.GOOS != "wasip1"
				case strings.HasPrefix(tag, "go1."):
					return true
				}
				return false
			})
		}
	}
	return true
}

var (
	knownOS   = map[string]bool{"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "illumos": true, "ios": true, "js": true, "linux": true, "netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true}
	knownArch = map[string]bool{"386": true, "amd64": true, "arm": true, "arm64": true, "loong64": true, "mips": true, "mips64": true, "mips64le": true, "mipsle": true, "ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true}
)

// otherPlatformName reports whether a file name ends _<os>.go, _<arch>.go or
// _<os>_<arch>.go for a platform other than the current one. The lists are the
// toolchain's usual ones, not every historical port.
func otherPlatformName(n string) bool {
	parts := strings.Split(strings.TrimSuffix(n, ".go"), "_")
	for i := 1; i < len(parts) && i >= len(parts)-2; i++ {
		switch p := parts[i]; {
		case knownOS[p] && p != runtime.GOOS, knownArch[p] && p != runtime.GOARCH:
			return true
		}
	}
	return false
}

// zeroArg is how the zero value of t is written in plugins.go.
func zeroArg(t ast.Expr, types map[string]*ast.TypeSpec) (string, error) {
	switch t := t.(type) {
	case *ast.ArrayType:
		if t.Len != nil {
			return "", refuse("New's parameter is a fixed-size array; it may take none, or one whose zero value works")
		}
		return "nil", nil
	case *ast.StarExpr, *ast.MapType, *ast.FuncType, *ast.ChanType, *ast.InterfaceType:
		// A pointer, even to a type from another package, is nil.
		return "nil", nil
	case *ast.SelectorExpr:
		// A bare type from another package: whether it is a struct, an
		// interface or a number cannot be told without loading that package,
		// so it is refused rather than guessed at.
		name := "a type"
		if x, ok := t.X.(*ast.Ident); ok {
			name = x.Name + "." + t.Sel.Name
		}
		return "", refuse(fmt.Sprintf("New takes %s, a type from another package whose zero value cannot be written for it; take a pointer, or a type the plugin's package declares", name))
	case *ast.Ident:
		ts, ok := types[t.Name]
		if !ok {
			if t.Name == "error" {
				return "nil", nil
			}
			return "", refuse(fmt.Sprintf("New takes a %s; it may take none, or one parameter whose zero value works (a struct, pointer, interface, slice, map or func)", t.Name))
		}
		if ts.TypeParams != nil {
			return "", refuse(fmt.Sprintf("New takes %s, a generic type; use a type that is not generic", t.Name))
		}
		switch ts.Type.(type) {
		case *ast.StructType:
			return t.Name, nil
		case *ast.InterfaceType:
			return "nil", nil
		}
		return "", refuse(fmt.Sprintf("New takes a %s, whose zero value cannot be written for it; use a struct of options", t.Name))
	}
	return "", refuse("New's parameter has a type collage plugin add cannot fill in")
}
