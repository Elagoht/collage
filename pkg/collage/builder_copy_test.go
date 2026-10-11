package collage_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func copyContent() *collage.Fragment {
	return collage.NewInlineFragment("c", "<p>c</p>").Build()
}

func copyActionHandler(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
	return collage.Status(204), nil
}

func TestBuild_ReturnsIndependentCopy(t *testing.T) {
	b := collage.NewPage("p").WithContent(copyContent()).WithPath("en", "/a")
	p := b.Build()
	b.WithPath("tr", "/b").
		WithDependencyTags("t").
		WithFragmentPath("en", "/a/f", copyContent()).
		WithAction("POST", copyActionHandler)
	if _, ok := p.Paths["tr"]; ok {
		t.Error("WithPath after Build reached the built page")
	}
	if len(p.DependencyTags) != 0 || len(p.FragmentPaths) != 0 || len(p.Actions) != 0 {
		t.Error("a WithX after Build reached the built page")
	}
	if b.Build() == p {
		t.Error("two Builds returned one value")
	}
}

// Review Focus 2, through the builders: two Builds share no slice or map, for
// every container field the built types have now or gain later. A container the
// builder leaves empty fails, so a field added later must be filled here.
func TestBuild_ReturnsIndependentCopy_NoSharedContainers(t *testing.T) {
	layout := collage.NewInlineFragment("l", `{{slot "content"}}`).Build()
	pb := collage.NewPage("p").WithLayouts(layout).WithContent(copyContent()).
		WithPath("en", "/p").WithRedirect("/old", "/p", 302).WithCacheParams("q").
		WithDependencyTags("t").WithAction("POST", copyActionHandler).
		WithFragmentPath("en", "/p/f", copyContent())
	assertBuildsShareNothing(t, "Page", reflect.ValueOf(pb.Build()), reflect.ValueOf(pb.Build()))

	fb := collage.NewInlineFragment("f", `{{slot "s"}}`).WithSlotFragment("s", copyContent())
	f1, f2 := fb.Build(), fb.Build()
	assertBuildsShareNothing(t, "Fragment", reflect.ValueOf(f1), reflect.ValueOf(f2))
	assertBuildsShareNothing(t, "Fragment.Slots[s]", reflect.ValueOf(f1.Slots["s"]), reflect.ValueOf(f2.Slots["s"]))

	db := collage.NewDocument("d", "text/plain").WithBody([]byte("x")).WithPath("en", "/d").
		WithCacheParams("q").WithDependencyTags("t").WithRedirect("/old.txt", "/d", 302)
	assertBuildsShareNothing(t, "Document", reflect.ValueOf(db.Build()), reflect.ValueOf(db.Build()))

	ab := collage.NewAction("a").WithHandler(copyActionHandler).WithPath("en", "/a").WithMethods("POST")
	assertBuildsShareNothing(t, "Action", reflect.ValueOf(ab.Build()), reflect.ValueOf(ab.Build()))
}

// assertBuildsShareNothing takes a and b as pointers to two built values.
func assertBuildsShareNothing(t *testing.T, name string, a, b reflect.Value) {
	t.Helper()
	if a.Pointer() == b.Pointer() {
		t.Fatalf("%s: two Builds returned one value", name)
	}
	va, vb := a.Elem(), b.Elem()
	for i := range va.NumField() {
		fa, fb := va.Field(i), vb.Field(i)
		field := name + "." + va.Type().Field(i).Name
		// Equal, field by field — exported ones, which is what this package
		// can read: reflect.DeepEqual calls two non-nil funcs unequal even when
		// they are one func.
		if fa.Kind() == reflect.Func {
			if fa.IsNil() != fb.IsNil() {
				t.Errorf("%s differs between two Builds", field)
			}
			continue
		}
		if fa.CanInterface() && !reflect.DeepEqual(fa.Interface(), fb.Interface()) {
			t.Errorf("%s differs between two Builds", field)
		}
		if fa.Kind() != reflect.Slice && fa.Kind() != reflect.Map {
			continue
		}
		if fa.Len() == 0 {
			t.Errorf("%s is empty: fill it through the builder so the copy is checked", field)
			continue
		}
		if fa.UnsafePointer() == fb.UnsafePointer() {
			t.Errorf("%s is shared between two Builds", field)
		}
	}
}

