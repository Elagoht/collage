// Package netaddr holds the address matching that more than one package needs:
// reading a request's direct peer and asking whether it is one of a set of
// trusted ranges. It imports nothing of collage's own, so the response layer
// and the forgery guard can both use it.
package netaddr

import (
	"net"
	"net/netip"
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
