package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type trustedProxiesKey struct{}

// ParseTrustedProxies parses ServerConfig.TrustedProxies: each entry is an address
// ("127.0.0.1", which becomes a /32, or "::1", a /128) or a CIDR range
// ("10.0.0.0/8"). An IPv4-mapped IPv6 entry is unmapped, so it matches the
// unmapped addresses ClientIP compares against, and a range is masked to its
// network. The error names the first entry that is neither.
func ParseTrustedProxies(entries []string) ([]netip.Prefix, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		p, ok := parseTrustedProxy(strings.TrimSpace(e))
		if !ok {
			return nil, fmt.Errorf("collage: Server.TrustedProxies: %q is not an address or CIDR range", e)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}

func parseTrustedProxy(e string) (netip.Prefix, bool) {
	if strings.Contains(e, "/") {
		p, err := netip.ParsePrefix(e)
		if err != nil {
			return netip.Prefix{}, false
		}
		addr, bits := p.Addr(), p.Bits()
		if addr.Is4In6() {
			// "::ffff:10.0.0.0/104" is "10.0.0.0/8". A mapped range shorter than
			// the 96 bits of the mapping prefix is not an IPv4 range at all: masked,
			// it would cover IPv6 space the entry never named, so it is refused.
			if bits < 96 {
				return netip.Prefix{}, false
			}
			addr, bits = addr.Unmap(), bits-96
		}
		return netip.PrefixFrom(addr, bits).Masked(), true
	}
	addr, err := netip.ParseAddr(e)
	if err != nil || addr.Zone() != "" {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), true
}

// WithTrustedProxies returns ctx carrying the proxies ClientIP believes
// X-Forwarded-For from.
func WithTrustedProxies(ctx context.Context, trusted []netip.Prefix) context.Context {
	return context.WithValue(ctx, trustedProxiesKey{}, trusted)
}

// ClientIP is the address of the client r comes from: RemoteAddr's host, unless
// that is one of the trusted proxies r's context carries. Then it is the first
// untrusted address in X-Forwarded-For read from the right — every value, in
// order, comma-separated — or the leftmost when every one is trusted. An entry
// may carry a port ("9.9.9.9:4567", "[2001:db8::1]:443") or brackets
// ("[2001:db8::1]"). An entry that is still not an address ("unknown") ends the
// walk before any untrusted address was found, so the client is unknown and the
// result is the zero Addr: answering with a trusted proxy would make every
// visitor one client. The result is unmapped and has no zone; it is the zero
// Addr too when RemoteAddr holds no address.
func ClientIP(r *http.Request) netip.Addr {
	remote := parseRemoteAddr(r.RemoteAddr)
	trusted, _ := r.Context().Value(trustedProxiesKey{}).([]netip.Prefix)
	if !remote.IsValid() || !containsAddr(trusted, remote) {
		return remote
	}
	client := remote
	values := r.Header.Values("X-Forwarded-For")
	for i := len(values) - 1; i >= 0; i-- {
		// Walk the line from its end, entry by entry, without splitting it whole:
		// the client is usually the last entry or close to it.
		line := values[i]
		for {
			entry := line
			comma := strings.LastIndexByte(line, ',')
			if comma >= 0 {
				entry, line = line[comma+1:], line[:comma]
			}
			a, ok := parseForwardedAddr(entry)
			if !ok {
				return netip.Addr{}
			}
			client = a
			if !containsAddr(trusted, client) {
				return client
			}
			if comma < 0 {
				break
			}
		}
	}
	return client
}

// parseForwardedAddr reads one X-Forwarded-For entry: an address, an address
// with a port, or an IPv6 address in brackets, with or without a port. The
// result is unmapped and has no zone.
func parseForwardedAddr(entry string) (netip.Addr, bool) {
	entry = strings.TrimSpace(entry)
	if a, err := netip.ParseAddr(entry); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(entry); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if len(entry) > 2 && entry[0] == '[' && entry[len(entry)-1] == ']' {
		if a, err := netip.ParseAddr(entry[1 : len(entry)-1]); err == nil && a.Is6() {
			return a.Unmap().WithZone(""), true
		}
	}
	return netip.Addr{}, false
}

// parseRemoteAddr is RemoteAddr's host, with or without a port.
func parseRemoteAddr(remoteAddr string) netip.Addr {
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

func containsAddr(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
