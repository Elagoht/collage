package render

import (
	"bytes"
	"context"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// renderState is everything one Render call accumulates while it walks the fragment
// tree. It needs no locking: the walk is strictly sequential and depth-first, so
// exactly one goroutine ever touches a given renderState.
type renderState struct {
	// page is the name of the page being rendered, for metrics and error messages.
	page string
	// stack holds the names of the fragments currently being rendered, outermost
	// first. It doubles as the depth counter and as the chain named by
	// ErrMaxDepthExceeded.
	stack []string
	// tags is the set of dependency tags collected so far.
	tags map[string]struct{}
	// fragments holds the per-fragment metadata, indexed in entry order.
	fragments []FragmentMetadata
	// dataTime is the total time spent in data handlers. Handlers never nest, so
	// this is a plain sum.
	dataTime time.Duration
	// templateTime is the total time spent executing templates, counting each
	// fragment's template only for itself: the time its descendants spend inside
	// that execution is subtracted, so nesting does not inflate the total.
	templateTime time.Duration
	// childTotal accumulates the total duration of the fragments that have finished
	// since the current fragment started, so a fragment can subtract its
	// descendants' time from its own template execution. Each fragment replaces its
	// descendants' contribution with its own on the way out.
	childTotal time.Duration
}

// addTags records tags in the render's tag set, ignoring empty ones.
func (s *renderState) addTags(tags []string) {
	for _, tag := range tags {
		if tag != "" {
			s.tags[tag] = struct{}{}
		}
	}
}

// sortedTags returns the de-duplicated, sorted union of the tags collected during the
// render and pageTags.
func (s *renderState) sortedTags(pageTags []string) []string {
	s.addTags(pageTags)
	tags := make([]string, 0, len(s.tags))
	for tag := range s.tags {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// chain renders the fragment stack as a readable path, for ErrMaxDepthExceeded.
func (s *renderState) chain() string {
	return strings.Join(s.stack, " > ")
}

// renderFragment renders one fragment and everything bound into its slots, and
// applies the per-fragment failure policy to the outcome:
//
//	Required            -> the error propagates and the whole render fails.
//	optional + fallback -> the fallback renders; if the fallback fails too, the
//	                       fragment emits nothing and the page still succeeds.
//	optional, no fallback -> the fragment emits nothing and the page still succeeds.
//
// Either way the failure is recorded in the fragment's metadata, which is what makes
// Result.Degraded true, and any tags the fragment's handler had already produced are
// kept: a fragment that failed halfway still describes what its output depended on.
func (e *SlotEngine) renderFragment(rc *types.RenderContext, f *types.Fragment, state *renderState) ([]byte, error) {
	if f == nil {
		// Fatal for the same reason as the depth limit: a nil entry in a Fill slice
		// is a malformed tree, and rendering the page around the hole would hide it.
		return nil, fatal(fmt.Errorf("%w: page %q below %q", types.ErrNilFragment, state.page, state.chain()))
	}

	state.stack = append(state.stack, f.Name)
	defer func() { state.stack = state.stack[:len(state.stack)-1] }()

	if len(state.stack) > e.maxDepth {
		// Fatal rather than absorbable: hitting the depth limit means the tree is
		// malformed — almost always a fragment bound into one of its own slots —
		// and quietly rendering the page without that subtree would hide it.
		return nil, fatal(fmt.Errorf("%w: limit %d reached at %s", ErrMaxDepthExceeded, e.maxDepth, state.chain()))
	}

	ctx, span := e.tracer.StartSpan(rc.Context(), "collage.fragment")
	defer span.End()
	span.SetAttribute("fragment", f.Name)
	rc = rc.WithContext(ctx)

	// The metadata slot is reserved on entry so parents appear before the children
	// that render inside them, and filled in once the outcome is known.
	index := len(state.fragments)
	state.fragments = append(state.fragments, FragmentMetadata{Name: f.Name})

	start := time.Now()
	childTotalBefore := state.childTotal

	out, err := e.attempt(rc, f, state)
	meta := FragmentMetadata{Name: f.Name}
	propagate := false

	if err != nil {
		meta.Failed = true
		meta.Err = err
		out = nil

		switch {
		case f.Required || isFatal(err):
			propagate = true
		case f.Fallback != nil:
			fallbackOut, fallbackErr := e.attempt(rc, f.Fallback, state)
			if fallbackErr != nil {
				// A fallback exists to contain a failure, so its own failure is
				// contained here rather than escalated: the page loses this
				// fragment's output and records both errors.
				meta.Err = fmt.Errorf("%w: fallback %q also failed: %w", err, f.Fallback.Name, fallbackErr)
			} else {
				out = fallbackOut
				meta.UsedFallback = true
			}
		}

		if !propagate && len(out) == 0 && e.devMode {
			out = devComment(f.Name, meta.Err)
		}
	}

	total := time.Since(start)
	meta.Duration = total
	state.childTotal = childTotalBefore + total
	state.fragments[index] = meta

	e.metrics.FragmentDuration(rc.Context(), state.page, f.Name, total, meta.Err)
	if meta.Err != nil {
		span.RecordError(meta.Err)
	}

	if propagate {
		return nil, fatal(err)
	}
	return out, nil
}

// attempt renders one fragment's data and template with no failure policy applied: it
// runs the data handler, verifies the fragment's required slots are filled, and
// executes the template with a slot function bound to this fragment. It is also how a
// fallback is rendered, which is precisely why the policy lives in renderFragment
// instead — a fallback must not get a fallback of its own, and a required fragment
// inside a fallback must not escalate past it.
func (e *SlotEngine) attempt(rc *types.RenderContext, f *types.Fragment, state *renderState) ([]byte, error) {
	var data any // any: fragment data is opaque to the framework and flows straight into the template engine, whose parameter is already any
	if f.DataHandler != nil {
		var tags []string
		started := time.Now()
		err := Execute(rc.Context(), f.EffectiveTimeout(e.defaultTimeout), func(ctx context.Context) error {
			var handlerErr error
			data, tags, handlerErr = f.DataHandler(ctx, rc.WithContext(ctx))
			return handlerErr
		})
		state.dataTime += time.Since(started)
		// Tags first, error second: a handler that resolved what it depends on and
		// then failed has still told us what would invalidate this page.
		state.addTags(tags)
		if err != nil {
			return nil, wrapFragment("data handler", f.Name, err)
		}
	}

	if err := requiredSlotsFilled(f); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	childTotalBefore := state.childTotal
	started := time.Now()
	// Template execution goes through Execute as well: a template function can
	// panic, and the fragment that owns it should fail rather than the process. No
	// timeout is imposed here — Fragment.Timeout bounds the data handler, which is
	// the part that talks to the outside world — but the render's context still
	// applies.
	err := Execute(rc.Context(), 0, func(ctx context.Context) error {
		return e.tmpl.RenderWithFuncs(ctx, &buf, f.TemplatePath, data, e.slotFuncs(rc, f, state))
	})
	state.templateTime += time.Since(started) - (state.childTotal - childTotalBefore)
	if err != nil {
		return nil, wrapFragment("template "+f.TemplatePath, f.Name, err)
	}
	return buf.Bytes(), nil
}

// slotFuncs builds the FuncMap for one fragment's template execution. The slot
// function closes over that one fragment, so {{slot "name"}} always means "this
// fragment's slot", and the template engine applies the map to a clone of the parsed
// template set — concurrent renders therefore never see each other's slot function.
//
// It can only rebind a function name the templates were parsed with; "slot" is
// registered as a placeholder at parse time precisely so this override has a name to
// take over.
func (e *SlotEngine) slotFuncs(rc *types.RenderContext, f *types.Fragment, state *renderState) htmltemplate.FuncMap {
	return htmltemplate.FuncMap{
		"slot": func(name string) (htmltemplate.HTML, error) {
			return e.renderSlot(rc, f, name, state)
		},
	}
}

// renderSlot renders every fragment bound to f's slot named name, in binding order,
// and returns the concatenation as template.HTML so the children's markup is not
// escaped a second time on its way into the parent.
//
// A name f does not declare is an error, not empty output: silently rendering nothing
// would turn a typo in a template into a section that is simply missing from the
// page, which nobody notices until a user does.
func (e *SlotEngine) renderSlot(rc *types.RenderContext, f *types.Fragment, name string, state *renderState) (htmltemplate.HTML, error) {
	slot, ok := f.Slot(name)
	if !ok {
		return "", fmt.Errorf("%w: fragment %q has no slot %q, only %v", types.ErrUnknownSlot, f.Name, name, f.SlotNames())
	}

	var buf strings.Builder
	for _, child := range slot.Fill {
		out, err := e.renderFragment(rc, child, state)
		if err != nil {
			return "", err
		}
		buf.Write(out)
	}
	return htmltemplate.HTML(buf.String()), nil
}

// requiredSlotsFilled reports ErrRequiredSlotEmpty for the first required slot of f
// with nothing bound to it. It runs before the template rather than inside the slot
// function so that a required slot is enforced even when the template never asks for
// it, and it walks the slots in sorted order so the same tree always names the same
// slot.
func requiredSlotsFilled(f *types.Fragment) error {
	for _, name := range f.SlotNames() {
		slot := f.Slots[name]
		if slot != nil && slot.Required && len(slot.Fill) == 0 {
			return fmt.Errorf("%w: fragment %q slot %q", ErrRequiredSlotEmpty, f.Name, name)
		}
	}
	return nil
}

// devComment renders a failed fragment's error as an HTML comment, used in DevMode in
// place of the empty output a failure would otherwise produce. The error text is
// HTML-escaped, which also makes it impossible for it to close the comment early: a
// comment can only end at a ">", and there are none left after escaping.
func devComment(name string, err error) []byte {
	return []byte("<!-- collage: fragment " +
		htmltemplate.HTMLEscapeString(name) +
		" failed: " +
		htmltemplate.HTMLEscapeString(err.Error()) +
		" -->")
}
