package build

import "testing"

// The root of a PrefixDefault export is the home page itself, so it names the
// prefixed address as canonical unless the page already names one.
func TestWithCanonical(t *testing.T) {
	const link = `<link rel="canonical" href="/en/">`
	for _, tc := range []struct{ name, in, want string }{
		{"head", `<html><head><title>x</title></head>`, `<html><head>` + link + `<title>x</title></head>`},
		{"upper case with attributes", `<HTML><HEAD lang="en"><title>x</title>`, `<HTML><HEAD lang="en">` + link + `<title>x</title>`},
		{"has one", `<head><link rel="canonical" href="/en/"></head>`, `<head><link rel="canonical" href="/en/"></head>`},
		{"has one, attributes swapped", `<head><link href="/en/" rel="canonical"></head>`, `<head><link href="/en/" rel="canonical"></head>`},
		{"no head", `<p>no head</p>`, `<p>no head</p>`},
		{"header is not head", `<header>x</header>`, `<header>x</header>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(withCanonical([]byte(tc.in), "/en/")); got != tc.want {
				t.Errorf("withCanonical(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
