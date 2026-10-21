package types

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// Review Focus 2: after a clone no slice or map is shared, for every field the
// types have now or gain later. A container field the fixture leaves empty fails
// the test, so a field added later has to be put in the fixture to pass.
//
// It descends into struct-valued fields, and into the pointers a clone owns —
// ownedPointers — wherever they sit: a field, a slice element or a map value. A
// pointer to another built value (a *Fragment child, an error *Page, an attached
// *Action) is not descended into: it is a value of its own, which its builder
// already copied, and sharing it is the point.
func assertNoSharedContainers(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	for i := range a.NumField() {
		fa, fb := a.Field(i), b.Field(i)
		name := path + "." + a.Type().Field(i).Name
		switch fa.Kind() {
		case reflect.Struct:
			assertNoSharedContainers(t, fa, fb, name)
		case reflect.Pointer:
			assertOwnedPointer(t, fa, fb, name)
		case reflect.Slice, reflect.Map:
			if fa.Len() == 0 {
				t.Errorf("%s is empty in the fixture: fill it so the clone is checked", name)
				continue
			}
			if fa.UnsafePointer() == fb.UnsafePointer() {
				t.Errorf("%s is shared after clone", name)
			}
			if fa.Kind() == reflect.Slice {
				for j := range min(fa.Len(), fb.Len()) {
					assertOwnedPointer(t, fa.Index(j), fb.Index(j), fmt.Sprintf("%s[%d]", name, j))
				}
				continue
			}
			for _, k := range fa.MapKeys() {
				ia, ib := fa.MapIndex(k), fb.MapIndex(k)
				if !ib.IsValid() {
					t.Errorf("%s[%v] is missing after clone", name, k)
					continue
				}
				// A map of maps — Page.FragmentPaths — is written through its
				// inner maps, so those must be independent too.
				if ia.Kind() == reflect.Map && ia.Len() > 0 && ia.UnsafePointer() == ib.UnsafePointer() {
					t.Errorf("%s[%v] is shared after clone", name, k)
				}
				assertOwnedPointer(t, ia, ib, fmt.Sprintf("%s[%v]", name, k))
			}
		}
	}
}

// ownedPointers are the pointer types a clone copies rather than shares: a
// builder writes through them in place. A *Redirect is not one — a builder
// makes it once and never writes it again.
var ownedPointers = map[reflect.Type]bool{
	reflect.TypeFor[*SlotDefinition](): true,
}

// assertOwnedPointer checks that a and b, when they are an owned pointer, point
// at two values sharing no container, and does nothing for any other value.
func assertOwnedPointer(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	if a.Kind() != reflect.Pointer || !ownedPointers[a.Type()] || a.IsNil() {
		return
	}
	if b.IsNil() || a.Pointer() == b.Pointer() {
		t.Errorf("%s is shared after clone", path)
		return
	}
	assertNoSharedContainers(t, a.Elem(), b.Elem(), path)
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
	// Descends into Fragment.Slots["s"], a *SlotDefinition the clone owns.
	assertNoSharedContainers(t, reflect.ValueOf(f).Elem(), reflect.ValueOf(c).Elem(), "Fragment")
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
		Handler:      func(context.Context, *RenderContext) (*ActionResult, error) { return nil, nil },
		MaxBodyBytes: 1, StreamingBody: true, BodyTimeout: time.Second, SkipCSRF: true,
		Guard:    func(context.Context, *http.Request) (*GuardDecision, error) { return nil, nil },
		buildErr: errors.New("recorded"),
	}
	av := reflect.ValueOf(a).Elem()
	for i := range av.NumField() {
		if av.Field(i).IsZero() {
			t.Errorf("Action.%s is zero in the fixture: set it so the clone is compared", av.Type().Field(i).Name)
		}
	}
	ca := CloneAction(a)
	assertNoSharedContainers(t, av, reflect.ValueOf(ca).Elem(), "Action")
	// Every field, unexported ones included: reflect.DeepEqual calls two
	// non-nil funcs unequal even when they are one func, so Handler and Guard
	// are compared by identity and left out of the rest.
	if reflect.ValueOf(ca.Handler).Pointer() != reflect.ValueOf(a.Handler).Pointer() {
		t.Error("CloneAction's Handler is not its source's")
	}
	if reflect.ValueOf(ca.Guard).Pointer() != reflect.ValueOf(a.Guard).Pointer() {
		t.Error("CloneAction's Guard is not its source's")
	}
	x, y := *a, *ca
	x.Handler, y.Handler = nil, nil
	x.Guard, y.Guard = nil, nil
	if !reflect.DeepEqual(x, y) {
		t.Errorf("CloneAction is not equal to its source:\n%+v\n%+v", y, x)
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
