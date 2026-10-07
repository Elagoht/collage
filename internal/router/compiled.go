package router

import "fmt"

// Pattern is a route or redirect pattern parsed as the router parses one, for
// code outside the router that has to judge a pattern it will never register:
// a static build checking the redirects a plugin hands it.
type Pattern struct {
	segments []patternSegment
	tree     *node
	key      string
}

// CompilePattern parses pattern as registration would, failing with
// ErrInvalidPattern where registration fails.
func CompilePattern(pattern string) (*Pattern, error) {
	segments, err := parsePattern(pattern)
	if err != nil {
		return nil, err
	}
	tree := &node{}
	// One pattern into an empty tree: there is nothing for it to be ambiguous
	// or overlapping with.
	target, err := tree.insert(segments)
	if err != nil {
		return nil, err
	}
	target.hasRedirect = true
	return &Pattern{segments: segments, tree: tree, key: normalizePattern(pattern)}, nil
}

// Key is the pattern's form for comparing two patterns as the router compares
// them: a trailing "/" dropped, except at the root, and placeholder names
// erased, so "/old" and "/old/", and "/blog/{slug}" and "/blog/{x}", share one.
func (p *Pattern) Key() string { return p.key }

// Literal reports whether the pattern has no placeholder.
func (p *Pattern) Literal() bool {
	for _, seg := range p.segments {
		if seg.kind != segmentStatic {
			return false
		}
	}
	return true
}

// Matches reports whether the router would match path, a decoded URL path,
// against the pattern; a trailing "/" is ignored, as matching ignores it.
func (p *Pattern) Matches(path string) bool {
	_, _, ok := p.tree.match(splitPath(path))
	return ok
}

// CheckCaptures reports, as ErrUnsubstitutedPlaceholder, a "{name}"
// placeholder in to that the pattern does not capture: one that could never be
// filled in.
func (p *Pattern) CheckCaptures(to string) error {
	if name, ok := uncaptured(p.segments, to); ok {
		return fmt.Errorf("%w: %q", ErrUnsubstitutedPlaceholder, name)
	}
	return nil
}

// uncaptured returns the first placeholder in to that segments do not capture.
func uncaptured(segments []patternSegment, to string) (string, bool) {
	captured := make(map[string]bool, len(segments))
	for _, seg := range segments {
		if seg.kind != segmentStatic {
			captured[seg.text] = true
		}
	}
	for _, name := range placeholderNames(to) {
		if !captured[name] {
			return name, true
		}
	}
	return "", false
}
