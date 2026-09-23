package minimizer

import (
	"bytes"
	"encoding/json"
	"strings"
)

// This file is the whole of the minification. Everything it does is conservative
// by construction, and that is a decision rather than a limitation.
//
// A correct minifier for HTML, CSS or JavaScript is a parser. Minifiers that are
// not parsers — the regex kind — work on almost every input and corrupt the rest
// silently: a regex literal containing "//", an apostrophe inside a CSS content
// string, a newline JavaScript needed for automatic semicolon insertion. Corrupting
// one page in a thousand, with no error anywhere, is far worse than saving less.
//
// So these are scanners, not parsers. They know where strings, comments and other
// protected regions begin and end, and outside those regions they remove only what
// cannot carry meaning. JavaScript in particular keeps every newline, because
// removing one can change what a program means; what goes is indentation, trailing
// space, and comments.

// minifyJSON compacts JSON exactly, via the standard library. This is the only one
// of the four that is lossless by construction rather than by care: encoding/json
// knows the grammar completely.
func minifyJSON(src []byte) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, src); err != nil {
		// Not valid JSON. Whatever it is, it is not this function's business to
		// guess at, and returning it untouched is the only safe answer.
		return src
	}
	return out.Bytes()
}

// protectedHTML are elements whose text content is significant to the byte. Inside
// them nothing is collapsed: <pre> and <textarea> render their whitespace, and
// <script> and <style> hold other languages entirely.
var protectedHTML = []string{"pre", "textarea", "script", "style"}

// minifyHTML removes comments and collapses runs of whitespace.
//
// Whitespace in HTML is not free to delete: a run of it between two inline elements
// renders as a single space, and removing it joins two words. So a run is collapsed
// to one space rather than removed — except where it sits between a ">" and a "<"
// with nothing else in it, which renders as nothing and can go entirely.
//
// Conditional comments are kept. They are comments to a parser and instructions to
// the browsers that read them.
func minifyHTML(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0

	for i < len(src) {
		// A protected element copies through verbatim, opening tag to closing tag.
		if src[i] == '<' {
			if name, end, ok := openingTagName(src, i); ok && contains(protectedHTML, name) {
				closing := "</" + name
				stop := indexFoldFrom(src, closing, end)
				if stop < 0 {
					out = append(out, src[i:]...)
					return out
				}
				tagEnd := bytes.IndexByte(src[stop:], '>')
				if tagEnd < 0 {
					out = append(out, src[i:]...)
					return out
				}
				out = append(out, src[i:stop+tagEnd+1]...)
				i = stop + tagEnd + 1
				continue
			}
		}

		// Comments, except the conditional kind.
		if bytes.HasPrefix(src[i:], []byte("<!--")) {
			end := bytes.Index(src[i:], []byte("-->"))
			if end < 0 {
				out = append(out, src[i:]...)
				return out
			}
			comment := src[i : i+end+3]
			if bytes.HasPrefix(comment, []byte("<!--[if")) || bytes.Contains(comment, []byte("<![endif]")) {
				out = append(out, comment...)
			}
			i += end + 3
			continue
		}

		if isSpace(src[i]) {
			j := i
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			// Between tags the run renders as nothing; anywhere else it renders as
			// one space, and deleting it would join two words.
			between := len(out) > 0 && out[len(out)-1] == '>' && j < len(src) && src[j] == '<'
			if !between {
				out = append(out, ' ')
			}
			i = j
			continue
		}

		out = append(out, src[i])
		i++
	}
	return out
}

// minifyCSS removes comments and collapses whitespace, leaving strings alone.
//
// Whitespace inside a CSS string is content — content: "a  b" is two spaces — and
// whitespace between two values is a separator that cannot be dropped, so runs
// collapse to a single space rather than vanishing.
//
// The spaces around punctuation stay too, which is where most of the remaining
// saving would be. "a :hover" and "a:hover" are a descendant selector and a
// pseudo-class, and telling a selector's colon from a declaration's requires
// knowing which side of the brace you are on — which requires parsing. The saving
// is not worth changing which elements a stylesheet applies to.
func minifyCSS(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0

	for i < len(src) {
		switch {
		case src[i] == '"' || src[i] == '\'':
			end := scanCSSString(src, i)
			out = append(out, src[i:end]...)
			i = end

		case bytes.HasPrefix(src[i:], []byte("/*")):
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return out
			}
			i += 2 + end + 2
			// A comment can sit between two whitespace runs. Removing it leaves
			// them adjacent, and neither run's own pass will see the other.
			for i < len(src) && isSpace(src[i]) && len(out) > 0 && out[len(out)-1] == ' ' {
				i++
			}

		case isSpace(src[i]):
			j := i
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			if len(out) > 0 && j < len(src) {
				out = append(out, ' ')
			}
			i = j

		default:
			out = append(out, src[i])
			i++
		}
	}
	return out
}

