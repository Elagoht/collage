package cache

import "testing"

func TestHost(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":         "example.com",
		"EXAMPLE.com:8080":    "example.com",
		"example.com.":        "example.com",
		"Example.COM.:443":    "example.com",
		"example.com:":        "example.com",
		"localhost:3000":      "localhost",
		"127.0.0.1:8080":      "127.0.0.1",
		"[::1]":               "[::1]",
		"[::1]:8080":          "[::1]",
		"[0:0::1]:9":          "[::1]",
		"[FE80::1]":           "[fe80::1]",
		"::1":                 "[::1]",
		"":                    "",
		"[::1":                "[::1",
		"[::1]x":              "[::1]x",
		"example.com:http":    "example.com:http",
		"[not-an-address]:80": "[not-an-address]",
	} {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}
