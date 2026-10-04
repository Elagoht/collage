package collage

import "github.com/Elagoht/collage/internal/router"

// SafeRedirect returns next when it is a path on this site — it starts with "/",
// not with "//" or "/\", and holds no control character — and fallback
// otherwise; a fallback that is not one either gives "/". It is the check
// collage applies to its own redirects, for a login that sends the reader back
// to a "next" taken from the URL:
//
//	http.Redirect(w, r, collage.SafeRedirect(r.URL.Query().Get("next"), "/"), http.StatusSeeOther)
//
// An absolute URL is never accepted, even one on this site's own origin: the rule
// stays one simple check. collage itself still sends a guard's or an action's
// Location as given, since some must leave the site (a sign-in provider).
func SafeRedirect(next, fallback string) string {
	if _, unsafe := router.UnsafeRedirectReason(next); !unsafe {
		return next
	}
	if _, unsafe := router.UnsafeRedirectReason(fallback); !unsafe {
		return fallback
	}
	return "/"
}
