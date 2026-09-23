package asset

import (
	"path"
	"strings"
)

// fingerprintLen is how many hex characters of the content hash a fingerprinted
// name carries.
//
// Sixteen, which is eight bytes. Long enough that a site would need on the order of
// a hundred million assets before a collision became likely, and short enough that
// the name is still a name — the point of these URLs is that a person reading their
// own HTML can tell which file is which.
const fingerprintLen = 16

// URL returns the content-addressed URL for name within this mount: the file's
// path with its content hash inserted before the extension, under the mount's
// prefix.
//
// This is the URL to link. A name derived from the bytes cannot describe anything
// but those bytes, which is what makes it safe to serve with "immutable" — the
// response never has to be revalidated, because a changed file is a changed name
// and the old one is simply never requested again. An ordinary name cannot be
// served that way at any max-age without eventually handing someone a stylesheet
// that no longer matches the page.
//
// A file that does not exist is an error rather than a URL, deliberately. The
// alternative is a page that renders successfully with a stylesheet that 404s,
// which is a broken page that reports itself as fine.
func (m *Mount) URL(name string) (string, error) {
	clean := path.Clean(name)
	tag, err := m.tags.get(m.fsys, clean)
	if err != nil {
		return "", err
	}

	hashed := insertFingerprint(clean, strings.Trim(tag, `"`)[:fingerprintLen])

	// Remembered because a static build has no other way to learn which names the
	// site links: it walks the mount's files, not its pages. Recording here — at
	// the moment a URL is minted — means the build writes exactly the
	// fingerprinted copies that something asked for, and no others.
	m.minted.Store(clean, hashed)

	return m.prefix + hashed, nil
}

// Fingerprinted returns every fingerprinted name this mount has produced, as a map
// from the file's own path to its fingerprinted one. A static build uses it to
// write the copies the rendered pages link.
func (m *Mount) Fingerprinted() map[string]string {
	out := make(map[string]string)
	m.minted.Range(func(key, value any) bool { // any: sync.Map's own signature
		out[key.(string)] = value.(string)
		return true
	})
	return out
}

// insertFingerprint puts hash before name's extension: "app.css" and "9f2a" become
// "app.9f2a.css". A name with no extension simply gains one.
func insertFingerprint(name, hash string) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + "." + hash + ext
}

// splitFingerprint separates a fingerprinted name into the file it names and the
// hash it carries. It reports false for a name that carries no fingerprint at all.
//
// It decides on shape alone — whether the component before the extension is
// exactly fingerprintLen hex characters — and says nothing about whether the hash
// is the right one. That question is the caller's, and it matters: serving a file
// under a hash that is not its own, with the year-long immutable lifetime a
// fingerprinted name earns, would pin one wrong URL in front of every client
// behind a shared cache.
func splitFingerprint(name string) (file, hash string, ok bool) {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)

	dot := strings.LastIndexByte(stem, '.')
	if dot < 0 {
		return "", "", false
	}
	candidate := stem[dot+1:]
	if len(candidate) != fingerprintLen || !isHex(candidate) {
		return "", "", false
	}
	return stem[:dot] + ext, candidate, true
}

func isHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isLower := c >= 'a' && c <= 'f'
		if !isDigit && !isLower {
			return false
		}
	}
	return true
}
