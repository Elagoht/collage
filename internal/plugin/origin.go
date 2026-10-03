package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrInvalidOrigin is returned by ParseOrigin for anything that is not a bare
// origin. pkg/collage exports it as ErrInvalidBaseURL, the error Config.BaseURL
// is refused with.
var ErrInvalidOrigin = errors.New("collage: BaseURL must be a bare origin, scheme://host[:port]")

// ParseOrigin checks that raw is a bare origin — an http or https scheme, a host,
// and nothing else: no path beyond "/", no query, no fragment, no user info — and
// returns it without a trailing slash, so a path joined to it gets one slash.
func ParseOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
	case u.Scheme != "http" && u.Scheme != "https":
	case u.Host == "", u.User != nil, u.Opaque != "":
	case u.Path != "" && u.Path != "/":
	case u.RawQuery != "", u.Fragment != "", u.RawFragment != "":
	default:
		return strings.TrimSuffix(raw, "/"), nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidOrigin, raw)
}

// OriginResolver is implemented by a plugin that knows the public origin of a
// host the site is served on — one site, a host per customer — so the absolute
// URLs a page, a sitemap or a feed carries follow the host it was asked for.
//
// host is lower-cased, without its port. The first plugin, in registration order,
// that reports ok decides; an origin that is not a bare one (see ParseOrigin) is
// ignored, as if the plugin had not known the host. A host no plugin knows has
// Config.BaseURL's origin.
type OriginResolver interface {
	Origin(ctx context.Context, host string) (origin string, ok bool)
}

// Origins is a capability of the Host and ConfigHost a plugin receives: reach it
// with a type assertion, host.(collage.Origins). It is how a plugin that writes
// absolute URLs outside a render — an invalidation, a command — learns a host's
// origin, and whether origins vary by host at all.
type Origins interface {
	// OriginFor is host's origin: an OriginResolver's, else Config.BaseURL, which
	// may be "".
	OriginFor(ctx context.Context, host string) string
	// Dynamic reports whether a plugin implementing OriginResolver is registered,
	// so an empty Config.BaseURL is not, by itself, a misconfiguration.
	Dynamic() bool
}
