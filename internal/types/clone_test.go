package types

import (
	"context"
	"reflect"
	"testing"
)

// Review Focus 2: after a clone no slice or map is shared, for every field the
// types have now or gain later. A container field the fixture leaves empty fails
// the test, so a field added later has to be put in the fixture to pass.
func assertNoSharedContainers(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	for i := range a.NumField() {
		fa, fb := a.Field(i), b.Field(i)
		name := path + "." + a.Type().Field(i).Name
		switch fa.Kind() {
		case reflect.Slice, reflect.Map:
			if fa.Len() == 0 {
				t.Errorf("%s is empty in the fixture: fill it so the clone is checked", name)
				continue
			}
			if fa.UnsafePointer() == fb.UnsafePointer() {
				t.Errorf("%s is shared after clone", name)
			}
			// A map of maps — Page.FragmentPaths — is written through its inner
			// maps, so those must be independent too.
			if fa.Kind() == reflect.Map && fa.Type().Elem().Kind() == reflect.Map {
				for _, k := range fa.MapKeys() {
					ia, ib := fa.MapIndex(k), fb.MapIndex(k)
					if ia.Len() > 0 && ia.UnsafePointer() == ib.UnsafePointer() {
						t.Errorf("%s[%v] is shared after clone", name, k)
					}
				}
			}
		}
	}
}

func clonePageFixture() *Page {
	layout := &Fragment{Name: "l"}
	p := &Page{
		Name:            "p",
		Paths:           map[string]string{"en": "/"},
		LayoutChain:     []*Fragment{layout},
		ContentFragment: &Fragment{Name: "c"},
		Redirects:       []*Redirect{{From: "/a", To: "/b"}},
		CacheParams:     []string{"q"},
		DependencyTags:  []string{"t"},
		Actions:         []*Action{{Name: "a", Methods: []string{"POST"}}},
		FragmentPaths:   map[string]map[string]*Fragment{"en": {"/f": {Name: "f"}}},
	}
	SetLayoutFragment(p, layout)
	return p
}

func TestClone_NoSharedContainers(t *testing.T) {
	p := clonePageFixture()
	cp := ClonePage(p)
	assertNoSharedContainers(t, reflect.ValueOf(p).Elem(), reflect.ValueOf(cp).Elem(), "Page")
	if !reflect.DeepEqual(p, cp) {
		t.Error("ClonePage is not equal to its source")
	}
	if LayoutFragment(cp) != cp.LayoutChain[0] {
		t.Error("the clone's layout fragment is not its chain's outermost layout")
	}

	f := &Fragment{Name: "f", Slots: map[string]*SlotDefinition{"s": {Name: "s", Fill: []*Fragment{{Name: "c"}}}}}
	c := CloneFragment(f)
	assertNoSharedContainers(t, reflect.ValueOf(f).Elem(), reflect.ValueOf(c).Elem(), "Fragment")
	if c.Slots["s"] == f.Slots["s"] {
		t.Error("SlotDefinition pointer shared")
	}
	assertNoSharedContainers(t, reflect.ValueOf(f.Slots["s"]).Elem(), reflect.ValueOf(c.Slots["s"]).Elem(), "Fragment.Slots[s]")
	if !reflect.DeepEqual(f, c) {
		t.Error("CloneFragment is not equal to its source")
	}

	d := &Document{
		Name: "d", Paths: map[string]string{"en": "/d"}, Body: []byte("x"),
		CacheParams: []string{"q"}, DependencyTags: []string{"t"},
		Redirects: []*Redirect{{From: "/a", To: "/d"}},
	}
	cd := CloneDocument(d)
	assertNoSharedContainers(t, reflect.ValueOf(d).Elem(), reflect.ValueOf(cd).Elem(), "Document")
	if !reflect.DeepEqual(d, cd) {
		t.Error("CloneDocument is not equal to its source")
	}

	a := &Action{
		Name: "a", Paths: map[string]string{"en": "/a"}, Methods: []string{"POST"},
		Handler: func(context.Context, *RenderContext) (*ActionResult, error) { return nil, nil },
	}
	ca := CloneAction(a)
	assertNoSharedContainers(t, reflect.ValueOf(a).Elem(), reflect.ValueOf(ca).Elem(), "Action")
	if ca.Name != a.Name || !reflect.DeepEqual(ca.Paths, a.Paths) || !reflect.DeepEqual(ca.Methods, a.Methods) {
		t.Error("CloneAction is not equal to its source")
	}
}

// An empty, non-nil CacheParams says "drop the query from the key"; a clone
// must not turn it into nil, which says "keep every parameter".
func TestClone_KeepsEmptyCacheParams(t *testing.T) {
	p := &Page{Name: "p", CacheParams: []string{}}
	if ClonePage(p).CacheParams == nil {
		t.Error("ClonePage turned an empty CacheParams into nil")
	}
	d := &Document{Name: "d", CacheParams: []string{}}
	if CloneDocument(d).CacheParams == nil {
		t.Error("CloneDocument turned an empty CacheParams into nil")
	}
}

func TestClone_Nil(t *testing.T) {
	if ClonePage(nil) != nil || CloneFragment(nil) != nil || CloneDocument(nil) != nil || CloneAction(nil) != nil {
		t.Error("a nil value cloned to non-nil")
	}
}
