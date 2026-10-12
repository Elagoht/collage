package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrInvalidOrigin is returned by ParseOrigin for anything that is not a bare
// origin. pkg/collage exports it as ErrInvalidOrigin.
var ErrInvalidOrigin = errors.New("collage: invalid origin: want scheme://host[:port] with no path, query or fragment")

// ParseOrigin checks that raw is a bare origin — an http or https scheme, a host
// name (a port alone, "https://:8080", is not one), an optional port, and nothing
// else: no path beyond "/", no query, no fragment, no user info — and
// returns it without a trailing slash, so a path joined to it gets one slash.
func ParseOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
	case u.Scheme != "http" && u.Scheme != "https":
	case u.Hostname() == "", u.User != nil, u.Opaque != "":
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
// host is lower-cased, without its port; an IPv6 literal is without its brackets,
// "::1". The first plugin, in registration order, that reports ok decides; an
// origin that is not a bare one (see ParseOrigin) — "https://:8080", which names
// no host, among them — is ignored, as if the plugin had not known the host. A host no
// plugin knows has Config.BaseURL's origin.
type OriginResolver interface {
	Origin(ctx context.Context, host string) (origin string, ok bool)
}
