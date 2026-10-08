// Package csrf issues and verifies cross-site request forgery tokens.
//
// The scheme is a signed double-submit cookie. A token is a random nonce and an
// HMAC of it under the application's key; the same value goes into a cookie and
// into the form, and a request is accepted when the two match and the signature
// holds.
//
// Signed rather than a bare double submit, because a bare one trusts that nobody
// else can set the cookie — and on a site with subdomains, or behind anything that
// can write a cookie for the parent domain, somebody else can. A value they cannot
// sign is a value they cannot invent.
//
// It is not a value they cannot obtain: any page with a form hands its reader a
// signed token, and an attacker is a reader too. One who can write a cookie for
// the domain plants their own token in the victim's browser and submits it, and
// the pair matches. So the token is not checked alone. The browser says where a
// request came from — Sec-Fetch-Site, or Origin when an older one sends only
// that — and a request it marks as coming from another origin is refused whatever
// it carries. A sibling subdomain is another origin: it is exactly who can write
// the cookie.
//
// Stateless, and that is the point. Verifying a token needs the key and nothing
// else: no session table, no store to configure, no shared state between instances.
// A framework that made forms safe only after you had chosen a session backend would
// be a framework where most forms are unsafe.
package csrf

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/ascii"
	"github.com/Elagoht/collage/internal/netaddr"
)

// ErrMissing reports a request that carried no token at all.
var ErrMissing = errors.New("collage: no csrf token")

// ErrMismatch reports a token that does not match the one in the cookie.
var ErrMismatch = errors.New("collage: csrf token does not match")

// ErrInvalid reports a token whose signature does not hold.
var ErrInvalid = errors.New("collage: csrf token is not valid")

// ErrCrossOrigin reports a request the browser says was sent from another origin.
var ErrCrossOrigin = errors.New("collage: cross-origin request")

// DefaultCookieName is the cookie a token is carried in.
const DefaultCookieName = "collage_csrf"

// DefaultFieldName is the form field a token is submitted in.
const DefaultFieldName = "_csrf"

// DefaultHeaderName is the header a token may be submitted in instead, which is what
// a fetch() uses when there is no form to put a field in.
const DefaultHeaderName = "X-CSRF-Token"

// Guard issues and verifies tokens under one key.
type Guard struct {
	key        []byte
	cookieName string
	fieldName  string
	headerName string
	maxAge     time.Duration // 0 disables the age check
	now        func() time.Time
	origin     *http.CrossOriginProtection
	trusted    []netip.Prefix
}

