package optiimage

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errBadToken covers every way a request path fails to name work this plugin
// authorised. They are deliberately one error: telling a forged signature apart
// from a malformed one, in a response, tells an attacker which half to keep trying.
var errBadToken = errors.New("opti-image: not a token this process issued")

// token carries what a rewritten URL has to say: which image, at what size.
//
// The output format is not in here. It is a path segment instead, because a
// document declares one content type for every response it serves — so "which
// format" has to be part of *which route*, not part of the token the route decodes.
type token struct {
	Source string
	Width  int
	Height int
}

// signer turns tokens into path segments and back.
//
// The signature is the point. Without one, the image endpoint takes a URL from the
// request and fetches it — a server-side request forgery primitive, reachable by
// anyone who can read the page's HTML and edit a query string. The allowlist is
// checked again at fetch time, so the signature is not the only defence; it is the
// one that keeps an attacker from probing the allowlist at all, and from turning
// the cache into unbounded storage keyed on strings they choose.
//
// The key is generated per process rather than configured. That means a restart
// invalidates every outstanding URL, which costs a re-render of pages still in a
// downstream cache and buys a key nobody has to store, rotate, or keep out of a
// configuration file. For a single-process deployment that is the right trade; a
// fleet behind a load balancer wants a shared key, and Config would need to grow
// one before this is used that way. Said plainly here rather than discovered.
type signer struct {
	key []byte
}

func newSigner() (*signer, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("opti-image: generate signing key: %w", err)
	}
	return &signer{key: key}, nil
}

// encode returns the path segment naming t.
func (s *signer) encode(t token) string {
	payload := t.Source + "\x00" + strconv.Itoa(t.Width) + "\x00" + strconv.Itoa(t.Height)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return s.sign(encoded) + "." + encoded
}

// decode returns the token a path segment names, or errBadToken.
func (s *signer) decode(segment string) (token, error) {
	signature, encoded, found := strings.Cut(segment, ".")
	if !found {
		return token{}, errBadToken
	}
	// Constant time: a byte-at-a-time comparison leaks how much of a guessed
	// signature was right, which is enough to forge one a byte at a time.
	if !hmac.Equal([]byte(signature), []byte(s.sign(encoded))) {
		return token{}, errBadToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return token{}, errBadToken
	}
	parts := strings.Split(string(payload), "\x00")
	if len(parts) != 3 {
		return token{}, errBadToken
	}
	width, err := strconv.Atoi(parts[1])
	if err != nil {
		return token{}, errBadToken
	}
	height, err := strconv.Atoi(parts[2])
	if err != nil {
		return token{}, errBadToken
	}
	return token{Source: parts[0], Width: width, Height: height}, nil
}

func (s *signer) sign(encoded string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encoded))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
