package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Next.js x-forwarded-headers: "should include x-forwarded-* headers relative to
// host". collage dev reaches the same end another way — the program is handed the
// Host the browser sent, so it needs no X-Forwarded-Host to learn it — and an
// X-Forwarded-* the browser wrote itself is not passed on as if collage dev had
// said it: a program that trusts its proxy would otherwise take a page's word for
// its own host and scheme.
func TestDevProxy_PassesTheHostAndNoForgedForwardedHeaders(t *testing.T) {
	seen := make(chan *http.Request, 1)
	program := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
	}))
	defer program.Close()
	target, err := url.Parse(program.URL)
	if err != nil {
		t.Fatal(err)
	}

	p := newDevProxy("localhost:6060", target.Host)
	p.set(devReady, nil, "")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "subdomain.localhost:6060"
	r.Header.Set("X-Forwarded-Host", "evil.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	got := <-seen
	if got.Host != "subdomain.localhost:6060" {
		t.Errorf("the program saw Host %q, want the browser's", got.Host)
	}
	for _, name := range []string{"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-For"} {
		if value := got.Header.Get(name); value != "" {
			t.Errorf("the program was told %s: %q, which the browser wrote", name, value)
		}
	}
}
