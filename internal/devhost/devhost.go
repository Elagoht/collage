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

// EnvHost is the environment variable naming the extra hosts a development
// server answers: a comma-separated list, such as "app,mybox.local" for a
// docker-compose service name and a LAN name. Both "collage dev"'s proxy and an
// application in development mode read it.
//
// "collage dev" also sets it on the program it runs, adding the host its proxy
// listens on: the program listens on a loopback address of the proxy's
// choosing but sees the browser's Host, the proxy's, and would otherwise
// refuse what the proxy allowed.
const EnvHost = "COLLAGE_DEV_HOST"

// ExposureWarning is what a development server logs when it is bound where
// other machines can reach it — the application's own server and "collage
// dev"'s proxy say the same thing.
const ExposureWarning = "collage: development mode is listening on an address other machines can reach; " +
	"its error pages and development tooling expose the application's internals to them. " +
	"Bind a loopback address (Server.Host \"localhost\") or turn DevMode off"

// Names splits an EnvHost value into its names, dropping blanks.
func Names(value string) []string {
	var names []string
	for _, n := range strings.Split(value, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// reservedSuffixes are the names RFC 2606 and RFC 6761 set aside: no registry
// will ever delegate them, so no attacker can make one resolve to this machine
// from the public DNS. They are what tests and examples use — httptest's
// requests are for example.com — and what a developer puts in /etc/hosts.
var reservedSuffixes = []string{".example", ".test", ".invalid"}

// reservedDomains are the three second-level names RFC 2606 reserves; they and
// every name under them are allowed.
var reservedDomains = []string{"example.com", "example.net", "example.org"}

// reserved reports whether name is a reserved name. See reservedSuffixes.
func reserved(name string) bool {
	for _, suffix := range reservedSuffixes {
		if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
			return true
		}
	}
	for _, domain := range reservedDomains {
		if name == domain || strings.HasSuffix(name, "."+domain) {
			return true
		}
	}
	return false
}

// Allowed reports whether host, a request's Host header with or without a
// port, names this machine: localhost or a name under it, an IP address, a
// reserved name (example.com, *.test, …: see reservedSuffixes), or one of
// names — the host the server was told to listen on and those in EnvHost,
// each given as a name or as "host:port".
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
	if name == "localhost" || strings.HasSuffix(name, ".localhost") || net.ParseIP(name) != nil || reserved(name) {
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
