package plugin

import (
	"errors"
	"testing"
)

func TestParseOrigin(t *testing.T) {
	good := map[string]string{
		"https://example.com":       "https://example.com",
		"https://example.com/":      "https://example.com",
		"http://localhost:3000":     "http://localhost:3000",
		"https://acme.app.com:8443": "https://acme.app.com:8443",
	}
	for raw, want := range good {
		got, err := ParseOrigin(raw)
		if err != nil || got != want {
			t.Errorf("ParseOrigin(%q) = %q, %v; want %q, nil", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "example.com", "ftp://example.com", "https://", "https://u:p@example.com",
		"https://example.com/blog", "https://example.com?x=1", "https://example.com#top", "mailto:a@b.c",
	} {
		if got, err := ParseOrigin(raw); !errors.Is(err, ErrInvalidOrigin) {
			t.Errorf("ParseOrigin(%q) = %q, %v; want ErrInvalidOrigin", raw, got, err)
		}
	}
}
