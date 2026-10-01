package collagetest

import (
	"html"
	"strings"

	"github.com/Elagoht/collage/internal/ascii"
)

// form is one <form> of a page, as much of it as a submission needs: where it
// goes, how, and the hidden values it carries.
type form struct {
	action string
	// method and enctype are lowercased: "post", "multipart/form-data".
	method  string
	enctype string
	// hidden are the form's type=hidden inputs, in document order — the forgery
	// token, a plugin's stamp. Only these are carried into a submission: a visible
	// field is the test's to fill, and a field a bot would fill, such as a
	// honeypot's, must stay empty for the submission to be a reader's.
	hidden []field
}

// field is one name and value a form submits.
type field struct {
	name, value string
}

// scan returns every <form> in page, in document order, and every hidden input
// in it, inside a form or not.
//
// It is a scanner, not an HTML parser, and reads what a rendered page has: tags
// with quoted, unquoted or bare attributes, in any case. Comments and the bodies
// of <script>, <style>, <template> and <textarea> are skipped, so a form written
// out as text inside one is not mistaken for a form. Forms do not nest in HTML, so
// a form runs to the next </form>, or to the end of the page.
func scan(page string) ([]form, []field) {
	var out []form
	var loose []field
	var current *form
	for pos := 0; pos < len(page); {
		lt := strings.IndexByte(page[pos:], '<')
		if lt < 0 {
			break
		}
		pos += lt
		rest := page[pos:]

		if strings.HasPrefix(rest, "<!--") {
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				break
			}
			pos += 4 + end + 3
			continue
		}

		closing := strings.HasPrefix(rest, "</")
		nameStart := 1
		if closing {
			nameStart = 2
		}
		name, after := tagName(rest[nameStart:])
		if name == "" {
			pos++
			continue
		}
		attrs, n := attributes(after)
		pos += len(rest) - len(after) + n

		switch {
		case closing && name == "form":
			if current != nil {
				out = append(out, *current)
				current = nil
			}
		case closing:
		case name == "form":
			if current != nil {
				out = append(out, *current)
			}
			current = &form{
				action:  attrs["action"],
				method:  ascii.LowerString(attrs["method"]),
				enctype: ascii.LowerString(attrs["enctype"]),
			}
		case name == "input":
			if !ascii.EqualFold(attrs["type"], "hidden") || attrs["name"] == "" {
				break
			}
			if _, disabled := attrs["disabled"]; disabled {
				break
			}
			hidden := field{name: attrs["name"], value: attrs["value"]}
			loose = append(loose, hidden)
			if current != nil {
				current.hidden = append(current.hidden, hidden)
			}
		case name == "script", name == "style", name == "template", name == "textarea":
			end := indexFold(page[pos:], "</"+name)
			if end < 0 {
				pos = len(page)
				continue
			}
			pos += end
		}
	}
	if current != nil {
		out = append(out, *current)
	}
	return out, loose
}

// tagName reads the element name at the start of s, lowercased, and returns what
// follows it. A '<' not followed by a letter starts no tag, and the name is empty.
func tagName(s string) (string, string) {
	end := 0
	for end < len(s) && isNameByte(s[end], end == 0) {
		end++
	}
	return ascii.LowerString(s[:end]), s[end:]
}

// isNameByte reports whether b belongs in an element name; the first byte must be
// a letter.
func isNameByte(b byte, first bool) bool {
	if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
		return true
	}
	return !first && (b >= '0' && b <= '9' || b == '-')
}

// attributes reads a tag's attributes from s, which starts just after its name,
// up to and including the '>' that ends it. It returns them by lowercased name,
// their values unescaped, and how many bytes of s the tag took. A bare attribute,
// such as disabled, is present with an empty value. The first of a repeated name
// wins, as it does in a browser.
func attributes(s string) (map[string]string, int) {
	attrs := make(map[string]string)
	i := 0
	for i < len(s) {
		for i < len(s) && (isSpace(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '>' {
			return attrs, i + 1
		}

		start := i
		for i < len(s) && !isSpace(s[i]) && s[i] != '=' && s[i] != '>' && s[i] != '/' {
			i++
		}
		name := ascii.LowerString(s[start:i])
		for i < len(s) && isSpace(s[i]) {
			i++
		}

		value := ""
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && isSpace(s[i]) {
				i++
			}
			switch {
			case i < len(s) && (s[i] == '"' || s[i] == '\''):
				quote := s[i]
				end := strings.IndexByte(s[i+1:], quote)
				if end < 0 {
					value, i = s[i+1:], len(s)
				} else {
					value, i = s[i+1:i+1+end], i+1+end+1
				}
			default:
				start := i
				for i < len(s) && !isSpace(s[i]) && s[i] != '>' {
					i++
				}
				value = s[start:i]
			}
		}
		if _, seen := attrs[name]; !seen && name != "" {
			attrs[name] = html.UnescapeString(value)
		}
	}
	return attrs, len(s)
}

// isSpace reports whether b is HTML whitespace.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

// indexFold is strings.Index ignoring ASCII case, for the end tag of an element
// whose content is skipped. ASCII only, so every index it finds is one into s.
func indexFold(s, substr string) int {
	n := len(substr)
	for i := 0; i+n <= len(s); i++ {
		if ascii.EqualFold(s[i:i+n], substr) {
			return i
		}
	}
	return -1
}
