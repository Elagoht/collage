package router

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidPattern is returned when a route or redirect pattern is malformed: it
// does not start with "/", contains an empty segment, contains a placeholder with
// an empty name, or places a catch-all segment ("{name...}") anywhere but the
// final segment.
var ErrInvalidPattern = errors.New("collage: invalid pattern")

// segmentKind distinguishes the three kinds of pattern segment.
type segmentKind int

const (
	// segmentStatic is a literal segment matched by exact text.
	segmentStatic segmentKind = iota
	// segmentDynamic is a "{name}" segment matching exactly one path segment.
	segmentDynamic
	// segmentCatchAll is a "{name...}" segment matching the rest of the path. It
	// is only valid as a pattern's final segment.
	segmentCatchAll
)

// patternSegment is one "/"-delimited piece of a parsed pattern.
type patternSegment struct {
	kind segmentKind
	// text is the literal text for segmentStatic, or the parameter name for
	// segmentDynamic and segmentCatchAll.
	text string
}

// splitPath splits path on "/" after trimming every leading and trailing "/", and
// returns the resulting segments. The root path "/" (and "") yields nil.
func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// normalizePattern returns pattern's canonical form for equality comparisons: its
// segments rejoined with a single leading "/" and no trailing "/", or "/" itself
// for the root. This makes "/blog/" and "/blog" compare equal, matching the
// trailing-slash equivalence route matching applies.
func normalizePattern(pattern string) string {
	segments := splitPath(pattern)
	if len(segments) == 0 {
		return "/"
	}
	return "/" + strings.Join(segments, "/")
}

// parsePattern parses a route or redirect pattern into its ordered segments. See
// ErrInvalidPattern for the conditions under which it fails.
func parsePattern(pattern string) ([]patternSegment, error) {
	if pattern == "" || pattern[0] != '/' {
		return nil, fmt.Errorf("%w: %q must start with \"/\"", ErrInvalidPattern, pattern)
	}
	raw := splitPath(pattern)
	segments := make([]patternSegment, 0, len(raw))
	for i, part := range raw {
		if part == "" {
			return nil, fmt.Errorf("%w: %q contains an empty segment", ErrInvalidPattern, pattern)
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			inner := part[1 : len(part)-1]
			if strings.HasSuffix(inner, "...") {
				name := strings.TrimSuffix(inner, "...")
				if name == "" {
					return nil, fmt.Errorf("%w: %q has an empty catch-all name", ErrInvalidPattern, pattern)
				}
				if i != len(raw)-1 {
					return nil, fmt.Errorf("%w: %q: catch-all segment %q must be the final segment", ErrInvalidPattern, pattern, part)
				}
				segments = append(segments, patternSegment{kind: segmentCatchAll, text: name})
				continue
			}
			if inner == "" {
				return nil, fmt.Errorf("%w: %q has an empty placeholder name", ErrInvalidPattern, pattern)
			}
			segments = append(segments, patternSegment{kind: segmentDynamic, text: inner})
			continue
		}
		segments = append(segments, patternSegment{kind: segmentStatic, text: part})
	}
	return segments, nil
}
