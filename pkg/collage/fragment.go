package collage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// ErrDuplicateSlot is returned when FragmentBuilder.WithSlot is called twice with the
// same slot name on the same fragment.
var ErrDuplicateSlot = errors.New("collage: slot already declared")

// FragmentBuilder builds a Fragment through a fluent chain of WithX calls. See the
// package doc comment for how builder errors are accumulated and why it is safe to
// ignore them until registration.
type FragmentBuilder struct {
	fragment *Fragment
	errs     []error
	// declared holds the slots WithSlot named, as opposed to those a binding
	// declared on its own, which WithSlot may still constrain.
	declared map[string]bool
}

// NewFragment starts a FragmentBuilder for a fragment named name that renders the
// template at templatePath.
func NewFragment(name, templatePath string) *FragmentBuilder {
	return &FragmentBuilder{
		fragment: &Fragment{
			Name:         name,
			TemplatePath: templatePath,
			Slots:        make(map[string]*SlotDefinition),
		},
	}
}

// WithDataHandler sets the fragment's data handler. A page rendering a fragment
// with a handler, and declaring no strategy, is dynamic.
//
// A value several handlers need is fetched once through Once, within one render,
// or Cached, across renders: every fragment path is a render of its own, so
// fragments refreshed separately share a fetch only through Cached.
func (b *FragmentBuilder) WithDataHandler(h DataHandlerFunc) *FragmentBuilder {
	b.fragment.DataHandler = h
	return b
}

// WithData hands the fragment's template v on every render — for data fixed when
// the program starts, a list of links or a heading — with no function to write:
//
//	collage.NewFragment("home-content", "pages/home.html").
//		WithData(homeView{Links: links}).
//		Build()
//
// Unlike a data handler, fixed data leaves a page that declares no strategy
// static. Data that changes while the program runs wants Load, or DataHandler to
// report what it came from. Setting both is ErrConflictingData at registration.
func (b *FragmentBuilder) WithData(v any) *FragmentBuilder { // any: fragment data is opaque to the framework and flows straight into the template engine
	b.fragment.Data = v
	return b
}

// WithTitle declares the page's <title>, as rc.HoistTitle would, without a data
// handler — so a layout can name the site and still leave its pages static:
//
//	collage.NewFragment("layout", "layouts/default.html").
//		WithTitle("My site").
//		Build()
//
// The innermost declaration wins, so a page's content declaring its own title
// replaces the layout's.
func (b *FragmentBuilder) WithTitle(title string) *FragmentBuilder {
	b.fragment.Title = title
	return b
}

// DataHandler adapts a data handler that returns a concrete type to
// DataHandlerFunc, so an application's handlers can be written against its own
// view types rather than against any:
//
//	collage.NewFragment("clock", "fragments/clock.html").
//		WithDataHandler(collage.DataHandler(clockData)).
//		Build()
//
// where clockData returns (clockView, []string, error).
//
// It is a function rather than a method because Go methods cannot take type
// parameters. On an error the data is dropped rather than boxed: a nil *view
// returned alongside an error would otherwise become a non-nil interface value, a
// typed nil that reads as present. A nil fn is a nil handler.
func DataHandler[T any](fn func(context.Context, *RenderContext) (T, []string, error)) DataHandlerFunc {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: restates DataHandlerFunc's own declaration
		data, tags, err := fn(ctx, rc)
		if err != nil {
			return nil, tags, err
		}
		return data, tags, nil
	}
}

// Load adapts a data handler that fetches its data but reports no dependency
// tags to DataHandlerFunc — DataHandler without the tags, for a page that is not
// cached or whose data does not change:
//
//	collage.NewFragment("clock", "fragments/clock.html").
//		WithDataHandler(collage.Load(func(ctx context.Context, rc *collage.RenderContext) (clockView, error) {
//			return clockView{Now: time.Now()}, nil
//		})).
//		Build()
//
// A handler whose page is cached and whose data changes — a post, a count —
// should report that data's tags, which is DataHandler's shape. As with
// DataHandler, the data is dropped on an error. A nil fn is a nil handler.
func Load[T any](fn func(context.Context, *RenderContext) (T, error)) DataHandlerFunc {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: restates DataHandlerFunc's own declaration
		data, err := fn(ctx, rc)
		if err != nil {
			return nil, nil, err
		}
		return data, nil, nil
	}
}

