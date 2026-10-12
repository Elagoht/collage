// Package netaddr holds the address matching that more than one package needs:
// reading a request's direct peer and asking whether it is one of a set of
// trusted ranges, and whether a request arrived over TLS. It imports nothing of
// collage's own but its ASCII helpers, so the response layer and the forgery
// guard can both use it.
package netaddr

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/Elagoht/collage/internal/ascii"
)

// Remote is the host of a request's RemoteAddr, with or without a port,
// unmapped and without a zone. It is the zero Addr when RemoteAddr holds no
// address — a Unix socket, a test that set something else.
func Remote(remoteAddr string) netip.Addr {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

// Contains reports whether a is in any of prefixes. The zero Addr is in none.
func Contains(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// FromTrusted reports whether the direct peer named by remoteAddr is one of
// trusted: the test for whether a header a proxy sets — X-Forwarded-For,
// X-Forwarded-Proto — was set by a proxy at all.
func FromTrusted(trusted []netip.Prefix, remoteAddr string) bool {
	return Contains(trusted, Remote(remoteAddr))
}

// HTTPS reports whether r arrived over TLS: on its own connection, or at a proxy
// in front that says so in X-Forwarded-Proto. A proxy that appends rather than
// replaces sends a list, "https, http", whose first entry is the one the
// reader's own connection used.
//
// With trusted proxies listed, the header is believed only from one of them:
// anything else that sends it is a client choosing what the server thinks of its
// own connection. With none listed it is believed from anyone, which is what an
// application behind a proxy it has not described needs, and what a forged
// header can only use against its own sender.
func HTTPS(trusted []netip.Prefix, r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if len(trusted) > 0 && !FromTrusted(trusted, r.RemoteAddr) {
		return false
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return ascii.EqualFold(strings.TrimSpace(first), "https")
}
