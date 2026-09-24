package router

import (
	"fmt"

	"github.com/Elagoht/collage/internal/types"
)

// RegisterDocument implements Router.
func (rt *router) RegisterDocument(doc *types.Document) error {
	for _, key := range doc.Locales() {
		pattern := doc.Paths[key]
		// A document outside every locale is matched where the default locale's
		// bare paths are; Match keeps a prefix off it.
		locale := key
		if key == types.RootLocale {
			locale = rt.localeOptions.Default
		}

		segments, err := parsePattern(pattern)
		if err != nil {
			return fmt.Errorf("collage: document %q: %w", doc.Name, err)
		}

		normalized := normalizePattern(pattern)
		owner := fmt.Sprintf("document %q", doc.Name)
		if err := rt.checkRedirectShadow(owner, pattern, locale, normalized); err != nil {
			return err
		}

		tree, ok := rt.routeTrees[locale]
		if !ok {
			tree = &node{}
			rt.routeTrees[locale] = tree
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

		rt.recordRoutedPath(locale, pattern, normalized, owner)
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
