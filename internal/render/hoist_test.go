package render

import (
	"context"
	htmltemplate "html/template"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

func hoistOf(area, key, html string) types.DataHandlerFunc {
	return func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: the framework's own handler signature
		if key != "" {
			rc.Hoist(area, key, htmltemplate.HTML(html))
		}
		return nil, nil, nil
	}
}

func renderHoist(t *testing.T, layoutTmpl, contentTmpl string, layoutData, contentData types.DataHandlerFunc) string {
	t.Helper()

	// Templates from a map rather than testdata, so each case carries its own
	// markup and a reader does not have to go looking for it.
	tmpl, err := template.NewHTML(template.HTMLConfig{
		FS: fstest.MapFS{
			"layout.html":  &fstest.MapFile{Data: []byte(layoutTmpl)},
			"content.html": &fstest.MapFile{Data: []byte(contentTmpl)},
		},
		Extension: ".html",
	})
	if err != nil {
		t.Fatalf("template.NewHTML: %v", err)
	}
	engine := New(tmpl, Options{})

	child := &types.Fragment{Name: "content", TemplatePath: "content.html", DataHandler: contentData}
	root := &types.Fragment{
		Name:         "layout",
		TemplatePath: "layout.html",
		DataHandler:  layoutData,
		Slots: map[string]*types.SlotDefinition{
			types.DefaultContentSlot: {Name: types.DefaultContentSlot, Required: true, Fill: []*types.Fragment{child}},
		},
	}
	page := &types.Page{Name: "page", LayoutFragment: root, ContentFragment: child}
	rc := types.NewRenderContext(context.Background(), nil, page, "en", nil)

	result, renderErr := engine.Render(context.Background(), rc)
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	return string(result.HTML)
}

func TestHoist_AFragmentContributesToTheLayoutsHead(t *testing.T) {
	// The ordering problem this exists for: the layout writes <head> before the
	// content has rendered, so the content's contribution has to arrive afterwards.
	got := renderHoist(t,
		`<head>{{hoist "head"}}</head><body>{{slot "content"}}</body>`,
		`<p>body</p>`,
		nil,
		hoistOf("head", "css", `<link rel="stylesheet" href="/a.css">`),
	)

	if !strings.Contains(got, `<head><link rel="stylesheet" href="/a.css"></head>`) {
		t.Errorf("the contribution did not reach the head:\n%s", got)
	}
}

func TestHoist_InnermostWins(t *testing.T) {
	// A layout naming a default title and an article naming its own are not in
	// conflict: the article is more specific.
	got := renderHoist(t,
		`<head>{{hoist "head"}}</head><body>{{slot "content"}}</body>`,
		`<p>body</p>`,
		hoistOf("head", "title", `<title>The Site</title>`),
		hoistOf("head", "title", `<title>The Article</title>`),
	)

	if !strings.Contains(got, `<title>The Article</title>`) {
		t.Errorf("the inner declaration did not win:\n%s", got)
	}
	if strings.Contains(got, `<title>The Site</title>`) {
		t.Errorf("both declarations were written:\n%s", got)
	}
}

func TestHoist_PositionComesFromTheFirstDeclaration(t *testing.T) {
	// Otherwise a page's head would reorder itself depending on whether something
	// nested happened to override a title.
	got := renderHoist(t,
		`<head>{{hoist "head"}}</head><body>{{slot "content"}}</body>`,
		`<p>body</p>`,
		func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: the framework's own handler signature
			rc.Hoist("head", "title", `<title>Default</title>`)
			rc.Hoist("head", "css", `<link href="/a.css">`)
			return nil, nil, nil
		},
		hoistOf("head", "title", `<title>Specific</title>`),
	)

	titleAt := strings.Index(got, "<title>Specific</title>")
	cssAt := strings.Index(got, `<link href="/a.css">`)
	if titleAt < 0 || cssAt < 0 {
		t.Fatalf("something is missing:\n%s", got)
	}
	if titleAt > cssAt {
		t.Errorf("the overridden key moved to the end:\n%s", got)
	}
}

func TestHoist_DistinctKeysAllAppear(t *testing.T) {
	got := renderHoist(t,
		`<head>{{hoist "head"}}</head><body>{{slot "content"}}</body>`,
		`<p>body</p>`,
		hoistOf("head", "css:a", `<link href="/a.css">`),
		hoistOf("head", "css:b", `<link href="/b.css">`),
	)

	for _, want := range []string{`<link href="/a.css">`, `<link href="/b.css">`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is missing:\n%s", want, got)
		}
	}
}

