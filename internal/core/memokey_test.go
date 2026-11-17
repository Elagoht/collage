package core

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A Shared fragment render is shown whether the request was TLS, as a page's is,
// so the memo it is kept under must say which, or the first scheme is the one
// every later push is given.
func TestApp_MemoKeyHoldsTheScheme(t *testing.T) {
	app := newTestApp(t, nil)
	at := fragmentMatch{
		page:     &types.Page{Name: "home"},
		fragment: &types.Fragment{Name: "card"},
		locale:   "en",
	}

	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	secure := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	secure.TLS = &tls.ConnectionState{}

	if app.memoKey(plain, at) == app.memoKey(secure, at) {
		t.Fatal("a TLS render and a plain one share a memo key")
	}
	if app.memoKey(plain, at) != app.memoKey(httptest.NewRequest(http.MethodGet, "/", nil), at) {
		t.Fatal("two plain requests do not share a memo key")
	}
}
