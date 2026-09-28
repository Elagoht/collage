// Package ascii is the case folding collage uses on anything a request carries:
// a path, a header, a host, the markup a page rendered.
//
// ASCII letters only, because Unicode folding is not what these comparisons
// mean. strings.EqualFold says the Kelvin sign is "k" and the long s is "s", so
// a check on a name the client sent treats two different names as one; and
// bytes.ToLower does not keep lengths — Turkish İ is two bytes and lowercases to
// i, one — so an index found in the lowered copy lands somewhere else in the
// original. Every protocol token collage folds (a tag, a scheme, a host, an
// element name) is ASCII, and folding only ASCII is folding exactly those.
package ascii

// Lower returns a copy of b with only its ASCII letters lowercased, so every
// index into it is an index into b.
func Lower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = lower(c)
	}
	return out
}

// LowerString is Lower for a string.
func LowerString(s string) string {
	return string(Lower([]byte(s)))
}

// EqualFold reports whether a and b are equal with ASCII letters compared
// without case, and every other byte compared as it is.
func EqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lower(a[i]) != lower(b[i]) {
			return false
		}
	}
	return true
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
