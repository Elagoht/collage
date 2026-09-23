package core

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/Elagoht/collage/internal/asset"
)

// ErrMountShadowsRoute reports that a mount prefix would swallow a URL path the
// router already answers to — a page path, a document path, or a redirect source —
// making that route unreachable. It is reported by buildHandler, not by Mount, so
// it fires whichever of the mount and the route was registered first — see
// checkMountsDoNotShadow.
var ErrMountShadowsRoute = errors.New("collage: mount shadows a route")

// ErrMountConflict reports that two mounts claim overlapping prefixes: one prefix
// begins with the other, so a request under the shorter prefix could be claimed by
// either mount depending on registration order.
var ErrMountConflict = errors.New("collage: mount prefixes overlap")

// Mount serves fsys under prefix, alongside the application's pages and documents.
// prefix must begin and end with "/" and must not be "/" alone, or asset.New's
// ErrInvalidPrefix is returned; a nil fsys returns asset.ErrNilFS.
//
// It returns ErrAppStarted after the server has started, exactly like RegisterPage
// and RegisterDocument. A prefix that would shadow a registered page path, document
// path, or redirect source is not rejected here: that check runs in buildHandler,
// once registration is closed, so it catches the conflict whichever of the mount
// and the route was registered first. See checkMountsDoNotShadow.
func (a *App) Mount(prefix string, fsys fs.FS, opts ...asset.Option) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return fmt.Errorf("%w: cannot mount %q", ErrAppStarted, prefix)
	}

	// Plugin wrappers are applied in registration order, so a later plugin sees
	// what an earlier one produced. They wrap the filesystem rather than the
	// response because a mount serves through http.ServeContent: transforming
	// bytes per request would shift every offset and make a Range request return
	// the wrong slice of a file whose advertised length no longer matches.
	for _, wrap := range a.mountWrappers {
		fsys = wrap(fsys)
	}

	m, err := asset.New(prefix, fsys, opts...)
	if err != nil {
		return err
	}
	a.mounts = append(a.mounts, m)
	return nil
}

// Mounts returns every mounted asset file system, in registration order, as a copy
// of the slice: appending to or reordering the returned slice does not affect the
// application. Task 8's static builder is what consumes it.
func (a *App) Mounts() []*asset.Mount {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return slices.Clone(a.mounts)
}

// checkMountsDoNotShadow reports ErrMountShadowsRoute for the first mount prefix
// that is a prefix of a URL path the router already answers to, and
// ErrMountConflict for the first pair of mount prefixes where one is a prefix of
// the other. It must run once registration is closed, from buildHandler, rather
// than from Mount itself: Mount is called in whatever order the embedding program
// happens to register mounts, pages, documents, and redirects in, and a mount
// registered before the route it would shadow must fail exactly as loudly as one
// registered after it. Checking at Mount time would only catch the second of those
// two orderings.
//
// What it checks against is the router's own answer — Router.ClaimedPaths — and
// not this package's registries. That is the whole correction: this used to walk
// a.pages and a.documents itself, which meant it saw two of the three registries
// that claim URL space and silently missed the third. A page carrying
// Redirect{From: "/static/old.css"} alongside Mount("/static/") started cleanly and
// answered that URL with a 404 and no Location header, even though a redirect
// colliding with a route is a startup error in either order everywhere else. The
// router is the component that owns "what URL space is claimed", so it is the
// component that is asked, and a fourth registry added later is covered without
// anyone remembering to teach this function about it.
//
// ClaimedPaths also reports the locale-prefixed form of every route, so a mount at
// "/tr/" is caught against a page registered at Paths{"tr": "/about"} — whose real
// URL is "/tr/about" and whose pattern alone shows no "/tr" at all.
//
// This is also what makes it safe for Handler.serve to check every mount before
// consulting the router. What the check establishes is narrower than "the mount
// owns URL space no route answers to", and the difference matters: the comparison
// is strings.HasPrefix against registered patterns, so it catches every literal
// path, including locale-prefixed ones, but it cannot see a *dynamic* pattern that
// would match inside the prefix. A page at "/{slug}" or a catch-all at "/{rest...}"
// is registered above the prefix and matches URLs beneath it, and no prefix
// comparison will show that. So what survives this check is: no route is
// registered *at* a path under the prefix. A catch-all that would otherwise have
// matched there loses those URLs to the mount — which is the documented trade of
// mounting a prefix, not a silent shadowing of a route the author named. See
// httpx.Handler.serve for the other half of the reasoning.
//
// It must be called after registration has closed, so a.mounts and the router's
// own registries are no longer being written to.
func (a *App) checkMountsDoNotShadow() error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	claimed := a.routes.ClaimedPaths()

	for i, m := range a.mounts {
		prefix := m.Prefix()

		for _, path := range claimed {
			if strings.HasPrefix(path.Pattern, prefix) {
				return fmt.Errorf("%w: mount %q would swallow %s at %q",
					ErrMountShadowsRoute, prefix, path.Owner, path.Pattern)
			}
		}

		for _, other := range a.mounts[i+1:] {
			otherPrefix := other.Prefix()
			if strings.HasPrefix(otherPrefix, prefix) || strings.HasPrefix(prefix, otherPrefix) {
				return fmt.Errorf("%w: %q and %q", ErrMountConflict, prefix, otherPrefix)
			}
		}
	}
	return nil
}
