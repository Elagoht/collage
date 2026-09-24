package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/template"
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
	// dataTime is the total time spent in data handlers, summed. Sibling handlers
	// now overlap, so this can exceed the render's wall-clock time: it is how much
	// handler work the page cost, not how long the page waited for it.
	dataTime time.Duration
	// order numbers fragments in the sequence they are launched. Launching happens
	// on this goroutine, in declaration order, even though the handlers launched
	// run concurrently — so the number is the same on every render of a page, and
	// hoisting can use it to settle ties without depending on which goroutine won.
	order int
	// templateTime is the total time spent executing templates, counting each
	// fragment's template only for itself: the time its descendants spend inside
	// that execution is subtracted, so nesting does not inflate the total.
	templateTime time.Duration
	// childTotal accumulates the total duration of the fragments that have finished
	// since the current fragment started, so a fragment can subtract its
	// descendants' time from its own template execution. Each fragment replaces its
	// descendants' contribution with its own on the way out.
	childTotal time.Duration
	// hoistToken is the per-render marker token, so {{hoist}} writes a placeholder
	// no other render could produce — including one carried in content a page is
	// rendering.
	hoistToken string
	// notFound is set once, at the required fragment whose own attempt first
	// failed with an error satisfying errors.Is(err, types.ErrNotFound). It is
	// never reset: the render either fails for this reason or it does not, and
	// only one frame ever qualifies to set it, see renderFragment.
	notFound bool
}

// nextOrder assigns the next launch number.
func (s *renderState) nextOrder() int {
	s.order++
	return s.order
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
	all := make([]string, 0, len(s.tags))
	for tag := range s.tags {
		all = append(all, tag)
	}
	return sortedTags(all)
}

