package csrf

import (
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// fetch(url, {method: "POST", body: new FormData(form)}) sends multipart, and the
// token in it is as much a submission as one in a URL-encoded body.
func TestVerify_ReadsAMultipartBody(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	var body strings.Builder
	form := multipart.NewWriter(&body)
	_ = form.WriteField(DefaultFieldName, token)
	_ = form.WriteField("title", "Hello")
	_ = form.Close()

	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: token})

	if err := g.Verify(req); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if got := req.FormValue("title"); got != "Hello" {
		t.Errorf("title = %q after Verify read the body, want %q", got, "Hello")
	}
}

// A token the guard signed for somebody else is still a valid token: anyone can
// load a page with a form and take one. What a sibling subdomain or a
// man-in-the-middle on plain http cannot do is make the browser say the form was
// posted from this origin — so a request the browser marks as coming from
// elsewhere is refused with the pair intact.
func TestVerify_RefusesACrossOriginRequestWithAValidPair(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	cases := map[string]map[string]string{
		"cross-site":             {"Sec-Fetch-Site": "cross-site"},
		"a sibling subdomain":    {"Sec-Fetch-Site": "same-site"},
		"an older browser":       {"Origin": "https://evil.example"},
		"a lying Origin, marked": {"Sec-Fetch-Site": "cross-site", "Origin": "http://example.com"},
	}
	for name, headers := range cases {
		req := postWith(t, token, token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if err := g.Verify(req); !errors.Is(err, ErrCrossOrigin) {
			t.Errorf("%s: Verify() = %v, want ErrCrossOrigin", name, err)
		}
	}
}

func TestVerify_AcceptsASameOriginRequest(t *testing.T) {
	g := guard(t)
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))

	for name, headers := range map[string]map[string]string{
		"marked same-origin":  {"Sec-Fetch-Site": "same-origin"},
		"typed by the reader": {"Sec-Fetch-Site": "none"},
		"an older browser":    {"Origin": "http://example.com"},
		"not a browser":       {},
	} {
		req := postWith(t, token, token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if err := g.Verify(req); err != nil {
			t.Errorf("%s: Verify() = %v, want nil", name, err)
		}
	}
}

func TestVerify_AcceptsATrustedOrigin(t *testing.T) {
	g, err := New(Config{Key: []byte("a key of some length"), TrustedOrigins: []string{"https://admin.example.com"}})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	token, _, _ := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	req := postWith(t, token, token)
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Origin", "https://admin.example.com")
	if err := g.Verify(req); err != nil {
		t.Fatalf("Verify() = %v, want nil for a trusted origin", err)
	}
}

func TestNew_RefusesAMalformedTrustedOrigin(t *testing.T) {
	for _, origin := range []string{"admin.example.com", "https://admin.example.com/path", "*"} {
		if _, err := New(Config{Key: []byte("k"), TrustedOrigins: []string{origin}}); err == nil {
			t.Errorf("New() accepted trusted origin %q", origin)
		}
	}
}

// failingBody fails the test that reads it.
type failingBody struct{ t *testing.T }

func (b failingBody) Read([]byte) (int, error) {
	b.t.Error("the body was read")
	return 0, errors.New("read")
}

// A cookie the guard never signed cannot match any token worth checking, so the
// body is not read for it: reading a multipart body means writing its files to
// disk, and an anonymous caller does not get that for the price of a made-up
// cookie.
func TestVerify_DoesNotReadTheBodyForAnUnsignedCookie(t *testing.T) {
	g := guard(t)
	req := httptest.NewRequest(http.MethodPost, "/posts", failingBody{t})
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.AddCookie(&http.Cookie{Name: DefaultCookieName, Value: "x"})
	if err := g.Verify(req); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() = %v, want ErrInvalid", err)
	}
}

// Only the value {{csrfToken}} renders is replaced. The marker anywhere else —
// in a comment a reader posted, escaped by the template into text or into
// another attribute — stays as it is: replaced, it would carry each reader's
// token to wherever the comment's author pointed it.
func TestPersonalise_ReplacesOnlyTheRenderedField(t *testing.T) {
	g := guard(t)
	m := g.Marker()
	content := `<input type="hidden" name="_csrf" value="` + m + `">` +
		`<a href="https://evil.example/?t=` + m + `">` + m + `</a>`
	got := string(g.Personalise([]byte(content), "TOKEN"))
	want := `<input type="hidden" name="_csrf" value="TOKEN">` +
		`<a href="https://evil.example/?t=` + m + `">` + m + `</a>`
	if got != want {
		t.Errorf("Personalise() =\n%s\nwant\n%s", got, want)
	}
	if !g.Carries([]byte(content)) || g.Carries([]byte(`<p>`+m+`</p>`)) {
		t.Error("Carries() must report the rendered field and only it")
	}
}

// A token is refused once it is older than MaxAge, and a request carrying the
// expired one is minted a fresh token rather than handed the stale one back.
func TestToken_ExpiresAfterMaxAge(t *testing.T) {
	clock := time.Now()
	g, err := New(Config{Key: []byte("a key of some length"), MaxAge: time.Hour, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	token, minted, err := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || !minted {
		t.Fatalf("TokenFor: err=%v minted=%v, want a fresh token", err, minted)
	}
	if !g.valid(token) {
		t.Fatal("a token is invalid the moment it is minted")
	}

	clock = clock.Add(59 * time.Minute)
	if !g.valid(token) {
		t.Error("token expired before MaxAge")
	}

	clock = clock.Add(2 * time.Minute) // 61 minutes old, past the hour
	if g.valid(token) {
		t.Error("token is still valid past MaxAge")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: g.CookieName(), Value: token})
	if _, minted, err := g.TokenFor(req); err != nil || !minted {
		t.Errorf("TokenFor on an expired cookie: err=%v minted=%v, want a fresh token minted", err, minted)
	}
}

// A negative MaxAge disables expiry: a token stays valid for as long as its
// signature does.
func TestToken_NegativeMaxAgeNeverExpires(t *testing.T) {
	clock := time.Now()
	g, err := New(Config{Key: []byte("a key of some length"), MaxAge: -1, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	token, _, err := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	clock = clock.Add(1000 * time.Hour)
	if !g.valid(token) {
		t.Error("a token with expiry disabled should stay valid")
	}
}

// The issue time is under the signature: moving it forward to dodge expiry
// invalidates the token.
func TestToken_TamperedIssueTimeIsRefused(t *testing.T) {
	g, err := New(Config{Key: []byte("a key of some length"), MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	token, _, err := g.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want nonce.issued.sig", len(parts))
	}
	tampered := parts[0] + "." + "99999999999" + "." + parts[2]
	if g.valid(tampered) {
		t.Error("a token with a rewritten issue time was accepted")
	}
}
