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

// node is one level of a radix tree. One tree is built per registered locale for
// pages, plus one tree shared across locales for redirects. A node's static
// children are keyed by literal segment text; it holds at most one dynamic edge
// and one catch-all edge besides. Matching tries static children, then the
// dynamic edge, then the catch-all edge, backtracking to a lower-priority edge
// when a higher-priority one fails to reach a terminal node deeper in the tree —
// this is what guarantees a static match always beats a dynamic or catch-all
// match at the same level, however deep the mismatch that would otherwise be
// hidden.
type node struct {
	static   map[string]*node
	dynamic  *edge
	catchAll *edge

	// page is set on a page-tree node that terminates a registered page path.
	page *types.Page

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

// terminal reports whether n is the end of a registered page, document, or
// redirect.
func (n *node) terminal() bool {
	return n.page != nil || n.document != nil || n.hasRedirect
}

// insert walks segments from n, creating nodes as needed, and returns the
// terminal node for the full pattern. It fails with ErrAmbiguousParameterName
// when a dynamic or catch-all segment lands on a position that already has an
// edge registered under a different placeholder name; otherwise it never fails,
// since pattern validity was already established by parsePattern.
func (n *node) insert(segments []patternSegment) (*node, error) {
	current := n
	for _, seg := range segments {
		switch seg.kind {
		case segmentDynamic:
			if current.dynamic == nil {
				current.dynamic = &edge{name: seg.text, node: &node{}}
			} else if current.dynamic.name != seg.text {
				return nil, fmt.Errorf("%w: %q conflicts with already-registered %q at the same position", ErrAmbiguousParameterName, seg.text, current.dynamic.name)
			}
			current = current.dynamic.node
		case segmentCatchAll:
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

// match finds the terminal node reached by segments from n — static children
// before the dynamic edge before the catch-all edge at every level, backtracking
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