// Config configures a Guard. Every field has a default.
type Config struct {
	Key        []byte
	CookieName string
	FieldName  string
	HeaderName string
	// TrustedOrigins are other origins whose forms may post here, each spelled
	// "scheme://host[:port]".
	TrustedOrigins []string
	// MaxAge is how long a token stays valid after it was issued. The issue time
	// is signed into the token, so it cannot be moved without invalidating the
	// signature; past MaxAge the token is refused and TokenFor mints a fresh one.
	// Zero selects the default; a negative value disables the age check, keeping a
	// token valid for as long as its signature verifies.
	MaxAge time.Duration
	// TrustedProxies are the proxies whose X-Forwarded-Proto is believed when
	// deciding the cookie's Secure flag — Server.TrustedProxies, the set
	// ClientIP believes X-Forwarded-For from. Empty believes the header from
	// anyone, which is right for a server that only a proxy can reach and was
	// the only behaviour before the field existed.
	TrustedProxies []netip.Prefix
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// DefaultMaxAge is the token lifetime used when Config.MaxAge is zero: long
// enough that a form filled at a reader's own pace still posts, short enough that
// a token that leaks cannot be replayed indefinitely.
const DefaultMaxAge = 12 * time.Hour

// New returns a Guard. A nil or empty Key is a programming error the caller must
// have resolved already — core generates one and warns when the application set
// none — so it is reported rather than worked around.
func New(cfg Config) (*Guard, error) {
	if len(cfg.Key) == 0 {
		return nil, errors.New("collage: csrf needs a key")
	}
	g := &Guard{
		key:        cfg.Key,
		cookieName: cfg.CookieName,
		fieldName:  cfg.FieldName,
		headerName: cfg.HeaderName,
		maxAge:     cfg.MaxAge,
		now:        cfg.Now,
		origin:     http.NewCrossOriginProtection(),
		trusted:    slices.Clone(cfg.TrustedProxies),
	}
	switch {
	case cfg.MaxAge == 0:
		g.maxAge = DefaultMaxAge
	case cfg.MaxAge < 0:
		g.maxAge = 0 // disabled: the signature alone keeps a token valid
	}
	if g.now == nil {
		g.now = time.Now
	}
	for _, origin := range cfg.TrustedOrigins {
		canonical, err := canonicalOrigin(origin)
		if err == nil {
			err = g.origin.AddTrustedOrigin(canonical)
		}
		if err != nil {
			return nil, fmt.Errorf("collage: csrf trusted origin %q: %w", origin, err)
		}
	}
	if g.cookieName == "" {
		g.cookieName = DefaultCookieName
	}
	if g.fieldName == "" {
		g.fieldName = DefaultFieldName
	}
	if g.headerName == "" {
		g.headerName = DefaultHeaderName
	}
	return g, nil
}

// forwardedHTTPS reports whether the proxy in front says r arrived over TLS. A
// proxy that appends rather than replaces sends a list, "https, http", whose
// first entry is the one the reader's own connection used.
//
// With trusted proxies configured, the header is believed only from one of
// them: anything else that sends it is a client choosing what the server
// thinks of its own connection.
func (g *Guard) forwardedHTTPS(r *http.Request) bool {
	if len(g.trusted) > 0 && !netaddr.FromTrusted(g.trusted, r.RemoteAddr) {
		return false
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return ascii.EqualFold(strings.TrimSpace(first), "https")
}

// canonicalOrigin returns origin spelled as a browser sends it in the Origin
// header, which is what a trusted origin is compared with, byte for byte: scheme
// and host in lower case, no default port. "https://Admin.Example.com:443"
// written as it is would match no request at all, and fail closed with nothing
// to say why.
//
// A wildcard is refused rather than accepted: net/http would take
// "https://*.example.com" as a host of its own and match nothing, and a list of
// the origins meant is the policy anyway — a sibling subdomain is exactly who
// this check does not trust by default.
func canonicalOrigin(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	scheme := ascii.LowerString(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New(`want "http://" or "https://" and a host`)
	}
	if u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New(`want "scheme://host[:port]" and nothing else`)
	}
	if strings.Contains(u.Host, "*") {
		return "", errors.New("wildcards are not supported; name each origin")
	}
	host := ascii.LowerString(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

// Marker is the placeholder a rendered page carries in place of a token.
//
// It exists so that a page with a form can still be cached. A token belongs to one
// visitor, so a cached body must not contain one — but it may contain something that
// stands for one, replaced with the reader's own token as the response is written.
// The expensive part, the render, is shared; the one per-visitor string is not.
//
// Derived from the key rather than random, which is what makes it work at all: a
// body cached by one process is served by the next, and a marker that changed per
// process would be served literally. Derived rather than constant, because a
// constant one appears in content an application did not write — a comment, a
// paste — and substitution would then put the reader's token wherever someone else
// chose. A value nobody can compute without the key cannot be planted.
func (g *Guard) Marker() string {
	return "collage-csrf-" + g.sign("marker")[:32]
}

// Carries reports whether content holds the field {{csrfToken}} renders, whose
// value Personalise replaces.
func (g *Guard) Carries(content []byte) bool {
	return bytes.Contains(content, g.renderedValue())
}

// Personalise returns content with the value of the field {{csrfToken}} renders
// set to token.
//
// The field's value attribute, not the marker wherever it occurs. A template
// escapes the quote a reader's text carries, so value="…" around the marker is
// something only {{csrfToken}} writes; the bare marker is something anyone who
// has seen it can write — a comment linking to their own site with it in the
// URL — and replacing it there hands them the token of everyone who reads the
// comment.
func (g *Guard) Personalise(content []byte, token string) []byte {
	return bytes.ReplaceAll(content, g.renderedValue(), []byte(`value="`+token+`"`))
}

// renderedValue is the value attribute {{csrfToken}} renders.
func (g *Guard) renderedValue() []byte {
	return []byte(`value="` + g.Marker() + `"`)
}

// CookieName returns the cookie a token is carried in.
func (g *Guard) CookieName() string { return g.cookieName }

// FieldName returns the form field a token is submitted in.
func (g *Guard) FieldName() string { return g.fieldName }

// TokenFor returns the token to put in a form on a response to r.
//
// It reuses the one the request already carries when that one is valid, so that two
// forms on a page carry the same token and a reader with several tabs open does not
// have one of them invalidated by the other. Only when there is nothing usable does
// it mint a new one.
func (g *Guard) TokenFor(r *http.Request) (token string, minted bool, err error) {
	if cookie, cookieErr := r.Cookie(g.cookieName); cookieErr == nil {
		if g.valid(cookie.Value) {
			return cookie.Value, false, nil
		}
	}
	fresh, err := g.mint()
	if err != nil {
		return "", false, err
	}
	return fresh, true, nil
}

// Cookie returns the cookie carrying token, for a response to r.
//
// HttpOnly, because nothing needs to read it from script: the token a form submits
// is rendered into the form, and a fetch() reads it from the same markup. SameSite
// Lax, which by itself refuses the cross-site POST this protects against — the
// signature is what covers the cases Lax does not, such as a sibling subdomain.
// Secure whenever the request arrived over TLS, so a site that has TLS does not hand
// its tokens to a plaintext one — over TLS here, or at a proxy that says so in
// X-Forwarded-Proto (believed only from Config.TrustedProxies when it is set).
func (g *Guard) Cookie(r *http.Request, token string) *http.Cookie {
	return &http.Cookie{
		Name:     g.cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || g.forwardedHTTPS(r),
	}
}

// Verify checks the token r submitted against the one it carries in its cookie.
//
// The submitted value is read from the form first and the header second, so an
// ordinary HTML form needs nothing added to it and a fetch() has a way in that does
// not require a form at all.
//
// The request's form is parsed here when it has not been already, which reads the
// body — bounded by the caller before this runs.
func (g *Guard) Verify(r *http.Request) error {
	// First, and whatever the request carries: see the package comment. A
	// request with neither header is not from a browser, and a forgery is
	// something a browser is tricked into sending.
	if err := g.origin.Check(r); err != nil {
		return fmt.Errorf("%w: %w", ErrCrossOrigin, err)
	}

	cookie, err := r.Cookie(g.cookieName)
	if err != nil || cookie.Value == "" {
		return ErrMissing
	}
	// Before the body is read. A cookie this guard never signed matches no token
	// worth checking, and reading a multipart body writes its files to disk — not
	// something an anonymous caller gets for the price of a made-up cookie.
	if !g.valid(cookie.Value) {
		return ErrInvalid
	}

	submitted := r.Header.Get(g.headerName)
	if submitted == "" {
		// Parsing is idempotent and leaves the form populated for the handler, so
		// a handler that parses it again is not reading an empty body.
		if err := parseForm(r); err != nil {
			return err
		}
		submitted = r.PostFormValue(g.fieldName)
	}
	if submitted == "" {
		return ErrMissing
	}

	// Constant time, on both comparisons. A token is a secret, and a comparison
	// that stops at the first wrong byte tells an attacker how many were right.
	if !hmac.Equal([]byte(submitted), []byte(cookie.Value)) {
		return ErrMismatch
	}
	if !g.valid(submitted) {
		return ErrInvalid
	}
	return nil
}

// multipartMemory is how much of a multipart body is held in memory while it is
// parsed; the rest of its files spill to disk. The same figure net/http uses. It
// is not a limit on the body — the caller's MaxBytesReader is that.
const multipartMemory = 32 << 20

// parseForm parses r's body as whichever kind of form it is.
//
// ParseForm alone ignores multipart/form-data, and multipart is what
// fetch(url, {method: "POST", body: new FormData(form)}) sends. Reading only
// URL-encoded bodies would refuse that submission with a valid token in it.
//
// ParseForm runs first and on its own, because ParseMultipartForm calls it and then
// discards its error whenever the body is not multipart — so a URL-encoded body
// over the size limit would come back as "not multipart" and be refused as a
// missing token rather than as a body too large to read.
func parseForm(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	if err := r.ParseMultipartForm(multipartMemory); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return err
	}
	return nil
}

// mint returns a fresh token: a random nonce, the time it was issued, and a
// signature over both. The issue time travels in the token so the guard need keep
// no per-token state to expire it, and signing it is what keeps a reader from
// moving it forward.
func (g *Guard) mint() (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(nonce) + "." + strconv.FormatInt(g.now().Unix(), 10)
	return payload + "." + g.sign(payload), nil
}

// valid reports whether token carries a signature this guard produced and, unless
// the age check is disabled, was issued no longer ago than maxAge.
func (g *Guard) valid(token string) bool {
	nonce, rest, found := strings.Cut(token, ".")
	if !found {
		return false
	}
	issued, signature, found := strings.Cut(rest, ".")
	if !found || nonce == "" || issued == "" || signature == "" {
		return false
	}
	if !hmac.Equal([]byte(signature), []byte(g.sign(nonce+"."+issued))) {
		return false
	}
	if g.maxAge <= 0 {
		return true
	}
	seconds, err := strconv.ParseInt(issued, 10, 64)
	if err != nil {
		return false
	}
	// The signature has already established issued was not tampered with, so the
	// only thing left to reject is a token simply too old. A future issue time can
	// come of clock skew between instances sharing a key; it is not refused, since
	// it only shortens the token's own life.
	return g.now().Unix()-seconds <= int64(g.maxAge/time.Second)
}

// sign returns the signature for nonce.
func (g *Guard) sign(nonce string) string {
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
