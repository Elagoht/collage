package csrf

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guard(t *testing.T) *Guard {
	t.Helper()
	g, err := New(Config{Key: []byte("a key of some length")})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return g
}

func TestNew_RefusesAnEmptyKey(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New() = nil error with no key, want an error")
	}
}

func TestTokenFor_MintsWhenThereIsNothingToReuse(t *testing.T) {
	g := guard(t)
	token, minted, err := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("TokenFor() = %v, want nil", err)
	}
	if !minted {
		t.Error("minted = false for a request carrying no cookie")
	}
	if !strings.Contains(token, ".") {
		t.Errorf("token = %q, want a nonce and a signature", token)
	}
}

// Two forms on one page must carry one token, and a reader with two tabs open must
// not have one invalidate the other.
func TestTokenFor_ReusesAValidCookie(t *testing.T) {
	g := guard(t)
	first, _, err := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("TokenFor() = %v, want nil", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: first})

	again, minted, err := g.TokenFor(req)
	if err != nil {
		t.Fatalf("TokenFor() = %v, want nil", err)
	}
	if minted {
		t.Error("minted = true for a request already carrying a valid token")
	}
	if again != first {
		t.Errorf("token = %q, want the one already held, %q", again, first)
	}
}

// A cookie this guard did not sign is not reused. Otherwise anything that can write
// a cookie for the domain chooses the token, and a double submit it controls both
// halves of is no protection at all.
func TestTokenFor_DoesNotReuseAnUnsignedCookie(t *testing.T) {
	g := guard(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: "attacker-chose-this.and-this"})

	token, minted, err := g.TokenFor(req)
	if err != nil {
		t.Fatalf("TokenFor() = %v, want nil", err)
	}
	if !minted {
		t.Fatal("minted = false: a cookie with no valid signature was reused")
	}
	if token == "attacker-chose-this.and-this" {
		t.Error("the planted value was kept")
	}
}

func postWith(t *testing.T, cookie, field string) *http.Request {
	t.Helper()
	body := ""
	if field != "" {
		body = DefaultFieldName + "=" + field
	}
	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: cookie})
	}
	return req
}

func TestVerify_AcceptsAMatchingPair(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	if err := g.Verify(postWith(t, token, token)); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestVerify_Refusals(t *testing.T) {
	g := guard(t)
	valid, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	other, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	for _, tc := range []struct {
		name   string
		cookie string
		field  string
		want   error
	}{
		{"no cookie and no field", "", "", ErrMissing},
		{"cookie but nothing submitted", valid, "", ErrMissing},
		{"submitted but no cookie", "", valid, ErrMissing},
		{"two different valid tokens", valid, other, ErrMismatch},
		{"a forged pair that matches itself", "forged.signature", "forged.signature", ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := g.Verify(postWith(t, tc.cookie, tc.field))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Verify() = %v, want %v", err, tc.want)
			}
		})
	}
}

// The forged pair is the case a bare double submit gets wrong: both halves match,
// and only the signature says they were never issued here.
func TestVerify_ARequestThatSubmitsItsOwnInventedPairIsRefused(t *testing.T) {
	g := guard(t)
	if err := g.Verify(postWith(t, "abc.def", "abc.def")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() = %v, want ErrInvalid", err)
	}
}

// A token signed with a different key is not accepted here.
func TestVerify_AnotherApplicationsTokenIsRefused(t *testing.T) {
	mine := guard(t)
	theirs, err := New(Config{Key: []byte("a different key entirely")})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	token, _, _ := theirs.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	if err := mine.Verify(postWith(t, token, token)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() = %v, want ErrInvalid", err)
	}
}

// A fetch() has no form to put a field in, so the header is the way in.
func TestVerify_AcceptsTheHeader(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: token})
	req.Header.Set(DefaultHeaderName, token)

	if err := g.Verify(req); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

// Verify parses the form, and the handler that runs afterwards must still find it.
func TestVerify_LeavesTheFormReadable(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	req := httptest.NewRequest(http.MethodPost, "/posts",
		strings.NewReader(DefaultFieldName+"="+token+"&title=Hello"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: token})

	if err := g.Verify(req); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if got := req.PostFormValue("title"); got != "Hello" {
		t.Errorf("title = %q after Verify read the body, want %q", got, "Hello")
	}
}

func TestCookie_Attributes(t *testing.T) {
	g := guard(t)
	cookie := g.Cookie(httptest.NewRequest(http.MethodGet, "/", nil), "token")

	if !cookie.HttpOnly {
		t.Error("HttpOnly = false: nothing needs to read this from script")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want /", cookie.Path)
	}
	if cookie.Secure {
		t.Error("Secure = true for a plaintext request; it would never be sent back")
	}

	tls := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	if !g.Cookie(tls, "token").Secure {
		t.Error("Secure = false over TLS: a site with TLS must not hand its tokens to a plaintext one")
	}
}

// The marker must be the same in every process that shares a key, or a page cached
// by one is served literally by the next.
func TestMarker_IsStableForAKey(t *testing.T) {
	first, _ := New(Config{Key: []byte("one key")})
	again, _ := New(Config{Key: []byte("one key")})
	other, _ := New(Config{Key: []byte("another key")})

	if first.Marker() != again.Marker() {
		t.Errorf("marker = %q then %q for one key, want the same", first.Marker(), again.Marker())
	}
	if first.Marker() == other.Marker() {
		t.Error("two keys produced one marker; it is not derived from the key")
	}
	if len(first.Marker()) < 32 {
		t.Errorf("marker = %q, want something no one can guess", first.Marker())
	}
}