func TestBuild_ReturnsIndependentCopy_Fragment(t *testing.T) {
	b := collage.NewInlineFragment("f", `{{slot "s"}}`)
	f := b.Build()
	b.WithSlot("s", collage.SlotRequired).WithSlotFragment("s", copyContent())
	if _, ok := f.Slots["s"]; ok {
		t.Error("WithSlot after Build reached the built fragment")
	}

	b2 := collage.NewInlineFragment("f2", `{{slot "s"}}`).WithSlotFragment("s", copyContent())
	f2 := b2.Build()
	b2.WithSlotFragment("s", copyContent()).WithSlot("s", collage.SlotRequired)
	if n := len(f2.Slots["s"].Fill); n != 1 {
		t.Errorf("built slot holds %d fragments after a later WithSlotFragment, want 1", n)
	}
	if f2.Slots["s"].Required {
		t.Error("WithSlot after Build changed the built slot")
	}
}

func TestBuild_ReturnsIndependentCopy_Document(t *testing.T) {
	b := collage.NewDocument("d", "text/plain").WithBody([]byte("x")).WithPath("en", "/d")
	d := b.Build()
	b.WithPath("tr", "/d").WithDependencyTags("t")
	if _, ok := d.Paths["tr"]; ok {
		t.Error("WithPath after Build reached the built document")
	}
	if len(d.DependencyTags) != 0 {
		t.Error("WithDependencyTags after Build reached the built document")
	}
}

func TestBuild_ReturnsIndependentCopy_Action(t *testing.T) {
	b := collage.NewAction("a").WithHandler(copyActionHandler).WithPath("en", "/a").WithMethods("POST")
	a := b.Build()
	b.WithMethods("PUT").WithPath("tr", "/a")
	if len(a.Methods) != 1 {
		t.Errorf("WithMethods after Build reached the built action: %v", a.Methods)
	}
	if _, ok := a.Paths["tr"]; ok {
		t.Error("WithPath after Build reached the built action")
	}
}

func TestBuild_TwiceRecordsOnce(t *testing.T) {
	b := collage.NewPage("p") // no content: Build records ErrMissingContent
	b.Build()
	b.Build()
	if n := countJoined(b.BuildErr()); n != 1 {
		t.Errorf("errors after two Builds = %d, want 1", n)
	}

	d := collage.NewDocument("d", "text/plain") // no handler or body
	d.Build()
	d.Build()
	if n := countJoined(d.BuildErr()); n != 1 {
		t.Errorf("document errors after two Builds = %d, want 1", n)
	}

	a := collage.NewAction("a") // no handler
	a.Build()
	a.Build()
	if n := countJoined(a.BuildErr()); n != 1 {
		t.Errorf("action errors after two Builds = %d, want 1", n)
	}
}

// A Build-time error describes the builder as it stood at that Build: content
// set afterwards clears it for the next one.
func TestBuild_LaterBuildDropsFixedBuildTimeError(t *testing.T) {
	b := collage.NewPage("p")
	b.Build()
	b.WithContent(copyContent())
	if err := b.BuildErr(); !errors.Is(err, collage.ErrMissingContent) {
		t.Fatalf("before the second Build: %v", err)
	}
	b.Build()
	if err := b.BuildErr(); err != nil {
		t.Errorf("after content and a second Build: %v", err)
	}
}

func countJoined(err error) int {
	if err == nil {
		return 0
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return len(j.Unwrap())
	}
	return 1
}

func TestActionBuilder_BuildErr(t *testing.T) {
	b := collage.NewAction("a") // no handler
	b.Build()
	if !errors.Is(b.BuildErr(), collage.ErrNoActionHandler) {
		t.Errorf("an action with no handler: %v", b.BuildErr())
	}

	nb := collage.NewAction("n").WithHandler(copyActionHandler).WithBodyTimeout(-time.Second)
	nb.Build()
	if !errors.Is(nb.BuildErr(), collage.ErrNegativeBodyTimeout) {
		t.Errorf("an action with a negative body timeout: %v", nb.BuildErr())
	}

	ok := collage.NewAction("ok").WithHandler(copyActionHandler).WithPath("en", "/ok").WithMethods("POST")
	ok.Build()
	if err := ok.BuildErr(); err != nil {
		t.Errorf("a complete action: %v", err)
	}
}

