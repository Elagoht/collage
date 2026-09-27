package types

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
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
	// Resolve, when set, decides the slot's fragments per render instead of Fill:
	// a page whose sections come from content — a CMS's list of blocks, in the
	// order an editor chose — rather than from the code. It runs after its own
	// fragment's data handler, so it can read what that handler put in SharedData,
	// and before the fragments it returns start their own handlers, which still
	// run concurrently. A slot has either Fill or Resolve, never both.
	Resolve SlotResolverFunc
}

// SlotResolverFunc returns the fragments a slot holds for one render.
type SlotResolverFunc func(rc *RenderContext) ([]*Fragment, error)

// Fragment is the framework's unit of composition: a template, an optional data
// contract, and the slots it exposes to child fragments.
type Fragment struct {
	// Name identifies the fragment, primarily for diagnostics and error messages.
	Name string
	// TemplatePath is the path to the fragment's template file.
	TemplatePath string
	// Source, when set, is the fragment's template itself rather than the path
	// to a file holding it: an inline fragment. A fragment has exactly one of
	// TemplatePath and Source. See TemplateName for how the engine addresses it.
	Source string
	// DataHandler fetches the data this fragment renders with. A nil DataHandler
	// means the fragment renders with Data.
	DataHandler DataHandlerFunc
	// Data is what the fragment's template renders with when it has no
	// DataHandler: data fixed when the program starts. Unlike a handler, it does
	// not make a page with no declared strategy dynamic. Setting both is
	// ErrConflictingData.
	Data any // any: fragment data is opaque to the framework and flows straight into the template engine
	// Title, when set, declares the page's <title> as rc.HoistTitle would, before
	// the fragment's DataHandler runs — so a handler of the same fragment that
	// hoists a title of its own replaces it. Like Data, it is fixed, and does not
	// make a page dynamic.
	Title string
	// Guard, when set, is asked whether a request may reach this fragment
	// before anything is served: before the page's cache is read, before its
	// render, before an action on the page's own URL runs. It runs for every
	// page whose spine this fragment is on — a layout in the page's layout
	// chain, or the page's content fragment — and for a fragment path opened
	// on this fragment's own URL, where it is the fragment's whole policy.
	// On any other fragment it is ignored: access policy belongs to routes,
	// not to rendering parts. See GuardFunc.
	Guard GuardFunc
	// Static states that the fragment's DataHandler returns the same for every
	// request to one URL: what it renders depends on the path's parameters and the
	// locale, which are part of a cache key, and on nothing a request carries
	// beyond them — no cookie, no header, no clock. Such a handler does not make a
	// page that declares no strategy dynamic. It says nothing about slot resolvers:
	// the fragments a resolver returns are not known until a render asks for them.
	Static bool
	// Shared states that the fragment's DataHandler returns the same for every
	// reader at one moment: it reads no cookie, no session, no header — nothing
	// that tells one reader from another — though what it returns may change from
	// one moment to the next. A render of it may be made once and sent to many
	// readers. Unlike Static it says nothing about time, so it leaves a page's
	// strategy alone. Static implies it.
	Shared bool
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
	// buildErr is what the builder that made this value recorded; see
	// RecordBuildErr. Registration refuses a value that carries one.
	buildErr error
}

// IsInline reports whether f carries its template as Source rather than naming a
// file. It is nil-safe.
func IsInline(f *Fragment) bool { return f != nil && f.Source != "" }

// TemplateName is the name the template engine knows f's template by: its
// TemplatePath, or for an inline fragment "inline:<name>#<hash>". The hash is of
// the source, so two fragments that share a name but not a template never collide,
// and one template used by two fragment values is parsed once. It is nil-safe.
func TemplateName(f *Fragment) string {
	if f == nil {
		return ""
	}
	if f.Source == "" {
		return f.TemplatePath
	}
	sum := sha256.Sum256([]byte(f.Source))
	return inlinePrefix + f.Name + "#" + hex.EncodeToString(sum[:8])
}

// inlinePrefix begins every inline template's name; TemplateName writes it and
// HumanizeTemplateNames reads it back.
const inlinePrefix = "inline:"

