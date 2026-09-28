package ascii

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestLower_KeepsEveryIndex(t *testing.T) {
	in := "İSTANBUL </HEAD> ẞ"
	got := Lower([]byte(in))
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d", len(got), len(in))
	}
	if want := "İstanbul </head> ẞ"; string(got) != want {
		t.Errorf("Lower = %q, want %q", got, want)
	}
}

func TestEqualFold_FoldsOnlyASCII(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"en-US", "EN-us", true},
		{"https", "HTTPS", true},
		{"sk", "sK", false}, // Kelvin sign
		{"es", "eſ", false}, // long s
		{"i", "İ", false},
		{"a", "ab", false},
	} {
		if got := EqualFold(tc.a, tc.b); got != tc.want {
			t.Errorf("EqualFold(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// Unicode case folding stays out of the framework's code, where what is folded
// is usually what a request carried: this package is what folds it. The few
// places folding Unicode is the point are named here, with why.
func TestNoUnicodeFoldingOutsideThisPackage(t *testing.T) {
	allowed := map[string]string{
		"internal/template/funcs.go": `the "upper" and "lower" template functions fold a page's text, where Unicode is right`,
		"internal/cli/build.go":      "a yes or no typed at the terminal",
		"internal/router/action.go":  "a method name at registration, the application's own",
	}
	forbidden := map[string]bool{
		"strings.EqualFold": true, "strings.ToLower": true, "strings.ToUpper": true,
		"bytes.EqualFold": true, "bytes.ToLower": true, "bytes.ToUpper": true,
		"unicode.ToLower": true, "unicode.ToUpper": true,
	}
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, ok := allowed[rel]; ok {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && forbidden[pkg.Name+"."+sel.Sel.Name] {
					t.Errorf("%s uses %s.%s: fold with internal/ascii, or name the file here with why", rel, pkg.Name, sel.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