// Registration refuses an action whose builder recorded an error, on its own
// and attached to a page, as it does a page.
func TestActionBuilder_RecordedErrorRefusedAtRegistration(t *testing.T) {
	app := registerApp(t)
	bad := collage.NewAction("bad").WithHandler(copyActionHandler).
		WithPath("en", "/bad").WithMethods("POST").WithPath("en", "/again")
	if err := app.RegisterAction(bad.Build()); !errors.Is(err, collage.ErrDuplicateLocalePath) {
		t.Errorf("RegisterAction: %v", err)
	}

	attached := collage.NewAction("attached").WithHandler(copyActionHandler).WithMethods("POST").WithPath("", "/x")
	page := collage.NewPage("p").WithContent(copyContent()).WithPath("en", "/p").WithActionFor(attached.Build())
	if err := app.RegisterPage(page.Build()); !errors.Is(err, collage.ErrInvalidLocale) {
		t.Errorf("RegisterPage with an attached action built with errors: %v", err)
	}
}

func TestWithPath_RepeatAndEmptyLocale(t *testing.T) {
	b := collage.NewPage("p").WithContent(copyContent()).WithPath("en", "/a").WithPath("en", "/b").WithPath("", "/c")
	p := b.Build()
	if !errors.Is(b.BuildErr(), collage.ErrDuplicateLocalePath) {
		t.Errorf("repeat: %v", b.BuildErr())
	}
	if !errors.Is(b.BuildErr(), collage.ErrInvalidLocale) {
		t.Errorf("empty locale: %v", b.BuildErr())
	}
	if p.Paths["en"] != "/a" {
		t.Errorf("a repeated locale replaced the first path: %q", p.Paths["en"])
	}
	if _, ok := p.Paths[""]; ok {
		t.Error("an empty locale was written")
	}

	d := collage.NewDocument("d", "text/plain").WithBody([]byte("x")).WithPath("en", "/a").WithPath("en", "/b").WithPath("", "/c")
	d.Build()
	if !errors.Is(d.BuildErr(), collage.ErrDuplicateLocalePath) || !errors.Is(d.BuildErr(), collage.ErrInvalidLocale) {
		t.Errorf("document: %v", d.BuildErr())
	}

	a := collage.NewAction("a").WithHandler(copyActionHandler).WithPath("en", "/a").WithPath("en", "/b").WithPath("", "/c")
	a.Build()
	if !errors.Is(a.BuildErr(), collage.ErrDuplicateLocalePath) || !errors.Is(a.BuildErr(), collage.ErrInvalidLocale) {
		t.Errorf("action: %v", a.BuildErr())
	}
}

// AtRoot is how a document reaches the root locale; WithPath("") is not.
func TestWithPath_AtRootStillWritesRoot(t *testing.T) {
	b := collage.NewDocument("robots", "text/plain").WithBody([]byte("x")).AtRoot("/robots.txt")
	d := b.Build()
	if err := b.BuildErr(); err != nil {
		t.Errorf("AtRoot: %v", err)
	}
	if d.Paths[""] != "/robots.txt" {
		t.Errorf("AtRoot path = %q", d.Paths[""])
	}
}

// AtRoot twice is the root locale's repeated WithPath: refused, first path kept.
func TestAtRoot_Twice(t *testing.T) {
	b := collage.NewDocument("robots", "text/plain").WithBody([]byte("x")).AtRoot("/robots.txt").AtRoot("/other.txt")
	d := b.Build()
	err := b.BuildErr()
	if !errors.Is(err, collage.ErrDuplicateLocalePath) {
		t.Fatalf("AtRoot twice: %v, want ErrDuplicateLocalePath", err)
	}
	if want := `AtRoot on document "robots"`; !strings.Contains(err.Error(), want) {
		t.Errorf("AtRoot twice error = %q, want it to name %s", err, want)
	}
	if d.Paths[""] != "/robots.txt" {
		t.Errorf("AtRoot twice replaced the first path: %q", d.Paths[""])
	}
}
