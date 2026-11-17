package types

import (
	"html/template"
	"sort"
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
	// order is the fragment's number within this render, taken as the walk reaches
	// it. It settles a tie between two declarations at equal depth, and it exists
	// because data handlers no longer run in the order they finish: siblings run
	// concurrently, so "the last one to declare" would mean "whichever goroutine
	// happened to win", which is not a rule anyone can rely on. It is assigned on
	// the render's own goroutine, in page order, so it is the same on every run.
	order int
	html  template.HTML
}

// hoistArea is the declarations for one marker, keyed and ordered.
type hoistArea struct {
	// position is where each key is written: at its earliest declaration in the
	// tree's own order — the declaring fragment's number, then the order
	// that fragment declared in. Not in the order declarations arrived: sibling
	// handlers run concurrently, so arrival order is whichever goroutine won, and
	// a page's <head> — the order of its stylesheets included — changed from one
	// request to the next. Earliest rather than winning declaration, so a nested
	// fragment overriding a title does not move it.
	position map[string]hoistPosition
	// declared counts each fragment's declarations so far, by that number,
	// which is what orders one fragment's own declarations among themselves.
	declared map[int]int
	byKey    map[string]hoistEntry
}

// hoistPosition is where a declaration sits in the tree's order.
type hoistPosition struct {
	order, seq int
}

func (p hoistPosition) before(other hoistPosition) bool {
	return p.order < other.order || (p.order == other.order && p.seq < other.seq)
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
// number is order.
//
// The innermost declaration of a key wins, because depth is what specificity looks
// like here: a layout naming a default title and an article naming its own are not
// in conflict, the article is simply more specific. At equal depth the
// later-declared one wins — two siblings writing one key is a genuine conflict with
// no specificity to settle it, so the rule is arbitrary and therefore stated rather
// than discovered.
//
// "Later" means later in the page, not in finishing order. Sibling data handlers
// run concurrently, so which of two goroutines reaches this function first is not
// something a page should depend on; order is assigned as the render's walk reaches
// each fragment, on one goroutine, in the order they sit in the page.
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
		a = &hoistArea{
			position: make(map[string]hoistPosition),
			declared: make(map[int]int),
			byKey:    make(map[string]hoistEntry),
		}
		h.areas[area] = a
	}

	here := hoistPosition{order: order, seq: a.declared[order]}
	a.declared[order]++
	if first, seen := a.position[key]; !seen || here.before(first) {
		a.position[key] = here
	}

	entry := hoistEntry{depth: depth, order: order, html: html}
	previous, seen := a.byKey[key]
	if !seen {
		a.byKey[key] = entry
		return
	}
	if depth > previous.depth || (depth == previous.depth && order >= previous.order) {
		a.byKey[key] = entry
	}
}

// HTML returns everything declared for area, each key at its earliest declaration
// in the tree's order.
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

	keys := make([]string, 0, len(a.position))
	for key := range a.position {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return a.position[keys[i]].before(a.position[keys[j]]) })

	var out strings.Builder
	for _, key := range keys {
		out.WriteString(string(a.byKey[key].html))
	}
	return template.HTML(out.String()) // any: the concatenation of values already marked safe by their declarers
}

// HoistItem is one hoisted declaration as it stands once the render is finished:
// the area it was declared for, the key that deduplicates it, and its markup.
type HoistItem struct {
	Area string
	Key  string
	HTML template.HTML
}

// Items returns what was declared for area, one item per key, each at its
// earliest declaration in the tree's order — the same order and the same winners
// HTML writes.
func (h *Hoisted) Items(area string) []HoistItem {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.areas[area]
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(a.position))
	for key := range a.position {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return a.position[keys[i]].before(a.position[keys[j]]) })
	items := make([]HoistItem, 0, len(keys))
	for _, key := range keys {
		items = append(items, HoistItem{Area: area, Key: key, HTML: a.byKey[key].html})
	}
	return items
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

// HoistLog is what one fragment declared for the page, held back until the
// fragment is known to be part of it.
//
// A declaration made straight into the collector cannot be taken back, and two
// kinds of fragment make declarations that must be. A fragment's data handler is
// started before the template decides whether the fragment is rendered at all, so
// its title, its robots meta and the tags its Cached calls carry would reach a page
// that never shows it. And a fragment whose attempt fails has its markup cut back
// but not what it declared, so an optional "Draft: secret" that errored left its
// title over the fallback.
//
// So each fragment writes into a log of its own, and the log is committed to its
// parent's — and the root's to the collector — only once the fragment has rendered.
// Committing a child's log into its parent's, rather than into the collector, is
// what lets a parent that fails afterwards take its descendants' declarations with
// it.
//
// Tags are different from markup in one respect: a fragment that failed halfway
// still describes what its output depended on, so a failed fragment's tags are
// kept (see CommitTo) while its hoists are not.
type HoistLog struct {
	mu      sync.Mutex
	entries []loggedHoist
	tags    []string
}

type loggedHoist struct {
	area, key    string
	depth, order int
	html         template.HTML
}

// NewHoistLog returns an empty log.
func NewHoistLog() *HoistLog { return &HoistLog{} }

func (l *HoistLog) add(e loggedHoist) {
	l.mu.Lock()
	l.entries = append(l.entries, e)
	l.mu.Unlock()
}

func (l *HoistLog) addTags(tags []string) {
	l.mu.Lock()
	l.tags = append(l.tags, tags...)
	l.mu.Unlock()
}

// Place gives everything l holds the position rc's fragment sits at. A prefetched
// handler declares before its fragment has a place in the page — what the walk
// numbers is the order fragments are reached in, which a handler running ahead of
// it cannot know — so its declarations are placed when the walk takes it.
func (l *HoistLog) Place(rc *RenderContext) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		l.entries[i].depth, l.entries[i].order = rc.depth, rc.order
	}
}

// CommitTo moves l's declarations to parent: into parent's own log when it has
// one, otherwise into the render's collector, which is what the root fragment's
// log reaches. keepHoists false commits only the tags, for a fragment that failed.
func (l *HoistLog) CommitTo(parent *RenderContext, keepHoists bool) {
	l.mu.Lock()
	entries, tags := l.entries, l.tags
	l.entries, l.tags = nil, nil
	l.mu.Unlock()
	if !keepHoists {
		entries = nil
	}

	if parent.hlog != nil {
		parent.hlog.mu.Lock()
		parent.hlog.entries = append(parent.hlog.entries, entries...)
		parent.hlog.tags = append(parent.hlog.tags, tags...)
		parent.hlog.mu.Unlock()
		return
	}
	for _, e := range entries {
		parent.hoisted.Add(e.area, e.key, e.depth, e.order, e.html)
	}
	if len(tags) > 0 && parent.state != nil {
		parent.state.mu.Lock()
		parent.state.tags = append(parent.state.tags, tags...)
		parent.state.mu.Unlock()
	}
}
