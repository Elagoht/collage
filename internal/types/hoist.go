package types

import (
	"html/template"
	"strings"
	"sync"
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

// hoistEntry is one declaration, with the position in the tree it was made from.
type hoistEntry struct {
	depth int
	// order is the fragment's launch number within this render. It settles a tie
	// between two declarations at equal depth, and it exists because data handlers
	// no longer run in the order they finish: siblings run concurrently, so "the
	// last one to declare" would mean "whichever goroutine happened to win",
	// which is not a rule anyone can rely on. Launch order is assigned on the
	// render's own goroutine, in declaration order, so it is the same on every run.
	order int
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
// It is locked because data handlers of sibling fragments run concurrently, and a
// handler is the usual place to declare a title. Where the declaration came from is
// carried in the call rather than held here as the engine's current position: a
// single "current depth" field is only meaningful while one fragment at a time is
// running, which is no longer true.
type Hoisted struct {
	mu    sync.Mutex
	areas map[string]*hoistArea
}

// NewHoisted returns an empty collector.
func NewHoisted() *Hoisted {
	return &Hoisted{areas: make(map[string]*hoistArea)}
}

// Add records a declaration for area under key, made from a fragment at depth whose
// launch number is order.
//
// The innermost declaration of a key wins, because depth is what specificity looks
// like here: a layout naming a default title and an article naming its own are not
// in conflict, the article is simply more specific. At equal depth the
// later-declared one wins — two siblings writing one key is a genuine conflict with
// no specificity to settle it, so the rule is arbitrary and therefore stated rather
// than discovered.
//
// "Later" means later in declaration order, not in finishing order. Sibling data
// handlers run concurrently, so which of two goroutines reaches this function first
// is not something a page should depend on; order is assigned when fragments are
// launched, on one goroutine, in the order the tree declares them.
//
// Position is decided by the first declaration of a key, not the winning one.
// Otherwise a page's <head> would reorder itself depending on whether a nested
// fragment happened to override something.
func (h *Hoisted) Add(area, key string, depth, order int, html template.HTML) {
	if h == nil || area == "" || key == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	a, ok := h.areas[area]
	if !ok {
		a = &hoistArea{byKey: make(map[string]hoistEntry)}
		h.areas[area] = a
	}

	entry := hoistEntry{depth: depth, order: order, html: html}
	previous, seen := a.byKey[key]
	if !seen {
		a.order = append(a.order, key)
		a.byKey[key] = entry
		return
	}
	if depth > previous.depth || (depth == previous.depth && order >= previous.order) {
		a.byKey[key] = entry
	}
}

// HTML returns everything declared for area, in first-declared order.
func (h *Hoisted) HTML(area string) template.HTML {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
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
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.areas))
	for name := range h.areas {
		names = append(names, name)
	}
	return names
}
