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
	return b.fragment
}

// BuildErr returns the errors accumulated by prior WithX calls, joined with
// errors.Join, or nil if none were recorded.
func (b *FragmentBuilder) BuildErr() error {
	return errors.Join(b.errs...)
}
