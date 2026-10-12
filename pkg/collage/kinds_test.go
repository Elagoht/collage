package collage_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// The named kinds and stages are the strings the engine reports: a plugin that
// compared against the literal before keeps matching, and one that compares
// against the constant matches what the engine sends.
func TestKindAndStageConstants_Values(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{collage.RouteKindPage, "page"},
		{collage.RouteKindDocument, "document"},
		{collage.RouteKindAction, "action"},
		{collage.RouteKindMount, "mount"},
		{collage.RouteKindHandler, "handler"},

		{collage.BuiltPage, "page"},
		{collage.BuiltDocument, "document"},
		{collage.BuiltAsset, "asset"},

		{collage.StageRoute, "route"},
		{collage.StageGuard, "guard"},
		{collage.StageBeforeAction, "before_action"},
		{collage.StageHandler, "handler"},
		{collage.StageBeforeRender, "before_render"},
		{collage.StageRender, "render"},
		{collage.StageAfterRender, "after_render"},
		{collage.StageCacheWrite, "cache_write"},
		{collage.StageErrorPage, "error_page"},
		{collage.StageNotFound, "not_found"},
		{collage.StageAsset, "asset"},
		{collage.StagePlugin, "plugin"},
		{collage.StagePanic, "panic"},
	} {
		if c.got != c.want {
			t.Errorf("constant = %q, want %q", c.got, c.want)
		}
	}
}

// constNames returns the exported constants declared in the non-test Go files
// of dir whose names start with one of prefixes.
func constNames(t *testing.T, dir string, prefixes ...string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, dir+"/"+e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				for _, name := range spec.(*ast.ValueSpec).Names {
					for _, prefix := range prefixes {
						if strings.HasPrefix(name.Name, prefix) && name.IsExported() {
							names = append(names, name.Name)
						}
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// Every kind and stage the engine names — the single internal source the
// handler, the router and the static build emit through — has a public
// constant of the same name, so a stage added to the engine cannot go out
// unnamed.
func TestKindAndStageConstants_CoverTheEngine(t *testing.T) {
	prefixes := []string{"RouteKind", "Built", "Stage"}
	engine := constNames(t, "../../internal/types", prefixes...)
	public := constNames(t, ".", prefixes...)
	if len(engine) == 0 {
		t.Fatal("found no kind or stage constants in the engine")
	}
	if strings.Join(engine, ",") != strings.Join(public, ",") {
		t.Errorf("engine names %v\npublic names %v", engine, public)
	}
}

// A request for a document resolves to the document kind.
func TestRouteInfo_DocumentKind(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var kind string
	doc := collage.NewDocument("robots", "text/plain").AtRoot("/robots.txt").
		WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			kind = collage.RouteInfo(ctx).Kind
			return []byte("ok"), nil, nil
		}).Build()
	if err := app.Register(doc); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /robots.txt = %d %s", rec.Code, rec.Body)
	}
	if kind != collage.RouteKindDocument {
		t.Errorf("RouteInfo(ctx).Kind = %q, want %q", kind, collage.RouteKindDocument)
	}
}
