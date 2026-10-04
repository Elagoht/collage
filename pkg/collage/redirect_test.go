package collage_test

import (
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestSafeRedirect(t *testing.T) {
	cases := []struct{ next, fallback, want string }{
		{"/panel", "/", "/panel"},
		{"/panel?tab=2#x", "/", "/panel?tab=2#x"},
		{"/çay", "/", "/çay"},
		{"//evil.com", "/home", "/home"},
		{`/\evil.com`, "/home", "/home"},
		{"https://evil.com", "/home", "/home"},
		{"javascript:alert(1)", "/home", "/home"},
		{"/a\nb", "/home", "/home"},
		{"/a\x7fb", "/home", "/home"},
		{"", "/home", "/home"},
		{"panel", "/home", "/home"},
		{"//evil.com", "https://evil.com", "/"},
		{"", "", "/"},
	}
	for _, c := range cases {
		if got := collage.SafeRedirect(c.next, c.fallback); got != c.want {
			t.Errorf("SafeRedirect(%q, %q) = %q, want %q", c.next, c.fallback, got, c.want)
		}
	}
}
