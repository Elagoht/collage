package router

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidPattern is returned when a route or redirect pattern is malformed: it
// does not start with "/", contains an empty segment, contains a placeholder with
// an empty name, holds more than one placeholder in a segment, places a catch-all
// segment ("{name...}") anywhere but the final segment, or gives a catch-all
// literal text around it.
var ErrInvalidPattern = errors.New("collage: invalid pattern")

// segmentKind distinguishes the three kinds of pattern segment.
type segmentKind int

const (
	// segmentStatic is a literal segment matched by exact text.
	segmentStatic segmentKind = iota
	// segmentDynamic is a "{name}" segment matching exactly one path segment, or
	// one with literal text around its placeholder — "{slug}.md", "post-{id}",
	// "v{version}.json" — matching a segment that begins with the prefix, ends
	// with the suffix, and has at least one character between them.
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
	// prefix and suffix are a segmentDynamic's literal text before and after its
	// placeholder: ".md" is the suffix of "{slug}.md". Both are empty for a
	// placeholder that is the whole segment.
	prefix, suffix string
}

// affixed reports whether s is a placeholder with literal text around it.
func (s patternSegment) affixed() bool { return s.prefix != "" || s.suffix != "" }

// literal returns the segment's text with its placeholder written as {name},
// as a pattern spells it.
func (s patternSegment) literal() string {
	switch s.kind {
	case segmentStatic:
		return s.text
	case segmentCatchAll:
		return "{" + s.text + "...}"
	default:
		return s.prefix + "{" + s.text + "}" + s.suffix
	}
}

// parseSegment reads one segment of a pattern. A segment holds at most one
// placeholder: "{slug}", "{slug}.md", "post-{id}", or the whole-segment catch-all
// "{path...}". ok is false, with a reason, for a segment that is none of those.
func parseSegment(part string) (seg patternSegment, reason string, ok bool) {
	open := strings.IndexByte(part, '{')
	if open < 0 {
		if strings.IndexByte(part, '}') >= 0 {
			return patternSegment{}, fmt.Sprintf("segment %q has a \"}\" with no \"{\"", part), false
		}
		return patternSegment{kind: segmentStatic, text: part}, "", true
	}
	end := strings.IndexByte(part[open:], '}')
	if end < 0 {
		return patternSegment{}, fmt.Sprintf("segment %q has a \"{\" with no \"}\"", part), false
	}
	end += open
	prefix, inner, suffix := part[:open], part[open+1:end], part[end+1:]
	if strings.ContainsAny(prefix, "{}") || strings.ContainsAny(suffix, "{}") || strings.ContainsAny(inner, "{") {
		return patternSegment{}, fmt.Sprintf("segment %q holds more than one placeholder; a segment holds one, such as \"{name}.md\" — register \"{name}.md\" and \"{name}.json\" as two routes", part), false
	}
	if name, isCatchAll := strings.CutSuffix(inner, "..."); isCatchAll {
		if name == "" {
			return patternSegment{}, "it has an empty catch-all name", false
		}
		if prefix != "" || suffix != "" {
			return patternSegment{}, fmt.Sprintf("catch-all segment %q has text around it; a catch-all is a whole segment", part), false
		}
		return patternSegment{kind: segmentCatchAll, text: name}, "", true
	}
	if inner == "" {
		return patternSegment{}, "it has an empty placeholder name", false
	}
	return patternSegment{kind: segmentDynamic, text: inner, prefix: prefix, suffix: suffix}, "", true
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
// for the root, with every placeholder segment reduced to "{}" or "{...}". This
// makes "/blog/" and "/blog" compare equal, matching the trailing-slash equivalence
// route matching applies, and makes "/blog/{slug}" and "/blog/{x}" compare equal,
// matching the fact that route matching never looks at a placeholder's name either.
//
// Erasing the name matters because this is what the redirect/page shadow check
// compares. Redirects are matched before pages and are not per-locale, so a
// redirect registered at "/blog/{x}" takes every request a page at "/blog/{slug}"
// would have served, in every locale — comparing the literal strings would let that
// pair through as two unrelated patterns and leave the page unreachable everywhere.
func normalizePattern(pattern string) string {
	segments := splitPath(pattern)
	if len(segments) == 0 {
		return "/"
	}
	normalized := make([]string, len(segments))
	for i, segment := range segments {
		normalized[i] = normalizeSegment(segment)
	}
	return "/" + strings.Join(normalized, "/")
}

// normalizeSegment returns segment with its placeholder's name erased: "{}" for
// a dynamic segment, "{...}" for a catch-all one, "pre{}suf" for one with text
// around its placeholder, and segment unchanged for a literal one. It reads the
// segment with parseSegment, so the two cannot disagree about what a placeholder
// is; a segment parseSegment refuses is returned as it is, since registration
// refuses it before anything compares it.
func normalizeSegment(segment string) string {
	seg, _, ok := parseSegment(segment)
	if !ok {
		return segment
	}
	switch seg.kind {
	case segmentCatchAll:
		return "{...}"
	case segmentDynamic:
		return seg.prefix + "{}" + seg.suffix
	default:
		return segment
	}
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
		seg, reason, ok := parseSegment(part)
		if !ok {
			return nil, fmt.Errorf("%w: %q: %s", ErrInvalidPattern, pattern, reason)
		}
		if seg.kind == segmentCatchAll && i != len(raw)-1 {
			return nil, fmt.Errorf("%w: %q: catch-all segment %q must be the final segment", ErrInvalidPattern, pattern, part)
		}
		segments = append(segments, seg)
	}
	return segments, nil
}

// overlaps reports whether some path segment matches both a and b, two
// placeholders with literal text around them. The placeholder matches any text of
// at least one character, so a segment long enough to begin with the longer prefix
// and end with the longer suffix matches both whenever each prefix begins the
// other and each suffix ends the other — and none does otherwise.
func overlaps(a, b patternSegment) bool {
	return (strings.HasPrefix(a.prefix, b.prefix) || strings.HasPrefix(b.prefix, a.prefix)) &&
		(strings.HasSuffix(a.suffix, b.suffix) || strings.HasSuffix(b.suffix, a.suffix))
}

// within reports whether every segment a matches, b matches too: a is the more
// specific of the two — "{slug}.min.md" within "{slug}.md", and every placeholder
// within the bare "{slug}".
func within(a, b patternSegment) bool {
	return strings.HasPrefix(a.prefix, b.prefix) && strings.HasSuffix(a.suffix, b.suffix)
}
