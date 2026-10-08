// Package devhost decides whether a request's Host header names this machine,
// which is what keeps a development server from answering a page on another
// site.
//
// A development server serves things a production one does not: error pages
// with stacks and source, the reload stream, a plugin's toolbar. A page on
// another site can make its own name resolve to 127.0.0.1 — DNS rebinding — and
// is then same-origin with the development server: it could read every page and
// post every form with a token it read. The Host header is the one thing it
// cannot choose; it is the attacker's own name. So a development server
// answers only Hosts that name this machine.
//
// Both "collage dev"'s proxy and an application running with DevMode on apply
// the same rule, from here.
package devhost

import (
	"net"
	"strings"

	"github.com/Elagoht/collage/internal/ascii"
)

// EnvHost is the environment variable "collage dev" sets on the program it
// runs: the host its proxy listens on, which is the Host the browser sends and
// the proxy passes on. The program itself listens on a loopback address of the
// proxy's choosing, so without this it would not know the name the proxy was
// started with, and would refuse what the proxy allowed.
const EnvHost = "COLLAGE_DEV_HOST"

// Allowed reports whether host, a request's Host header with or without a
// port, names this machine: localhost or a name under it, an IP address, or one
// of names — the host the server was told to listen on, given as a name or as
// "host:port".
//
// Any IP address is allowed, not only this machine's: a rebinding attacker's
// page is reached by its own name, never by an address, and a development
// server bound to 0.0.0.0 is meant to be opened by a phone on the LAN at its
// address.
func Allowed(host string, names ...string) bool {
	name := normalize(host)
	if name == "" {
		return false
	}
	if name == "localhost" || strings.HasSuffix(name, ".localhost") || net.ParseIP(name) != nil {
		return true
	}
	for _, n := range names {
		if n := normalize(n); n != "" && n == name {
			return true
		}
	}
	return false
}

// normalize is host without its port, brackets or trailing dot, in lower case.
func normalize(host string) string {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	return ascii.LowerString(strings.TrimSuffix(strings.Trim(name, "[]"), "."))
}
