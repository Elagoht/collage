package collage

import (
	"path"
	"strings"

	"github.com/Elagoht/collage/internal/router"
)

// SafeRedirect returns next when it is a path on this site, and fallback
// otherwise; a fallback that is not one either gives "/". A path on this site
// starts with "/", not with "//", holds no backslash and no control character,
// and still starts with a single "/" once cleaned. It is for a login that sends
// the reader back to a "next" taken from the URL:
//
//	http.Redirect(w, r, collage.SafeRedirect(r.URL.Query().Get("next"), "/"), http.StatusSeeOther)
//
// The check is the one collage applies to its own redirects, plus two rules for
// what http.Redirect does to a Location: it cleans a rooted path, so "/./\evil.com"
// would leave as "/\evil.com", which a browser reads as "//evil.com". A backslash
// is refused anywhere, the query included, and the cleaned path is checked too.
//
// An absolute URL is never accepted, even one on this site's own origin: the rule
// stays one simple check. collage itself still sends a guard's or an action's
// Location as given, since some must leave the site (a sign-in provider).
func SafeRedirect(next, fallback string) string {
	if onSite(next) {
		return next
	}
	if onSite(fallback) {
		return fallback
	}
	return "/"
}

// onSite reports whether target is a path on this site, as SafeRedirect means it.
func onSite(target string) bool {
	if _, unsafe := router.UnsafeRedirectReason(target); unsafe {
		return false
	}
	if strings.Contains(target, `\`) {
		return false
	}
	// Belt and braces: with the backslash gone, a cleaned rooted path cannot
	// start with "//" either, but this is what http.Redirect will send.
	p := target
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	_, unsafe := router.UnsafeRedirectReason(path.Clean(p))
	return !unsafe
}
