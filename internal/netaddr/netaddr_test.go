package netaddr

import (
	"net/netip"
	"testing"
)

func TestFromTrusted(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")}
	for remote, want := range map[string]bool{
		"10.1.2.3:80":            true,
		"10.1.2.3":               true,
		"[::ffff:10.1.2.3]:80":   true,
		"[2001:db8::1%eth0]:443": true,
		"192.0.2.1:80":           false,
		"":                       false,
		"@":                      false,
	} {
		if got := FromTrusted(trusted, remote); got != want {
			t.Errorf("FromTrusted(%q) = %v, want %v", remote, got, want)
		}
	}
	if FromTrusted(nil, "10.1.2.3:80") {
		t.Error("an empty trusted set trusted a peer")
	}
}
