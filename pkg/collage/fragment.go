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

// WithDataHandler sets the fragment's data handler.
func (b *FragmentBuilder) WithDataHandler(h DataHandlerFunc) *FragmentBuilder {
	b.fragment.DataHandler = h
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

// Data is a data handler that hands the fragment's template v on every render —
// for data fixed when the program starts, a list of links or a heading:
//
//	collage.NewFragment("home-content", "pages/home.html").
//		WithDataHandler(collage.Data(homeView{Links: links})).
//		Build()
//
// It reports no dependency tags: data that changes while the program runs wants
// Load, or DataHandler to report what it came from.
func Data[T any](v T) DataHandlerFunc {
	return func(context.Context, *RenderContext) (any, []string, error) { // any: restates DataHandlerFunc's own declaration
		return v, nil, nil
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

// WithSlot declares a slot named name on the fragment being built. Declaring a slot
// under a name already declared on this fragment records ErrDuplicateSlot, retrievable
// via BuildErr, and leaves the existing slot untouched.
func (b *FragmentBuilder) WithSlot(name string, required, allowMultiple bool) *FragmentBuilder {
	if _, exists := b.fragment.Slots[name]; exists {
		b.errs = append(b.errs, fmt.Errorf("%w: %q on fragment %q", ErrDuplicateSlot, name, b.fragment.Name))
		return b
	}
	b.fragment.Slots[name] = &SlotDefinition{
		Name:          name,
		Required:      required,
		AllowMultiple: allowMultiple,
	}
	return b
}

// WithSlotFragment binds child into the slot named slotName via Fragment.Bind,
// recording any error Bind returns (ErrNilFragment, ErrUnknownSlot, or
// ErrSlotOccupied) on the builder rather than returning it.
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
//		WithSlot("sections", false, true).
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
// page renders. Declare the slot first with WithSlot. A slot is filled either by
// a resolver or by WithSlotFragment, never both; mixing them records
// ErrSlotResolved.
func (b *FragmentBuilder) WithSlotResolver(slotName string, resolve SlotResolverFunc) *FragmentBuilder {
	slot, ok := b.fragment.Slots[slotName]
	switch {
	case !ok:
		b.errs = append(b.errs, fmt.Errorf("%w: %q on fragment %q", ErrUnknownSlot, slotName, b.fragment.Name))
	case resolve == nil:
		b.errs = append(b.errs, fmt.Errorf("collage: nil slot resolver for %q on fragment %q", slotName, b.fragment.Name))
	case len(slot.Fill) > 0:
		b.errs = append(b.errs, fmt.Errorf("%w: %q on fragment %q already has fragments bound", ErrSlotResolved, slotName, b.fragment.Name))
	default:
		slot.Resolve = resolve
	}
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
