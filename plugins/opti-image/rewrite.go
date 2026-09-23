package optiimage

import (
	"bytes"
	"strconv"
	"strings"
)

// rewriteImages replaces the src of every <img> that declares both width and height
// and points at an allowed origin.
//
// Only declared-size images are touched, and that is the contract rather than a
// simplification. The declared size is the one piece of information that says how
// large the image will actually be drawn; without it the plugin would be guessing,
// and an image resized to a guess is worse than the original at every guess but one.
// It also means the page already reserves the right space, so nothing reflows when
// the smaller file arrives.
//
// This is a scanner over attributes, not an HTML parser. What it changes is one
// attribute value inside a tag it has already identified by name, and what it does
// with anything it does not fully recognise is nothing at all.
func (p *Plugin) rewriteImages(html []byte) []byte {
	out := make([]byte, 0, len(html))
	i := 0

	for {
		next := indexTag(html[i:], "img")
		if next < 0 {
			return append(out, html[i:]...)
		}
		start := i + next

		end := bytes.IndexByte(html[start:], '>')
		if end < 0 {
			return append(out, html[i:]...)
		}
		tag := html[start : start+end+1]

		out = append(out, html[i:start]...)
		out = append(out, p.rewriteTag(tag)...)
		i = start + end + 1
	}
}

// rewriteTag returns tag with its src replaced, or tag unchanged.
func (p *Plugin) rewriteTag(tag []byte) []byte {
	src, srcStart, srcEnd, ok := attribute(tag, "src")
	if !ok {
		return tag
	}
	width, okW := intAttribute(tag, "width")
	height, okH := intAttribute(tag, "height")
	if !okW || !okH || width <= 0 || height <= 0 {
		return tag
	}
	if _, allowed := p.cfg.allows(src); !allowed {
		return tag
	}

	format := p.formatFor(src)
	replacement := p.cfg.Prefix + format.name + "/" + p.signer.encode(token{Source: src, Width: width, Height: height})

	out := make([]byte, 0, len(tag)+len(replacement))
	out = append(out, tag[:srcStart]...)
	out = append(out, replacement...)
	out = append(out, tag[srcEnd:]...)
	return out
}

// indexTag finds the next "<name" that opens an element, returning its offset.
func indexTag(html []byte, name string) int {
	needle := "<" + name
	from := 0
	for {
		idx := indexFold(html[from:], needle)
		if idx < 0 {
			return -1
		}
		at := from + idx
		after := at + len(needle)
		// "<image" must not match "<img": the character after the name has to end
		// it.
		if after >= len(html) || isSpace(html[after]) || html[after] == '>' || html[after] == '/' {
			return at
		}
		from = after
	}
}

// attribute returns the value of name within tag, and the byte range the value
// occupies.
//
// Only quoted values are recognised. An unquoted one has no unambiguous end without
// a parser, and getting the end wrong corrupts the tag — so an unquoted src is left
// alone, which costs an optimisation and never costs a page.
func attribute(tag []byte, name string) (value string, start, end int, ok bool) {
	needle := name + "="
	from := 0
	for {
		idx := indexFold(tag[from:], needle)
		if idx < 0 {
			return "", 0, 0, false
		}
		at := from + idx
		// Must be preceded by whitespace, or "data-src=" matches "src=" and the
		// wrong attribute is rewritten.
		if at == 0 || !isSpace(tag[at-1]) {
			from = at + len(needle)
			continue
		}
		valueAt := at + len(needle)
		if valueAt >= len(tag) {
			return "", 0, 0, false
		}
		quote := tag[valueAt]
		if quote != '"' && quote != '\'' {
			return "", 0, 0, false
		}
		closing := bytes.IndexByte(tag[valueAt+1:], quote)
		if closing < 0 {
			return "", 0, 0, false
		}
		return string(tag[valueAt+1 : valueAt+1+closing]), valueAt + 1, valueAt + 1 + closing, true
	}
}

// intAttribute returns a numeric attribute's value.
//
// A dimension carrying a unit — "100%", "50vw" — is reported absent. It is not a
// pixel count, and resizing to a number that meant something else produces an image
// that is wrong at every viewport.
func intAttribute(tag []byte, name string) (int, bool) {
	raw, _, _, ok := attribute(tag, name)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return n, true
}

func indexFold(haystack []byte, needle string) int {
	return bytes.Index(bytes.ToLower(haystack), []byte(strings.ToLower(needle)))
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}
