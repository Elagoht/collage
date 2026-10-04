package core

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"time"

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
	// CSRFFieldName overrides the form field a token is submitted in, both in
	// what {{csrfToken}} renders and in what the verifier reads.
	CSRFFieldName string
	// CSRFHeaderName overrides the header a token may be submitted in.
	CSRFHeaderName string
	// CSRFTrustedOrigins are other origins whose forms may post to this
	// application, each "scheme://host[:port]". A request the browser marks as
	// coming from any other origin is refused even with a valid token.
	CSRFTrustedOrigins []string
	// DisableCSRF turns forgery checking off for the whole application.
	//
	// It is for an application that has no browser-submitted forms at all — a pure
	// API behind its own authentication. On anything a browser posts to, it gives
	// the protection away, which is why it is a field rather than the absence of a
	// key.
	DisableCSRF bool
	// CSRFTokenTTL is how long a forgery token stays valid after it is issued.
	// Zero selects the default (csrf.DefaultMaxAge); a negative value disables
	// expiry, keeping a token valid for as long as its signature verifies.
	CSRFTokenTTL time.Duration
	// FrameOptions is the X-Frame-Options header value sent on every response.
	// Empty means the default "SAMEORIGIN"; "-" sends none; anything else is sent
	// verbatim. See baselineHeaders.
	FrameOptions string
	// NoSniff, unless it points at false, sends X-Content-Type-Options: nosniff on
	// every response. See baselineHeaders.
	NoSniff *bool
}

// baselineHeaders resolves the security headers sent on every response from the
// configuration: the X-Frame-Options value to send ("" meaning send none) and
// whether to send X-Content-Type-Options: nosniff. These are the defaults a site
// gets without the elagoht/secure plugin; that plugin's own headers override them.
func baselineHeaders(cfg SecurityConfig) (frameOptions string, noSniff bool) {
	switch cfg.FrameOptions {
	case "-":
		frameOptions = ""
	case "":
		frameOptions = "SAMEORIGIN"
	default:
		frameOptions = cfg.FrameOptions
	}
	noSniff = cfg.NoSniff == nil || *cfg.NoSniff
	return frameOptions, noSniff
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
		Key:            key,
		CookieName:     cfg.CSRFCookieName,
		FieldName:      cfg.CSRFFieldName,
		HeaderName:     cfg.CSRFHeaderName,
		TrustedOrigins: cfg.CSRFTrustedOrigins,
		MaxAge:         cfg.CSRFTokenTTL,
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
// In development it is said at Info rather than Warn. A generated key is the
// expected state there — nobody sets a production secret to try a form on their
// own machine — and a warning printed on every start of a correct project is one
// that teaches people to stop reading warnings.
//
// It must be called with a.mu held, or from a path that has closed registration.
func (a *App) warnAboutGeneratedKey() {
	if a.csrf == nil || len(a.cfg.Security.CSRFKey) != 0 {
		return
	}
	level := slog.LevelWarn
	if a.devMode {
		level = slog.LevelInfo
	}
	for _, action := range a.actionOrder {
		// An action exempted with WithoutCSRF never verifies a token, so a
		// generated key costs it nothing.
		if action.SkipCSRF {
			continue
		}
		for _, method := range action.Methods {
			if types.SafeMethod(method) {
				continue
			}
			a.logger.Log(context.Background(), level, "collage: no Security.CSRFKey set, so one was generated for this process; "+
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

// warnTrustEverything logs, once, when TrustedProxies holds a range with zero
// bits (0.0.0.0/0, ::/0). It is accepted — a server reachable only through its
// proxies may mean it — but every client then counts as a proxy and can name any
// address it likes in X-Forwarded-For.
func warnTrustEverything(trusted []netip.Prefix, logger *slog.Logger) {
	var everything []string
	for _, p := range trusted {
		if p.Bits() == 0 {
			everything = append(everything, p.String())
		}
	}
	if len(everything) > 0 {
		logger.Warn("collage: Server.TrustedProxies trusts every address; it lets any client name any address in X-Forwarded-For, so list only the proxies in front of the server",
			"entries", strings.Join(everything, ", "))
	}
}