// minifyJS removes comments and horizontal whitespace, and keeps every newline.
//
// The newlines stay because JavaScript inserts semicolons at them. "return\n  x" is
// "return; x", and joining those two lines changes what the program does. Deciding
// which newlines are safe to remove requires knowing where every statement ends,
// which requires a parser — so this does not try, and what it saves is indentation
// and comments rather than line breaks.
//
// It tracks strings, template literals and regular expression literals, because
// each of them can contain the character sequences that start a comment. A minifier
// that treats "https://example.com" inside a string as the start of a line comment
// deletes the rest of the line.
func minifyJS(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0

	for i < len(src) {
		switch {
		case src[i] == '"' || src[i] == '\'' || src[i] == '`':
			end := scanJSString(src, i)
			out = append(out, src[i:end]...)
			i = end

		case bytes.HasPrefix(src[i:], []byte("//")):
			end := bytes.IndexByte(src[i:], '\n')
			if end < 0 {
				return out
			}
			i += end // leave the newline: it may be terminating a statement

		case bytes.HasPrefix(src[i:], []byte("/*")):
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return out
			}
			// A block comment spanning lines stood where a newline stood, and the
			// newline may have been terminating a statement.
			if bytes.Contains(src[i:i+2+end+2], []byte("\n")) {
				out = append(out, '\n')
			}
			i += 2 + end + 2

		case src[i] == '/' && regexCanStartHere(out):
			end := scanJSRegex(src, i)
			out = append(out, src[i:end]...)
			i = end

		case src[i] == ' ' || src[i] == '\t' || src[i] == '\r':
			j := i
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r') {
				j++
			}
			// A run at the start of a line, or before one, carries nothing.
			trailing := j < len(src) && src[j] == '\n'
			leading := len(out) == 0 || out[len(out)-1] == '\n'
			if !trailing && !leading {
				out = append(out, ' ')
			}
			i = j

		default:
			out = append(out, src[i])
			i++
		}
	}
	return out
}

// scanCSSString returns the index just past the string literal starting at i.
func scanCSSString(src []byte, i int) int {
	quote := src[i]
	j := i + 1
	for j < len(src) {
		if src[j] == '\\' {
			j += 2
			continue
		}
		if src[j] == quote {
			return j + 1
		}
		j++
	}
	return len(src)
}

// scanJSString returns the index just past the string or template literal starting
// at i. Template literals nest expressions, and an expression can hold another
// template literal, so the nesting is counted rather than assumed away.
func scanJSString(src []byte, i int) int {
	quote := src[i]
	j := i + 1
	depth := 0
	for j < len(src) {
		switch {
		case src[j] == '\\':
			j += 2
			continue
		case quote == '`' && src[j] == '$' && j+1 < len(src) && src[j+1] == '{':
			depth++
			j += 2
			continue
		case quote == '`' && depth > 0 && src[j] == '}':
			depth--
			j++
			continue
		case depth == 0 && src[j] == quote:
			return j + 1
		}
		j++
	}
	return len(src)
}

// scanJSRegex returns the index just past the regular expression literal starting
// at i, including its flags. A character class may hold an unescaped "/", so the
// class is tracked.
func scanJSRegex(src []byte, i int) int {
	j := i + 1
	inClass := false
	for j < len(src) {
		switch {
		case src[j] == '\\':
			j += 2
			continue
		case src[j] == '[':
			inClass = true
		case src[j] == ']':
			inClass = false
		case src[j] == '/' && !inClass:
			j++
			for j < len(src) && isLetter(src[j]) {
				j++
			}
			return j
		case src[j] == '\n':
			// A regex literal cannot span lines, so this was a division after all.
			return i + 1
		}
		j++
	}
	return len(src)
}

// regexCanStartHere decides whether a "/" begins a regular expression literal or is
// a division operator, from the last significant character emitted.
//
// This is the classic ambiguity in JavaScript's grammar and it cannot be resolved
// perfectly without parsing. The heuristic is the usual one: a "/" after a value —
// an identifier, a number, a closing bracket — divides; anywhere else it opens a
// regex. It errs toward treating "/" as division, which leaves a regex unscanned
// and its contents merely copied through unchanged rather than mangled.
func regexCanStartHere(out []byte) bool {
	for i := len(out) - 1; i >= 0; i-- {
		c := out[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if isLetter(c) || isDigit(c) || c == ')' || c == ']' || c == '}' || c == '_' || c == '$' {
			return false
		}
		return true
	}
	return true
}

func isSpace(c byte) bool  { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// openingTagName returns the lowercased element name of the tag starting at i and
// the index just past its ">".
func openingTagName(src []byte, i int) (string, int, bool) {
	j := i + 1
	if j >= len(src) || !isLetter(src[j]) {
		return "", 0, false
	}
	start := j
	for j < len(src) && (isLetter(src[j]) || isDigit(src[j])) {
		j++
	}
	name := strings.ToLower(string(src[start:j]))
	end := bytes.IndexByte(src[j:], '>')
	if end < 0 {
		return "", 0, false
	}
	return name, j + end + 1, true
}

// indexFoldFrom finds needle in src at or after from, ignoring ASCII case.
func indexFoldFrom(src []byte, needle string, from int) int {
	lower := bytes.ToLower(src[from:])
	idx := bytes.Index(lower, []byte(strings.ToLower(needle)))
	if idx < 0 {
		return -1
	}
	return from + idx
}
