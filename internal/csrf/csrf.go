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
// sign is a value they cannot use.
//
// Stateless, and that is the point. Verifying a token needs the key and nothing
// else: no session table, no store to configure, no shared state between instances.
// A framework that made forms safe only after you had chosen a session backend would
// be a framework where most forms are unsafe.
package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

// ErrMissing reports a request that carried no token at all.
var ErrMissing = errors.New("collage: no csrf token")

// ErrMismatch reports a token that does not match the one in the cookie.
var ErrMismatch = errors.New("collage: csrf token does not match")

// ErrInvalid reports a token whose signature does not hold.
var ErrInvalid = errors.New("collage: csrf token is not valid")

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
}

// Config configures a Guard. Every field has a default.
type Config struct {
	Key        []byte
	CookieName string
	FieldName  string
	HeaderName string
}

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
// its tokens to a plaintext one.
func (g *Guard) Cookie(r *http.Request, token string) *http.Cookie {
	return &http.Cookie{
		Name:     g.cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
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
	cookie, err := r.Cookie(g.cookieName)
	if err != nil || cookie.Value == "" {
		return ErrMissing
	}

	submitted := r.Header.Get(g.headerName)
	if submitted == "" {
		// ParseForm is idempotent and leaves r.PostForm populated for the
		// handler, so a handler that parses it again is not reading an empty body.
		if err := r.ParseForm(); err != nil {
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

// mint returns a fresh token: a random nonce and its signature.
func (g *Guard) mint() (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(nonce)
	return encoded + "." + g.sign(encoded), nil
}

// valid reports whether token carries a signature this guard produced.
func (g *Guard) valid(token string) bool {
	nonce, signature, found := strings.Cut(token, ".")
	if !found || nonce == "" || signature == "" {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(g.sign(nonce)))
}

// sign returns the signature for nonce.
func (g *Guard) sign(nonce string) string {
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
