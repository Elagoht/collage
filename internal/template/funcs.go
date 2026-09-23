package template

import (
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"
	"unicode"
)

// ErrDictOddArgs is returned by dict when it is called with an odd number of
// arguments, so its key/value pairs cannot line up.
var ErrDictOddArgs = errors.New("collage: dict requires an even number of arguments")

// ErrDictKeyNotString is returned by dict when a positional key argument is not a
// string.
var ErrDictKeyNotString = errors.New("collage: dict key must be a string")

// DefaultFuncs returns the FuncMap every HTMLEngine parses its templates with. It
// must be registered at construction time, not merged in later: html/template only
// allows a function to be called if its name was already known when the template
// was parsed. In particular "slot" is registered here as a placeholder that returns
// ErrSlotOutsideRender when called; the render engine replaces it per render via
// RenderWithFuncs, which overlays a real implementation onto a Clone() of the parsed
// template set.
func DefaultFuncs() template.FuncMap {
	return template.FuncMap{
		"slot":       slotPlaceholder,
		"safeHTML":   safeHTML,
		"safeURL":    safeURL,
		"dict":       dict,
		"default":    defaultValue,
		"upper":      strings.ToUpper,
		"lower":      strings.ToLower,
		"title":      title,
		"join":       join,
		"hoist":      hoistPlaceholder,
		"asset":      assetPlaceholder,
		"formatTime": formatTime,
	}
}

// hoistPlaceholder is the parse-time stand-in for "hoist", for the same reason
// slotPlaceholder exists: html/template can only call a name that was in the
// FuncMap at parse time, and the real implementation is bound per render. If this
// one runs, a template used {{hoist}} outside a render that bound it.
func hoistPlaceholder(area string) (template.HTML, error) {
	return "", fmt.Errorf("%w: %q", ErrHoistOutsideRender, area)
}

// assetPlaceholder is the parse-time stand-in for "asset", for the same reason
// slotPlaceholder and hoistPlaceholder exist: the real implementation needs the
// application's mounts and is bound per render, but the name has to be in the
// FuncMap before any template that calls it is parsed.
func assetPlaceholder(urlPath string) (string, error) {
	return "", fmt.Errorf("%w: %q", ErrAssetOutsideRender, urlPath)
}

// slotPlaceholder is the parse-time stand-in for "slot". It is never meant to run: a
// real render always overlays its own implementation via RenderWithFuncs. If it does
// run, a template contained {{slot "name"}} outside of a bound render, which is a
// programming error the caller should see as ErrSlotOutsideRender rather than a
// nil-map panic.
func slotPlaceholder(name string) (template.HTML, error) {
	return "", fmt.Errorf("%w: %q", ErrSlotOutsideRender, name)
}

// safeHTML marks s as trusted HTML, bypassing html/template's contextual escaping.
// It is an escape hatch: only pass content the caller has already validated or
// generated itself, never unsanitized user input.
func safeHTML(s string) template.HTML {
	return template.HTML(s)
}

// safeURL marks s as a trusted URL, bypassing html/template's URL sanitization. It is
// an escape hatch: only pass a URL the caller has already validated, never
// unsanitized user input.
func safeURL(s string) template.URL {
	return template.URL(s)
}

// dict builds a map from an alternating key/value argument list, for constructing
// ad-hoc data to pass into a sub-template. It returns ErrDictOddArgs when the
// argument count is odd, and ErrDictKeyNotString when a key position holds a
// non-string value.
func dict(pairs ...any) (map[string]any, error) { // any: builds ad-hoc template data
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("%w: got %d", ErrDictOddArgs, len(pairs))
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("%w: position %d is %T", ErrDictKeyNotString, i, pairs[i])
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// defaultValue returns value unless it is empty, in which case it returns fallback.
func defaultValue(fallback, value string) string {
	if value == "" {
		return fallback
	}
	return value
}

// title upper-cases the first letter of each whitespace-separated word in s and
// lower-cases the rest, leaving other characters (including surrounding punctuation)
// untouched.
func title(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevIsLetter := false
	for _, r := range s {
		isLetter := unicode.IsLetter(r)
		switch {
		case isLetter && !prevIsLetter:
			b.WriteRune(unicode.ToUpper(r))
		case isLetter:
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
		prevIsLetter = isLetter
	}
	return b.String()
}

// join concatenates items with sep between them.
func join(sep string, items []string) string {
	return strings.Join(items, sep)
}

// formatTime formats t using layout (a reference-time layout as accepted by
// time.Time.Format).
func formatTime(t time.Time, layout string) string {
	return t.Format(layout)
}
