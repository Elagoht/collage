package router

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/Elagoht/collage/internal/types"
)

// BuildPath fills pattern's placeholders from params and returns the path it
// describes.
//
// It is strict where substitute, which serves redirects, is lenient: every
// placeholder must be given a non-empty value, and every value must name a
// placeholder. A link built from a misspelled parameter is a link to the wrong
// page — or to a path with an empty segment, which is no page at all — and the
// place to find that out is the render that built it, not the reader who
// clicked it.
//
// Values are escaped the way the router decodes them, so a value comes back from
// the match as exactly what was given: an ordinary placeholder's value is one
// segment, "/" included, and a catch-all's "/" separators are kept.
func BuildPath(pattern string, params map[string]string) (string, error) {
	segments, err := parsePattern(pattern)
	if err != nil {
		return "", err
	}

	used := 0
	var b strings.Builder
	for _, segment := range segments {
		b.WriteByte('/')
		if segment.kind == segmentStatic {
			b.WriteString(segment.text)
			continue
		}
		value, ok := params[segment.text]
		if !ok || value == "" {
			return "", fmt.Errorf("%w: %q needs a value for %q", types.ErrRouteParams, pattern, segment.text)
		}
		used++
		if segment.affixed() {
			// The text around the placeholder is the pattern's, and only the
			// value is escaped. The whole segment is what a browser resolves,
			// so it is the whole segment that cannot be "." or "..".
			if strings.Contains(value, "/") {
				return "", fmt.Errorf("%w: %q: %q holds a \"/\", which only a catch-all such as {%s...} can", types.ErrRouteParams, pattern, segment.text, segment.text)
			}
			// The value cannot be "." or "..", which the router never captures
			// (see affixEdge.capture), nor make the whole segment one, which a
			// browser resolves as a path step.
			if value == "." || value == ".." {
				return "", fmt.Errorf("%w: %q: %q cannot be %q", types.ErrRouteParams, pattern, segment.text, value)
			}
			if whole := segment.prefix + value + segment.suffix; whole == "." || whole == ".." {
				return "", fmt.Errorf("%w: %q: %q makes the segment %q, which a browser resolves as a path step", types.ErrRouteParams, pattern, segment.text, whole)
			}
			b.WriteString(segment.prefix + url.PathEscape(value) + segment.suffix)
			continue
		}
		pieces := []string{value}
		if segment.kind == segmentCatchAll {
			pieces = strings.Split(strings.Trim(value, "/"), "/")
		}
		for i, piece := range pieces {
			// A browser resolves "." and ".." as it would in a file path —
			// escaped as %2E or not, per the URL standard — so a link carrying
			// one points somewhere other than the page it names.
			if piece == "" {
				return "", fmt.Errorf("%w: %q: %q has an empty segment", types.ErrRouteParams, pattern, segment.text)
			}
			// A "/" in a single segment's value would be escaped as "%2F",
			// which the router refuses: see decodeSegments. A value that is a
			// path is a catch-all's.
			if segment.kind != segmentCatchAll && strings.Contains(piece, "/") {
				return "", fmt.Errorf("%w: %q: %q holds a \"/\", which only a catch-all such as {%s...} can", types.ErrRouteParams, pattern, segment.text, segment.text)
			}
			if piece == "." || piece == ".." {
				return "", fmt.Errorf("%w: %q: %q cannot be %q, which a browser resolves as a path step", types.ErrRouteParams, pattern, segment.text, piece)
			}
			if i > 0 {
				b.WriteByte('/')
			}
			b.WriteString(url.PathEscape(piece))
		}
	}

	if used != len(params) {
		var extra []string
		for name := range params {
			if !slices.ContainsFunc(segments, func(s patternSegment) bool { return s.kind != segmentStatic && s.text == name }) {
				extra = append(extra, name)
			}
		}
		slices.Sort(extra)
		return "", fmt.Errorf("%w: %q has no placeholder named %s", types.ErrRouteParams, pattern, strings.Join(extra, ", "))
	}

	if b.Len() == 0 {
		return "/", nil
	}
	return b.String(), nil
}
