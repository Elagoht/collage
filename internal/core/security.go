package core

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Elagoht/collage/internal/csrf"
)

// SecurityConfig configures the framework's request-forgery protection.
type SecurityConfig struct {
	// CSRFKey signs forgery tokens. It should be at least 32 random bytes, kept
	// with the application's other secrets, and the same on every instance.
	//
	// An empty key is not an error: one is generated and the application logs that
	// it did. That is the right default for a first run and the wrong thing to
	// deploy, because a generated key is different in every process — so a token
	// issued before a restart is refused after it, and a token issued by one
	// instance is refused by the next.
	CSRFKey []byte
	// CSRFCookieName overrides the cookie a token is carried in.
	CSRFCookieName string
	// CSRFFieldName overrides the form field a token is submitted in. Changing it
	// means {{csrfToken}} and the verifier disagree, so it exists for an
	// application that renders its own field rather than using that function.
	CSRFFieldName string
	// CSRFHeaderName overrides the header a token may be submitted in.
	CSRFHeaderName string
	// DisableCSRF turns forgery checking off for the whole application.
	//
	// It is for an application that has no browser-submitted forms at all — a pure
	// API behind its own authentication. On anything a browser posts to, it gives
	// the protection away, which is why it is a field rather than the absence of a
	// key.
	DisableCSRF bool
}

// buildCSRF returns the guard the application will use, or nil when forgery
// checking is off.
func buildCSRF(cfg SecurityConfig, logger *slog.Logger) (*csrf.Guard, error) {
	if cfg.DisableCSRF {
		logger.Warn("collage: request-forgery protection is off; any site can submit this application's forms")
		return nil, nil
	}

	key := cfg.CSRFKey
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		logger.Warn("collage: no Security.CSRFKey set, so one was generated for this process; " +
			"tokens will be refused after a restart and across instances")
	}

	return csrf.New(csrf.Config{
		Key:        key,
		CookieName: cfg.CSRFCookieName,
		FieldName:  cfg.CSRFFieldName,
		HeaderName: cfg.CSRFHeaderName,
	})
}

// csrfToken returns the forgery token for r, and is what backs {{csrfToken}}.
//
// It returns an error rather than an empty string when the protection is off: a
// template asking for a token is a form that expects one, and rendering the form
// without it would produce a page whose submission is refused with nothing to
// explain why.
func (a *App) csrfToken(r *http.Request) (string, error) {
	if a.csrf == nil {
		return "", ErrCSRFDisabled
	}
	token, _, err := a.csrf.TokenFor(r)
	return token, err
}

// ErrCSRFDisabled reports a template asking for a forgery token in an application
// that turned the protection off.
var ErrCSRFDisabled = errors.New("collage: csrfToken used but request-forgery protection is disabled")