// Effect adapts a data handler that renders nothing to DataHandlerFunc: one that
// only declares things for the page, through rc.HoistTitle or a plugin's Emit.
//
//	collage.NewFragment("seo", "fragments/seo.html").
//		WithDataHandler(collage.Effect(func(ctx context.Context, rc *collage.RenderContext) error {
//			rc.HoistTitle(post.Title)
//			return nil
//		}))
//
// The fragment's template receives no data, and it reports no dependency tags: a
// handler whose declarations come from data that changes — a post's title — and
// whose page is cached should return that data's tags, which is DataHandler's
// shape, or the page should declare them with WithDependency. A nil fn is a nil
// handler.
func Effect(fn func(context.Context, *RenderContext) error) DataHandlerFunc {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context, rc *RenderContext) (any, []string, error) { // any: restates DataHandlerFunc's own declaration
		return nil, nil, fn(ctx, rc)
	}
}

// WithSlot constrains the slot named name on the fragment being built: required,
// the render fails with nothing bound to it; not allowMultiple, it holds one
// fragment at most.
//
// A slot needs no declaring otherwise. Its template calling {{slot "name"}} is
// enough to render what is bound there, and WithSlotFragment or WithSlotResolver
// bind into a slot whether or not it was declared — as an optional one, open to
// any number of fragments. WithSlot may come before or after them.
//
// Naming a slot WithSlot already named records ErrDuplicateSlot, retrievable via
// BuildErr, and leaves the slot untouched. Constraining a slot to one fragment
// that already has more records ErrSlotOccupied.
func (b *FragmentBuilder) WithSlot(name string, required, allowMultiple bool) *FragmentBuilder {
	if b.declared[name] {
		b.errs = append(b.errs, fmt.Errorf("%w: %q on fragment %q", ErrDuplicateSlot, name, b.fragment.Name))
		return b
	}
	if b.declared == nil {
		b.declared = make(map[string]bool)
	}
	b.declared[name] = true

	slot, bound := b.fragment.Slots[name]
	if !bound {
		slot = &SlotDefinition{Name: name}
		b.fragment.Slots[name] = slot
	}
	if !allowMultiple && len(slot.Fill) > 1 {
		b.errs = append(b.errs, fmt.Errorf("%w: %q on fragment %q has %d fragments bound", ErrSlotOccupied, name, b.fragment.Name, len(slot.Fill)))
	}
	slot.Required = required
	slot.AllowMultiple = allowMultiple
	return b
}

// WithSlotFragment binds child into the slot named slotName via Fragment.Bind,
// declaring the slot if nothing has, and recording any error Bind returns
// (ErrNilFragment, ErrSlotResolved, or ErrSlotOccupied) on the builder rather than
// returning it.
func (b *FragmentBuilder) WithSlotFragment(slotName string, child *Fragment) *FragmentBuilder {
	if err := b.fragment.Bind(slotName, child); err != nil {
		b.errs = append(b.errs, err)
	}
	return b
}

// WithSlotResolver fills the slot named slotName per render rather than at build
// time: resolve returns the fragments it holds for one request.
//
//	sections := collage.NewFragment("sections", "pages/sections.html").
//		WithDataHandler(collage.DataHandler(loadSections)). // puts the section list in SharedData
//		WithSlotResolver("sections", func(rc *collage.RenderContext) ([]*collage.Fragment, error) {
//			list, _ := rc.Get("sections")
//			return fragmentsFor(list), nil
//		}).
//		Build()
//
// It is for a page whose parts come from content — a CMS's blocks in the order an
// editor chose — so adding, removing or reordering them needs no restart and no
// guard against the code's order disagreeing with the data's.
//
// The resolver runs after this fragment's own data handler, so it can read what
// that handler fetched, and before the fragments it returns start theirs, which
// still run concurrently. What it returns is held to the slot's own rules — one
// fragment unless it allows multiple, at least one if it is required — when the
// page renders; a slot nothing declared is optional and open to any number. A slot
// is filled either by a resolver or by WithSlotFragment, never both; mixing them
// records ErrSlotResolved.
func (b *FragmentBuilder) WithSlotResolver(slotName string, resolve SlotResolverFunc) *FragmentBuilder {
	slot, ok := b.fragment.Slots[slotName]
	if !ok && slotName != "" {
		slot = &SlotDefinition{Name: slotName, AllowMultiple: true}
		b.fragment.Slots[slotName] = slot
	}
	switch {
	case slotName == "":
		b.errs = append(b.errs, fmt.Errorf("%w: empty slot name on fragment %q", ErrInvalidSlotDefinition, b.fragment.Name))
	case resolve == nil:
		b.errs = append(b.errs, fmt.Errorf("collage: nil slot resolver for %q on fragment %q", slotName, b.fragment.Name))
	case len(slot.Fill) > 0:
		b.errs = append(b.errs, fmt.Errorf("%w: %q on fragment %q already has fragments bound", ErrSlotResolved, slotName, b.fragment.Name))
	default:
		slot.Resolve = resolve
	}
	return b
}

