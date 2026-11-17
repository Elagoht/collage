package httpx

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A shared render is shown whether the request arrived over TLS (see
// sharedRequest), so a page that builds an absolute URL from it can write
// http:// or https:// into what it renders. The cache key must say which, or the
// first reader's scheme is served to everyone.
func TestHandler_TLSAndPlainRequestsDoNotShareAnEntry(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})

	plain := func() { env.do(httptest.NewRequest(http.MethodGet, "/", nil)) }
	secure := func() {
		req := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
		req.TLS = &tls.ConnectionState{}
		env.do(req)
	}

	plain()
	secure()
	if got := env.engine.pageCalls("home"); got != 2 {
		t.Fatalf("renders after one plain and one TLS request = %d, want 2: the TLS request was served the plain render", got)
	}
	plain()
	secure()
	if got := env.engine.pageCalls("home"); got != 2 {
		t.Fatalf("renders after repeating both = %d, want 2: each scheme should be cached", got)
	}
}
