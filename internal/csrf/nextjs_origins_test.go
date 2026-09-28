package csrf

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// csrf-protection.test.ts: origins match case-insensitively (RFC 1035). A
// browser sends the host in lower case, so a trusted origin is stored so too.
func TestNextjs_TrustedOriginIsCaseInsensitive(t *testing.T) {
	g := trustedGuard(t, "https://Admin.Example.com")
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	req := postWith(t, token, token)
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Origin", "https://admin.example.com")
	if err := g.Verify(req); err != nil {
		t.Fatalf("Verify() = %v, want nil: the browser serialises the origin in lower case", err)
	}
}

// A browser leaves the default port out of Origin, so a trusted origin that
// spells it out must still match.
func TestNextjs_TrustedOriginDefaultPort(t *testing.T) {
	g := trustedGuard(t, "https://admin.example.com:443")
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	req := postWith(t, token, token)
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Origin", "https://admin.example.com")
	if err := g.Verify(req); err != nil {
		t.Fatalf("Verify() = %v, want nil: the browser omits the default port", err)
	}
}

// csrf-protection.test.ts supports "*.example.com". Here a wildcard is refused
// when the guard is built: net/http would take it as a host of its own and
// match nothing, failing closed with nothing to say why.
func TestNextjs_AWildcardIsRefused(t *testing.T) {
	for _, origin := range []string{"https://*.example.com", "https://**.example.com"} {
		if _, err := New(Config{Key: []byte("k"), TrustedOrigins: []string{origin}}); err == nil {
			t.Errorf("New() accepted %q", origin)
		}
	}
}