func TestHoist_AnEmptyAreaLeavesNothingBehind(t *testing.T) {
	// A marker on the wire is a comment that leaks the mechanism, and one a later
	// render could mistake for its own.
	got := renderHoist(t,
		`<head>{{hoist "head"}}</head><body>{{slot "content"}}</body>`,
		`<p>body</p>`,
		nil, nil,
	)

	if strings.Contains(got, "collage:hoist") {
		t.Errorf("a marker reached the output:\n%s", got)
	}
	if !strings.Contains(got, "<head></head>") {
		t.Errorf("the head is not empty:\n%s", got)
	}
}

func TestHoist_ContentContainingAMarkerIsNotReplaced(t *testing.T) {
	// The token is random per render precisely so a page rendering text that looks
	// like a marker does not have that text replaced by its own stylesheets.
	got := renderHoist(t,
		`<head>{{hoist "head"}}</head><body>{{slot "content"}}</body>`,
		`<p>{{safeHTML "<!--collage:hoist:static:head-->"}}</p>`,
		nil,
		hoistOf("head", "css", `<link href="/a.css">`),
	)

	if !strings.Contains(got, "<!--collage:hoist:static:head-->") {
		t.Errorf("a marker-shaped string in content was consumed:\n%s", got)
	}
	if strings.Count(got, `<link href="/a.css">`) != 1 {
		t.Errorf("the contribution was written %d times:\n%s", strings.Count(got, `<link href="/a.css">`), got)
	}
}

// TestHoist_DepthBeatsOrder is the case that tells "innermost wins" apart from
// "last wins", which the simpler tests above cannot: a parent's data handler always
// runs before its children's, so for a straight parent/child pair the two rules
// agree and either implementation passes.
//
// Here they disagree. The tree has two branches: the first goes two levels deep and
// declares at depth 3, the second declares at depth 2 afterwards. Ordering would
// hand the key to the shallower fragment because it wrote last; depth hands it to
// the deeper one, which is the rule.
func TestHoist_DepthBeatsOrder(t *testing.T) {
	tmpl, err := template.NewHTML(template.HTMLConfig{
		FS: fstest.MapFS{
			"root.html":   &fstest.MapFile{Data: []byte(`<head>{{hoist "head"}}</head>{{slot "deep"}}{{slot "shallow"}}`)},
			"branch.html": &fstest.MapFile{Data: []byte(`<div>{{slot "content"}}</div>`)},
			"leaf.html":   &fstest.MapFile{Data: []byte(`<p>leaf</p>`)},
		},
		Extension: ".html",
	})
	if err != nil {
		t.Fatalf("template.NewHTML: %v", err)
	}

	// depth 3: root -> branch -> leaf
	leaf := &types.Fragment{Name: "leaf", TemplatePath: "leaf.html",
		DataHandler: hoistOf("head", "title", `<title>Deep</title>`)}
	branch := &types.Fragment{
		Name: "branch", TemplatePath: "branch.html",
		Slots: map[string]*types.SlotDefinition{
			"content": {Name: "content", Required: true, Fill: []*types.Fragment{leaf}},
		},
	}
	// depth 2: root -> shallow, rendered after the branch above
	shallow := &types.Fragment{Name: "shallow", TemplatePath: "leaf.html",
		DataHandler: hoistOf("head", "title", `<title>Shallow</title>`)}

	root := &types.Fragment{
		Name: "root", TemplatePath: "root.html",
		Slots: map[string]*types.SlotDefinition{
			"deep":    {Name: "deep", Required: true, Fill: []*types.Fragment{branch}},
			"shallow": {Name: "shallow", Required: true, Fill: []*types.Fragment{shallow}},
		},
	}

	page := &types.Page{Name: "page", ContentFragment: root}
	rc := types.NewRenderContext(context.Background(), nil, page, "en", nil)

	result, err := New(tmpl, Options{}).Render(context.Background(), rc)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(result.HTML)

	if !strings.Contains(got, "<title>Deep</title>") {
		t.Errorf("the deeper declaration lost to the one that merely wrote last:\n%s", got)
	}
	if strings.Contains(got, "<title>Shallow</title>") {
		t.Errorf("the shallower declaration overwrote a more specific one:\n%s", got)
	}
}
