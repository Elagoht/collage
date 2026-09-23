package types

import (
	"html/template"
	"strings"
)

// Hoisting is how a fragment contributes something that belongs to the page rather
// than to itself: a stylesheet it needs, a title it is the subject of, a preload
// hint for its own image.
//
// The difficulty is ordering. Fragments render depth-first, so a layout has already
// written its <head> by the time its content renders — a contribution made then has
// nowhere to go. So {{hoist "head"}} writes a marker instead, the fragments below
// declare as they render, and the engine replaces the marker once the tree is
// finished. One pass, and the layout keeps deciding where the contributions land.

// hoistEntry is one declaration, with the depth it was made at.
type hoistEntry struct {
	depth int
	html  template.HTML
}

// hoistArea is the declarations for one marker, keyed and ordered.
type hoistArea struct {
	// order is the keys in the order they were first declared, which is the order
	// they are written in. First-seen rather than innermost-wins order, so a page
	// does not reshuffle its own <head> because a nested fragment happened to
	// override a title.
	order []string
	byKey map[string]hoistEntry
}

// Hoisted collects what the fragments of one render declared.
//
// It needs no locking, for the same reason renderState does not: the walk is
// strictly sequential and depth-first, so exactly one goroutine touches it. A data
// handler that hoists from a goroutine of its own is outside that guarantee and
// outside what this supports.
type Hoisted struct {
	areas map[string]*hoistArea
	// depth is the nesting level of the fragment currently rendering. The engine
	// maintains it; Hoist reads it.
	depth int
}

// NewHoisted returns an empty collector.
func NewHoisted() *Hoisted {
	return &Hoisted{areas: make(map[string]*hoistArea)}
}

// Depth reports the nesting level currently being rendered.
func (h *Hoisted) Depth() int { return h.depth }

// SetDepth records the nesting level currently being rendered. It is called by the
// render engine as it descends and again as it returns; an application has no
// reason to call it.
func (h *Hoisted) SetDepth(depth int) {
	if h != nil {
		h.depth = depth
	}
}

// Add records a declaration for area under key, made at depth.
//
// The innermost declaration of a key wins, because depth is what specificity looks
// like here: a layout naming a default title and an article naming its own are not
// in conflict, the article is simply more specific. At equal depth the later
// declaration wins — two siblings writing one key is a genuine conflict with no
// specificity to settle it, so the rule is arbitrary and therefore stated rather
// than discovered.
//
// Position is decided by the first declaration of a key, not the winning one.
// Otherwise a page's <head> would reorder itself depending on whether a nested
// fragment happened to override something.
func (h *Hoisted) Add(area, key string, depth int, html template.HTML) {
	if h == nil || area == "" || key == "" {
		return
	}
	a, ok := h.areas[area]
	if !ok {
		a = &hoistArea{byKey: make(map[string]hoistEntry)}
		h.areas[area] = a
	}

	previous, seen := a.byKey[key]
	if !seen {
		a.order = append(a.order, key)
		a.byKey[key] = hoistEntry{depth: depth, html: html}
		return
	}
	if depth >= previous.depth {
		a.byKey[key] = hoistEntry{depth: depth, html: html}
	}
}

// HTML returns everything declared for area, in first-declared order.
func (h *Hoisted) HTML(area string) template.HTML {
	if h == nil {
		return ""
	}
	a, ok := h.areas[area]
	if !ok {
		return ""
	}

	var out strings.Builder
	for _, key := range a.order {
		out.WriteString(string(a.byKey[key].html))
	}
	return template.HTML(out.String()) // any: the concatenation of values already marked safe by their declarers
}

// Areas returns the area names that have declarations, for diagnostics.
func (h *Hoisted) Areas() []string {
	if h == nil {
		return nil
	}
	names := make([]string, 0, len(h.areas))
	for name := range h.areas {
		names = append(names, name)
	}
	return names
}
