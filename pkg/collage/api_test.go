package collage_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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
		{reflect.TypeOf((*collage.RenderContext)(nil)), []string{"ActionURL", "Asset", "Context", "Hoist", "HoistAlternate", "HoistLink", "HoistMeta", "HoistProperty", "HoistStylesheet", "HoistTitle", "Hoisted", "Param", "URL", "WithContext", "WithFragment"}},
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