// HumanizeTemplateNames rewrites every inline template name in s — what the
// template engine prints in its own errors — as `inline template of fragment
// "name"`, so a message names the fragment someone wrote rather than a hash
// nobody did. Anything that is not an inline name is left as it is.
func HumanizeTemplateNames(s string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, inlinePrefix)
		if start < 0 {
			b.WriteString(s)
			return b.String()
		}
		end, name, ok := inlineNameAt(s, start)
		if !ok {
			b.WriteString(s[:start+len(inlinePrefix)])
			s = s[start+len(inlinePrefix):]
			continue
		}
		// A name the engine quoted is replaced quotes and all, so the result
		// does not nest one pair of quotes inside another.
		if start > 0 && s[start-1] == '"' && end < len(s) && s[end] == '"' {
			start--
			end++
		}
		b.WriteString(s[:start])
		b.WriteString(`inline template of fragment "` + name + `"`)
		s = s[end:]
	}
}

// inlineNameAt reads the inline template name starting at s[start]: the prefix,
// the fragment name, "#" and the sixteen hex digits TemplateName writes. It
// reports the end of the name and the fragment name.
func inlineNameAt(s string, start int) (end int, name string, ok bool) {
	rest := s[start+len(inlinePrefix):]
	hash := strings.IndexByte(rest, '#')
	if hash < 0 || len(rest) < hash+1+16 {
		return 0, "", false
	}
	for _, c := range rest[hash+1 : hash+1+16] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return 0, "", false
		}
	}
	return start + len(inlinePrefix) + hash + 1 + 16, rest[:hash], true
}

// humanizedError is an error whose message has had its inline template names
// rewritten, and which still unwraps to what it was built from.
type humanizedError struct{ err error }

func (h humanizedError) Error() string { return HumanizeTemplateNames(h.err.Error()) }
func (h humanizedError) Unwrap() error { return h.err }

// HumanizeTemplateError wraps err so its message names inline templates by their
// fragments (see HumanizeTemplateNames), keeping err reachable through
// errors.Is and errors.As. A nil err stays nil.
func HumanizeTemplateError(err error) error {
	if err == nil {
		return nil
	}
	return humanizedError{err: err}
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

// Bind appends child to the slot named slotName's Fill, declaring the slot first —
// optional, and open to any number of fragments — when f does not declare it: a
// slot needs declaring only to be required or to hold one fragment at most. It
// returns ErrNilFragment if child is nil, ErrInvalidSlotDefinition for an empty
// slotName, ErrSlotResolved if a resolver fills the slot, and ErrSlotOccupied
// wrapped with slotName if the slot already has a fill and does not allow multiple.
func (f *Fragment) Bind(slotName string, child *Fragment) error {
	if child == nil {
		return ErrNilFragment
	}
	if f == nil {
		return ErrNilFragment
	}
	if slotName == "" {
		return fmt.Errorf("%w: empty slot name", ErrInvalidSlotDefinition)
	}
	slot, ok := f.Slots[slotName]
	if !ok {
		if f.Slots == nil {
			f.Slots = make(map[string]*SlotDefinition)
		}
		slot = &SlotDefinition{Name: slotName, AllowMultiple: true}
		f.Slots[slotName] = slot
	}
	if slot.Resolve != nil {
		return fmt.Errorf("%w: %q", ErrSlotResolved, slotName)
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
	switch {
	case f.TemplatePath == "" && f.Source == "":
		return ErrEmptyTemplatePath
	case f.TemplatePath != "" && f.Source != "":
		return fmt.Errorf("%w: fragment %q", ErrConflictingTemplate, f.Name)
	}
	if f.Timeout < 0 {
		return fmt.Errorf("%w: fragment %q has negative timeout", ErrInvalidTimeout, f.Name)
	}
	if f.DataHandler != nil && f.Data != nil {
		return fmt.Errorf("%w: fragment %q", ErrConflictingData, f.Name)
	}

	for _, key := range f.SlotNames() {
		slot := f.Slots[key]
		if slot == nil || slot.Name == "" {
			return fmt.Errorf("%w: slot key %q has no name", ErrInvalidSlotDefinition, key)
		}
		if slot.Name != key {
			return fmt.Errorf("%w: slot key %q does not match slot name %q", ErrInvalidSlotDefinition, key, slot.Name)
		}
		// A resolved slot's fragments are not known until a render asks for them;
		// the render checks them instead.
		if slot.Required && len(slot.Fill) == 0 && slot.Resolve == nil {
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
