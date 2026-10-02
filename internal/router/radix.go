package router

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Elagoht/collage/internal/types"
)

// ErrDuplicateRoute is returned at registration time when a pattern is already
// registered and a second registration targets the same structural position:
// two pages, two documents, or a page and a document sharing a path pattern
// for the same locale (see occupantName, the single place both occupant
// fields are read), or two redirects sharing a From pattern. Every one of
// those collisions leaves the router unable to determine which registration
// should win, and a caller fixes any of them the same way — by removing the
// duplicate — so they deliberately share this one sentinel.
var ErrDuplicateRoute = errors.New("collage: duplicate route")

// ErrAmbiguousParameterName is returned at registration time when a dynamic or
// catch-all segment is registered at the same structural tree position as one
// already registered under a different placeholder name — for example
// "/blog/{slug}" followed by "/blog/{category}/new". A radix tree has exactly
// one outgoing dynamic (or catch-all) edge per node, so the two names cannot
// both be honored: whichever pattern registered second would silently have its
// captured value reported under the first pattern's name instead of its own.
var ErrAmbiguousParameterName = errors.New("collage: ambiguous parameter name")

// ErrOverlappingPattern is returned at registration time when a placeholder with
// literal text around it lands at a tree position that already has one whose
// matches cross its own: some segment matches both, and neither is the more
// specific — "a{x}" and "{x}b", which "aXb" matches both of. Two such patterns at
// one position are disjoint ("{slug}.md" and "{slug}.json"), which the router
// tells apart by their text, or one lies within the other ("{slug}.min.md" within
// "{slug}.md", every placeholder within "{slug}"), where the more specific wins.
// Crossing ones have no answer that does not depend on registration order, so
// they are refused.
var ErrOverlappingPattern = errors.New("collage: overlapping pattern")

// node is one level of a radix tree. One tree is built per registered locale,
// shared by pages and documents, plus one tree shared across locales for
// redirects. A node's static
// children are keyed by literal segment text; it holds edges for placeholders
// with literal text around them ("{slug}.md"), at most one dynamic edge for a
// bare placeholder, and one catch-all edge besides. Matching tries static
// children, then the affixed edges from the most specific, then the dynamic
// edge, then the catch-all edge, backtracking to a lower-priority edge
// when a higher-priority one fails to reach a terminal node deeper in the tree —
// this is what guarantees a static match always beats a dynamic or catch-all
// match at the same level, however deep the mismatch that would otherwise be
// hidden.
type node struct {
	static map[string]*node
	// affixed are the edges of placeholders with text around them, the most
	// specific first: pairwise disjoint or nested, never crossing (see
	// ErrOverlappingPattern), so the order decides only between nested ones.
	affixed  []*affixEdge
	dynamic  *edge
	catchAll *edge

	// page is set on a route-tree node that terminates a registered page path.
	page *types.Page

	// actions are the actions terminating at this node, keyed by method. Unlike
	// page and document, actions coexist with either: a page and the POST its own
	// form submits are one URL, and separating them would mean inventing a second
	// URL for every form.
	actions map[string]*types.Action

	// document is the document terminating at this node, if any. At most one of
	// page and document is ever set; occupantName is the only place both are read.
	document *types.Document

	// hasRedirect, redirectTo, redirectStatus, and redirectCatchAll are set on a
	// redirect-tree node that terminates a registered redirect. redirectTo is the
	// unsubstituted destination template. redirectCatchAll names the From
	// pattern's catch-all parameter, or is empty when the pattern has none: it is
	// recorded here because substitution has to escape a whole path tail
	// differently from a single segment, and only the pattern knows which
	// parameter is which.
	hasRedirect      bool
	redirectTo       string
	redirectStatus   int
	redirectCatchAll string
}

// edge is a dynamic or catch-all outgoing edge: the parameter name it captures,
// and the node it leads to. A second pattern registered at the same structural
// position reuses this same edge and node only when its placeholder name
// matches; a differently-named placeholder at the same position is
// ErrAmbiguousParameterName.
type edge struct {
	name string
	node *node
}

// affixEdge is an edge for a placeholder with literal text around it: the
// segment it was registered from, whose prefix and suffix a path segment must
// carry, and whose name captures what lies between them.
type affixEdge struct {
	segment patternSegment
	node    *node
}

// capture returns what seg holds between e's prefix and suffix, if seg carries
// both around at least one character.
//
// A value of "." or ".." is no capture. A bare placeholder never sees one — a dot
// segment is redirected to its clean path before matching — but "...md" is no dot
// segment, and would hand "{slug}.md" the slug "..": a handler joining a value
// onto a directory, as one for a version or a file name does, would climb out of
// it. Such a segment falls through to the bare placeholder, which captures it
// whole.
func (e *affixEdge) capture(seg string) (string, bool) {
	p, s := e.segment.prefix, e.segment.suffix
	if len(seg) <= len(p)+len(s) || !strings.HasPrefix(seg, p) || !strings.HasSuffix(seg, s) {
		return "", false
	}
	value := seg[len(p) : len(seg)-len(s)]
	if value == "." || value == ".." {
		return "", false
	}
	return value, true
}

// terminal reports whether n is the end of a registered page, document, or
// redirect.
func (n *node) terminal() bool {
	return n.page != nil || n.document != nil || len(n.actions) > 0 || n.hasRedirect
}

