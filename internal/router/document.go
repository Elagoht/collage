package router

import (
	"fmt"

	"github.com/Elagoht/collage/internal/types"
)

// RegisterDocument implements Router.
func (rt *router) RegisterDocument(doc *types.Document) error {
	for _, locale := range doc.Locales() {
		pattern := doc.Paths[locale]

		segments, err := parsePattern(pattern)
		if err != nil {
			return fmt.Errorf("collage: document %q: %w", doc.Name, err)
		}

		tree, ok := rt.pageTrees[locale]
		if !ok {
			tree = &node{}
			rt.pageTrees[locale] = tree
		}
		target, err := tree.insert(segments)
		if err != nil {
			return fmt.Errorf("collage: document %q path %q locale %q: %w", doc.Name, pattern, locale, err)
		}
		if occupant := occupantName(target); occupant != "" {
			return fmt.Errorf("%w: document %q and %s both claim %q for locale %q",
				ErrDuplicateRoute, doc.Name, occupant, pattern, locale)
		}
		target.document = doc
	}

	return rt.registerRedirects(doc.Name, doc.Redirects)
}

// occupantName describes whatever already terminates at n, or returns the empty
// string when nothing does. It is the single place the duplicate check reads both
// occupant fields, so the page and document registration paths cannot drift.
func occupantName(n *node) string {
	switch {
	case n.page != nil:
		return fmt.Sprintf("page %q", n.page.Name)
	case n.document != nil:
		return fmt.Sprintf("document %q", n.document.Name)
	default:
		return ""
	}
}