// sortedTags returns tags deduplicated, sorted and with empty entries dropped.
// Determinism here is a framework invariant: the same input must always produce
// the same tag slice, so cache invalidation is reproducible.
func sortedTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	unique := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		unique = append(unique, tag)
	}
	sort.Strings(unique)
	return unique
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
func (e *SlotEngine) renderFragment(rc *types.RenderContext, f *types.Fragment, state *renderState, pre *prefetch) ([]byte, error) {
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

	// The fragment's position travels in its own context rather than being held as
	// the engine's "current" depth. A data handler calls Hoist and has no other way
	// to know how deep it is, and with sibling handlers running at once there is no
	// single current position to read. A prefetched fragment keeps the position it
	// was launched with, so what it hoists lands where the tree says it should.
	depth, order := len(state.stack), 0
	if pre != nil {
		depth, order = pre.depth, pre.order
	} else {
		order = state.nextOrder()
	}
	rc = rc.WithFragment(depth, order)

	// The metadata slot is reserved on entry so parents appear before the children
	// that render inside them, and filled in once the outcome is known.
	index := len(state.fragments)
	state.fragments = append(state.fragments, FragmentMetadata{Name: f.Name})

	start := time.Now()
	childTotalBefore := state.childTotal

	out, err := e.attempt(rc, f, state, pre)
	meta := FragmentMetadata{Name: f.Name}
	propagate := false

	if err != nil {
		meta.Failed = true
		meta.Err = err
		out = nil

		switch {
		case f.Required || isFatal(err):
			// Classify only at the frame where this error first became fatal: a
			// required fragment's own attempt failing fresh. An error that is
			// already fatal here was fatal-marked, and therefore classified if it
			// warranted classification, at the deeper frame it came from —
			// re-checking it here would either duplicate that classification or,
			// for a non-required ancestor merely forwarding a fatal error it
			// cannot absorb, wrongly attribute it to the wrong fragment.
			if f.Required && !isFatal(err) && errors.Is(err, types.ErrNotFound) {
				state.notFound = true
			}
			propagate = true
		case f.Fallback != nil:
			// No prefetch for a fallback: it exists precisely because the primary
			// failed, which is not known until it has.
			fallbackOut, fallbackErr := e.attempt(rc, f.Fallback, state, nil)
			if fallbackErr != nil {
				// A fallback exists to contain a failure, so its own failure is
				// contained here rather than escalated: the page loses this
				// fragment's output and records both errors. This holds however the
				// fallback failed, including for a Required fragment inside the
				// fallback's own subtree — required-ness is scoped to the primary
				// tree. "The page cannot render without me" and "this alternative
				// cannot render without me" are different claims, and letting the
				// second one through would let a broken fallback take down the very
				// page it exists to protect.
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
func (e *SlotEngine) attempt(rc *types.RenderContext, f *types.Fragment, state *renderState, pre *prefetch) ([]byte, error) {
	var data any // any: fragment data is opaque to the framework and flows straight into the template engine, whose parameter is already any
	switch {
	case pre != nil:
		// Already running, started by this fragment's parent. Waiting for it is
		// all that is left, and by now it has usually finished.
		var tags []string
		var err error
		var elapsed time.Duration
		data, tags, elapsed, err = pre.wait()
		state.dataTime += elapsed
		state.addTags(tags)
		if err != nil {
			return nil, wrapFragment("data handler", f.Name, err)
		}

	case f.DataHandler != nil:
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

	// After the handler, so a resolver can read what it fetched; before the
	// children start, so what it returns is prefetched like anything bound.
	fills, err := resolveSlots(rc, f)
	if err != nil {
		return nil, err
	}
	if err := requiredSlotsFilled(f, fills); err != nil {
		return nil, err
	}

	// Started before the template runs, not after: the whole point is that a
	// sibling's upstream call is already in flight by the time the slot that needs
	// it is reached. Released afterwards, so a slot the template decided not to
	// render does not leave a call running for nobody.
	started := e.prefetchChildren(rc, f, state, fills)
	defer release(started)

	var buf bytes.Buffer
	childTotalBefore := state.childTotal
	renderStarted := time.Now()
	// Template execution goes through Execute as well: a template function can
	// panic, and the fragment that owns it should fail rather than the process. No
	// timeout is imposed here — Fragment.Timeout bounds the data handler, which is
	// the part that talks to the outside world — but the render's context still
	// applies.
	err = Execute(rc.Context(), 0, func(ctx context.Context) error {
		return e.tmpl.RenderWithFuncs(ctx, &buf, f.TemplatePath, data, e.slotFuncs(rc, f, state, started, fills))
	})
	state.templateTime += time.Since(renderStarted) - (state.childTotal - childTotalBefore)
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
func (e *SlotEngine) slotFuncs(rc *types.RenderContext, f *types.Fragment, state *renderState, started map[*types.Fragment][]*prefetch, fills slotFills) htmltemplate.FuncMap {
	// taken counts how many of a fragment's prefetches have been consumed, so the
	// same fragment bound into two slots — or twice into one — takes a different
	// one each time rather than sharing a single result. Two bindings of one
	// fragment are two fragments as far as a render is concerned.
	taken := make(map[*types.Fragment]int)
	return htmltemplate.FuncMap{
		"slot": func(name string) (htmltemplate.HTML, error) {
			return e.renderSlot(rc, f, name, state, started, taken, fills)
		},
		"hoist": hoistFunc(state.hoistToken),
		"asset": e.assetFunc(),
		"stylesheet": func(urlPath string) (string, error) {
			return "", rc.HoistStylesheet(urlPath)
		},
		"csrfToken": e.csrfFunc(rc),
		"pageURL":   e.pageURLFunc(rc),
		"pageURLIn": e.pageURLInFunc(),
		"localeURL": e.localeURLFunc(rc),
	}
}

// renderSlot renders every fragment bound to f's slot named name, in binding order,
// and returns the concatenation as template.HTML so the children's markup is not
// escaped a second time on its way into the parent.
//
// A name f does not declare is an error, not empty output: silently rendering nothing
// would turn a typo in a template into a section that is simply missing from the
// page, which nobody notices until a user does.
func (e *SlotEngine) renderSlot(
	rc *types.RenderContext,
	f *types.Fragment,
	name string,
	state *renderState,
	started map[*types.Fragment][]*prefetch,
	taken map[*types.Fragment]int,
	fills slotFills,
) (htmltemplate.HTML, error) {
	slot, ok := f.Slot(name)
	if !ok {
		return "", fmt.Errorf("%w: fragment %q has no slot %q, only %v", types.ErrUnknownSlot, f.Name, name, f.SlotNames())
	}
	// Slot reports ok for a key mapped to a nil definition, and Render never
	// requires that Fragment.Validate has run, so the nil is checked here rather
	// than assumed away. Without it the deref below panics and the fragment's
	// failure reason becomes "invalid memory address" instead of the real fault.
	if slot == nil {
		return "", fmt.Errorf("%w: fragment %q slot %q is nil", types.ErrInvalidSlotDefinition, f.Name, name)
	}

	var buf strings.Builder
	for _, child := range fills.of(slot) {
		out, err := e.renderFragment(rc, child, state, take(started, taken, child))
		if err != nil {
			return "", err
		}
		buf.Write(out)
	}
	return htmltemplate.HTML(buf.String()), nil
}

// slotFills is what each resolved slot of one fragment holds for one render, by
// slot name. A slot not in it holds its bound Fill.
type slotFills map[string][]*types.Fragment

// of returns what slot holds in this render.
func (fills slotFills) of(slot *types.SlotDefinition) []*types.Fragment {
	if resolved, ok := fills[slot.Name]; ok {
		return resolved
	}
	return slot.Fill
}

// resolveSlots runs f's slot resolvers, in slot-name order, and checks what each
// returned by the rules a bound slot is held to at build time.
//
// Through Execute, like a data handler and a template, so a resolver that panics
// fails its fragment rather than the process.
func resolveSlots(rc *types.RenderContext, f *types.Fragment) (slotFills, error) {
	var fills slotFills
	for _, name := range f.SlotNames() {
		slot := f.Slots[name]
		if slot == nil || slot.Resolve == nil {
			continue
		}
		var resolved []*types.Fragment
		err := Execute(rc.Context(), 0, func(context.Context) error {
			var resolveErr error
			resolved, resolveErr = slot.Resolve(rc)
			return resolveErr
		})
		if err != nil {
			return nil, wrapFragment("slot resolver "+name, f.Name, err)
		}
		if len(resolved) > 1 && !slot.AllowMultiple {
			return nil, fmt.Errorf("%w: fragment %q slot %q: the resolver returned %d fragments for a slot that holds one",
				types.ErrSlotOccupied, f.Name, name, len(resolved))
		}
		for _, child := range resolved {
			if child == nil {
				return nil, fmt.Errorf("%w: fragment %q slot %q: the resolver returned a nil fragment", types.ErrNilFragment, f.Name, name)
			}
		}
		if fills == nil {
			fills = make(slotFills)
		}
		fills[slot.Name] = resolved
	}
	return fills, nil
}

// take returns the next unconsumed prefetch for child, or nil if there is none —
// a fragment with no data handler, or one reached past the depth limit, was never
// launched, and a nil prefetch simply means "run it here".
func take(started map[*types.Fragment][]*prefetch, taken map[*types.Fragment]int, child *types.Fragment) *prefetch {
	list := started[child]
	i := taken[child]
	if i >= len(list) {
		return nil
	}
	taken[child] = i + 1
	return list[i]
}

// requiredSlotsFilled reports ErrRequiredSlotEmpty for the first required slot of f
// with nothing bound to it. It runs before the template rather than inside the slot
// function so that a required slot is enforced even when the template never asks for
// it, and it walks the slots in sorted order so the same tree always names the same
// slot.
func requiredSlotsFilled(f *types.Fragment, fills slotFills) error {
	for _, name := range f.SlotNames() {
		slot := f.Slots[name]
		if slot != nil && slot.Required && len(fills.of(slot)) == 0 {
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

// bindAssets gives rc's data handlers the resolver behind {{asset}}, so rc.Asset
// and rc.HoistStylesheet work in Go too.
func (e *SlotEngine) bindAssets(rc *types.RenderContext) {
	if e.assetURL != nil {
		types.BindAssets(rc, e.assetURL)
	}
}

// assetFunc is the per-render implementation of {{asset "/static/app.css"}}. It
// returns the content-addressed URL for a mounted file.
//
// An unresolvable asset is an error rather than the path unchanged. A page that
// renders successfully while linking a stylesheet that 404s is a broken page
// reporting itself as fine, and the typo that caused it survives to production;
// failing the fragment puts the mistake in front of whoever made it.
func (e *SlotEngine) assetFunc() func(string) (string, error) {
	return func(urlPath string) (string, error) {
		if e.assetURL == nil {
			return "", fmt.Errorf("%w: %q: this application has mounted no assets", types.ErrUnknownAsset, urlPath)
		}
		resolved, err := e.assetURL(urlPath)
		if err != nil {
			return "", fmt.Errorf("%w: %q: %w", types.ErrUnknownAsset, urlPath, err)
		}
		return resolved, nil
	}
}

// pageURLFunc is the per-render implementation of
// {{pageURL "blog-post" "slug" .Slug}}: the route's path in the render's own
// locale, or in the default locale when the route has none in this one — a
// Turkish page linking a page that exists only in English links the English one.
func (e *SlotEngine) pageURLFunc(rc *types.RenderContext) func(string, ...string) (string, error) {
	return func(name string, pairs ...string) (string, error) {
		params, err := routeParams(name, pairs)
		if err != nil {
			return "", err
		}
		built, err := e.buildURL(name, rc.Locale, params)
		if errors.Is(err, types.ErrNoPathInLocale) && rc.Locale != e.defaultLocale {
			return e.buildURL(name, e.defaultLocale, params)
		}
		return built, err
	}
}

// pageURLInFunc is the per-render implementation of
// {{pageURLIn "tr" "about"}}: the route's path in exactly that locale.
func (e *SlotEngine) pageURLInFunc() func(string, string, ...string) (string, error) {
	return func(locale, name string, pairs ...string) (string, error) {
		params, err := routeParams(name, pairs)
		if err != nil {
			return "", err
		}
		return e.buildURL(name, locale, params)
	}
}

// localeURLFunc is the per-render implementation of {{localeURL "tr"}}: the page
// being rendered, in another locale, with the same path parameters. It is what a
// language switcher is made of.
//
// A page with no path in that locale is the empty string rather than an error, so
// a switcher can skip it with {{with localeURL "tr"}}; a locale no URL can reach
// is still an error, because that is a mistake in the template rather than a page
// nobody translated.
func (e *SlotEngine) localeURLFunc(rc *types.RenderContext) func(string) (string, error) {
	return func(locale string) (string, error) {
		if rc.Page == nil {
			return "", fmt.Errorf("collage: localeURL %q: this render is not a page", locale)
		}
		built, err := e.buildURL(rc.Page.Name, locale, rc.PathParams)
		if errors.Is(err, types.ErrNoPathInLocale) {
			return "", nil
		}
		return built, err
	}
}

// buildURL calls the application's URL builder.
func (e *SlotEngine) buildURL(name, locale string, params map[string]string) (string, error) {
	if e.url == nil {
		return "", fmt.Errorf("%w: %q: this engine knows no routes", types.ErrUnknownRoute, name)
	}
	return e.url(name, locale, params)
}

// routeParams turns a template's "name" "value" pairs into a map.
func routeParams(route string, pairs []string) (map[string]string, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("%w: %q: parameters come in name and value pairs, got %d values", types.ErrRouteParams, route, len(pairs))
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	params := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		params[pairs[i]] = pairs[i+1]
	}
	return params, nil
}

// csrfFunc is the per-render implementation of {{csrfToken}}. It returns the hidden
// input a form submits alongside its own fields.
//
// A whole input rather than the bare value, because the bare value is a footgun: it
// has to be placed in a field with exactly the right name, and a form that names it
// wrong is a form that fails to submit in a way that looks like the token is broken.
// The one thing a template has to get right is putting it inside the <form>.
//
// What it renders is a marker rather than a token. A token belongs to one visitor,
// so a cached page must not contain one — but it may contain something that stands
// for one, which the response layer replaces with the reader's own token on the way
// out. That is what lets a page with a form still be cached: the render is shared,
// the one per-visitor string is not.
func (e *SlotEngine) csrfFunc(rc *types.RenderContext) func() (htmltemplate.HTML, error) {
	return func() (htmltemplate.HTML, error) {
		if e.csrfMarker == nil {
			return "", template.ErrCSRFOutsideRender
		}
		marker, err := e.csrfMarker()
		if err != nil {
			return "", err
		}
		return htmltemplate.HTML(`<input type="hidden" name="` +
			htmltemplate.HTMLEscapeString(CSRFFieldName) +
			`" value="` + marker + `">`), nil
	}
}

// csrfFieldName is the form field a token is submitted in. It matches
// internal/csrf's DefaultFieldName; the render engine does not import that package,
// because a template function that needed the verifier would make every render
// depend on the thing that checks submissions.
// CSRFFieldName is exported so one test can assert it matches what the verifier
// reads; see internal/core.
const CSRFFieldName = "_csrf"
