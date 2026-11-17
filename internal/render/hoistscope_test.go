package render

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// fetched gives f a data handler.
func fetched(f *types.Fragment, handler types.DataHandlerFunc) *types.Fragment {
	types.SetFragmentDataSource(f, types.FetchedData(handler, nil))
	return f
}

// renderTree renders root, a layout over whatever its slots hold, from templates
// given by name.
func renderTree(t *testing.T, files map[string]string, root *types.Fragment) *Result {
	t.Helper()
	mapFS := fstest.MapFS{}
	for name, body := range files {
		mapFS[name] = &fstest.MapFile{Data: []byte(body)}
	}
	tmpl, err := template.NewHTML(template.HTMLConfig{FS: mapFS, Extension: ".html"})
	if err != nil {
		t.Fatalf("template.NewHTML: %v", err)
	}
	page := &types.Page{Name: "page", ContentFragment: root}
	rc := types.NewRenderContext(context.Background(), nil, page, "en", nil)
	result, renderErr := New(tmpl, Options{}).Render(context.Background(), rc)
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	return result
}

func slotOf(name string, children ...*types.Fragment) map[string]*types.SlotDefinition {
	return map[string]*types.SlotDefinition{name: {Name: name, AllowMultiple: true, Fill: children}}
}

func slots(defs ...map[string]*types.SlotDefinition) map[string]*types.SlotDefinition {
	all := map[string]*types.SlotDefinition{}
	for _, def := range defs {
		for name, slot := range def {
			all[name] = slot
		}
	}
	return all
}

// A fragment behind a slot the template never renders is not on the page, so what
// it declares for the page — its title, its robots meta, the tags its Cached call
// carries — is not either. Its handler is started ahead of the walk and may have
// declared all of it by the time the template decides against the slot.
func TestHoist_AFragmentTheTemplateNeverRendersDeclaresNothing(t *testing.T) {
	cachedKey := types.NewKey[string]("admin-data")
	for range 20 {
		admin := fetched(&types.Fragment{Name: "admin", TemplatePath: "admin.html", Title: "ADMIN PANEL"},
			func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				rc.HoistMeta("robots", "noindex")
				_, err := types.Cached(rc, cachedKey, time.Minute, []string{"admin:secret"},
					func(context.Context) (string, error) { return "x", nil })
				return nil, nil, err
			})
		root := fetched(&types.Fragment{Name: "layout", TemplatePath: "layout.html", Slots: slotOf("admin", admin)},
			func(context.Context, *types.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				return map[string]bool{"Show": false}, nil, nil
			})
		result := renderTree(t, map[string]string{
			"layout.html": `<head>{{hoist "head"}}</head>{{if .Show}}{{slot "admin"}}{{end}}`,
			"admin.html":  `<p>admin</p>`,
		}, root)

		if got := string(result.HTML); strings.Contains(got, "ADMIN PANEL") || strings.Contains(got, "robots") {
			t.Fatalf("a fragment that was not rendered changed the head:\n%s", got)
		}
		if slices.Contains(result.DependencyTags, "admin:secret") {
			t.Fatalf("a fragment that was not rendered added its tags: %v", result.DependencyTags)
		}
	}
}

// A fragment whose attempt failed has its output cut back; what it declared for
// the page goes with it. The fallback that renders instead is the page's content,
// and it is not "Draft: secret".
func TestHoist_AFailedFragmentsDeclarationsAreDropped(t *testing.T) {
	for _, prefetched := range []bool{false, true} {
		draft := &types.Fragment{Name: "draft", TemplatePath: "draft.html", Title: "Draft: secret",
			Fallback: &types.Fragment{Name: "fallback", TemplatePath: "fallback.html"}}
		fetched(draft, func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: the framework's own handler signature
			rc.HoistMeta("robots", "noindex")
			return nil, nil, errors.New("draft is gone")
		})
		var root *types.Fragment
		if prefetched {
			// A parent with a handler of its own hands the draft over as a prefetch.
			root = fetched(&types.Fragment{Name: "layout", TemplatePath: "layout.html", Slots: slotOf("c", draft)},
				func(context.Context, *types.RenderContext) (any, []string, error) { return nil, nil, nil }) // any: the framework's own handler signature
		} else {
			root = &types.Fragment{Name: "layout", TemplatePath: "layout.html", Slots: slotOf("c", draft)}
		}
		result := renderTree(t, map[string]string{
			"layout.html":   `<head>{{hoist "head"}}</head>{{slot "c"}}`,
			"draft.html":    `<p>draft</p>`,
			"fallback.html": `<p>fallback</p>`,
		}, root)

		got := string(result.HTML)
		if !strings.Contains(got, "fallback") {
			t.Fatalf("prefetched=%v: the fallback did not render:\n%s", prefetched, got)
		}
		if strings.Contains(got, "Draft: secret") || strings.Contains(got, "robots") {
			t.Errorf("prefetched=%v: a failed fragment's declarations stayed in the page:\n%s", prefetched, got)
		}
	}
}

// At equal depth the later fragment wins, later meaning later in the page: the
// order a slot's fragments are bound in, and across slots the order the template
// renders them. Not the order handlers were started in, which followed slot names
// and put every fragment with a handler ahead of every one without.
func TestHoist_TheLaterFragmentWinsAtEqualDepth(t *testing.T) {
	titled := func(name, title string) *types.Fragment {
		return fetched(&types.Fragment{Name: name, TemplatePath: "plain.html"},
			func(_ context.Context, rc *types.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				rc.HoistTitle(title)
				return nil, nil, nil
			})
	}
	files := map[string]string{
		"layout.html": `<head>{{hoist "head"}}</head>{{slot "z"}}{{slot "a"}}`,
		"plain.html":  `<p></p>`,
	}

	t.Run("across slots, the one the template renders last", func(t *testing.T) {
		root := &types.Fragment{Name: "layout", TemplatePath: "layout.html",
			Slots: slots(slotOf("z", titled("first", "FIRST")), slotOf("a", titled("second", "SECOND")))}
		if got := string(renderTree(t, files, root).HTML); !strings.Contains(got, "<title>SECOND</title>") {
			t.Errorf("the later slot did not win:\n%s", got)
		}
	})

	t.Run("a sibling with only a Title against one with a handler", func(t *testing.T) {
		for _, titleFirst := range []bool{true, false} {
			titleOnly := &types.Fragment{Name: "static", TemplatePath: "plain.html", Title: "STATIC"}
			handler := titled("dynamic", "DYNAMIC")
			children, want := []*types.Fragment{handler, titleOnly}, "STATIC"
			if titleFirst {
				children, want = []*types.Fragment{titleOnly, handler}, "DYNAMIC"
			}
			root := &types.Fragment{Name: "layout", TemplatePath: "layout.html", Slots: slots(slotOf("z", children...), slotOf("a"))}
			if got := string(renderTree(t, files, root).HTML); !strings.Contains(got, "<title>"+want+"</title>") {
				t.Errorf("titleFirst=%v: want %s to win, the later:\n%s", titleFirst, want, got)
			}
		}
	})
}
