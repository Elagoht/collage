package core

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/Elagoht/collage/internal/asset"
)

// ErrMountShadowsRoute reports that a mount prefix would swallow a registered page
// or document path, making that route unreachable. It is reported by buildHandler,
// not by Mount, so it fires whichever of the mount and the route was registered
// first — see checkMountsDoNotShadow.
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
// and RegisterDocument. A prefix that would shadow a registered page or document
// path is not rejected here: that check runs in buildHandler, once registration is
// closed, so it catches the conflict whichever of the mount and the route was
// registered first. See checkMountsDoNotShadow.
func (a *App) Mount(prefix string, fsys fs.FS, opts ...asset.Option) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return fmt.Errorf("%w: cannot mount %q", ErrAppStarted, prefix)
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
// that is a prefix of a registered page or document path, and ErrMountConflict for
// the first pair of mount prefixes where one is a prefix of the other. It must run
// once registration is closed, from buildHandler, rather than from Mount itself:
// Mount is called in whatever order the embedding program happens to register
// mounts, pages, and documents in, and a mount registered before the route it
// would shadow must fail exactly as loudly as one registered after it. Checking at
// Mount time would only catch the second of those two orderings.
//
// This is also what makes it safe for Handler.ServeHTTP to check every mount
// before consulting the router at all: a mount prefix that survived this check is
// guaranteed to own URL space no route answers to, so handing it the request
// first cannot take a request away from a page or a document. See
// httpx.Handler.ServeHTTP for the other half of that reasoning.
//
// It must be called with a.mu held for reading, and after registration has closed
// so a.pages, a.documents, and a.mounts are no longer being written to.
func (a *App) checkMountsDoNotShadow() error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	for i, m := range a.mounts {
		prefix := m.Prefix()

		for _, name := range a.order {
			page := a.pages[name]
			for _, pattern := range page.Paths {
				if strings.HasPrefix(pattern, prefix) {
					return fmt.Errorf("%w: mount %q would swallow page %q's path %q",
						ErrMountShadowsRoute, prefix, page.Name, pattern)
				}
			}
		}

		for _, name := range a.docOrder {
			doc := a.documents[name]
			for _, pattern := range doc.Paths {
				if strings.HasPrefix(pattern, prefix) {
					return fmt.Errorf("%w: mount %q would swallow document %q's path %q",
						ErrMountShadowsRoute, prefix, doc.Name, pattern)
				}
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
