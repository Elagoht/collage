package cache

import (
	"net/netip"
	"strings"

	"github.com/Elagoht/collage/internal/ascii"
)

// Host returns the one spelling of a request's Host a cached render is keyed on,
// and made for: lower-case, with no port, no trailing dot, and an IPv6 address in
// brackets, in its canonical text. "EXAMPLE.com:8080", "example.com." and
// "example.com" are one host; "[0:0::1]:9" and "[::1]" are one address.
//
// The port goes because a server answers one site whatever port a client names
// in Host — a client chooses it, and keeping it made every port a render and an
// entry of its own. Code that builds an absolute URL for a site on a port that
// is not the scheme's default reads it from Config.BaseURL, not from the request.
//
// A value that is not a host — empty, or holding a byte no host has — is returned
// lower-cased and otherwise as given: it is still one key, and only a request that
// spells it exactly the same way shares it.
func Host(host string) string {
	host = ascii.LowerString(host)
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return host
		}
		rest := host[end+1:]
		if rest != "" && !isPort(rest) {
			return host
		}
		if addr, err := netip.ParseAddr(host[1:end]); err == nil && addr.Is6() {
			return "[" + addr.String() + "]"
		}
		return host[:end+1]
	}
	if strings.Count(host, ":") > 1 {
		// A bare IPv6 address, which Host should have bracketed.
		if addr, err := netip.ParseAddr(host); err == nil && addr.Is6() {
			return "[" + addr.String() + "]"
		}
		return host
	}
	if colon := strings.LastIndexByte(host, ':'); colon >= 0 && isPort(host[colon:]) {
		host = host[:colon]
	}
	return strings.TrimSuffix(host, ".")
}

// isPort reports whether s is ":" followed by nothing but digits, an empty port
// included, as net/http accepts "host:".
func isPort(s string) bool {
	if s == "" || s[0] != ':' {
		return false
	}
	for _, c := range []byte(s[1:]) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
