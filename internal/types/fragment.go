package types

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// DataHandlerFunc fetches the data a fragment renders with. It returns the template
// data, the dependency tags the data was derived from, and an error.
type DataHandlerFunc func(ctx context.Context, rc *RenderContext) (data any, tags []string, err error) // any: html/template renders arbitrary data

// SlotDefinition declares a named position inside a fragment's template and the
// fragments bound to it.
type SlotDefinition struct {
	// Name is the slot's identifier, matched by {{slot "name"}} in the template.
	Name string
	// Required reports whether rendering fails when no fragment is bound.
	Required bool
	// AllowMultiple reports whether more than one fragment may be bound. When false,
	// binding a second fragment is a validation error.
	AllowMultiple bool
	// Fill holds the fragments bound to this slot, rendered in binding order.
	Fill []*Fragment
}

// Fragment is the framework's unit of composition: a template, an optional data
// contract, and the slots it exposes to child fragments.
type Fragment struct {
	// Name identifies the fragment, primarily for diagnostics and error messages.
	Name string
	// TemplatePath is the path to the fragment's template file.
	TemplatePath string
	// DataHandler fetches the data this fragment renders with. A nil DataHandler
	// means the fragment renders with no data.
	DataHandler DataHandlerFunc
	// Slots declares the named positions this fragment exposes to child fragments,
	// keyed by slot name. Each key must equal its SlotDefinition's own Name field.
	Slots map[string]*SlotDefinition
	// Required reports whether a failed render of this fragment must fail the page
	// render rather than falling back to Fallback. Enforcement lives in the render
	// pipeline, outside this package.
	Required bool
	// Fallback is the fragment rendered in place of this one when this fragment's
	// render fails.
	Fallback *Fragment
	// Timeout bounds how long this fragment's DataHandler may run. Zero means no
	// fragment-specific timeout; see EffectiveTimeout.
	Timeout time.Duration
}

// Slot looks up the slot named name on f. It is nil-safe: a nil receiver or a
// fragment with no matching slot both report ok == false.
func (f *Fragment) Slot(name string) (*SlotDefinition, bool) {
	if f == nil {
		return nil, false
	}
	s, ok := f.Slots[name]
	return s, ok
}

// SlotNames returns the names of f's declared slots in sorted order, for
// deterministic iteration and error messages. It is nil-safe.
func (f *Fragment) SlotNames() []string {
	if f == nil {
		return nil
	}
	names := make([]string, 0, len(f.Slots))
	for name := range f.Slots {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Bind appends child to the slot named slotName's Fill. It returns ErrNilFragment if
// child is nil, ErrUnknownSlot wrapped with slotName if f declares no such slot, and
// ErrSlotOccupied wrapped with slotName if the slot already has a fill and does not
// allow multiple.
func (f *Fragment) Bind(slotName string, child *Fragment) error {
	if child == nil {
		return ErrNilFragment
	}
	if f == nil {
		return ErrNilFragment
	}
	slot, ok := f.Slots[slotName]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSlot, slotName)
	}
	if len(slot.Fill) > 0 && !slot.AllowMultiple {
		return fmt.Errorf("%w: %q", ErrSlotOccupied, slotName)
	}
	slot.Fill = append(slot.Fill, child)
	return nil
}

// EffectiveTimeout returns f.Timeout when it is greater than zero, otherwise def. It
// is nil-safe, returning def for a nil receiver.
func (f *Fragment) EffectiveTimeout(def time.Duration) time.Duration {
	if f != nil && f.Timeout > 0 {
		return f.Timeout
	}
	return def
}

// Validate reports whether f and every fragment reachable from it (through Fill and
// Fallback) form a well-formed tree: Name is non-empty (ErrEmptyName), TemplatePath
// is non-empty (ErrEmptyTemplatePath), Timeout is not negative (ErrInvalidTimeout),
// every slot's map key equals its SlotDefinition.Name and no slot name is empty
// (ErrInvalidSlotDefinition), and every slot declared Required has at least one fill
// (ErrRequiredSlotUnfilled). It returns ErrNilFragment for a nil receiver and
// ErrFragmentCycle, with the offending fragment's name, if the tree is not acyclic.
// It returns nil for a valid tree.
func (f *Fragment) Validate() error {
	return f.validate(make(map[*Fragment]bool))
}

// validate implements Validate's recursion. stack tracks the fragments currently on
// the recursion path (added on entry, removed on exit via defer), not a global
// visited set — this lets a diamond (two slots binding the same child fragment) pass
// while still catching a genuine cycle such as A -> B -> A.
func (f *Fragment) validate(stack map[*Fragment]bool) error {
	if f == nil {
		return ErrNilFragment
	}
	if stack[f] {
		return fmt.Errorf("%w: %s", ErrFragmentCycle, f.Name)
	}
	stack[f] = true
	defer delete(stack, f)

	if f.Name == "" {
		return ErrEmptyName
	}
	if f.TemplatePath == "" {
		return ErrEmptyTemplatePath
	}
	if f.Timeout < 0 {
		return fmt.Errorf("%w: fragment %q has negative timeout", ErrInvalidTimeout, f.Name)
	}

	for _, key := range f.SlotNames() {
		slot := f.Slots[key]
		if slot == nil || slot.Name == "" {
			return fmt.Errorf("%w: slot key %q has no name", ErrInvalidSlotDefinition, key)
		}
		if slot.Name != key {
			return fmt.Errorf("%w: slot key %q does not match slot name %q", ErrInvalidSlotDefinition, key, slot.Name)
		}
		if slot.Required && len(slot.Fill) == 0 {
			return fmt.Errorf("%w: required slot %q has no fill", ErrRequiredSlotUnfilled, slot.Name)
		}
		for _, child := range slot.Fill {
			if err := child.validate(stack); err != nil {
				return err
			}
		}
	}

	if f.Fallback != nil {
		if err := f.Fallback.validate(stack); err != nil {
			return err
		}
	}

	return nil
}
