package core

import (
	"crypto/rand"
	"errors"
	"log/slog"

	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/types"
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
		// Not warned about here. Whether a generated key matters depends on
		// whether anything will ever verify a token, and that is not known until
		// registration closes — so the warning lives at Start, where the answer
		// is. Saying it at construction means every application that has no forms
		// at all is told about a key it has no use for, and a warning that is
		// usually noise is a warning nobody reads.
	}

	return csrf.New(csrf.Config{
		Key:        key,
		CookieName: cfg.CSRFCookieName,
		FieldName:  cfg.CSRFFieldName,
		HeaderName: cfg.CSRFHeaderName,
	})
}

// csrfMarker returns the placeholder {{csrfToken}} renders, which the response layer
// replaces with each reader's own token.
//
// It returns an error rather than an empty string when the protection is off: a
// template asking for a token is a form that expects one, and rendering the form
// without it would produce a page whose submission is refused with nothing to
// explain why.
func (a *App) csrfMarker() (string, error) {
	if a.csrf == nil {
		return "", ErrCSRFDisabled
	}
	return a.csrf.Marker(), nil
}

// ErrCSRFDisabled reports a template asking for a forgery token in an application
// that turned the protection off.
var ErrCSRFDisabled = errors.New("collage: csrfToken used but request-forgery protection is disabled")

// warnAboutGeneratedKey reports a generated forgery key, but only to an application
// that will actually verify tokens.
//
// It runs once registration has closed, because that is when the question can be
// answered: an application with no action that answers an unsafe method never checks
// a token, and telling it about a key it has no use for is how a warning becomes
// noise. One that does check will refuse every submission made before its last
// restart, which is worth interrupting for.
//
// It must be called with a.mu held, or from a path that has closed registration.
func (a *App) warnAboutGeneratedKey() {
	if a.csrf == nil || len(a.cfg.Security.CSRFKey) != 0 {
		return
	}
	for _, action := range a.actionOrder {
		for _, method := range action.Methods {
			if types.SafeMethod(method) {
				continue
			}
			a.logger.Warn("collage: no Security.CSRFKey set, so one was generated for this process; "+
				"form submissions will be refused after a restart and across instances",
				"action", action.Name)
			return
		}
	}
}

// CSRFMarker is the placeholder a rendered page carries where a request-forgery
// token goes, or the empty string when this application has no forgery protection.
//
// It is exported for the static builder, which refuses to write a page containing
// it: a built site has no server to replace it with a reader's own token, and none
// to submit the form to either.
func (a *App) CSRFMarker() string {
	if a.csrf == nil {
		return ""
	}
	return a.csrf.Marker()
}
