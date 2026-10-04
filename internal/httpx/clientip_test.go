package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        []string
		trusted    []string
		want       string
	}{
		{"no trusted proxies: a forged header is ignored", "1.2.3.4:5", []string{"9.9.9.9"}, nil, "1.2.3.4"},
		{"behind a trusted proxy", "10.0.0.1:5", []string{"9.9.9.9"}, []string{"10.0.0.0/8"}, "9.9.9.9"},
		{"the client's forged entry does not win", "10.0.0.1:5", []string{"6.6.6.6, 9.9.9.9, 10.0.0.2"}, []string{"10.0.0.0/8"}, "9.9.9.9"},
		{"every hop trusted: the leftmost", "10.0.0.1:5", []string{"10.0.0.3, 10.0.0.2"}, []string{"10.0.0.0/8"}, "10.0.0.3"},
		{"two header lines are one list", "10.0.0.1:5", []string{"8.8.8.8", "9.9.9.9"}, []string{"10.0.0.0/8"}, "9.9.9.9"},
		{"the walk stops at garbage", "10.0.0.1:5", []string{"8.8.8.8, garbage, 10.0.0.2"}, []string{"10.0.0.0/8"}, "10.0.0.2"},
		{"only garbage: the proxy", "10.0.0.1:5", []string{"garbage"}, []string{"10.0.0.0/8"}, "10.0.0.1"},
		{"an empty entry ends the walk", "10.0.0.1:5", []string{"9.9.9.9, "}, []string{"10.0.0.0/8"}, "10.0.0.1"},
		{"an IPv4-mapped address is unmapped", "[::ffff:1.2.3.4]:5", nil, nil, "1.2.3.4"},
		{"IPv6 loopback", "[::1]:8080", nil, nil, "::1"},
		{"the zone is dropped", "[fe80::1%eth0]:80", nil, nil, "fe80::1"},
		{"no port", "1.2.3.4", nil, nil, "1.2.3.4"},
		{"garbage", "garbage", nil, nil, ""},
		{"empty", "", nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != nil {
				r.Header["X-Forwarded-For"] = tt.xff
			}
			if len(tt.trusted) > 0 {
				prefixes, err := ParseTrustedProxies(tt.trusted)
				if err != nil {
					t.Fatalf("ParseTrustedProxies: %v", err)
				}
				r = r.WithContext(WithTrustedProxies(r.Context(), prefixes))
			}
			got := ClientIP(r)
			if tt.want == "" {
				if got.IsValid() {
					t.Errorf("ClientIP() = %v, want the zero Addr", got)
				}
				return
			}
			if got.String() != tt.want {
				t.Errorf("ClientIP() = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies([]string{"10.0.0.0/8", "127.0.0.1", "::1", "fd00::/8", "::ffff:10.1.2.3", "10.1.2.3/8", "::ffff:10.0.0.0/104"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	want := []string{"10.0.0.0/8", "127.0.0.1/32", "::1/128", "fd00::/8", "10.1.2.3/32", "10.0.0.0/8", "10.0.0.0/8"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("entry %d = %v, want %s", i, got[i], want[i])
		}
	}

	for _, bad := range []string{"10.0.0.0/33", "nope", "::ffff:1.2.3.4/80", "fe80::1%eth0"} {
		_, err := ParseTrustedProxies([]string{"10.0.0.0/8", bad})
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("ParseTrustedProxies(%q) = %v, want an error naming it", bad, err)
		}
	}
}
