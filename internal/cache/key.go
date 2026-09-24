package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strings"
)

// KeyInput identifies one cacheable render: the request path, resolved locale,
// request parameters, and any extra cache dimensions a caller wants to vary on.
type KeyInput struct {
	// Path is the request path being rendered.
	Path string
	// Locale is the resolved locale for the render.
	Locale string
	// Params are request parameters that affect the rendered output, keyed by
	// parameter name. Iteration order does not matter: Key sorts by key before
	// hashing.
	Params map[string]string
	// Vary lists extra cache-key discriminators beyond Path, Locale, and Params —
	// e.g. a plugin's own cache dimension. Order does not matter: Key sorts before
	// hashing.
	Vary []string
	// Request lists the dimensions the application declared for this request,
	// through collage.Vary: a language its middleware resolved, a tier its auth
	// layer read. It is a section of its own rather than more Vary entries,
	// because Vary carries the raw query — and a query is chosen by whoever sent
	// the request, so sharing a section would let one spell another's key.
	Request []string
}

// Key returns the canonical cache key for in: the hex-encoded SHA-256 of a
// length-prefixed, unambiguous serialisation of Path, Locale, Params (sorted by
// key), Vary (sorted) and Request (sorted), prefixed "v1:" so the scheme can be changed later
// without colliding with keys produced by this one.
//
// Every string field is written as its own decimal byte length followed by ':' and
// the bytes themselves (fmt.Fprintf(h, "%d:%s", len(s), s)), and the number of
// Params entries and the number of Vary entries are each written the same way, as a
// decimal count followed by ':'. Because every field and every section is
// self-delimiting this way, the mapping from KeyInput to hash input is injective:
// no two distinct inputs can ever serialise to the same byte stream — neither a
// case like {Path: "/ab", Locale: "c"} versus {Path: "/a", Locale: "bc"} (defeated
// by length-prefixing Path and Locale themselves), nor a value moved from Params
// into Vary, or vice versa (defeated by length-prefixing the Params and Vary
// counts, which fixes exactly how many fields belong to each section).
func Key(in KeyInput) string {
	h := sha256.New()

	writeField(h, in.Path)
	writeField(h, in.Locale)

	paramKeys := make([]string, 0, len(in.Params))
	for k := range in.Params {
		paramKeys = append(paramKeys, k)
	}
	sort.Strings(paramKeys)
	fmt.Fprintf(h, "%d:", len(paramKeys))
	for _, k := range paramKeys {
		writeField(h, k)
		writeField(h, in.Params[k])
	}

	vary := append([]string(nil), in.Vary...)
	sort.Strings(vary)
	fmt.Fprintf(h, "%d:", len(vary))
	for _, v := range vary {
		writeField(h, v)
	}

	request := append([]string(nil), in.Request...)
	sort.Strings(request)
	fmt.Fprintf(h, "%d:", len(request))
	for _, v := range request {
		writeField(h, v)
	}

	return "v1:" + hex.EncodeToString(h.Sum(nil))
}

// writeField writes s to h as its own decimal byte length, ':', then s, making the
// field self-delimiting so it cannot be confused with an adjacent field regardless
// of either one's content.
func writeField(h hash.Hash, s string) {
	fmt.Fprintf(h, "%d:%s", len(s), s)
}

// ETag returns a strong HTTP ETag for content: the double-quoted hex encoding of
// the first 16 bytes of its SHA-256 hash. The surrounding quotes are part of the
// return value because the HTTP ETag grammar requires them.
func ETag(content []byte) string {
	sum := sha256.Sum256(content)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// ETagMatch reports whether etag satisfies the If-None-Match request header value
// ifNoneMatch: true when ifNoneMatch, trimmed of surrounding whitespace, is exactly
// "*", or when it is a comma-separated list containing an item equal to etag once
// that item is trimmed of surrounding whitespace and any leading weak "W/" prefix.
// etag itself is always compared as a strong tag, exactly as returned by ETag —
// only the request's side of the comparison is weakened. Malformed input, such as
// empty items between stray commas or a missing quote, simply never matches; it
// never panics.
func ETagMatch(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" || etag == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}