// Static states that the fragment's data handler returns the same for every
// request to one URL, so it does not make a page that declares no strategy
// dynamic — for a fragment many pages share whose data is fixed per URL:
//
//	collage.NewFragment("more-recipes", "fragments/more-recipes.html").
//		WithDataHandler(loadMore). // reads the recipes and rc.Param("slug")
//		Static().
//		Build()
//
// It is the fragment's half of what PageBuilder.Static says for a whole page, and
// the same promise: the handler reads path parameters and the locale, which the
// cache key and a static build both carry, and nothing else a request brings — a
// cookie, a header, the clock. A handler that breaks the promise serves one
// reader's render to the next.
//
// A page's strategy is still its own: Dynamic() on a page is kept whatever its
// fragments say, and a page with another fragment's handler, or a slot resolver,
// is still dynamic unless it says otherwise.
func (b *FragmentBuilder) Static() *FragmentBuilder {
	b.fragment.Static = true
	return b
}

// Shared states that the fragment's data handler returns the same for every
// reader at one moment — it reads no cookie, no session, no header, nothing that
// tells one reader from another — though what it returns changes over time:
//
//	collage.NewFragment("cpu", "fragments/cpu.html").
//		WithDataHandler(cpuUsage). // a measurement, the same for everyone
//		Shared().
//		Build()
//
// A render of such a fragment can be made once and sent to every reader, which is
// what a plugin pushing fragments over a stream does with it (see
// FragmentRender.Shared): one render per change rather than one per open tab.
//
// It is the half of Static that says nothing about time. Static promises the
// handler depends on the URL and nothing else, so a page of Static fragments is
// cached and exported; a measurement is not that, and marking it Static would
// have the page cached, and a build write one moment's reading into the HTML.
// Shared leaves the page's strategy alone: a page with a Shared fragment's handler
// is dynamic unless it declares otherwise. Static implies Shared.
//
// A handler that breaks the promise sends one reader's render to another reader.
// Only mark a fragment Shared when its handler reads nothing of the request that
// could differ between readers.
func (b *FragmentBuilder) Shared() *FragmentBuilder {
	b.fragment.Shared = true
	return b
}

// Required marks the fragment being built as required: a failed render of it must
// fail the page render rather than falling back to its Fallback fragment.
func (b *FragmentBuilder) Required() *FragmentBuilder {
	b.fragment.Required = true
	return b
}

// WithFallback sets the fragment rendered in place of this one when this fragment's
// render fails.
func (b *FragmentBuilder) WithFallback(f *Fragment) *FragmentBuilder {
	b.fragment.Fallback = f
	return b
}

// WithTimeout sets the duration that bounds how long the fragment's DataHandler may
// run. A negative d records ErrInvalidTimeout, retrievable via BuildErr, and leaves
// the fragment's Timeout unchanged.
func (b *FragmentBuilder) WithTimeout(d time.Duration) *FragmentBuilder {
	if d < 0 {
		b.errs = append(b.errs, fmt.Errorf("%w: fragment %q timeout %s", types.ErrInvalidTimeout, b.fragment.Name, d))
		return b
	}
	b.fragment.Timeout = d
	return b
}

// Build returns the Fragment constructed so far. It never panics and never returns
// nil for a non-nil builder, even if WithX calls recorded errors along the way; call
// BuildErr to check whether any were recorded.
func (b *FragmentBuilder) Build() *Fragment {
	types.RecordBuildErr(b.fragment, b.BuildErr())
	return b.fragment
}

// BuildErr returns the errors accumulated by prior WithX calls, joined with
// errors.Join, or nil if none were recorded.
func (b *FragmentBuilder) BuildErr() error {
	return errors.Join(b.errs...)
}