// insert walks segments from n, creating nodes as needed, and returns the
// terminal node for the full pattern. It fails with ErrAmbiguousParameterName
// when a dynamic or catch-all segment lands on a position that already has an
// edge registered under a different placeholder name; otherwise it never fails,
// since pattern validity was already established by parsePattern.
func (n *node) insert(segments []patternSegment) (*node, error) {
	current := n
	for _, seg := range segments {
		switch {
		case seg.kind == segmentDynamic && seg.affixed():
			next, err := current.insertAffixed(seg)
			if err != nil {
				return nil, err
			}
			current = next
		case seg.kind == segmentDynamic:
			if current.dynamic == nil {
				current.dynamic = &edge{name: seg.text, node: &node{}}
			} else if current.dynamic.name != seg.text {
				return nil, fmt.Errorf("%w: %q conflicts with already-registered %q at the same position", ErrAmbiguousParameterName, seg.text, current.dynamic.name)
			}
			current = current.dynamic.node
		case seg.kind == segmentCatchAll:
			if current.catchAll == nil {
				current.catchAll = &edge{name: seg.text, node: &node{}}
			} else if current.catchAll.name != seg.text {
				return nil, fmt.Errorf("%w: %q conflicts with already-registered %q at the same position", ErrAmbiguousParameterName, seg.text, current.catchAll.name)
			}
			current = current.catchAll.node
		default: // segmentStatic
			if current.static == nil {
				current.static = make(map[string]*node)
			}
			next, ok := current.static[seg.text]
			if !ok {
				next = &node{}
				current.static[seg.text] = next
			}
			current = next
		}
	}
	return current, nil
}

// insertAffixed returns the node an affixed placeholder leads to from n: the
// edge already registered with the same text, or a new one, kept in order of
// specificity. It refuses a placeholder whose matches cross an existing one's, and
// one with the same text under a different name.
func (n *node) insertAffixed(seg patternSegment) (*node, error) {
	for _, e := range n.affixed {
		if e.segment.prefix == seg.prefix && e.segment.suffix == seg.suffix {
			if e.segment.text != seg.text {
				return nil, fmt.Errorf("%w: %q conflicts with already-registered %q at the same position", ErrAmbiguousParameterName, seg.literal(), e.segment.literal())
			}
			return e.node, nil
		}
		if overlaps(seg, e.segment) && !within(seg, e.segment) && !within(e.segment, seg) {
			return nil, fmt.Errorf("%w: %q and already-registered %q at the same position both match a segment such as %q, and neither is more specific; make one's text begin or end the other's, or give them text that cannot both match",
				ErrOverlappingPattern, seg.literal(), e.segment.literal(), crossingExample(seg, e.segment))
		}
	}
	e := &affixEdge{segment: seg, node: &node{}}
	// The most specific first: of two nested placeholders the one within the
	// other has the longer text, so ordering by length puts it first, and
	// disjoint ones never compete for a segment.
	at := len(n.affixed)
	for i, existing := range n.affixed {
		if len(seg.prefix)+len(seg.suffix) > len(existing.segment.prefix)+len(existing.segment.suffix) {
			at = i
			break
		}
	}
	n.affixed = append(n.affixed[:at], append([]*affixEdge{e}, n.affixed[at:]...)...)
	return e.node, nil
}

// crossingExample is a segment two crossing placeholders both match, for the
// error that refuses them: the longer prefix, a character, the longer suffix.
func crossingExample(a, b patternSegment) string {
	prefix, suffix := a.prefix, a.suffix
	if len(b.prefix) > len(prefix) {
		prefix = b.prefix
	}
	if len(b.suffix) > len(suffix) {
		suffix = b.suffix
	}
	return prefix + "x" + suffix
}

// match finds the terminal node reached by segments from n — static children
// before the affixed edges before the dynamic edge before the catch-all edge at
// every level, backtracking
// as needed — and returns it along with the path parameters captured along the
// way.
func (n *node) match(segments []string) (*node, map[string]string, bool) {
	return n.matchFrom(segments, 0)
}

func (n *node) matchFrom(segments []string, idx int) (*node, map[string]string, bool) {
	if idx == len(segments) {
		if n.terminal() {
			return n, map[string]string{}, true
		}
		return nil, nil, false
	}

	seg := segments[idx]

	if n.static != nil {
		if child, ok := n.static[seg]; ok {
			if result, params, ok := child.matchFrom(segments, idx+1); ok {
				return result, params, true
			}
		}
	}

	for _, e := range n.affixed {
		value, ok := e.capture(seg)
		if !ok {
			continue
		}
		if result, params, ok := e.node.matchFrom(segments, idx+1); ok {
			params[e.segment.text] = value
			return result, params, true
		}
	}

	if n.dynamic != nil {
		if result, params, ok := n.dynamic.node.matchFrom(segments, idx+1); ok {
			params[n.dynamic.name] = seg
			return result, params, true
		}
	}

	if n.catchAll != nil && n.catchAll.node.terminal() {
		params := map[string]string{n.catchAll.name: strings.Join(segments[idx:], "/")}
		return n.catchAll.node, params, true
	}

	return nil, nil, false
}
