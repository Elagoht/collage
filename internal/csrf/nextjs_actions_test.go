package csrf

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Properties Next.js pins for its server actions' origin check, checked here
// against the forgery guard that plays the same part.

// trustedGuard returns a guard trusting origins, failing the test if New refuses.
func trustedGuard(t *testing.T, origins ...string) *Guard {
	t.Helper()
	g, err := New(Config{Key: []byte("a key of some length"), TrustedOrigins: origins})
	if err != nil {
		t.Fatalf("New(%q) = %v, want nil", origins, err)
	}
	return g
}

// Next.js: e2e/app-dir/actions-allowed-origins/app-action-opaque-origin.test.ts.
// A sandboxed iframe or a data: URL posts with Origin "null": an origin that
// is no one's, which an attacker can produce at will. It matches no Host, so it
// is refused with the pair intact, whether or not the browser also marks it.
func TestNextjs_AnOpaqueOriginIsRefused(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	for name, headers := range map[string]map[string]string{
		"an older browser": {"Origin": "null"},
		"marked":           {"Origin": "null", "Sec-Fetch-Site": "cross-site"},
	} {
		req := postWith(t, token, token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if err := g.Verify(req); !errors.Is(err, ErrCrossOrigin) {
			t.Errorf("%s: Verify() = %v, want ErrCrossOrigin", name, err)
		}
	}
}

// Next.js: e2e/app-dir/actions-allowed-origins/app-action-opaque-origin.test.ts.
// "null" cannot be named as a trusted origin: trusting it would trust every
// sandboxed page on the web.
func TestNextjs_AnOpaqueOriginCannotBeTrusted(t *testing.T) {
	if _, err := New(Config{Key: []byte("k"), TrustedOrigins: []string{"null"}}); err == nil {
		t.Fatal(`New() accepted "null" as a trusted origin`)
	}
}

// Next.js: e2e/app-dir/actions-allowed-origins/app-action-disallowed-origins.test.ts.
// X-Forwarded-Host is a header the client writes. Were it compared with Origin
// instead of Host, a forger would send both and have them agree.
func TestNextjs_ForwardedHostDoesNotVouchForAnOrigin(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	req := postWith(t, token, token)
	req.Host = "example.com"
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("Origin", "https://evil.example")
	if err := g.Verify(req); !errors.Is(err, ErrCrossOrigin) {
		t.Fatalf("Verify() = %v, want ErrCrossOrigin", err)
	}
}

// Next.js: packages/next/src/server/app-render/csrf-protection.test.ts
// ("should correctly handle origins that don't have a TLD", exact match).
// A trusted origin is that origin: not its subdomains, not the same host over
// another scheme or port. Each of those is a different party.
func TestNextjs_ATrustedOriginTrustsOnlyItself(t *testing.T) {
	g := trustedGuard(t, "https://partner.test")
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	for origin, want := range map[string]bool{
		"https://partner.test":      true,
		"https://sub.partner.test":  false,
		"http://partner.test":       false,
		"https://partner.test:8443": false,
		"https://partner.test.evil": false,
		"https://evilpartner.test":  false,
	} {
		req := postWith(t, token, token)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", origin)
		err := g.Verify(req)
		if want && err != nil {
			t.Errorf("Origin %s: Verify() = %v, want nil", origin, err)
		}
		if !want && !errors.Is(err, ErrCrossOrigin) {
			t.Errorf("Origin %s: Verify() = %v, want ErrCrossOrigin", origin, err)
		}
	}
}

// Next.js: packages/next/src/server/app-render/csrf-protection.test.ts
// ("wildcards are only supported below the domain level", "empty string").
// An entry that would trust everything, or that names nothing, is refused when
// the guard is built rather than quietly doing either.
func TestNextjs_ATrustedOriginThatNamesEverythingOrNothingIsRefused(t *testing.T) {
	for _, origin := range []string{"", "*", "**"} {
		if _, err := New(Config{Key: []byte("k"), TrustedOrigins: []string{origin}}); err == nil {
			t.Errorf("New() accepted trusted origin %q", origin)
		}
	}
}

// Next.js: packages/next/src/server/app-render/csrf-protection.test.ts
// ("should return false when allowedOrigins is empty").
// With nothing trusted, a request the browser marks as cross-site is refused.
func TestNextjs_NoTrustedOriginsTrustsNoOne(t *testing.T) {
	g := trustedGuard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	req := postWith(t, token, token)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://vercel.com")
	if err := g.Verify(req); !errors.Is(err, ErrCrossOrigin) {
		t.Fatalf("Verify() = %v, want ErrCrossOrigin", err)
	}
}

// Next.js: packages/next/src/server/app-render/action-handler.ts (a request with
// no Origin is "handcrafted", and passes the origin check with a warning).
// Passing the origin check is not passing: without the browser's headers the
// token is the whole defence, and a request without one is refused.
func TestNextjs_AHandcraftedRequestStillNeedsAToken(t *testing.T) {
	g := guard(t)
	if err := g.Verify(postWith(t, "", "")); !errors.Is(err, ErrMissing) {
		t.Fatalf("Verify() = %v, want ErrMissing", err)
	}
}
