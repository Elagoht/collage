package types

import (
	"errors"
	"testing"
)

func TestRedirectTextError(t *testing.T) {
	tests := []struct {
		from, to string
		bad      bool
	}{
		{"/old", "/new", false},
		{"/old/{slug}", "https://example.com/x", false},
		{"/old\r\nX-Evil: 1", "/new", true},
		{"/old", "/new\n/evil 301", true},
		{"/old\t", "/new", true},
		{"/old", "/new\x7f", true},
		{"/old\u0085", "/new", true},
		{"/old", "/new\u2028", true},
		{"/old", "/new\u2029", true},
	}
	for _, test := range tests {
		err := RedirectTextError(test.from, test.to)
		if (err != nil) != test.bad {
			t.Errorf("RedirectTextError(%q, %q) = %v, want bad=%v", test.from, test.to, err, test.bad)
		}
		if err != nil && !errors.Is(err, ErrInvalidRedirect) {
			t.Errorf("error %v does not wrap ErrInvalidRedirect", err)
		}
	}
}
